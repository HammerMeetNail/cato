package games

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// waitFor polls fn until it returns true or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func shutdownService(t *testing.T, svc *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestStartPlatformSyncPopulatesLookup(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	// Migration v14 seeds a static platform list; clear it to simulate a
	// never-fetched table so the IGDB fetch path runs.
	if _, err := database.Exec(`DELETE FROM platforms`); err != nil {
		t.Fatalf("clear platforms: %v", err)
	}

	igdb := &fakeIGDB{}
	svc := NewService(store, igdb, database)
	defer shutdownService(t, svc)

	svc.StartPlatformSync()

	waitFor(t, 5*time.Second, func() bool {
		n, err := store.CountPlatforms(context.Background())
		return err == nil && n == 3
	}, "platform sync did not populate the lookup table")
	if igdb.platformCalls != 1 {
		t.Errorf("expected exactly 1 GetPlatforms call, got %d", igdb.platformCalls)
	}
	// Curated shortnames are stamped on the fetched rows.
	var shortname string
	if err := database.QueryRow("SELECT shortname FROM platforms WHERE id = 508").Scan(&shortname); err != nil {
		t.Fatalf("read shortname: %v", err)
	}
	if shortname != "sw2 ns2 switch2" {
		t.Errorf("expected curated shortname for Switch 2, got %q", shortname)
	}

	// Idempotent: a second run must not re-fetch when rows already exist.
	if err := svc.SyncPlatforms(context.Background()); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if igdb.platformCalls != 1 {
		t.Errorf("second sync re-fetched platforms: %d calls", igdb.platformCalls)
	}
}

func TestStartStaleRefreshRefreshesOldGame(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	old := time.Now().AddDate(0, 0, -100).Unix()
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, source_updated_at)
		VALUES (1, 'Old Game', 'old-game', 'old game', ?)`, old); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	igdb := &fakeIGDB{getFunc: func(ctx context.Context, id int64) (*Game, error) {
		return &Game{ID: id, Name: "Old Game", Slug: "old-game", SafeName: "Old Game",
			NormalizedName: "old game", CoverURL: "https://images.igdb.com/old.jpg",
			SourceUpdatedAt: time.Now().Unix()}, nil
	}}
	svc := NewService(store, igdb, database)
	defer shutdownService(t, svc)

	svc.StartStaleRefresh()

	var refreshedAt int64
	waitFor(t, 5*time.Second, func() bool {
		return database.QueryRow("SELECT source_updated_at FROM games WHERE id = 1").Scan(&refreshedAt) == nil && refreshedAt > old
	}, "stale refresh did not update the game")

	// The refreshed cover URL must enqueue a cover job for the downloader.
	var sourceURL string
	if err := database.QueryRow("SELECT source_url FROM cover_jobs WHERE game_id = 1").Scan(&sourceURL); err != nil && err != sql.ErrNoRows {
		t.Fatalf("read cover job: %v", err)
	}
	if sourceURL != "https://images.igdb.com/old.jpg" {
		t.Errorf("expected cover job with refreshed URL, got %q", sourceURL)
	}
}

func TestStartStaleRefreshMarksDeletedGames(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	old := time.Now().AddDate(0, 0, -100).Unix()
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, source_updated_at)
		VALUES (1, 'Gone Game', 'gone-game', 'gone game', ?)`, old); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	// fakeIGDB.GetGame default returns (nil, nil): deleted upstream. The loop
	// must stamp source_updated_at so the queue advances instead of re-selecting
	// the same row forever.
	svc := NewService(store, &fakeIGDB{}, database)
	defer shutdownService(t, svc)

	svc.StartStaleRefresh()

	var refreshedAt int64
	waitFor(t, 5*time.Second, func() bool {
		return database.QueryRow("SELECT source_updated_at FROM games WHERE id = 1").Scan(&refreshedAt) == nil && refreshedAt > old
	}, "deleted game was not marked refreshed")
}

func TestStartCoverRepairRevivesExhaustedJob(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, cover_url)
		VALUES (1, 'Broken Cover', 'broken-cover', 'broken cover', 'https://images.igdb.com/old.jpg')`); err != nil {
		t.Fatalf("insert game: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id, source_url, attempts, next_attempt_at)
		VALUES (1, 'https://images.igdb.com/old.jpg', 5, '2000-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert cover job: %v", err)
	}

	igdb := &fakeIGDB{getFunc: func(ctx context.Context, id int64) (*Game, error) {
		return &Game{ID: id, Name: "Broken Cover", Slug: "broken-cover", SafeName: "Broken Cover",
			NormalizedName: "broken cover", CoverURL: "https://images.igdb.com/fixed.jpg"}, nil
	}}
	svc := NewService(store, igdb, database)
	defer shutdownService(t, svc)

	svc.StartCoverRepair()

	var attempts int
	var sourceURL string
	waitFor(t, 5*time.Second, func() bool {
		if err := database.QueryRow("SELECT attempts, source_url FROM cover_jobs WHERE game_id = 1").Scan(&attempts, &sourceURL); err != nil {
			return false
		}
		return attempts == 0 && sourceURL == "https://images.igdb.com/fixed.jpg"
	}, "exhausted cover job was not revived with the corrected URL")
}

func TestStartNormalizationRepairFixesAccents(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name)
		VALUES (1, 'Pokémon GO', 'pokemon-go', 'pokémon go')`); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	svc := NewService(store, &fakeIGDB{}, database)
	defer shutdownService(t, svc)

	svc.StartNormalizationRepair()

	var normalized string
	waitFor(t, 5*time.Second, func() bool {
		return database.QueryRow("SELECT normalized_name FROM games WHERE id = 1").Scan(&normalized) == nil && normalized == "pokemon go"
	}, "normalization repair did not fix the accented name")
}

func TestStartQueryCacheRefreshSweepsStaleEntries(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	// A cache entry that expired an hour ago must be picked up by the sweep.
	expired := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if _, err := database.Exec(`INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at)
		VALUES ('search:stale query', '{"cached":true}', ?)`, expired); err != nil {
		t.Fatalf("insert cache row: %v", err)
	}

	igdb := &fakeIGDB{searchFunc: func(ctx context.Context, query string, limit int) ([]Game, error) {
		return []Game{{ID: 1, Name: "Stale Game", Slug: "stale-game", SafeName: "Stale Game", NormalizedName: "stale game"}}, nil
	}}
	svc := NewService(store, igdb, database)
	svc.queryCacheInitialDelay = 10 * time.Millisecond // test seam: skip the 30s startup wait
	defer shutdownService(t, svc)

	svc.StartQueryCacheRefresh()

	waitFor(t, 5*time.Second, func() bool {
		return igdb.searchCalls >= 1
	}, "query cache refresh did not re-fetch the stale query")

	var newExpiry string
	if err := database.QueryRow("SELECT expires_at FROM igdb_query_cache WHERE normalized_query = 'search:stale query'").Scan(&newExpiry); err != nil {
		t.Fatalf("read cache row: %v", err)
	}
	if newExpiry == expired {
		t.Errorf("stale cache row was not re-cached: expires_at still %q", newExpiry)
	}
}
