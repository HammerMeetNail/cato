package games

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"cato/internal/db"
)

type IGDBClient interface {
	SearchGames(ctx context.Context, query string, limit int) ([]Game, error)
	GetGame(ctx context.Context, id int64) (*Game, error)
	// GetGamesBatch fetches many games per rate-limited request (IGDB allows
	// up to 500 ids/query), requesting only id/name/alternative_names.
	GetGamesBatch(ctx context.Context, ids []int64) ([]Game, error)
	// GetPlatforms fetches the full platform reference list (id → name) in
	// a single request; used by SyncPlatforms to populate the lookup table.
	GetPlatforms(ctx context.Context) ([]Platform, error)
}

type Service struct {
	store *Store
	igdb  IGDBClient
	db    *db.DB
}

func NewService(store *Store, igdb IGDBClient, db *db.DB) *Service {
	return &Service{
		store: store,
		igdb:  igdb,
		db:    db,
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

	s.refreshFromIGDB(ctx, query)

	return s.store.SearchLocal(ctx, query, 10)
}

// SearchPaged performs a paginated search with a relevance floor applied to
// exclude weak (tier-3 substring) matches unless popular. On page 1 (offset=0),
// it runs the IGDB live fallback before returning results; on deeper pages,
// it returns pure local DB results (no IGDB hammering).
func (s *Service) SearchPaged(ctx context.Context, query string, limit, offset int) ([]GameResult, error) {
	results, _, err := s.SearchPagedFull(ctx, query, limit, offset, "", 0, 0, 0)
	return results, err
}

// SearchPagedFull is the full-results-page search: paginated, floored,
// optionally sorted/filtered (SEARCH_IMPROVEMENTS.md §4.4), and returning the
// total match count so the UI can display "N results". As in SearchPaged, the
// IGDB live fallback runs on page 1 only; deeper pages are pure local queries.
func (s *Service) SearchPagedFull(ctx context.Context, query string, limit, offset int, sort string, yearFrom, yearTo, minRating int64) ([]GameResult, int64, error) {
	query = NormalizeName(query)
	if len(query) < 2 {
		return nil, 0, nil
	}

	opts := func() searchOptions {
		return searchOptions{
			limit:     limit,
			offset:    offset,
			sort:      sort,
			yearFrom:  yearFrom,
			yearTo:    yearTo,
			minRating: minRating,
		}
	}

	local, total, err := s.store.SearchGamesPaged(ctx, query, opts())
	if err != nil {
		return nil, 0, err
	}

	// Only ask IGDB on page 1 and only if the query is long enough and not cached.
	if offset > 0 || !s.shouldAskIGDB(query) {
		return local, total, nil
	}

	// Page 1: refresh from IGDB, then re-query locally.
	s.refreshFromIGDB(ctx, query)

	return s.store.SearchGamesPaged(ctx, query, opts())
}

// refreshFromIGDB fetches a query from IGDB, upserts all results, and records
// the search in the cache. (The per-game "igdb:" cache entries the old version
// also wrote were never read by anything — removed.)
func (s *Service) refreshFromIGDB(ctx context.Context, query string) {
	remote, err := s.igdb.SearchGames(ctx, query, 10)
	if err != nil {
		return
	}

	for _, game := range remote {
		if err := s.store.UpsertIGDBGame(ctx, game); err != nil {
			continue
		}
		if game.CoverURL != "" {
			s.store.EnqueueCoverJob(ctx, game.ID, game.CoverURL)
		}
	}

	cacheSearchResultsDB(ctx, s.db, query, remote)
}

func (s *Service) GetGame(ctx context.Context, id int64) (*Game, error) {
	game, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if game != nil {
		names, _ := s.store.PlatformNames(ctx)
		game.Platforms = ResolvePlatformNames(game.PlatformsJSON, names)
	}
	return game, nil
}

// SyncPlatforms populates the platform ID → name lookup table from IGDB's
// /platforms endpoint — one rate-limited request covering every platform.
// Idempotent: skipped once the table has rows (the list changes ~yearly;
// a restart re-runs only if it was never fetched). Without a real IGDB
// client this is a no-op and platform IDs resolve to nothing (displayed as
// absent rather than wrong).
func (s *Service) SyncPlatforms(ctx context.Context) error {
	if n, err := s.store.CountPlatforms(ctx); err == nil && n > 0 {
		return nil
	}
	plats, err := s.igdb.GetPlatforms(ctx)
	if err != nil {
		return fmt.Errorf("fetch platforms: %w", err)
	}
	if err := s.store.UpsertPlatforms(ctx, plats); err != nil {
		return err
	}
	log.Printf("platform sync: populated %d platform names", len(plats))
	return nil
}

// StartPlatformSync runs SyncPlatforms in the background with bounded
// retries, so a transient network failure at container start doesn't leave
// platforms missing until the next deploy.
func (s *Service) StartPlatformSync() {
	go func() {
		ctx := context.Background()
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(15 * time.Second)
			}
			if err = s.SyncPlatforms(ctx); err == nil {
				return
			}
		}
		log.Printf("platform sync: giving up after retries: %v", err)
	}()
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

