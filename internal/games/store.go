package games

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"cato/internal/db"
)

type Store struct {
	db *db.DB
}

func NewStore(db *db.DB) *Store {
	return &Store{db: db}
}

// Search is composed at query time (buildSearchUnion + buildFilterWhere)
// rather than from fixed templates: the name branch and the alias branch
// (SEARCH_IMPROVEMENTS.md §4.1) share the ranking/floor/filter plumbing, and
// both a results query and a COUNT query are produced from identical pieces.

// EscapeLike escapes SQL LIKE wildcards in user input so that a query like
// "100%" matches the literal string "100%" rather than "100<anything>".
// Use with `LIKE ? ESCAPE '\'`.
func EscapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// ValidSorts whitelists the sort= values accepted on the search endpoint.
var ValidSorts = map[string]bool{
	"":            true, // relevance (default)
	"relevance":   true,
	"release_new": true,
	"release_old": true,
	"rating":      true,
	"popularity":  true,
	"name":        true,
}

// searchOptions controls one search execution.
type searchOptions struct {
	limit      int
	offset     int
	applyFloor bool  // hide weak tier-3 substring matches unless popular
	sort       string
	yearFrom   int64 // unix seconds, inclusive; 0 = unset
	yearTo     int64 // unix seconds, inclusive; 0 = unset
	minRating  int64 // aggregated_rating >= minRating with count > 0; 0 = unset
	platform   string // availability filter: substring of platform name/abbrev
	withTotal  bool  // also run the COUNT query
}

func (s *Store) SearchLocal(ctx context.Context, query string, limit int) ([]GameResult, error) {
	results, _, err := s.search(ctx, query, searchOptions{limit: limit})
	return results, err
}

// SearchLocalPaged performs a paginated search with optional relevance floor.
// When applyFloor is true, weak (tier-3 substring) matches are hidden unless
// they have popularity_score > 0. Alias matches always pass the floor — an
// alias hit is a strong signal regardless of the game's popularity.
func (s *Store) SearchLocalPaged(ctx context.Context, query string, limit, offset int, applyFloor bool) ([]GameResult, error) {
	results, _, err := s.search(ctx, query, searchOptions{
		limit:      limit,
		offset:     offset,
		applyFloor: applyFloor,
	})
	return results, err
}

// SearchGamesPaged is the full-results-page entry point: paginated, floored,
// sorted/filtered per options, and returning the total match count so the UI
// can display "N games".
func (s *Store) SearchGamesPaged(ctx context.Context, query string, o searchOptions) ([]GameResult, int64, error) {
	o.applyFloor = true
	o.withTotal = true
	return s.search(ctx, query, o)
}

// search runs the engine cascade: FTS phrase → (on zero rows) FTS token-AND →
// LIKE fallback (short queries or missing FTS tables). A healthy FTS path is
// authoritative even when it returns zero rows; falling through to LIKE on
// every empty result would turn each miss into a full-table scan.
func (s *Store) search(ctx context.Context, query string, o searchOptions) ([]GameResult, int64, error) {
	if o.limit <= 0 {
		o.limit = 10
	}
	if o.offset < 0 {
		o.offset = 0
	}
	if !ValidSorts[o.sort] {
		o.sort = ""
	}

	prefix := EscapeLike(query) + "%"
	wordPrefix := "% " + EscapeLike(query) + "%"
	like := "%" + EscapeLike(query) + "%"

	run := func(engine, match string) ([]GameResult, int64, error) {
		return s.execSearch(ctx, engine, match, query, prefix, wordPrefix, like, o)
	}

	if match, ok := BuildFTSMatch(query); ok {
		results, total, err := run("fts", match)
		if err != nil {
			// FTS table missing or query error: fall through to the LIKE
			// path below. Keeps search working on databases migrated before
			// v5/v7 or if a virtual table is ever dropped.
		} else if len(results) == 0 {
			// Word-order retry: same index, order-insensitive AND of tokens.
			// Skipped when identical to the phrase (single-token queries).
			if tok, ok2 := BuildFTSTokenMatch(query); ok2 && tok != match {
				results, total, err = run("fts", tok)
				if err == nil {
					return results, total, nil
				}
			} else {
				return results, total, nil
			}
		} else {
			return results, total, nil
		}
	}
	return run("like", "")
}

