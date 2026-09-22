package covers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkerShutdownCancelsDownloadAndJoins(t *testing.T) {
	database := testDB(t)
	entered, canceled := make(chan struct{}), make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(canceled) }))
	defer remote.Close()
	seedUserAndLibraryItem(t, database, "worker-user", 700)
	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id,source_url) VALUES (700,?)`, remote.URL); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(database, t.TempDir())
	worker.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer worker.Shutdown(ctx)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("download not started")
	}
	if err := worker.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("HTTP request not canceled")
	}
	var attempts int
	var next string
	if err := database.QueryRow(`SELECT attempts,next_attempt_at FROM cover_jobs WHERE game_id=700`).Scan(&attempts, &next); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || next > utcNow() {
		t.Fatalf("canceled job not released: attempts=%d next=%s", attempts, next)
	}
	if CoverExists(worker.coverDir, 700) {
		t.Fatal("canceled download saved cover")
	}
	worker.Start() // cannot restart or admit more work after shutdown
}

func TestWorkerShutdownBeforeStart(t *testing.T) {
	worker := NewWorker(testDB(t), t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	worker.Start()
	if err := worker.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteJobRollsBackAtRemovalBoundary(t *testing.T) {
	database := testDB(t)
	seedGame(t, database, 701)
	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id,source_url) VALUES (701,'unused')`); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(database, t.TempDir())
	// Force failure after the metadata update, exactly at the job-removal
	// boundary. Neither half of completion may survive a transaction failure.
	if _, err := database.Exec(`CREATE TRIGGER interrupt_completion BEFORE DELETE ON cover_jobs BEGIN SELECT RAISE(ABORT,'interrupted completion'); END`); err != nil {
		t.Fatal(err)
	}
	if err := worker.completeJob(context.Background(), 701); err == nil {
		t.Fatal("completion unexpectedly succeeded")
	}
	assertPending := func() {
		t.Helper()
		var path string
		var count int
		if err := database.QueryRow(`SELECT local_cover_path FROM games WHERE id=701`).Scan(&path); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id=701`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if path != "" || count != 1 {
			t.Fatalf("partial completion: path=%q jobs=%d", path, count)
		}
	}
	assertPending()
	if _, err := database.Exec(`DROP TRIGGER interrupt_completion`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.completeJob(ctx, 701); err == nil {
		t.Fatal("canceled completion succeeded")
	}
	assertPending()
	if err := worker.completeJob(context.Background(), 701); err != nil {
		t.Fatal(err)
	}
	var path string
	var count int
	if err := database.QueryRow(`SELECT local_cover_path FROM games WHERE id=701`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM cover_jobs WHERE game_id=701`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if path != publicCoverPath(701) || count != 0 {
		t.Fatalf("completion: path=%q jobs=%d", path, count)
	}
}
