package games

import (
	"context"
	"testing"
	"time"
)

// TestSearchLocalLikeWildcardEscaping covers §3.12: % and _ in queries must
// be matched literally, not treated as LIKE wildcards.
func TestSearchLocalLikeWildcardEscaping(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, '100% Pure', '100-pure', '100% pure'),
		(2, '1000 Ways to Die', '1000-ways', '1000 ways to die')`)

	// Searching "100%" must match only the literal "100%" name — before the
	// fix it matched "1000 Ways to Die" too (verified in prod audit).
	results, err := store.SearchLocal(context.Background(), "100%", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	for _, r := range results {
		if r.ID == 2 {
			t.Errorf("query '100%%' should not match '1000 Ways to Die' via wildcard; got results: %v", results)
		}
	}

	// Underscore is also a wildcard.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(3, 'Half Life', 'half-life', 'half life'),
		(4, 'Half-Life Source', 'half-life-source', 'half life source')`)
	results, err = store.SearchLocal(context.Background(), "half_life", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("query 'half_life' (literal underscore) should match nothing since names use spaces; got %d", len(results))
	}
}

// TestMarkRefreshed verifies stale-queue advancement for permanently-failed
// refreshes that would otherwise block the queue forever.
func TestMarkRefreshed(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	old := time.Now().AddDate(-1, 0, 0).Unix()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, source_updated_at) VALUES
		(1, 'Deleted Upstream', 'deleted', 'deleted upstream', ?)`, old)

	stale, err := store.GetStaleGames(context.Background(), 10)
	if err != nil || len(stale) != 1 {
		t.Fatalf("expected 1 stale game, got %v (%v)", stale, err)
	}

	if err := store.MarkRefreshed(context.Background(), 1); err != nil {
		t.Fatalf("mark refreshed: %v", err)
	}

	stale, err = store.GetStaleGames(context.Background(), 10)
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected queue to advance past marked game, got %v", stale)
	}
}

// TestEnqueueCoverJobRevivesExhausted verifies that re-enqueueing resets an
// exhausted job (the old ON CONFLICT DO NOTHING left stuck jobs stuck forever).
func TestEnqueueCoverJobRevivesExhausted(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Game', 'game', 'game')`)
	database.Exec(`INSERT INTO cover_jobs (game_id, source_url, attempts, last_error)
		VALUES (1, 'https://old.example/co00001.jpg', 5, 'http status 404')`)

	if err := store.EnqueueCoverJob(ctx, 1, "https://images.igdb.com/igdb/image/upload/t_cover_big/co99999.jpg"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var attempts int
	var lastError, sourceURL string
	database.QueryRow("SELECT attempts, last_error, source_url FROM cover_jobs WHERE game_id = 1").
		Scan(&attempts, &lastError, &sourceURL)
	if attempts != 0 {
		t.Errorf("expected exhausted job revived with attempts=0, got %d", attempts)
	}
	if lastError != "" {
		t.Errorf("expected last_error cleared, got %q", lastError)
	}
	if sourceURL != "https://images.igdb.com/igdb/image/upload/t_cover_big/co99999.jpg" {
		t.Errorf("expected source_url updated, got %q", sourceURL)
	}

	// Re-enqueueing the SAME URL while healthy must not restart the job.
	database.Exec("UPDATE cover_jobs SET attempts = 2 WHERE game_id = 1")
	store.EnqueueCoverJob(ctx, 1, "https://images.igdb.com/igdb/image/upload/t_cover_big/co99999.jpg")
	database.QueryRow("SELECT attempts FROM cover_jobs WHERE game_id = 1").Scan(&attempts)
	if attempts != 2 {
		t.Errorf("healthy job was restarted: attempts = %d, want 2", attempts)
	}
}
