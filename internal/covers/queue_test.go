package covers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F'}

// F4: an enqueue wakes the idle worker at once instead of waiting for a poll.
func TestWakeStartsQueuedJobImmediately(t *testing.T) {
	database := testDB(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(jpegBytes) }))
	defer remote.Close()
	seedUserAndLibraryItem(t, database, "waker", 800)
	worker := NewWorker(database, t.TempDir())
	worker.idleWait = time.Hour // only Wake can start the job
	worker.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer worker.Shutdown(ctx)
	time.Sleep(50 * time.Millisecond) // let the worker go idle on an empty queue
	if _, err := database.Exec(`INSERT INTO cover_jobs (game_id, source_url) VALUES (800, ?)`, remote.URL); err != nil {
		t.Fatal(err)
	}
	worker.Wake()
	deadline := time.Now().Add(2 * time.Second)
	for !CoverExists(worker.coverDir, 800) {
		if time.Now().After(deadline) {
			t.Fatal("woken worker did not download the cover")
		}
		time.Sleep(10 * time.Millisecond)
	}
	worker.Wake() // never blocks, even with nothing to do
	worker.Wake()
}

// F4: jobs for games that are in nobody's library are pruned.
func TestPruneJobsKeepsOnlyLibraryGames(t *testing.T) {
	database := testDB(t)
	seedUserAndLibraryItem(t, database, "keeper", 810)
	seedGame(t, database, 811)
	for _, id := range []int{810, 811} {
		if _, err := database.Exec(`INSERT INTO cover_jobs (game_id, source_url) VALUES (?, 'https://images.igdb.com/x.jpg')`, id); err != nil {
			t.Fatal(err)
		}
	}
	n, err := PruneJobs(context.Background(), database)
	if err != nil || n != 1 {
		t.Fatalf("PruneJobs = %d, %v", n, err)
	}
	var left int64
	database.QueryRow(`SELECT game_id FROM cover_jobs`).Scan(&left)
	if left != 810 {
		t.Fatalf("remaining job = %d", left)
	}
}
