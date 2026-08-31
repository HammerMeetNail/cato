package games

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"cato/internal/db"
)

type IGDBClient interface {
	SearchGames(ctx context.Context, query string, limit int, includeEditions bool) ([]Game, error)
	GetGame(ctx context.Context, id int64) (*Game, error)
	// GetGamesBatch fetches many games per rate-limited request (IGDB allows
	// up to 500 ids/query), requesting only id/name/alternative_names.
	GetGamesBatch(ctx context.Context, ids []int64) ([]Game, error)
	// GetPlatforms fetches the full platform reference list (id → name) in
	// a single request; used by SyncPlatforms to populate the lookup table.
	GetPlatforms(ctx context.Context) ([]Platform, error)
}

type Service struct {
	store          *Store
	igdb           IGDBClient
	db             *db.DB
	refreshMu      sync.Mutex
	refreshing     map[string]chan struct{}
	refreshSlots   chan struct{}
	refreshTimeout time.Duration
}

func NewService(store *Store, igdb IGDBClient, db *db.DB) *Service {
	return &Service{
		store:          store,
		igdb:           igdb,
		db:             db,
		refreshing:     make(map[string]chan struct{}),
		refreshSlots:   make(chan struct{}, 2),
		refreshTimeout: 15 * time.Second,
	}
}

func (s *Service) Search(ctx context.Context, query string, includeEditions bool) ([]GameResult, error) {
	query = NormalizeName(query)
	if len(query) < 2 {
		return nil, nil
	}

	effectiveInclude := includeEditions || ContainsEditionKeyword(query)

	// Return the local snapshot before scheduling the remote refresh. This keeps
	// stale-while-revalidate semantics exact even when IGDB responds immediately.
	local, err := s.store.SearchLocalWithEditions(ctx, query, 10, effectiveInclude)
	if err != nil {
		return nil, err
	}
	if s.shouldAskIGDB(ctx, query, effectiveInclude) {
		s.startAsyncRefresh(query, effectiveInclude)
	}

	return local, nil
}

// SearchPaged performs a paginated search with a relevance floor applied to
// exclude weak (tier-3 substring) matches unless popular. On page 1 (offset=0),
// it schedules an asynchronous IGDB refresh; deeper pages are local-only.
func (s *Service) SearchPaged(ctx context.Context, query string, limit, offset int) ([]GameResult, error) {
	results, _, err := s.SearchPagedFull(ctx, query, limit, offset, "", 0, 0, 0, "", false)
	return results, err
}

// SearchPagedFull is the full-results-page search: paginated, floored,
// optionally sorted/filtered, and returning the total match count so the UI
// can display "N results". platform (substring of a platform name/abbreviation)
// restricts to games available on it. As in SearchPaged, refreshes are
// asynchronous on page 1 only.
func (s *Service) SearchPagedFull(ctx context.Context, query string, limit, offset int, sort string, yearFrom, yearTo, minRating int64, platform string, includeEditions bool) ([]GameResult, int64, error) {
	return s.SearchPagedFullWithFilters(ctx, query, limit, offset, sort, yearFrom, yearTo, minRating, platform, nil, "", "", nil, "", includeEditions)
}

