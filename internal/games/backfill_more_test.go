package games

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cato/internal/db"
)

// seedBackfillGames inserts n games with all backfill markers unset.
func seedBackfillGames(t *testing.T, database *db.DB, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		_, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date, aggregated_rating_count, cover_url, cover_id)
			VALUES (?, ?, ?, ?, ?, 5, '', 0)`,
			i, fmt.Sprintf("Game %d", i), fmt.Sprintf("game-%d", i), fmt.Sprintf("game %d", i), 1600000000)
		if err != nil {
			t.Fatalf("seed game %d: %v", i, err)
		}
	}
}

// backfillIGDB returns games matching the requested IDs (like IGDB), with
// configurable misses and errors.
type backfillIGDB struct {
	fakeIGDB
	err      error
	batchIds func(ids []int64) []int64 // returned subset (default: all)
}

func (b *backfillIGDB) GetGamesBatch(ctx context.Context, ids []int64) ([]Game, error) {
	b.batchCalls++
	if b.err != nil {
		return nil, b.err
	}
	out := ids
	if b.batchIds != nil {
		out = b.batchIds(ids)
	}
	games := make([]Game, 0, len(out))
	for _, id := range out {
		vp := int64(0)
		if id == 2 {
			vp = 1 // game 2 is an edition of game 1
		}
		games = append(games, Game{
			ID: id, Name: fmt.Sprintf("Game %d", id), Slug: fmt.Sprintf("game-%d", id),
			NormalizedName: fmt.Sprintf("game %d", id), SafeName: fmt.Sprintf("Game %d", id),
			Aliases:       []string{fmt.Sprintf("g%d", id)},
			CoverURL:      "https://images.igdb.com/igdb/image/upload/t_cover_big/co1.jpg",
			CoverID:       1,
			VersionParent: vp,
			Category:      0,
		})
	}
	return games, nil
}

func TestBackfillAliasesMarksAllAndStoresAliases(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 5)
	igdb := &backfillIGDB{batchIds: func(ids []int64) []int64 { return ids[:len(ids)-1] }} // last ID "deleted upstream"
	svc := NewService(NewStore(database), igdb, database)

	var progressCalls int
	done, err := svc.BackfillAliases(context.Background(), 2, func(done, total int) { progressCalls++ })
	if err != nil {
		t.Fatalf("BackfillAliases: %v", err)
	}
	if done != 5 {
		t.Errorf("done = %d, want 5", done)
	}
	if progressCalls == 0 {
		t.Error("progress never called")
	}
	var pending int64
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE aliases_fetched_at = 0`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d rows still pending", pending)
	}
	// Aliases stored.
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 1`).Scan(&n)
	if n == 0 {
		t.Error("no aliases stored for game 1")
	}
}

func TestBackfillAliasesPersistentBatchFailure(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 3)
	igdb := &backfillIGDB{err: errors.New("down")}
	svc := NewService(NewStore(database), igdb, database)

	// fetchBatchWithRetry backs off 2s+4s+8s; zero the delays for the test.
	oldDelays := backfillRetryDelays
	backfillRetryDelays = []time.Duration{0, 0, 0}
	defer func() { backfillRetryDelays = oldDelays }()

	_, err := svc.BackfillAliases(context.Background(), 2, func(done, total int) {})
	if err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("expected persistent failure, got %v", err)
	}
}

func TestBackfillEditions(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 4)
	igdb := &backfillIGDB{batchIds: func(ids []int64) []int64 {
		// Game 3 is gone upstream; game 2 is an edition (version_parent=1).
		var out []int64
		for _, id := range ids {
			if id == 3 {
				continue
			}
			out = append(out, id)
		}
		return out
	}}
	svc := NewService(NewStore(database), igdb, database)
	done, err := svc.BackfillEditions(context.Background(), 3, func(done, total int) {})
	if err != nil {
		t.Fatalf("BackfillEditions: %v", err)
	}
	if done != 4 {
		t.Errorf("done = %d, want 4", done)
	}
	var vp int64
	database.QueryRow(`SELECT version_parent FROM games WHERE id = 2`).Scan(&vp)
	if vp != 1 {
		t.Errorf("game 2 version_parent = %d, want 1", vp)
	}
	var pending int64
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE version_parent_fetched_at = 0`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d rows still pending", pending)
	}
}