// buildSearchUnion composes the inner subquery listing matching game IDs:
// the name branch (tier 0-3 by match strength) plus the alias branch
// (tier 3, src 1). args is appended in positional order.
func buildSearchUnion(b *strings.Builder, args *[]interface{}, engine, match, query, prefix, wordPrefix, like string) {
	switch engine {
	case "fts":
		b.WriteString(`SELECT g0.id,
		       CASE WHEN g0.normalized_name = ? THEN 0
		            WHEN g0.normalized_name LIKE ? ESCAPE '\' THEN 1
		            WHEN g0.normalized_name LIKE ? ESCAPE '\' THEN 2
		            ELSE 3 END AS tier,
		       0 AS src
		     FROM games g0
		     JOIN games_fts f ON f.rowid = g0.id
		     WHERE f.normalized_name MATCH ?`)
		*args = append(*args, query, prefix, wordPrefix, match)

		b.WriteString(`
		     UNION ALL
		     SELECT a.game_id, 3 AS tier, 1 AS src
		     FROM game_aliases a
		     JOIN aliases_fts af ON af.rowid = a.rowid
		     WHERE af.normalized_alias MATCH ?
		       AND a.game_id NOT IN (
		         SELECT g1.id FROM games g1 JOIN games_fts f1 ON f1.rowid = g1.id
		         WHERE f1.normalized_name MATCH ?)`)
		*args = append(*args, match, match)
	default: // "like"
		b.WriteString(`SELECT g0.id,
		       CASE WHEN g0.normalized_name = ? THEN 0
		            WHEN g0.normalized_name LIKE ? ESCAPE '\' THEN 1
		            WHEN g0.normalized_name LIKE ? ESCAPE '\' THEN 2
		            ELSE 3 END AS tier,
		       0 AS src
		     FROM games g0
		     WHERE g0.normalized_name LIKE ? ESCAPE '\'`)
		*args = append(*args, query, prefix, wordPrefix, like)

		b.WriteString(`
		     UNION ALL
		     SELECT a.game_id, 3 AS tier, 1 AS src
		     FROM game_aliases a
		     WHERE a.normalized_alias LIKE ? ESCAPE '\'
		       AND a.game_id NOT IN (
		         SELECT g1.id FROM games g1 WHERE g1.normalized_name LIKE ? ESCAPE '\')`)
		*args = append(*args, like, like)
	}
}

// buildFilterWhere emits the WHERE clause shared by results and COUNT queries
// (relevance floor + year/rating filters), or "" when nothing applies.
func buildFilterWhere(b *strings.Builder, args *[]interface{}, o searchOptions, query, prefix, wordPrefix string) {
	var conds []string
	if o.applyFloor {
		conds = append(conds,
			`(x.src = 1 OR g.normalized_name = ? OR g.normalized_name LIKE ? ESCAPE '\' OR g.normalized_name LIKE ? ESCAPE '\' OR g.popularity_score > 0)`)
		*args = append(*args, query, prefix, wordPrefix)
	}
	if o.yearFrom > 0 {
		conds = append(conds, `g.first_release_date >= ?`)
		*args = append(*args, o.yearFrom)
	}
	if o.yearTo > 0 {
		conds = append(conds, `g.first_release_date <= ?`)
		*args = append(*args, o.yearTo)
	}
	if o.minRating > 0 {
		conds = append(conds, `g.aggregated_rating >= ?`, `g.aggregated_rating_count > 0`)
		*args = append(*args, o.minRating)
	}
	if p := strings.TrimSpace(o.platform); p != "" {
		frag, fargs := PlatformFilter("g", p)
		conds = append(conds, frag)
		*args = append(*args, fargs...)
	}
	if len(conds) == 0 {
		return
	}
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(conds, " AND "))
}

// sortOrder maps the whitelisted sort key to its ORDER BY expression. The
// default ("" / "relevance") preserves the historical ranking: match tier,
// then main-game-over-DLC, then popularity tie-breakers.
func sortOrder(sort string) string {
	switch sort {
	case "release_new":
		return `CASE WHEN g.first_release_date = 0 THEN 1 ELSE 0 END,
		       g.first_release_date DESC, g.popularity_score DESC`
	case "release_old":
		return `CASE WHEN g.first_release_date = 0 THEN 1 ELSE 0 END,
		       g.first_release_date ASC, g.popularity_score DESC`
	case "rating":
		return `g.aggregated_rating DESC, g.aggregated_rating_count DESC, g.popularity_score DESC`
	case "popularity":
		return `g.popularity_score DESC, g.aggregated_rating_count DESC,
		       g.aggregated_rating DESC, g.first_release_date DESC`
	case "name":
		return `g.name COLLATE NOCASE ASC, g.popularity_score DESC`
	default:
		return `x.tier ASC, x.src ASC,
		       CASE WHEN g.category = 0 THEN 0 ELSE 1 END,
		       g.popularity_score DESC,
		       g.aggregated_rating_count DESC,
		       g.aggregated_rating DESC,
		       g.first_release_date DESC`
	}
}