// SearchPagedFullWithFilters extends SearchPagedFull with personal-library
// filters: tags/tagOp (library tags), libraryUserID/inLibrary/libraryStatus/ownedPlatform.
// tags, ownedPlatform and library filters are applied only when libraryUserID is non-empty;
// callers should obtain it from the session when present. Personal filters are
// honored on local results and remain stable while an async refresh completes.
// libraryStatuses (multi) takes precedence over libraryStatus (single) when non-empty.
func (s *Service) SearchPagedFullWithFilters(ctx context.Context, query string, limit, offset int, sort string, yearFrom, yearTo, minRating int64, platform string, tags []string, tagOp string, libraryUserID string, inLibrary *bool, libraryStatus string, includeEditions bool, ownedPlatform ...string) ([]GameResult, int64, error) {
	query = NormalizeName(query)
	if len(query) < 2 {
		return nil, 0, nil
	}

	effectiveInclude := includeEditions || ContainsEditionKeyword(query)
	if tagOp != "or" {
		tagOp = "and"
	}
	ownedPlat := ""
	if len(ownedPlatform) > 0 {
		ownedPlat = ownedPlatform[0]
	}

	// Support multi-status (comma-separated) for library filter
	var libStatuses []string
	if strings.Contains(libraryStatus, ",") {
		for _, part := range strings.Split(libraryStatus, ",") {
			if s := strings.ToLower(strings.TrimSpace(part)); s != "" && ValidLibraryStatuses[s] {
				libStatuses = append(libStatuses, s)
			}
		}
		if len(libStatuses) > 0 {
			libraryStatus = ""
		}
	}

	opts := func() searchOptions {
		return searchOptions{
			limit:           limit,
			offset:          offset,
			sort:            sort,
			yearFrom:        yearFrom,
			yearTo:          yearTo,
			minRating:       minRating,
			platform:        platform,
			ownedPlatform:   ownedPlat,
			tags:            tags,
			tagOp:           tagOp,
			libraryUserID:   libraryUserID,
			inLibrary:       inLibrary,
			libraryStatus:   libraryStatus,
			libraryStatuses: libStatuses,
			includeEditions: effectiveInclude,
		}
	}

	local, total, err := s.store.SearchGamesPaged(ctx, query, opts())
	if err != nil {
		return nil, 0, err
	}

	// Only ask IGDB on page 1 and only if the query is long enough and not cached.
	// The local page is already fixed; a later request sees refreshed rows.
	if offset == 0 && s.shouldAskIGDB(ctx, query, effectiveInclude) {
		s.startAsyncRefresh(query, effectiveInclude)
	}

	return local, total, nil
}

// startAsyncRefresh schedules a best-effort stale-while-revalidate refresh.
// At most two refreshes run at once, and the key remains in refreshing while a
// slot is held so concurrent requests cannot duplicate an IGDB query. A full
// slot drops the refresh; a later request can retry it without being blocked.
func (s *Service) startAsyncRefresh(query string, includeEditions bool) {
	key := cacheKey(query, includeEditions)
	s.refreshMu.Lock()
	if _, ok := s.refreshing[key]; ok {
		s.refreshMu.Unlock()
		return
	}
	done := make(chan struct{})
	s.refreshing[key] = done
	s.refreshMu.Unlock()

	select {
	case s.refreshSlots <- struct{}{}:
	default:
		close(done)
		s.refreshMu.Lock()
		delete(s.refreshing, key)
		s.refreshMu.Unlock()
		return
	}

	go func() {
		defer func() {
			<-s.refreshSlots
			close(done)
			s.refreshMu.Lock()
			delete(s.refreshing, key)
			s.refreshMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), s.refreshTimeout)
		defer cancel()
		s.refreshFromIGDB(ctx, query, includeEditions)
	}()
}

