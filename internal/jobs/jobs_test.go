package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cato/internal/db"
)

func openDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunnerRunsAndPersistsSchedule(t *testing.T) {
	database := openDB(t)
	var runs atomic.Int32
	r := NewRunner(database, nil)
	r.Add(Job{Name: "tick", Interval: time.Hour, Run: func(context.Context) error { runs.Add(1); return nil }})
	r.Add(Job{Name: "broken", Interval: time.Hour, Run: func(context.Context) error { return errors.New("boom") }})
	r.Add(Job{Name: "panics", Interval: time.Hour, Run: func(context.Context) error { panic("bad") }})
	r.Start(context.Background())
	waitFor(t, func() bool {
		sts, _ := r.Statuses(context.Background())
		for _, st := range sts {
			if st.Runs == 0 {
				return false
			}
		}
		return true
	})
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Errorf("tick ran %d times, want 1 (next run is an hour away)", runs.Load())
	}
	sts, _ := r.Statuses(context.Background())
	for _, st := range sts {
		if until := time.Until(st.NextRunAt); until < 59*time.Minute || until > 61*time.Minute {
			t.Errorf("%s next run in %v", st.Name, until)
		}
		switch st.Name {
		case "broken":
			if st.Failures != 1 || st.LastError != "boom" {
				t.Errorf("broken: %+v", st)
			}
		case "panics":
			if st.Failures != 1 || st.LastError == "" {
				t.Errorf("a panicking job must be recorded, not crash the runner: %+v", st)
			}
		}
	}

	// A restart keeps the persisted schedule instead of running again.
	r2 := NewRunner(database, nil)
	var again atomic.Int32
	r2.Add(Job{Name: "tick", Interval: time.Hour, Run: func(context.Context) error { again.Add(1); return nil }})
	r2.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	r2.Shutdown(context.Background())
	if again.Load() != 0 {
		t.Errorf("restarted runner re-ran a job that isn't due")
	}
}

func TestRunNowAndLag(t *testing.T) {
	database := openDB(t)
	var runs atomic.Int32
	r := NewRunner(database, nil)
	r.Add(Job{Name: "later", Interval: time.Hour, FirstRun: time.Hour, Run: func(context.Context) error { runs.Add(1); return nil }})
	r.Start(context.Background())
	defer r.Shutdown(context.Background())
	waitFor(t, func() bool { sts, _ := r.Statuses(context.Background()); return !sts[0].NextRunAt.IsZero() })
	if runs.Load() != 0 {
		t.Fatal("FirstRun delay ignored")
	}
	if !r.RunNow("later") || r.RunNow("nope") {
		t.Fatal("RunNow")
	}
	waitFor(t, func() bool { return runs.Load() == 1 })

	// A job whose next run is long past reports lag.
	database.Exec(`UPDATE jobs SET next_run_at = ? WHERE name = 'later'`, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339Nano))
	lag, name := r.MaxLag(context.Background())
	if name != "later" || lag < 8*time.Minute {
		t.Errorf("lag %v %q", lag, name)
	}
}

func TestShutdownCancelsRunningJob(t *testing.T) {
	database := openDB(t)
	r := NewRunner(database, nil)
	started := make(chan struct{})
	r.Add(Job{Name: "slow", Interval: time.Hour, Run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	r.Start(context.Background())
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
