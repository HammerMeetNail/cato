package games

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) SearchLocal(ctx context.Context, query string, limit int) ([]GameResult, error) {
	if limit <= 0 {
		limit = 10
	}

	like := "%" + query + "%"
	sql := `SELECT id, name, slug, cover_url, local_cover_path, first_release_date
FROM games
WHERE normalized_name LIKE ?1
ORDER BY
  CASE
    WHEN normalized_name = ?2 THEN 0
    WHEN normalized_name LIKE ?3 THEN 1
    WHEN normalized_name LIKE ?4 THEN 2
    ELSE 3
  END,
  aggregated_rating_count DESC,
  aggregated_rating DESC,
  first_release_date DESC
LIMIT ?5`

	prefix := query + "%"
	wordPrefix := "% " + query + "%"

	rows, err := s.db.QueryContext(ctx, sql, like, query, prefix, wordPrefix, limit)
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
		platforms_json, genres_json, trailer, igdb_url, source_updated_at
		FROM games WHERE id = ?`, id).Scan(
		&g.ID, &g.Name, &g.Slug, &g.SafeName, &g.NormalizedName,
		&g.Summary, &g.Storyline, &g.CoverID, &g.CoverURL, &g.LocalCoverPath,
		&g.FirstReleaseDate, &g.AggregatedRating, &g.AggregatedRatingCount,
		&g.PlatformsJSON, &g.GenresJSON, &g.Trailer, &g.IGDBURL, &g.SourceUpdatedAt,
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO games (
		id, name, slug, safe_name, normalized_name, summary, storyline,
		cover_id, cover_url, first_release_date, aggregated_rating,
		aggregated_rating_count, platforms_json, genres_json, trailer,
		igdb_url, source_updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		source_updated_at = excluded.source_updated_at`,
		g.ID, g.Name, g.Slug, g.SafeName, g.NormalizedName,
		g.Summary, g.Storyline, g.CoverID, g.CoverURL,
		g.FirstReleaseDate, g.AggregatedRating, g.AggregatedRatingCount,
		g.PlatformsJSON, g.GenresJSON, g.Trailer, g.IGDBURL, g.SourceUpdatedAt,
	)
	return err
}

func (s *Store) GetStaleGames(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id FROM games g
		WHERE g.source_updated_at > 0 AND g.source_updated_at < ?
		ORDER BY CASE WHEN g.id IN (SELECT DISTINCT game_id FROM library_items) THEN 0 ELSE 1 END,
		g.source_updated_at ASC LIMIT ?`,
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

func (s *Store) EnsqueueCoverJob(ctx context.Context, gameID int64, sourceURL string) error {
	if sourceURL == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cover_jobs (game_id, source_url)
		VALUES (?, ?) ON CONFLICT(game_id) DO NOTHING`, gameID, sourceURL)
	return err
}
