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

// Search is composed at query time: direct name and alias matches are
// materialized once, then branch-local filters produce the candidate set used
// for both the page and its window count.

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
	limit           int
	offset          int
	applyFloor      bool // hide weak tier-3 substring matches unless popular
	sort            string
	yearFrom        int64  // unix seconds, inclusive; 0 = unset
	yearTo          int64  // unix seconds, inclusive; 0 = unset
	minRating       int64  // aggregated_rating >= minRating with count > 0; 0 = unset
	platform        string // availability filter: substring of platform name/abbrev
	tags            []string
	tagOp           string // "and" (default) or "or"
	libraryUserID   string // when set, enable library-scoped filters below
	inLibrary       *bool  // nil = no filter; true = only owned, false = not owned
	libraryStatus   string // when set with libraryUserID, filter to that library status
	withTotal       bool   // include a total via COUNT(*) OVER()
	includeEditions bool   // when false, hide IGDB editions (version_parent != 0) unless query explicitly asks for one
}

func (s *Store) SearchLocal(ctx context.Context, query string, limit int) ([]GameResult, error) {
	results, _, err := s.search(ctx, query, searchOptions{limit: limit})
	return results, err
}

// SearchLocalWithEditions is the edition-aware variant of SearchLocal.
// When includeEditions is false, edition rows (version_parent != 0) are
// hidden unless the query itself explicitly asks for an edition.
func (s *Store) SearchLocalWithEditions(ctx context.Context, query string, limit int, includeEditions bool) ([]GameResult, error) {
	results, _, err := s.search(ctx, query, searchOptions{limit: limit, includeEditions: includeEditions})
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

// ValidLibraryStatuses whitelists library status values for search filtering.
var ValidLibraryStatuses = map[string]bool{
	"wishlist":  true,
	"backlog":   true,
	"playing":   true,
	"completed": true,
	"abandoned": true,
}

// appendGameFilterWhere emits filters for one candidate branch. Direct name
// matches get the relevance floor; alias and parent matches are already strong
// signals and bypass it. Keeping this predicate inside each branch removes
// rows before the final sort and window count on broad searches.
func appendGameFilterWhere(b *strings.Builder, args *[]interface{}, o searchOptions, query, prefix, wordPrefix string, directName bool) {
	var conds []string
	if o.applyFloor && directName {
		conds = append(conds,
			`(g.normalized_name = ? OR g.normalized_name LIKE ? ESCAPE '\' OR g.normalized_name LIKE ? ESCAPE '\' OR g.popularity_score > 0)`)
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
	if len(o.tags) > 0 && o.libraryUserID != "" {
		placeholders := make([]string, len(o.tags))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		inClause := strings.Join(placeholders, ", ")
		if o.tagOp == "or" {
			conds = append(conds, `EXISTS (SELECT 1 FROM library_tags lt WHERE lt.game_id = g.id AND lt.user_id = ? AND lt.tag IN (`+inClause+`))`)
			*args = append(*args, o.libraryUserID)
			for _, t := range o.tags {
				*args = append(*args, t)
			}
		} else {
			conds = append(conds, `(SELECT COUNT(DISTINCT lt.tag) FROM library_tags lt WHERE lt.game_id = g.id AND lt.user_id = ? AND lt.tag IN (`+inClause+`)) = ?`)
			*args = append(*args, o.libraryUserID)
			for _, t := range o.tags {
				*args = append(*args, t)
			}
			*args = append(*args, len(o.tags))
		}
	}
	if o.libraryUserID != "" {
		if o.inLibrary != nil {
			if *o.inLibrary {
				conds = append(conds, `EXISTS (SELECT 1 FROM library_items li_f WHERE li_f.game_id = g.id AND li_f.user_id = ?)`)
			} else {
				conds = append(conds, `NOT EXISTS (SELECT 1 FROM library_items li_f WHERE li_f.game_id = g.id AND li_f.user_id = ?)`)
			}
			*args = append(*args, o.libraryUserID)
		}
		if o.libraryStatus != "" && ValidLibraryStatuses[o.libraryStatus] {
			conds = append(conds, `EXISTS (SELECT 1 FROM library_items li_s WHERE li_s.game_id = g.id AND li_s.user_id = ? AND li_s.status = ?)`)
			*args = append(*args, o.libraryUserID, o.libraryStatus)
		}
	}
	if !o.includeEditions && !ContainsEditionKeyword(query) {
		conds = append(conds, `g.version_parent = 0`)
	}
	if !o.includeEditions && !ContainsPackKeyword(query) {
		conds = append(conds, `g.category IN (0,1,2,4,8,9,10,11)`)
	}
	if len(conds) == 0 {
		b.WriteString(" WHERE 1")
		return
	}
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(conds, " AND "))
}

// buildSearchCandidates builds the CTE graph shared by the page query and
// the rare out-of-range count fallback. name_match and alias_match are kept
// unfiltered: parent discovery must see a parent even when the parent itself
// fails a child-specific date/platform/library filter.
//
// Arguments are appended in the exact order their placeholders appear in the
// generated SQL. The FTS join starts at the virtual table and uses CROSS JOIN
// to keep SQLite from choosing the full games table as the driving relation.
func buildSearchCandidates(b *strings.Builder, args *[]interface{}, engine, match, query, prefix, wordPrefix, like string, o searchOptions) {
	b.WriteString(`WITH name_match(id, tier) AS MATERIALIZED (`)
	switch engine {
	case "fts":
		b.WriteString(`SELECT g.id,
		       CASE WHEN g.normalized_name = ? THEN 0
		            WHEN g.normalized_name LIKE ? ESCAPE '\' THEN 1
		            WHEN g.normalized_name LIKE ? ESCAPE '\' THEN 2
		            ELSE 3 END
		     FROM games_fts f
		     CROSS JOIN games g
		     WHERE f.normalized_name MATCH ? AND g.id = f.rowid`)
		*args = append(*args, query, prefix, wordPrefix, match)
	default: // "like"
		b.WriteString(`SELECT g.id,
		       CASE WHEN g.normalized_name = ? THEN 0
		            WHEN g.normalized_name LIKE ? ESCAPE '\' THEN 1
		            WHEN g.normalized_name LIKE ? ESCAPE '\' THEN 2
		            ELSE 3 END
		     FROM games g
		     WHERE g.normalized_name LIKE ? ESCAPE '\'`)
		*args = append(*args, query, prefix, wordPrefix, like)
	}

	b.WriteString(`), alias_match(game_id) AS MATERIALIZED (`)
	if engine == "fts" {
		b.WriteString(`SELECT DISTINCT a.game_id
		     FROM aliases_fts af
		     CROSS JOIN game_aliases a
		     WHERE af.normalized_alias MATCH ? AND a.rowid = af.rowid`)
		*args = append(*args, match)
	} else {
		b.WriteString(`SELECT DISTINCT a.game_id
		     FROM game_aliases a
		     WHERE a.normalized_alias LIKE ? ESCAPE '\'`)
		*args = append(*args, like)
	}

	b.WriteString(`), parent_match(game_id) AS MATERIALIZED (
		     SELECT id FROM name_match
		     UNION
		     SELECT game_id FROM alias_match
		   ), eligible(id, tier, src) AS MATERIALIZED (
		     SELECT nm.id, nm.tier, 0
		     FROM name_match nm
		     CROSS JOIN games g`)
	appendGameFilterWhere(b, args, o, query, prefix, wordPrefix, true)
	b.WriteString(` AND g.id = nm.id`)

	b.WriteString(`
		     UNION ALL
		     SELECT am.game_id, 3, 1
		     FROM alias_match am
		     CROSS JOIN games g`)
	appendGameFilterWhere(b, args, o, query, prefix, wordPrefix, false)
	b.WriteString(` AND g.id = am.game_id`)
	b.WriteString(`
		       AND NOT EXISTS (SELECT 1 FROM name_match nm WHERE nm.id = g.id)

		     UNION ALL
		     SELECT g.id, 3, 2
		     FROM parent_match pm
		     CROSS JOIN games g`)
	appendGameFilterWhere(b, args, o, query, prefix, wordPrefix, false)
	b.WriteString(` AND g.parent_game = pm.game_id`)
	b.WriteString(`
		       AND NOT EXISTS (SELECT 1 FROM name_match nm WHERE nm.id = g.id)
		       AND NOT EXISTS (SELECT 1 FROM alias_match am WHERE am.game_id = g.id)
		   ) `)
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

// execSearch runs one engine variant. Full searches carry COUNT(*) OVER() on
// every returned row, avoiding a second broad query. If OFFSET is beyond the
// end there is no row carrying the window value, so only that case uses a
// count fallback.
func (s *Store) execSearch(ctx context.Context, engine, match, query, prefix, wordPrefix, like string, o searchOptions) ([]GameResult, int64, error) {
	var candidates strings.Builder
	candidateArgs := make([]interface{}, 0, 32)
	buildSearchCandidates(&candidates, &candidateArgs, engine, match, query, prefix, wordPrefix, like, o)

	var sql strings.Builder
	sql.WriteString(`SELECT g.id, g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date, g.platforms_json`)
	if o.withTotal {
		sql.WriteString(", COUNT(*) OVER() AS total_count")
	}
	sql.WriteString(" FROM eligible x JOIN games g ON g.id = x.id")
	sql.WriteString(" ORDER BY ")
	sql.WriteString(sortOrder(o.sort))
	sql.WriteString(" LIMIT ? OFFSET ?")
	finalArgs := append(append([]interface{}{}, candidateArgs...), o.limit, o.offset)

	results, total, err := s.querySearch(ctx, candidates.String()+sql.String(), finalArgs, o.withTotal)
	if err != nil {
		return nil, 0, fmt.Errorf("search games: %w", err)
	}

	if o.withTotal && len(results) == 0 && o.offset > 0 {
		countArgs := append([]interface{}{}, candidateArgs...)
		if err := s.db.QueryRowContext(ctx, candidates.String()+"SELECT COUNT(*) FROM eligible", countArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count search games: %w", err)
		}
	}
	return results, total, nil
}

func (s *Store) querySearch(ctx context.Context, sql string, args []interface{}, withTotal bool) ([]GameResult, int64, error) {
	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search games: %w", err)
	}
	defer rows.Close()

	// One tiny reference-table load per search (not per row) resolves IGDB
	// platform IDs to display names. An empty/missing table just yields
	// empty platform lists — never an error.
	names, _ := s.PlatformNames(ctx)

	var results []GameResult
	var total int64
	for rows.Next() {
		var g GameResult
		var platformsJSON string
		scanArgs := []interface{}{&g.ID, &g.Name, &g.Slug, &g.CoverURL, &g.LocalCoverPath, &g.FirstReleaseDate, &platformsJSON}
		var rowTotal int64
		if withTotal {
			scanArgs = append(scanArgs, &rowTotal)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, 0, fmt.Errorf("scan game: %w", err)
		}
		if withTotal {
			total = rowTotal
		}
		g.Platforms = ResolvePlatformNames(platformsJSON, names)
		results = append(results, g)
	}
	return results, total, rows.Err()
}

func (s *Store) GetByID(ctx context.Context, id int64) (*Game, error) {
	var g Game
	err := s.db.QueryRowContext(ctx, `SELECT id, name, slug, safe_name, normalized_name, summary, storyline,
		cover_id, cover_url, local_cover_path, first_release_date, aggregated_rating, aggregated_rating_count,
		platforms_json, genres_json, trailer, igdb_url, source_updated_at,
		rating, rating_count, total_rating, total_rating_count, follows, hypes, igdb_popularity,
		category, status, version_parent, parent_game, popularity_score
		FROM games WHERE id = ?`, id).Scan(
		&g.ID, &g.Name, &g.Slug, &g.SafeName, &g.NormalizedName,
		&g.Summary, &g.Storyline, &g.CoverID, &g.CoverURL, &g.LocalCoverPath,
		&g.FirstReleaseDate, &g.AggregatedRating, &g.AggregatedRatingCount,
		&g.PlatformsJSON, &g.GenresJSON, &g.Trailer, &g.IGDBURL, &g.SourceUpdatedAt,
		&g.Rating, &g.RatingCount, &g.TotalRating, &g.TotalRatingCount, &g.Follows,
		&g.Hypes, &g.IGDBPopularity, &g.Category, &g.Status, &g.VersionParent,
		&g.ParentGame, &g.PopularityScore,
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
		igdb_popularity, category, status, version_parent, parent_game, popularity_score,
		popularity_fetched_at, aliases_fetched_at, version_parent_fetched_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		parent_game = excluded.parent_game,
		popularity_score = excluded.popularity_score,
		popularity_fetched_at = excluded.popularity_fetched_at,
		aliases_fetched_at = excluded.aliases_fetched_at,
		version_parent_fetched_at = excluded.version_parent_fetched_at`,
		g.ID, g.Name, g.Slug, g.SafeName, g.NormalizedName,
		g.Summary, g.Storyline, g.CoverID, g.CoverURL,
		g.FirstReleaseDate, g.AggregatedRating, g.AggregatedRatingCount,
		g.PlatformsJSON, g.GenresJSON, g.Trailer, g.IGDBURL, g.SourceUpdatedAt,
		g.Rating, g.RatingCount, g.TotalRating, g.TotalRatingCount, g.Follows,
		g.Hypes, g.IGDBPopularity, g.Category, g.Status, g.VersionParent,
		g.ParentGame, g.PopularityScore, now, now, now,
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

// GetCoverRepairCandidates returns up to `limit` games whose cover_url was
// likely built by guessing co%05d from the numeric cover_id rather than from
// cover.image_id. Those URLs look like .../co<digits>.jpg where the digits
// match the stored cover_id (zero-padded). They are 404-prone whenever IGDB's
// image_id is a hash like "cobmj0" rather than the numeric id. The candidate
// set is intentionally broad (any co+digits URL) — re-fetching a correct
// numeric URL is harmless, while missing a wrong one leaves a broken cover.
// Ordered by id for stable, resumable paging.
func (s *Store) GetCoverRepairCandidates(ctx context.Context, limit int) ([]int64, error) {
	// GLOB '*co[0-9]*.jpg' catches numeric image_ids (co542412) but not hash
	// ones where the first char after "co" is a letter (cobmj0). Hashes like
	// co81qo (digit then letters) still match, which is fine — re-checking
	// them is cheap and verifies they are still correct.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM games WHERE cover_url GLOB '*co[0-9]*.jpg' AND cover_id != 0 ORDER BY id ASC LIMIT ?`, limit)
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

// CountPendingCoverRepair reports how many rows still look like guessed cover
// URLs (for progress reporting).
func (s *Store) CountPendingCoverRepair(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM games WHERE cover_url GLOB '*co[0-9]*.jpg' AND cover_id != 0`).Scan(&n)
	return n, err
}

// GetCoverRepairCandidatesAfter returns the next `limit` guessed-cover
// candidates after `afterID` (exclusive), ordered by id. Used by the full
// backfill to make steady forward progress without re-checking already-
// visited rows that are correctly numeric (co96746) and thus still match the
// GLOB.
func (s *Store) GetCoverRepairCandidatesAfter(ctx context.Context, afterID int64, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM games WHERE cover_url GLOB '*co[0-9]*.jpg' AND cover_id != 0 AND id > ? ORDER BY id ASC LIMIT ?`, afterID, limit)
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

// SetCoverAndMarkFetched writes a corrected cover_url/cover_id without
// touching other game columns. Used by the cover backfill, which fetches only
// cover fields — unlike UpsertIGDBGame it leaves name, ratings, etc. alone.
func (s *Store) SetCoverAndMarkFetched(ctx context.Context, gameID int64, coverID int64, coverURL string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET cover_id = ?, cover_url = ? WHERE id = ?`,
		coverID, coverURL, gameID)
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

// GetEditionBackfillCandidates returns up to `limit` games whose
// version_parent has never been fetched from IGDB. Ordered by id for
// stable, resumable paging. Used by backfill-editions to populate legacy
// rows imported with version_parent=0.
func (s *Store) GetEditionBackfillCandidates(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM games WHERE version_parent_fetched_at = 0 ORDER BY id ASC LIMIT ?`, limit)
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

// CountPendingEditionBackfill reports how many rows still need their
// version_parent fetched (for progress reporting).
func (s *Store) CountPendingEditionBackfill(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM games WHERE version_parent_fetched_at = 0`).Scan(&n)
	return n, err
}

// MarkEditionFetched stamps the completion marker without touching the
// version_parent — used when IGDB no longer knows a game (deleted
// upstream), so the backfill queue advances.
func (s *Store) MarkEditionFetched(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET version_parent_fetched_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}

// SetVersionParentAndMarkFetched writes the authoritative version_parent
// and stamps the marker in one statement. Used by the edition backfill,
// which fetches only id/version_parent/version_title.
func (s *Store) SetVersionParentAndMarkFetched(ctx context.Context, gameID int64, versionParent int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET version_parent = ?, version_parent_fetched_at = ? WHERE id = ?`,
		versionParent, time.Now().Unix(), gameID)
	return err
}

// SetEditionInfoAndMarkFetched writes version_parent, category and
// parent_game together and stamps the edition marker. Used when the batch
// fetch returns the authoritative game_type/parent_game alongside
// version_parent.
func (s *Store) SetEditionInfoAndMarkFetched(ctx context.Context, gameID int64, versionParent, category, parentGame int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET version_parent = ?, category = ?, parent_game = ?, version_parent_fetched_at = ? WHERE id = ?`,
		versionParent, category, parentGame, time.Now().Unix(), gameID)
	return err
}

// CountTotalGames returns the total number of games (for full backfills).
func (s *Store) CountTotalGames(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM games`).Scan(&n)
	return n, err
}

// GetGameIDsAfter returns up to `limit` game IDs after `afterID` (exclusive),
// ordered by id. Used by full-catalog backfills that must touch every row
// regardless of fetched markers.
func (s *Store) GetGameIDsAfter(ctx context.Context, afterID int64, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM games WHERE id > ? ORDER BY id ASC LIMIT ?`, afterID, limit)
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

// UpdateCategoryAndParentGame writes category and parent_game without
// touching the edition marker. Used by the category backfill for rows
// already marked as edition-fetched but with stale category.
func (s *Store) UpdateCategoryAndParentGame(ctx context.Context, gameID int64, category, parentGame int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET category = ?, parent_game = ? WHERE id = ?`,
		category, parentGame, gameID)
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
