package games

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func waitForRefresh(s *Service, ctx context.Context, key string) error {
	s.refreshMu.Lock()
	done, ok := s.refreshing[key]
	s.refreshMu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSearchAsyncRefreshReturnsLocalResultsAndDeduplicates(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name)
		VALUES (1, 'Local Game', 'local-game', 'local game')`); err != nil {
		t.Fatalf("insert local game: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	igdb := &fakeIGDB{searchFunc: func(ctx context.Context, query string, limit int) ([]Game, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		select {
		case <-release:
			return []Game{{ID: 2, Name: "Remote Game", Slug: "remote-game", SafeName: "Remote Game", NormalizedName: "remote game"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	svc := NewService(store, igdb, database)
	svc.refreshTimeout = time.Second

	startedAt := time.Now()
	results, err := svc.Search(context.Background(), "local game", false)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("search waited for async refresh: %v", elapsed)
	}
	if len(results) != 1 || results[0].ID != 1 {
		t.Fatalf("local response = %v, want local game", results)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	// A concurrent request for the same key must not start another remote call.
	if _, err := svc.Search(context.Background(), "local game", false); err != nil {
		t.Fatalf("second search: %v", err)
	}
	mu.Lock()
	if calls != 1 {
		t.Errorf("refresh calls = %d, want 1", calls)
	}
	mu.Unlock()

	close(release)
	if err := waitForRefresh(svc, context.Background(), cacheKey("local game", false)); err != nil {
		t.Fatalf("wait for refresh: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM games WHERE id = 2`).Scan(&count); err != nil {
		t.Fatalf("count remote game: %v", err)
	}
	if count != 1 {
		t.Errorf("remote game count = %d, want 1", count)
	}
}

func TestSearchAsyncRefreshCachesSuccessfulEmptyResponse(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	calls := 0
	igdb := &fakeIGDB{searchFunc: func(ctx context.Context, query string, limit int) ([]Game, error) {
		calls++
		return nil, nil
	}}
	svc := NewService(store, igdb, database)

	if _, err := svc.Search(context.Background(), "missing game", false); err != nil {
		t.Fatalf("first search: %v", err)
	}
	if err := waitForRefresh(svc, context.Background(), cacheKey("missing game", false)); err != nil {
		t.Fatalf("wait for empty refresh: %v", err)
	}
	if _, err := svc.Search(context.Background(), "missing game", false); err != nil {
		t.Fatalf("second search: %v", err)
	}
	if calls != 1 {
		t.Errorf("empty response refresh calls = %d, want 1", calls)
	}
	var cached int
	if err := database.QueryRow(`SELECT COUNT(*) FROM igdb_query_cache WHERE normalized_query = ?`, cacheKey("missing game", false)).Scan(&cached); err != nil {
		t.Fatalf("count cache row: %v", err)
	}
	if cached != 1 {
		t.Errorf("cache rows = %d, want 1", cached)
	}
}

func TestSearchAsyncRefreshUsesDetachedContextAndBoundsCancellation(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	igdb := &fakeIGDB{searchFunc: func(ctx context.Context, query string, limit int) ([]Game, error) {
		started <- ctx
		select {
		case <-finish:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	svc := NewService(store, igdb, database)
	svc.refreshTimeout = 50 * time.Millisecond

	requestCtx, cancel := context.WithCancel(context.Background())
	if _, err := svc.Search(requestCtx, "cancelled request", false); err != nil {
		t.Fatalf("search: %v", err)
	}
	cancel()

	var refreshCtx context.Context
	select {
	case refreshCtx = <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	if refreshCtx.Err() != nil {
		t.Fatalf("refresh inherited request cancellation: %v", refreshCtx.Err())
	}
	if err := waitForRefresh(svc, context.Background(), cacheKey("cancelled request", false)); err != nil {
		t.Fatalf("wait for bounded refresh: %v", err)
	}
	if !errors.Is(refreshCtx.Err(), context.DeadlineExceeded) {
		t.Errorf("refresh context error = %v, want deadline exceeded", refreshCtx.Err())
	}
	close(finish)
}