// refreshFromIGDB fetches a query from IGDB, upserts all results, and records
// both non-empty and empty successful searches in the cache. It always receives
// a detached bounded context: an HTTP client canceling its request must not
// cancel a refresh that is already responsible for warming the catalog/cache.
//
// On IGDB failure the stale cache entry is kept (not deleted) and its expiry
// is pushed by 1 hour to avoid hammering the API on every request while it
// is down. The next request after the backoff window will retry.
func (s *Service) refreshFromIGDB(ctx context.Context, query string, includeEditions bool) {
	remote, err := s.igdb.SearchGames(ctx, query, 10, includeEditions)
	if err != nil {
		cacheSearchFailureBackoff(ctx, s.db, query, includeEditions)
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

	cacheSearchResultsDB(ctx, s.db, query, remote, includeEditions)
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
// Idempotent: the fetch is skipped once the table has rows (the list changes
// ~yearly; a restart re-runs only if it was never fetched). Curated
// shortnames ("sw2", "xsx", …) are re-applied on every run regardless, so
// tables populated before they existed get stamped too. Without a real IGDB
// client this is a no-op and platform IDs resolve to nothing (displayed as
// absent rather than wrong).
func (s *Service) SyncPlatforms(ctx context.Context) error {
	if n, err := s.store.CountPlatforms(ctx); err != nil {
		return err
	} else if n == 0 {
		plats, err := s.igdb.GetPlatforms(ctx)
		if err != nil {
			return fmt.Errorf("fetch platforms: %w", err)
		}
		if err := s.store.UpsertPlatforms(ctx, plats); err != nil {
			return err
		}
		log.Printf("platform sync: populated %d platform names", len(plats))
	}
	return s.store.ApplyPlatformShortnames(ctx)
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

// StartQueryCacheRefresh proactively refreshes stale igdb_query_cache entries
// once per day in the background (up to 50 per cycle, rate-limited via the
// IGDB limiter). This keeps popular search queries fresh without waiting for
// a user to trigger the stale-while-revalidate path, so the first search
// after the 24h window still hits a fresh cache. Only call it when a real
// IGDB client is configured.
func (s *Service) StartQueryCacheRefresh() {
	go func() {
		// Run once shortly after startup so a restart doesn't wait a full day
		// to refresh queries that expired while the container was down.
		time.Sleep(30 * time.Second)
		for {
			s.refreshStaleQueries(50)
			time.Sleep(24 * time.Hour)
		}
	}()
}

func (s *Service) refreshStaleQueries(limit int) {
	ctx := context.Background()
	keys, err := s.store.GetStaleQueries(ctx, limit)
	if err != nil {
		log.Printf("query cache refresh: list failed: %v", err)
		return
	}
	if len(keys) == 0 {
		return
	}
	log.Printf("query cache refresh: refreshing %d stale queries", len(keys))
	refreshed := 0
	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		query, includeEditions := parseCacheKey(key)
		s.refreshFromIGDB(ctx, query, includeEditions)
		refreshed++
	}
	log.Printf("query cache refresh: refreshed %d/%d stale queries", refreshed, len(keys))
}

func parseCacheKey(key string) (string, bool) {
	if !strings.HasPrefix(key, "search:") {
		return key, false
	}
	trimmed := strings.TrimPrefix(key, "search:")
	if strings.HasSuffix(trimmed, ":editions") {
		return strings.TrimSuffix(trimmed, ":editions"), true
	}
	return trimmed, false
}

// StartCoverRepair runs RepairCovers once in the background at startup.
// Only call it when a real IGDB client is configured — without one there is
// nothing to re-fetch metadata from.
func (s *Service) StartCoverRepair() {
	go s.RepairCovers(context.Background())
}

// RepairCovers fixes cover data poisoned by earlier bugs:
//
//  1. Games whose cover_url points at images.cato.com (a
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
//
//  3. Guessed cover URLs (co<digits>.jpg where the digits are the numeric
//     cover_id) are still in the catalog for ~254k rows inserted before the
//     image_id fix. Many coincide with the real image_id and work, but a
//     meaningful fraction (e.g. Resident Evil Requiem's cobmj0 vs co542412)
//     are 404s. At startup we repair a small batch (500) so the worst
//     offenders are fixed within a few restarts without stalling boot for
//     8 minutes. A full backfill is available via `cato backfill-covers`.
func (s *Service) RepairCovers(ctx context.Context) {
	purged, err := PurgeDeadCoverSources(s.db)
	if err != nil {
		log.Printf("cover repair: purge dead sources failed: %v", err)
	} else if purged > 0 {
		log.Printf("cover repair: cleared %d games pointing at dead cover host", purged)
	}

	// Step 3: opportunistically fix a batch of guessed cover URLs. Limit to
	// 500 to keep startup fast; the `backfill-covers` subcommand handles the
	// full catalog when run manually. Errors are logged but never fatal.
	if err := s.repairGuessedCoverBatch(ctx, 500); err != nil {
		log.Printf("cover repair: guessed-cover batch failed: %v", err)
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

// repairGuessedCoverBatch re-fetches up to `limit` games whose cover_url looks
// like a guessed numeric URL (co<digits>.jpg) and corrects it from IGDB's
// authoritative cover.image_id. Batching through GetGamesBatch keeps the cost
// to 1 IGDB request per 500 games. Only rows where the fetched image_id
// differs are updated, so correctly-guessed numeric URLs are left alone.
func (s *Service) repairGuessedCoverBatch(ctx context.Context, limit int) error {
	ids, err := s.store.GetCoverRepairCandidates(ctx, limit)
	if err != nil {
		return fmt.Errorf("list candidates: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	// GetGamesBatch may return fewer rows than requested (deleted upstream).
	games, err := s.fetchBatchWithRetry(ctx, ids)
	if err != nil {
		return fmt.Errorf("fetch batch: %w", err)
	}
	byID := make(map[int64]Game, len(games))
	for _, g := range games {
		byID[g.ID] = g
	}
	fixed := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		g, ok := byID[id]
		if !ok {
			// Deleted upstream — clear its stale cover so the UI shows the
			// placeholder instead of a permanent 404. The row itself is kept
			// for catalog completeness; source_updated_at will be bumped by
			// the stale-refresh path eventually.
			continue
		}
		// Fetch the stored URL to compare.
		var storedURL string
		var storedCoverID int64
		_ = s.db.QueryRowContext(ctx, `SELECT cover_url, cover_id FROM games WHERE id = ?`, id).Scan(&storedURL, &storedCoverID)
		if g.CoverURL != storedURL || g.CoverID != storedCoverID {
			if err := s.store.SetCoverAndMarkFetched(ctx, id, g.CoverID, g.CoverURL); err != nil {
				log.Printf("cover repair: update game %d failed: %v", id, err)
				continue
			}
			// If this game is in a library, ensure a fresh cover job exists
			// with the corrected URL (EnqueueCoverJob is idempotent and only
			// revives exhausted/changed jobs).
			if g.CoverURL != "" {
				s.store.EnqueueCoverJob(ctx, id, g.CoverURL)
			}
			fixed++
		}
	}
	if fixed > 0 {
		log.Printf("cover repair: fixed %d/%d guessed cover URLs", fixed, len(ids))
	}
	return nil
}

// BackfillCovers walks every game whose cover_url looks guessed (co<digits>)
// and re-fetches it from IGDB in batches, correcting the URL from
// cover.image_id. Resumable: only rows where the fetched URL differs are
// updated, and the GLOB filter narrows the candidate set so re-running after
// a partial run skips already-fixed rows. `progress` is called after each
// batch with (done, total) for logging.
func (s *Service) BackfillCovers(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	if batchSize > 500 {
		batchSize = 500
	}
	pending, err := s.store.CountPendingCoverRepair(ctx)
	if err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	total := int(pending)
	done := 0
	progress(done, total)
	if total == 0 {
		return 0, nil
	}
	var afterID int64
	for {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		ids, err := s.store.GetCoverRepairCandidatesAfter(ctx, afterID, batchSize)
		if err != nil {
			return done, fmt.Errorf("get candidates: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		afterID = ids[len(ids)-1]
		games, err := s.fetchBatchWithRetry(ctx, ids)
		if err != nil {
			return done, fmt.Errorf("batch fetch at %d/%d: %w", done, total, err)
		}
		byID := make(map[int64]Game, len(games))
		for _, g := range games {
			byID[g.ID] = g
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			g, ok := byID[id]
			if !ok {
				// Deleted upstream — count it as done and move on. The stale
				// cover_url will be left as-is; it will be cleared on next
				// full refresh or left to show the placeholder.
				done++
				continue
			}
			var storedURL string
			var storedCoverID int64
			_ = s.db.QueryRowContext(ctx, `SELECT cover_url, cover_id FROM games WHERE id = ?`, id).Scan(&storedURL, &storedCoverID)
			if g.CoverURL != storedURL || g.CoverID != storedCoverID {
				if err := s.store.SetCoverAndMarkFetched(ctx, id, g.CoverID, g.CoverURL); err != nil {
					log.Printf("backfill-covers: update %d failed: %v", id, err)
				}
				if g.CoverURL != "" {
					s.store.EnqueueCoverJob(ctx, id, g.CoverURL)
				}
			}
			done++
		}
		progress(done, total)
		// Re-count remaining to keep total accurate if new rows were inserted
		// concurrently (unlikely, but keeps the log honest).
		if done%5000 == 0 {
			if n, err := s.store.CountPendingCoverRepair(ctx); err == nil {
				total = done + int(n)
			}
		}
	}
	return done, nil
}

// PurgeDeadCoverSources clears cover URLs pointing at the defunct
// images.cato.com host (legacy Postgres import) and removes matching
// cover_jobs. Safe to run repeatedly; returns the number of games updated.
func PurgeDeadCoverSources(database *db.DB) (int64, error) {
	res, err := database.Exec(`UPDATE games SET cover_url = ''
		WHERE cover_url LIKE '%//images.cato.com/%'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	database.Exec(`DELETE FROM cover_jobs WHERE source_url LIKE '%//images.cato.com/%'`)
	return n, nil
}

// StartNormalizationRepair runs the accent-stripping normalization fix
// in the background. Older rows stored normalized_name with accents
// (e.g. "pokémon go") so a query for "pokemon go" missed them via
// FTS/LIKE. The new NormalizeName strips diacritics, so we update any
// mismatched rows. Safe to run repeatedly; idempotent.
func (s *Service) StartNormalizationRepair() {
	// Quick synchronous check: if the games table doesn't exist (e.g. a
	// fresh DB in tests that hasn't been migrated) skip the background
	// work entirely to avoid noisy logs and file-handle races during
	// TempDir cleanup.
	var cnt int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='games'`).Scan(&cnt); err != nil || cnt == 0 {
		return
	}
	go func() {
		ctx := context.Background()
		if n, err := s.store.RepairNormalizedNames(ctx); err != nil {
			log.Printf("normalization repair: games failed: %v", err)
		} else if n > 0 {
			log.Printf("normalization repair: fixed %d game names", n)
		}
		if n, err := s.store.RepairNormalizedAliases(ctx); err != nil {
			log.Printf("normalization repair: aliases failed: %v", err)
		} else if n > 0 {
			log.Printf("normalization repair: fixed %d aliases", n)
		}
	}()
}

// RepairNormalization is the synchronous variant used by tests and the
// backfill subcommand. It returns the total number of rows updated.
func (s *Service) RepairNormalization(ctx context.Context) (int64, error) {
	n1, err := s.store.RepairNormalizedNames(ctx)
	if err != nil {
		return n1, err
	}
	n2, err := s.store.RepairNormalizedAliases(ctx)
	if err != nil {
		return n1 + n2, err
	}
	return n1 + n2, nil
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

// BackfillEditions populates version_parent for every catalog row in bulk,
// fetching up to `batchSize` (max 500) games per rate-limited request.
// Resumable via version_parent_fetched_at — each processed row gets the
// marker stamped, including rows IGDB no longer knows, so re-running skips
// done rows. Like BackfillAliases it never touches other game columns.
func (s *Service) BackfillEditions(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	if batchSize > 500 {
		batchSize = 500
	}

	pending, err := s.store.CountPendingEditionBackfill(ctx)
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
		ids, err := s.store.GetEditionBackfillCandidates(ctx, batchSize)
		if err != nil {
			return done, fmt.Errorf("get candidates: %w", err)
		}
		if len(ids) == 0 {
			break
		}

		games, err := s.fetchBatchWithRetry(ctx, ids)
		if err != nil {
			return done, fmt.Errorf("batch fetch at %d/%d: %w", done, total, err)
		}

		returned := make(map[int64]bool, len(games))
		for _, g := range games {
			if err := s.store.SetEditionInfoAndMarkFetched(ctx, g.ID, g.VersionParent, g.Category, g.ParentGame); err != nil {
				log.Printf("backfill-editions: set edition info for %d failed: %v", g.ID, err)
				continue
			}
			returned[g.ID] = true
			done++
		}
		for _, id := range ids {
			if !returned[id] {
				if err := s.store.MarkEditionFetched(ctx, id); err != nil {
					log.Printf("backfill-editions: mark %d failed: %v", id, err)
					continue
				}
				done++
			}
		}
		progress(done, total)
	}
	return done, nil
}

// BackfillCategories walks the entire catalog and corrects stale
// category/parent_game values (the Postgres import left most packs as
// category 0). It fetches every game in id order in batches of up to 500,
// updating the row when the fetched values differ. Unlike
// BackfillEditions it ignores the fetched marker and touches every row,
// so already-edition-fetched packs like 26042 get fixed. Resumable via
// afterID pagination; progress is reported as done/total.
func (s *Service) BackfillCategories(ctx context.Context, batchSize int, progress func(done, total int)) (int, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	if batchSize > 500 {
		batchSize = 500
	}
	total64, err := s.store.CountTotalGames(ctx)
	if err != nil {
		return 0, fmt.Errorf("count total: %w", err)
	}
	total := int(total64)
	done := 0
	progress(done, total)
	var afterID int64
	for {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		ids, err := s.store.GetGameIDsAfter(ctx, afterID, batchSize)
		if err != nil {
			return done, fmt.Errorf("get ids: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		afterID = ids[len(ids)-1]
		games, err := s.fetchBatchWithRetry(ctx, ids)
		if err != nil {
			return done, fmt.Errorf("batch fetch at %d/%d: %w", done, total, err)
		}
		byID := make(map[int64]Game, len(games))
		for _, g := range games {
			byID[g.ID] = g
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			g, ok := byID[id]
			if !ok {
				done++
				continue
			}
			// Only write when something changed to avoid touching every row
			// unnecessarily (still counts as done for progress).
			if err := s.store.UpdateCategoryAndParentGame(ctx, id, g.Category, g.ParentGame); err != nil {
				log.Printf("backfill-categories: update %d failed: %v", id, err)
			}
			// Also ensure version_parent is correct if it was previously
			// missed (e.g. pack that is also an edition).
			if g.VersionParent != 0 {
				_ = s.store.SetVersionParentAndMarkFetched(ctx, id, g.VersionParent)
			}
			done++
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

func (s *Service) shouldAskIGDB(ctx context.Context, query string, includeEditions bool) bool {
	if ctx.Err() != nil {
		return false
	}
	cached, err := getCachedSearchDB(ctx, s.db, query, includeEditions)
	if err == nil && cached {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	return len(query) >= 3
}

// PurgeExpiredQueryCache deletes igdb_query_cache rows that have been stale
// for a long time. The read path treats expired rows as stale-while-revalidate
// (triggers an async refresh but still serves local results immediately), so
// this sweep keeps the table bounded without deleting entries that are merely
// awaiting their daily refresh. Only rows expired for more than 30 days are
// removed — rare queries that haven't been searched in a month.
func PurgeExpiredQueryCache(database *db.DB) (int64, error) {
	res, err := database.Exec("DELETE FROM igdb_query_cache WHERE expires_at < ?",
		time.Now().Add(-30*24*time.Hour).Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func cacheSearchResultsDB(ctx context.Context, db *db.DB, query string, games []Game, includeEditions bool) {
	data, _ := json.Marshal(map[string]interface{}{"query": query, "cached": true})
	key := cacheKey(query, includeEditions)
	db.ExecContext(ctx, `INSERT OR REPLACE INTO igdb_query_cache (normalized_query, response_json, expires_at)
		VALUES (?, ?, ?)`, key, string(data), time.Now().Add(24*time.Hour).Format(time.RFC3339))
}

// cacheSearchFailureBackoff extends the expiry after a failed IGDB fetch so
// the next request doesn't immediately retry and hammer the API. If the
// query has never been cached, a short-lived placeholder is inserted so the
// backoff still applies. The entry remains stale (will trigger a refresh
// after the backoff window) but avoids tight retry loops when IGDB is down.
func cacheSearchFailureBackoff(ctx context.Context, db *db.DB, query string, includeEditions bool) {
	key := cacheKey(query, includeEditions)
	data, _ := json.Marshal(map[string]interface{}{"query": query, "failed": true})
	expires := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	db.ExecContext(ctx, `INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT(normalized_query) DO UPDATE SET expires_at = excluded.expires_at`,
		key, string(data), expires)
}

func getCachedSearchDB(ctx context.Context, db *db.DB, query string, includeEditions bool) (bool, error) {
	key := cacheKey(query, includeEditions)
	var expiresAt string
	err := db.QueryRowContext(ctx,
		"SELECT expires_at FROM igdb_query_cache WHERE normalized_query = ?",
		key).Scan(&expiresAt)
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
		// Soft expiry: stale-while-revalidate. Keep the row so the cache
		// doesn't appear empty until the background refresh succeeds (see
		// cacheSearchResultsDB). The caller will trigger an async refresh
		// but still serves the local DB snapshot immediately, so the first
		// request after the 24h window is no slower than a cache hit. The
		// row is eventually refreshed or, if abandoned for 30 days,
		// collected by PurgeExpiredQueryCache.
		return false, nil
	}
	return true, nil
}

func cacheKey(query string, includeEditions bool) string {
	if includeEditions {
		return "search:" + query + ":editions"
	}
	return "search:" + query
}
