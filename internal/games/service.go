package games

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"cato/internal/db"
)

type IGDBClient interface {
	SearchGames(ctx context.Context, query string, limit int) ([]Game, error)
	GetGame(ctx context.Context, id int64) (*Game, error)
}

type Service struct {
	store       *Store
	igdb        IGDBClient
	db          *db.DB
	rateLimiter *IGDBRateLimiter
}

func NewService(store *Store, igdb IGDBClient, db *db.DB) *Service {
	return &Service{
		store:       store,
		igdb:        igdb,
		db:          db,
		rateLimiter: NewIGDBRateLimiter(),
	}
}

func (s *Service) Search(ctx context.Context, query string) ([]GameResult, error) {
	query = NormalizeName(query)
	if len(query) < 2 {
		return nil, nil
	}

	local, err := s.store.SearchLocal(ctx, query, 10)
	if err != nil {
		return nil, err
	}
	if !s.shouldAskIGDB(query) {
		return local, nil
	}

	remote, err := s.igdb.SearchGames(ctx, query, 10)
	if err != nil {
		return local, nil
	}

	for _, game := range remote {
		if err := s.store.UpsertIGDBGame(ctx, game); err != nil {
			continue
		}
		if game.CoverURL != "" {
			s.store.EnqueueCoverJob(ctx, game.ID, game.CoverURL)
		}

		cacheKey := "igdb:" + NormalizeName(game.Name)
		s.cacheIGDBGame(ctx, cacheKey, game)
	}

	cacheSearchResultsDB(ctx, s.db, query, remote)

	return s.store.SearchLocal(ctx, query, 10)
}

func (s *Service) GetGame(ctx context.Context, id int64) (*Game, error) {
	game, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return game, nil
}

func (s *Service) StartStaleRefresh() {
	const maxPerDay = 100
	const interval = 6 * time.Hour

	go func() {
		for {
			s.refreshStaleGames(maxPerDay)
			time.Sleep(interval)
		}
	}()
}

func (s *Service) EnqueueMissingCovers() {
	ctx := context.Background()
	count, err := s.store.EnqueueMissingCoverJobs(ctx)
	if err != nil {
		log.Printf("cover backfill: failed to enqueue missing cover jobs: %v", err)
		return
	}
	if count > 0 {
		log.Printf("cover backfill: enqueued %d cover download jobs", count)
	}
}

func (s *Service) refreshStaleGames(maxPerDay int) {
	ctx := context.Background()

	ids, err := s.store.GetStaleGames(ctx, maxPerDay)
	if err != nil {
		log.Printf("stale refresh: failed to get stale games: %v", err)
		return
	}

	if len(ids) == 0 {
		return
	}

	log.Printf("stale refresh: refreshing %d games older than 90 days", len(ids))

	refreshed := 0
	for _, id := range ids {
		s.rateLimiter.Wait()

		game, err := s.igdb.GetGame(ctx, id)
		if err != nil {
			log.Printf("stale refresh: game %d failed: %v", id, err)
			continue
		}
		if game == nil {
			continue
		}

		if err := s.store.UpsertIGDBGame(ctx, *game); err != nil {
			log.Printf("stale refresh: upsert game %d failed: %v", id, err)
			continue
		}

		if game.CoverURL != "" {
			s.store.EnqueueCoverJob(ctx, game.ID, game.CoverURL)
		}

		refreshed++
	}

	log.Printf("stale refresh: refreshed %d/%d games", refreshed, len(ids))
}

func (s *Service) shouldAskIGDB(query string) bool {
	cached, err := getCachedSearchDB(context.Background(), s.db, query)
	if err == nil && cached {
		return false
	}
	return len(query) >= 3
}

func (s *Service) cacheIGDBGame(ctx context.Context, key string, game Game) {
	data, _ := json.Marshal(game)
	s.db.ExecContext(ctx, `INSERT OR REPLACE INTO igdb_query_cache (normalized_query, response_json, expires_at)
		VALUES (?, ?, ?)`, key, string(data), time.Now().Add(24*time.Hour).Format(time.RFC3339))
}

func cacheSearchResultsDB(ctx context.Context, db *db.DB, query string, games []Game) {
	if len(games) == 0 {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{"query": query, "cached": true})
	db.ExecContext(ctx, `INSERT OR REPLACE INTO igdb_query_cache (normalized_query, response_json, expires_at)
		VALUES (?, ?, ?)`, "search:"+query, string(data), time.Now().Add(24*time.Hour).Format(time.RFC3339))
}

func getCachedSearchDB(ctx context.Context, db *db.DB, query string) (bool, error) {
	var expiresAt string
	err := db.QueryRowContext(ctx,
		"SELECT expires_at FROM igdb_query_cache WHERE normalized_query = ?",
		"search:"+query).Scan(&expiresAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false, nil
	}
	if time.Now().After(t) {
		db.ExecContext(ctx, "DELETE FROM igdb_query_cache WHERE normalized_query = ?", "search:"+query)
		return false, nil
	}
	return true, nil
}
