package games

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"cato/internal/db"
)

type Store struct {
	db *db.DB
}

func NewStore(db *db.DB) *Store {
	return &Store{db: db}
}

// searchSQL is the primary search path: FTS5 trigram MATCH on normalized_name,
// joined back to the games rowid, ranked by (name-match tier, main-game vs
// DLC, popularity_score, then existing tie-breakers). See SEARCH_PLAN.md §3.
const searchSQL = `SELECT g.id, g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date
FROM games g
JOIN games_fts f ON f.rowid = g.id
WHERE f.normalized_name MATCH ?1
ORDER BY
  CASE
    WHEN g.normalized_name = ?2 THEN 0
    WHEN g.normalized_name LIKE ?3 THEN 1
    WHEN g.normalized_name LIKE ?4 THEN 2
    ELSE 3
  END,
  CASE WHEN g.category = 0 THEN 0 ELSE 1 END,
  g.popularity_score DESC,
  g.aggregated_rating_count DESC,
  g.aggregated_rating DESC,
  g.first_release_date DESC
LIMIT ?5`

// searchLikeFallback preserves the pre-FTS behavior for queries too short for
// the trigram tokenizer (< 3 chars) or if the FTS table is unavailable.
const searchLikeFallback = `SELECT id, name, slug, cover_url, local_cover_path, first_release_date
FROM games
WHERE normalized_name LIKE ?1
ORDER BY
  CASE
    WHEN normalized_name = ?2 THEN 0
    WHEN normalized_name LIKE ?3 THEN 1
    WHEN normalized_name LIKE ?4 THEN 2
    ELSE 3
  END,
  CASE WHEN category = 0 THEN 0 ELSE 1 END,
  popularity_score DESC,
  aggregated_rating_count DESC,
  aggregated_rating DESC,
  first_release_date DESC
LIMIT ?5`

func (s *Store) SearchLocal(ctx context.Context, query string, limit int) ([]GameResult, error) {
	if limit <= 0 {
		limit = 10
	}

	like := "%" + query + "%"
	prefix := query + "%"
	wordPrefix := "% " + query + "%"

	if match, ok := BuildFTSMatch(query); ok {
		results, err := s.querySearch(ctx, searchSQL, []interface{}{match, query, prefix, wordPrefix, limit})
		if err == nil {
			return results, nil
		}
		// FTS table missing or query error: fall through to the LIKE path.
		// This keeps search working on databases migrated before v5 or if
		// the FTS virtual table is ever dropped.
	}
	return s.querySearch(ctx, searchLikeFallback, []interface{}{like, query, prefix, wordPrefix, limit})
}

func (s *Store) querySearch(ctx context.Context, sql string, args []interface{}) ([]GameResult, error) {
	rows, err := s.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search games: %w", err)
	}
	defer rows.Close()

	var results []GameResult
	for rows.Next() {
		var g GameResult
		if err := rows.Scan(&g.ID, &g.Name, &g.Slug, &g.CoverURL, &g.LocalCoverPath, &g.FirstReleaseDate); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
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

func (s *Store) UpsertIGDBGame(ctx context.Context, g Game) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO games (
		id, name, slug, safe_name, normalized_name, summary, storyline,
		cover_id, cover_url, local_cover_path, first_release_date, aggregated_rating,
		aggregated_rating_count, platforms_json, genres_json, trailer,
		igdb_url, source_updated_at,
		rating, rating_count, total_rating, total_rating_count, follows, hypes,
		igdb_popularity, category, status, version_parent, popularity_score,
		popularity_fetched_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		popularity_fetched_at = excluded.popularity_fetched_at`,
		g.ID, g.Name, g.Slug, g.SafeName, g.NormalizedName,
		g.Summary, g.Storyline, g.CoverID, g.CoverURL,
		g.FirstReleaseDate, g.AggregatedRating, g.AggregatedRatingCount,
		g.PlatformsJSON, g.GenresJSON, g.Trailer, g.IGDBURL, g.SourceUpdatedAt,
		g.Rating, g.RatingCount, g.TotalRating, g.TotalRatingCount, g.Follows,
		g.Hypes, g.IGDBPopularity, g.Category, g.Status, g.VersionParent,
		g.PopularityScore, now,
	)
	return err
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO cover_jobs (game_id, source_url)
		VALUES (?, ?) ON CONFLICT(game_id) DO NOTHING`, gameID, sourceURL)
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