func TestBackfillCovers(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 4)
	// Give games "guessed" cover URLs so they match the repair GLOB
	// (which also requires cover_id != 0).
	database.Exec(`UPDATE games SET cover_url = 'https://images.igdb.com/igdb/image/upload/t_cover_big/co542412.jpg', cover_id = 542412`)
	igdb := &backfillIGDB{}
	svc := NewService(NewStore(database), igdb, database)
	done, err := svc.BackfillCovers(context.Background(), 2, func(done, total int) {})
	if err != nil {
		t.Fatalf("BackfillCovers: %v", err)
	}
	if done != 4 {
		t.Errorf("done = %d, want 4", done)
	}
	// The batch returns co1.jpg while stored is co542412 → URL corrected.
	var url string
	database.QueryRow(`SELECT cover_url FROM games WHERE id = 1`).Scan(&url)
	if url != "https://images.igdb.com/igdb/image/upload/t_cover_big/co1.jpg" {
		t.Errorf("cover_url = %q", url)
	}
}

func TestBackfillPopularity(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 3)
	igdb := &backfillIGDB{}
	svc := NewService(NewStore(database), igdb, database)
	// BackfillPopularity uses GetGame (per-game), which the base fakeIGDB
	// answers with nil — simulating "gone upstream". done must still advance.
	done, err := svc.BackfillPopularity(context.Background(), 2, 2, func(done, total int) {})
	if err != nil {
		t.Fatalf("BackfillPopularity: %v", err)
	}
	if done != 3 {
		t.Errorf("done = %d, want 3", done)
	}
	var pending int64
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE popularity_fetched_at = 0`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d rows still pending", pending)
	}
}

func TestBackfillCategories(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 4)
	igdb := &backfillIGDB{batchIds: func(ids []int64) []int64 { return ids[:len(ids)-1] }}
	svc := NewService(NewStore(database), igdb, database)
	done, err := svc.BackfillCategories(context.Background(), 2, func(done, total int) {})
	if err != nil {
		t.Fatalf("BackfillCategories: %v", err)
	}
	if done != 4 {
		t.Errorf("done = %d, want 4", done)
	}
}

func TestBackfillContextCancellation(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 5)
	igdb := &backfillIGDB{}
	svc := NewService(NewStore(database), igdb, database)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Canceled context should return promptly with ctx.Err().
	_, err := svc.BackfillAliases(ctx, 2, func(done, total int) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("BackfillAliases ctx: %v", err)
	}
	_, err = svc.BackfillPopularity(ctx, 2, 2, func(done, total int) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("BackfillPopularity ctx: %v", err)
	}
	_, err = svc.BackfillCovers(ctx, 2, func(done, total int) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("BackfillCovers ctx: %v", err)
	}
	_, err = svc.BackfillEditions(ctx, 2, func(done, total int) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("BackfillEditions ctx: %v", err)
	}
	_, err = svc.BackfillCategories(ctx, 2, func(done, total int) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("BackfillCategories ctx: %v", err)
	}
}

func TestPurgeDeadCoverSources(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 2)
	database.Exec(`UPDATE games SET cover_url = 'https://images.cato.com/t_cover_big/co1.jpg' WHERE id = 1`)
	database.Exec(`UPDATE games SET cover_url = 'https://images.igdb.com/x.jpg' WHERE id = 2`)
	database.Exec(`INSERT INTO cover_jobs (game_id, source_url) VALUES (1, 'https://images.cato.com/t_cover_big/co1.jpg')`)

	n, err := PurgeDeadCoverSources(database)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
	var url string
	database.QueryRow(`SELECT cover_url FROM games WHERE id = 1`).Scan(&url)
	if url != "" {
		t.Errorf("dead cover_url kept: %q", url)
	}
	var jobs int
	database.QueryRow(`SELECT COUNT(*) FROM cover_jobs`).Scan(&jobs)
	if jobs != 0 {
		t.Errorf("dead cover job kept: %d", jobs)
	}

	// Idempotent: second run purges nothing.
	if n, _ := PurgeDeadCoverSources(database); n != 0 {
		t.Errorf("second purge removed %d", n)
	}
}

func TestPurgeExpiredQueryCache(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-time.Hour).Format(time.RFC3339)
	database.Exec(`INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at) VALUES ('q1', '{}', ?)`, old)
	database.Exec(`INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at) VALUES ('q2', '{}', ?)`, recent)

	n, err := PurgeExpiredQueryCache(database)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
	var keys int
	database.QueryRow(`SELECT COUNT(*) FROM igdb_query_cache`).Scan(&keys)
	if keys != 1 {
		t.Errorf("remaining rows = %d", keys)
	}
}

func TestGetStaleQueries(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	database.Exec(`INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at) VALUES ('a', '{}', ?)`, past)
	database.Exec(`INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at) VALUES ('b', '{}', ?)`, past)
	database.Exec(`INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at) VALUES ('c', '{}', ?)`,
		time.Now().Add(time.Hour).Format(time.RFC3339))

	keys, err := NewStore(database).GetStaleQueries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Errorf("stale keys = %v", keys)
	}
}

func TestParseCacheKey(t *testing.T) {
	if q, ed := parseCacheKey("search:zelda:editions"); q != "zelda" || !ed {
		t.Errorf("editions key: %q %v", q, ed)
	}
	if q, ed := parseCacheKey("search:zelda"); q != "zelda" || ed {
		t.Errorf("plain key: %q %v", q, ed)
	}
	if q, ed := parseCacheKey("other"); q != "other" || ed {
		t.Errorf("other key: %q %v", q, ed)
	}
}

func TestCacheSearchRoundtrip(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	if cached, _ := getCachedSearchDB(ctx, database, "zelda", false); cached {
		t.Error("nothing cached yet")
	}
	cacheSearchResultsDB(ctx, database, "zelda", nil, false)
	if cached, _ := getCachedSearchDB(ctx, database, "zelda", false); !cached {
		t.Error("cached entry not found")
	}
	// Editions variant is a different key.
	if cached, _ := getCachedSearchDB(ctx, database, "zelda", true); cached {
		t.Error("editions variant should be a separate key")
	}

	// Failure backoff inserts a placeholder and pushes expiry.
	cacheSearchFailureBackoff(ctx, database, "mario", false)
	if cached, _ := getCachedSearchDB(ctx, database, "mario", false); !cached {
		t.Error("backoff placeholder should read as cached within the window")
	}
}

func TestServiceGetGameResolvesPlatformNames(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, platforms_json) VALUES (9, 'P', 'p', 'p', '[6,130]')`)
	database.Exec(`INSERT INTO platforms (id, name, abbreviation) VALUES (6, 'PC (Microsoft Windows)', 'PC'), (130, 'Nintendo Switch', 'Switch')`)
	svc := NewService(NewStore(database), &fakeIGDB{}, database)

	g, err := svc.GetGame(context.Background(), 9)
	if err != nil || g == nil {
		t.Fatalf("GetGame: (%v, %v)", g, err)
	}
	if len(g.Platforms) != 2 || g.Platforms[0] != "PC (Microsoft Windows)" {
		t.Errorf("platforms = %v", g.Platforms)
	}

	if g, err := svc.GetGame(context.Background(), 404); g != nil || err != nil {
		t.Errorf("missing game: (%v, %v)", g, err)
	}
}