// execSearch runs one engine variant: builds the results SQL (and the COUNT
// SQL when o.withTotal) from shared pieces, executes both, and scans rows.
func (s *Store) execSearch(ctx context.Context, engine, match, query, prefix, wordPrefix, like string, o searchOptions) ([]GameResult, int64, error) {
	var union strings.Builder
	unionArgs := make([]interface{}, 0, 16)
	buildSearchUnion(&union, &unionArgs, engine, match, query, prefix, wordPrefix, like)

	var where strings.Builder
	whereArgs := make([]interface{}, 0, len(unionArgs)+4)
	buildFilterWhere(&where, &whereArgs, o, query, prefix, wordPrefix)
	whereClause := where.String()

	var sql strings.Builder
	sql.WriteString(`SELECT g.id, g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date, g.platforms_json`)
	sql.WriteString(" FROM (")
	sql.WriteString(union.String())
	sql.WriteString(") x JOIN games g ON g.id = x.id")
	sql.WriteString(whereClause)
	sql.WriteString(" ORDER BY ")
	sql.WriteString(sortOrder(o.sort))
	sql.WriteString(" LIMIT ? OFFSET ?")
	finalArgs := append(append(append([]interface{}{}, unionArgs...), whereArgs...), o.limit, o.offset)

	results, err := s.querySearch(ctx, sql.String(), finalArgs)
	if err != nil {
		return nil, 0, fmt.Errorf("search games: %w", err)
	}

	var total int64
	if o.withTotal {
		var csql strings.Builder
		csql.WriteString("SELECT COUNT(*) FROM (")
		csql.WriteString(union.String())
		csql.WriteString(") x JOIN games g ON g.id = x.id")
		csql.WriteString(whereClause)
		countArgs := append(append([]interface{}{}, unionArgs...), whereArgs...)
		if err := s.db.QueryRowContext(ctx, csql.String(), countArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count search games: %w", err)
		}
	}
	return results, total, nil
}

func (s *Store) querySearch(ctx context.Context, sql string, args []interface{}) ([]GameResult, error) {
	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search games: %w", err)
	}
	defer rows.Close()

	// One tiny reference-table load per search (not per row) resolves IGDB
	// platform IDs to display names. An empty/missing table just yields
	// empty platform lists — never an error.
	names, _ := s.PlatformNames(ctx)

	var results []GameResult
	for rows.Next() {
		var g GameResult
		var platformsJSON string
		if err := rows.Scan(&g.ID, &g.Name, &g.Slug, &g.CoverURL, &g.LocalCoverPath, &g.FirstReleaseDate, &platformsJSON); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		g.Platforms = ResolvePlatformNames(platformsJSON, names)
		results = append(results, g)
	}
	return results, rows.Err()
}

func (s *Store) GetByID(ctx context.Context, id int64) (*Game, error) {
	var g Game
	err := s.db.QueryRowContext(ctx, `SELECT id, name, slug, safe_name, normalized_name, summary, storyline,
		cover_id, cover_url, local_cover_path, first_release_date, aggregated_rating, aggregated_rating_count,
		platforms_json, genres_json, trailer, igdb_url, source_updated_at,
		rating, rating_count, total_rating, total_rating_count, follows, hypes, igdb_popularity,
		category, status, version_parent, popularity_score
		FROM games WHERE id = ?`, id).Scan(
		&g.ID, &g.Name, &g.Slug, &g.SafeName, &g.NormalizedName,
		&g.Summary, &g.Storyline, &g.CoverID, &g.CoverURL, &g.LocalCoverPath,
		&g.FirstReleaseDate, &g.AggregatedRating, &g.AggregatedRatingCount,
		&g.PlatformsJSON, &g.GenresJSON, &g.Trailer, &g.IGDBURL, &g.SourceUpdatedAt,
		&g.Rating, &g.RatingCount, &g.TotalRating, &g.TotalRatingCount, &g.Follows,
		&g.Hypes, &g.IGDBPopularity, &g.Category, &g.Status, &g.VersionParent,
		&g.PopularityScore,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get game by id: %w", err)
	}
	return &g, nil
}

