package games

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestServiceShutdownCancelsAndJoinsDetachedRefresh(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := &fakeIGDB{searchFunc: func(ctx context.Context, _ string, _ int) ([]Game, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}}
	svc := NewService(store, client, database)
	var releaseOnce sync.Once
	releaseRefresh := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		releaseRefresh()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		if err := svc.Shutdown(cleanupCtx); err != nil {
			t.Errorf("cleanup shutdown: %v", err)
		}
	}()
	svc.startAsyncRefresh("first", false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("refresh did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- svc.Shutdown(ctx) }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("refresh not canceled")
	}
	select {
	case err := <-stopped:
		t.Fatalf("shutdown failed to join: %v", err)
	default:
	}
	svc.startAsyncRefresh("second", false) // would panic in the fake if admitted
	releaseRefresh()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if len(svc.refreshSlots) != 0 {
		t.Fatal("refresh slot leaked")
	}
	svc.refreshMu.Lock()
	defer svc.refreshMu.Unlock()
	if len(svc.refreshing) != 0 {
		t.Fatal("refresh identity leaked")
	}
}

func TestServiceWorkerAdmissionClosesAtShutdown(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	svc := NewService(store, &fakeIGDB{}, database)
	started := make(chan struct{})
	svc.startWorker(func(ctx context.Context) { close(started); waitContext(ctx, 24*time.Hour) })
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{}, 1)
	svc.startWorker(func(context.Context) { ran <- struct{}{} })
	// Admission is synchronous; after it returns, wait for any incorrectly added
	// worker before checking its result so no assertion runs after test teardown.
	svc.workers.Wait()
	select {
	case <-ran:
		t.Fatal("worker admitted after shutdown")
	default:
	}
}