func TestSyncPlatformsShortnamesAndIdempotency(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	svc := NewService(NewStore(database), &fakeIGDB{}, database)

	if err := svc.SyncPlatforms(context.Background()); err != nil {
		t.Fatalf("SyncPlatforms: %v", err)
	}
	var n int64
	database.QueryRow(`SELECT COUNT(*) FROM platforms`).Scan(&n)
	if n == 0 {
		t.Error("platforms table empty after sync")
	}
	// Shortnames applied.
	var sn string
	database.QueryRow(`SELECT shortname FROM platforms WHERE id = 508`).Scan(&sn)
	if sn != "sw2 ns2 switch2" {
		t.Errorf("shortname for 508 = %q", sn)
	}
	// Idempotent second run (fetch skipped once populated).
	if err := svc.SyncPlatforms(context.Background()); err != nil {
		t.Fatalf("second SyncPlatforms: %v", err)
	}
}

func TestRepairNormalizationSync(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (20, 'Pokémon Go', 'pokemon-go', 'pokémon go')`)
	svc := NewService(NewStore(database), &fakeIGDB{}, database)
	n, err := svc.RepairNormalization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("fixed %d rows, want >= 1", n)
	}
	var norm string
	database.QueryRow(`SELECT normalized_name FROM games WHERE id = 20`).Scan(&norm)
	if norm != "pokemon go" {
		t.Errorf("normalized_name = %q", norm)
	}
}

func TestRepairCoversRevivesExhaustedJobs(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	seedBackfillGames(t, database, 2)
	// An exhausted cover job for game 1.
	database.Exec(`INSERT INTO cover_jobs (game_id, source_url, attempts) VALUES (1, 'u', 5)`)
	// Game 2's job references a game deleted upstream (nothing in batch).
	igdb := &backfillIGDB{batchIds: func(ids []int64) []int64 { return []int64{1} }}
	svc := NewService(NewStore(database), igdb, database)

	svc.RepairCovers(context.Background())
	// RepairCovers is fire-and-forget; assert observable DB effects.
	var attempts int
	database.QueryRow(`SELECT attempts FROM cover_jobs WHERE game_id = 1`).Scan(&attempts)
	if attempts != 0 {
		t.Errorf("exhausted job not revived (attempts=%d)", attempts)
	}
}

func TestUpsertIGDBGameSwapsAliasSet(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	g := Game{ID: 1, Name: "Zelda", Slug: "zelda", NormalizedName: "zelda", Aliases: []string{"botw", "Zelda"}}
	if err := store.UpsertIGDBGame(ctx, g); err != nil {
		t.Fatal(err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 1`).Scan(&n)
	if n != 1 { // "Zelda" dedupes against the normalized name
		t.Errorf("aliases = %d, want 1", n)
	}

	// Re-upsert with a different alias set replaces the old rows.
	g.Aliases = []string{"totk"}
	if err := store.UpsertIGDBGame(ctx, g); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 1 AND normalized_alias = 'botw'`).Scan(&n)
	if n != 0 {
		t.Error("stale alias kept after replace")
	}
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 1 AND normalized_alias = 'totk'`).Scan(&n)
	if n != 1 {
		t.Error("new alias missing")
	}
}