// UpsertIGDBGame inserts or refreshes a game row and replaces its alias set
// (game_aliases) in one transaction, so a crash can't leave stale aliases for
// a renamed game. The aliases_fts index is maintained by triggers.
// aliases_fetched_at is stamped because igdbFields always includes
// alternative_names.name — every full upsert carries the authoritative set.
func (s *Store) UpsertIGDBGame(ctx context.Context, g Game) error {
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `INSERT INTO games (
		id, name, slug, safe_name, normalized_name, summary, storyline,
		cover_id, cover_url, local_cover_path, first_release_date, aggregated_rating,
		aggregated_rating_count, platforms_json, genres_json, trailer,
		igdb_url, source_updated_at,
		rating, rating_count, total_rating, total_rating_count, follows, hypes,
		igdb_popularity, category, status, version_parent, popularity_score,
		popularity_fetched_at, aliases_fetched_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		slug = excluded.slug,
		safe_name = excluded.safe_name,
		normalized_name = excluded.normalized_name,
		summary = excluded.summary,
		storyline = excluded.storyline,
		cover_id = excluded.cover_id,
		cover_url = excluded.cover_url,
		first_release_date = excluded.first_release_date,
		aggregated_rating = excluded.aggregated_rating,
		aggregated_rating_count = excluded.aggregated_rating_count,
		platforms_json = excluded.platforms_json,
		genres_json = excluded.genres_json,
		trailer = excluded.trailer,
		igdb_url = excluded.igdb_url,
		source_updated_at = excluded.source_updated_at,
		rating = excluded.rating,
		rating_count = excluded.rating_count,
		total_rating = excluded.total_rating,
		total_rating_count = excluded.total_rating_count,
		follows = excluded.follows,
		hypes = excluded.hypes,
		igdb_popularity = excluded.igdb_popularity,
		category = excluded.category,
		status = excluded.status,
		version_parent = excluded.version_parent,
		popularity_score = excluded.popularity_score,
		popularity_fetched_at = excluded.popularity_fetched_at,
		aliases_fetched_at = excluded.aliases_fetched_at`,
		g.ID, g.Name, g.Slug, g.SafeName, g.NormalizedName,
		g.Summary, g.Storyline, g.CoverID, g.CoverURL,
		g.FirstReleaseDate, g.AggregatedRating, g.AggregatedRatingCount,
		g.PlatformsJSON, g.GenresJSON, g.Trailer, g.IGDBURL, g.SourceUpdatedAt,
		g.Rating, g.RatingCount, g.TotalRating, g.TotalRatingCount, g.Follows,
		g.Hypes, g.IGDBPopularity, g.Category, g.Status, g.VersionParent,
		g.PopularityScore, now, now,
	)
	if err != nil {
		return err
	}

	if err := replaceAliases(ctx, tx, g.ID, g.NormalizedName, g.Aliases); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceAliases swaps the stored alias set for a game: delete-all then
// insert-normalized. The game's own normalized name is skipped (searching it
// already hits the name branch; storing it would just duplicate rows).
func replaceAliases(ctx context.Context, tx *sql.Tx, gameID int64, normalizedName string, aliases []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_aliases WHERE game_id = ?`, gameID); err != nil {
		return err
	}
	seen := make(map[string]bool, len(aliases))
	for _, a := range aliases {
		na := NormalizeName(a)
		if na == "" || na == normalizedName || seen[na] {
			continue
		}
		seen[na] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO game_aliases (game_id, normalized_alias) VALUES (?, ?)`,
			gameID, na); err != nil {
			return err
		}
	}
	return nil
}