// StartCoverRepair runs RepairCovers once in the background at startup.
// Only call it when a real IGDB client is configured — without one there is
// nothing to re-fetch metadata from.
func (s *Service) StartCoverRepair() {
	go s.RepairCovers(context.Background())
}

// RepairCovers fixes cover data poisoned by earlier bugs:
//
//  1. Games whose cover_url points at images.themediapedia.com (a
//     decommissioned host referenced by ~2.7k rows of the legacy Postgres
//     import) can never load an image. Purging those URLs makes the UI fall
//     back to the placeholder; the stale-refresh loop and search repopulate
//     valid IGDB CDN URLs over time. Matching cover_jobs are deleted — their
//     source_url is just as dead and would only burn retries.
//
//  2. Cover jobs that exhausted their 5 retries did so under URLs guessed
//     from the numeric cover ID (see internal/igdb client). Re-fetching each
//     such game from IGDB now yields a URL built from the authoritative
//     covers.image_id; EnqueueCoverJob revives the exhausted job when the URL
//     differs. Jobs whose game no longer exists upstream are removed.
func (s *Service) RepairCovers(ctx context.Context) {
	purged, err := PurgeDeadCoverSources(s.db)
	if err != nil {
		log.Printf("cover repair: purge dead sources failed: %v", err)
	} else if purged > 0 {
		log.Printf("cover repair: cleared %d games pointing at dead cover host", purged)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT game_id FROM cover_jobs WHERE attempts >= 5`)
	if err != nil {
		log.Printf("cover repair: list exhausted jobs failed: %v", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	repaired, dropped := 0, 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}

		game, err := s.igdb.GetGame(ctx, id)
		if err != nil {
			// Transient failure — leave the job for the next startup.
			log.Printf("cover repair: refetch game %d failed: %v", id, err)
			continue
		}
		if game == nil {
			// Game deleted upstream; its job can never succeed.
			s.db.ExecContext(ctx, "DELETE FROM cover_jobs WHERE game_id = ?", id)
			dropped++
			continue
		}

		if err := s.store.UpsertIGDBGame(ctx, *game); err != nil {
			log.Printf("cover repair: upsert game %d failed: %v", id, err)
			continue
		}
		if game.CoverURL == "" {
			s.db.ExecContext(ctx, "DELETE FROM cover_jobs WHERE game_id = ?", id)
			dropped++
			continue
		}
		// Revives because attempts >= 5 (or the URL changed).
		s.store.EnqueueCoverJob(ctx, game.ID, game.CoverURL)
		repaired++
	}

	if len(ids) > 0 {
		log.Printf("cover repair: %d exhausted jobs examined: %d revived, %d dropped", len(ids), repaired, dropped)
	}
}

// PurgeDeadCoverSources clears cover URLs pointing at the defunct
// images.themediapedia.com host (legacy Postgres import) and removes matching
// cover_jobs. Safe to run repeatedly; returns the number of games updated.
func PurgeDeadCoverSources(database *db.DB) (int64, error) {
	res, err := database.Exec(`UPDATE games SET cover_url = ''
		WHERE cover_url LIKE '%//images.themediapedia.com/%'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	database.Exec(`DELETE FROM cover_jobs WHERE source_url LIKE '%//images.themediapedia.com/%'`)
	return n, nil
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

// BackfillPopularity walks backfill-candidate rows (see GetBackfillCandidates)
// and re-fetches each from IGDB so the new popularity fields (follows, hypes,
// total_rating_count, category, status) get populated. Respects the IGDB rate
// limiter baked into the client (~1 req/sec). Resumable: each successfully
// upserted row gets popularity_fetched_at set, so re-running skips done rows.
// `progress` is called after each batch with (done, total) for logging.
func (s *Service) BackfillPopularity(ctx context.Context, batchSize, recentYears int, progress func(done, total int)) (int, error) {
	pending, err := s.store.CountPendingBackfill(ctx, recentYears)
	if err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	total := int(pending)
	done := 0
	progress(done, total)

	for {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		ids, err := s.store.GetBackfillCandidates(ctx, batchSize, recentYears)
		if err != nil {
			return done, fmt.Errorf("get candidates: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		for _, id := range ids {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}

			game, err := s.igdb.GetGame(ctx, id)
			if err != nil {
				log.Printf("backfill: game %d failed: %v", id, err)
				continue
			}
			if game == nil {
				// IGDB no longer knows this ID; mark fetched so we don't
				// retry it forever.
				s.store.MarkPopularityFetched(ctx, id)
				done++
				continue
			}

			if err := s.store.UpsertIGDBGame(ctx, *game); err != nil {
				log.Printf("backfill: upsert game %d failed: %v", id, err)
				continue
			}
			done++
		}
		progress(done, total)
	}
	return done, nil
}

// backfillRetryDelays spaces out retries when IGDB rejects a batch (e.g. a
// 429 caused by another process's requests sharing the API key — the rate
// limiter is per-process). Bounded so an unattended run either rides out
// transient throttling or fails fast with progress already saved.
var backfillRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

func (s *Service) fetchBatchWithRetry(ctx context.Context, ids []int64) ([]Game, error) {
	for attempt := 0; ; attempt++ {
		games, err := s.igdb.GetGamesBatch(ctx, ids)
		if err == nil {
			return games, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt >= len(backfillRetryDelays) {
			return nil, err
		}
		delay := backfillRetryDelays[attempt]
		log.Printf("backfill: batch of %d failed (%v) — retrying in %v", len(ids), err, delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// BackfillAliases populates game_aliases for every catalog row in bulk,
// fetching up to `batchSize` (max 500, IGDB's per-query id cap) games per
// rate-limited request. Resumable: each processed row gets aliases_fetched_at
// stamped — including rows IGDB no longer knows — so re-running skips done
// rows and interrupted runs continue where they left off. Unlike
// BackfillPopularity this never touches other game columns. `progress` is
// called after each batch with (done, total) for logging.
func (s *Service) BackfillAliases(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	if batchSize > 500 {
		batchSize = 500
	}

	pending, err := s.store.CountPendingAliasBackfill(ctx)
	if err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	total := int(pending)
	done := 0
	progress(done, total)

	for {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		ids, err := s.store.GetAliasBackfillCandidates(ctx, batchSize)
		if err != nil {
			return done, fmt.Errorf("get candidates: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		games, err := s.fetchBatchWithRetry(ctx, ids)
		if err != nil {
			// Persistent failure (retries exhausted): stop and let the
			// operator re-run — the marker column makes that cheap and
			// race-free.
			return done, fmt.Errorf("batch fetch at %d/%d: %w", done, total, err)
		}

		returned := make(map[int64]bool, len(games))
		for _, g := range games {
			if err := s.store.SetAliasesAndMarkFetched(ctx, g.ID, g.Name, g.Aliases); err != nil {
				log.Printf("backfill: set aliases for %d failed: %v", g.ID, err)
				continue
			}
			returned[g.ID] = true
			done++
		}
		// IDs absent from the response are gone upstream; stamp them so the
		// queue advances instead of re-selecting the same head rows forever.
		for _, id := range ids {
			if !returned[id] {
				if err := s.store.MarkAliasesFetched(ctx, id); err != nil {
					log.Printf("backfill: mark %d failed: %v", id, err)
					continue
				}
				done++
			}
		}
		progress(done, total)
	}
	return done, nil
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
		game, err := s.igdb.GetGame(ctx, id)
		if err != nil {
			log.Printf("stale refresh: game %d failed: %v", id, err)
			continue
		}
		if game == nil {
			// IGDB no longer knows this ID (deleted upstream). Mark it
			// refreshed so the queue advances — otherwise the same rows are
			// re-selected every cycle forever, starving newer games.
			if err := s.store.MarkRefreshed(ctx, id); err != nil {
				log.Printf("stale refresh: marking game %d failed: %v", id, err)
			}
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

// PurgeExpiredQueryCache deletes igdb_query_cache rows past their expiry.
// The read path deletes lazily per-key; this sweep keeps the table bounded
// overall. Call it periodically (e.g. from a daily maintenance ticker).
func PurgeExpiredQueryCache(database *db.DB) (int64, error) {
	res, err := database.Exec("DELETE FROM igdb_query_cache WHERE expires_at < ?",
		time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