func TestEnqueueCoverJobReviveConditions(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'G', 'g', 'g')`)

	// Empty source URL is a no-op.
	if err := store.EnqueueCoverJob(ctx, 1, ""); err != nil {
		t.Fatal(err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM cover_jobs`).Scan(&n)
	if n != 0 {
		t.Fatal("empty URL enqueued")
	}

	if err := store.EnqueueCoverJob(ctx, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	// Re-enqueue with same URL: healthy job untouched.
	database.Exec(`UPDATE cover_jobs SET attempts = 1 WHERE game_id = 1`)
	if err := store.EnqueueCoverJob(ctx, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT attempts FROM cover_jobs WHERE game_id = 1`).Scan(&n)
	if n != 1 {
		t.Errorf("healthy job reset (attempts=%d)", n)
	}
	// Exhausted job is revived.
	database.Exec(`UPDATE cover_jobs SET attempts = 5 WHERE game_id = 1`)
	if err := store.EnqueueCoverJob(ctx, 1, "u1"); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT attempts FROM cover_jobs WHERE game_id = 1`).Scan(&n)
	if n != 0 {
		t.Errorf("exhausted job not revived (attempts=%d)", n)
	}
	// URL change revives an in-flight job.
	database.Exec(`UPDATE cover_jobs SET attempts = 2 WHERE game_id = 1`)
	if err := store.EnqueueCoverJob(ctx, 1, "u2"); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT attempts FROM cover_jobs WHERE game_id = 1`).Scan(&n)
	if n != 0 {
		t.Errorf("changed URL did not revive job (attempts=%d)", n)
	}
}