// GetAliasBackfillCandidates returns up to `limit` games whose alias set has
// never been fetched from IGDB. Ordered by id for stable, resumable paging.
func (s *Store) GetAliasBackfillCandidates(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM games WHERE aliases_fetched_at = 0 ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountPendingAliasBackfill reports how many rows still need their aliases
// fetched (for progress reporting in the backfill subcommand).
func (s *Store) CountPendingAliasBackfill(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM games WHERE aliases_fetched_at = 0`).Scan(&n)
	return n, err
}

// MarkAliasesFetched stamps the completion marker without touching aliases —
// used when IGDB no longer knows a game (deleted upstream), so the backfill
// queue advances instead of retrying forever.
func (s *Store) MarkAliasesFetched(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET aliases_fetched_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}

// SetAliasesAndMarkFetched writes an authoritative alias set and stamps the
// marker in one transaction. Used by the alias backfill, which fetches only
// id/name/alternative_names — unlike UpsertIGDBGame it deliberately leaves
// all other game columns untouched.
func (s *Store) SetAliasesAndMarkFetched(ctx context.Context, gameID int64, name string, aliases []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := replaceAliases(ctx, tx, gameID, NormalizeName(name), aliases); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE games SET aliases_fetched_at = ? WHERE id = ?`,
		time.Now().Unix(), gameID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetStaleGames(ctx context.Context, limit int) ([]int64, error) {
	// The ORDER BY here uses idx_games_source_updated, so this is an O(limit)
	// index scan rather than a full-table sort.  The previous version had a
	// correlated subquery (IN (SELECT DISTINCT game_id FROM library_items))
	// which forced a full-table sort of the games table — potentially a
	// multi-second hold on the single DB connection.
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM games
		WHERE source_updated_at > 0 AND source_updated_at < ?
		ORDER BY source_updated_at ASC LIMIT ?`,
		daysAgo(90), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func daysAgo(days int) int64 {
	return time.Now().AddDate(0, 0, -days).Unix()
}

// MarkRefreshed bumps source_updated_at to now so GetStaleGames stops
// re-selecting a row whose IGDB refresh permanently failed (e.g. the ID was
// deleted upstream). Without this, failed rows sit at the head of the stale
// queue forever and block newer games from ever being refreshed.
func (s *Store) MarkRefreshed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET source_updated_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}

// GetBackfillCandidates returns up to `limit` game IDs that have not yet had
// their popularity fields fetched, restricted to rows likely to matter for
// search ranking: anything with a non-zero critic rating count, or released
// within the last `recentYears`. The long tail of zero-rating obscure stubs
// is skipped entirely (their popularity_score stays 0, ranking them last).
// Resumable: a row is excluded once its popularity_fetched_at is non-zero.
func (s *Store) GetBackfillCandidates(ctx context.Context, limit int, recentYears int) ([]int64, error) {
	recentCutoff := time.Now().AddDate(-recentYears, 0, 0).Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM games
		WHERE popularity_fetched_at = 0
		  AND (aggregated_rating_count > 0 OR first_release_date > ?)
		ORDER BY aggregated_rating_count DESC, first_release_date DESC
		LIMIT ?`, recentCutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountPendingBackfill returns how many rows still need popularity backfill
// (for progress reporting in the backfill subcommand).
func (s *Store) CountPendingBackfill(ctx context.Context, recentYears int) (int64, error) {
	recentCutoff := time.Now().AddDate(-recentYears, 0, 0).Unix()
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM games
		WHERE popularity_fetched_at = 0
		  AND (aggregated_rating_count > 0 OR first_release_date > ?)`, recentCutoff).Scan(&n)
	return n, err
}

// MarkPopularityFetched records that an IGDB lookup was attempted for `id`,
// even if it returned nothing, so the backfill loop doesn't retry forever.
func (s *Store) MarkPopularityFetched(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET popularity_fetched_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}

func (s *Store) EnqueueCoverJob(ctx context.Context, gameID int64, sourceURL string) error {
	if sourceURL == "" {
		return nil
	}
	// ON CONFLICT DO UPDATE (not DO NOTHING): a job that exhausted its retries
	// under an old/broken URL must be revivable when the game is re-enqueued
	// with a corrected URL. Only reset when the job is exhausted or the URL
	// changed, so routine re-saves of a library item don't restart healthy
	// in-flight jobs.
	_, err := s.db.ExecContext(ctx, `INSERT INTO cover_jobs (game_id, source_url)
		VALUES (?, ?)
		ON CONFLICT(game_id) DO UPDATE SET
			source_url = excluded.source_url,
			attempts = 0,
			last_error = '',
			next_attempt_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE cover_jobs.attempts >= 5 OR cover_jobs.source_url != excluded.source_url`,
		gameID, sourceURL)
	return err
}

func (s *Store) EnqueueMissingCoverJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO cover_jobs (game_id, source_url)
		SELECT id, cover_url FROM games
		WHERE cover_url != '' AND id NOT IN (SELECT game_id FROM cover_jobs)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
