// Package jobs runs Cato's background work (catalog refresh, cover queue
// sweeps, cleanup, digests, alerts) on one runner: each job has an
// interval with jitter, its next run time is persisted so restarts keep
// the cadence, runs are cancelled on shutdown, and the schedule is visible
// to /healthz and the admin System page.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"cato/internal/db"
)

// Job is one recurring task.
type Job struct {
	Name     string
	Interval time.Duration
	// Jitter adds up to this much random delay to each next run, so jobs
	// sharing an interval don't fire together.
	Jitter time.Duration
	// FirstRun is the delay before the first ever run (no persisted
	// schedule yet). Zero runs it right away.
	FirstRun time.Duration
	// Timeout bounds one run (default: the interval).
	Timeout time.Duration
	Run     func(ctx context.Context) error
}

// Status is a job's persisted schedule and last outcome.
type Status struct {
	Name           string    `json:"name"`
	NextRunAt      time.Time `json:"next_run_at"`
	LastStartedAt  time.Time `json:"last_started_at"`
	LastFinishedAt time.Time `json:"last_finished_at"`
	LastDurationMS int64     `json:"last_duration_ms"`
	LastError      string    `json:"last_error"`
	Runs           int64     `json:"runs"`
	Failures       int64     `json:"failures"`
	Running        bool      `json:"running"`
	// LagSeconds is how overdue the job is (0 when on schedule).
	LagSeconds float64 `json:"lag_seconds"`
}

type Runner struct {
	db   *db.DB
	jobs []Job
	now  func() time.Time
	log  *slog.Logger

	mu      sync.Mutex
	running map[string]bool
	wake    map[string]chan struct{}
	wg      sync.WaitGroup
	cancel  context.CancelFunc
}

func NewRunner(database *db.DB, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{db: database, now: time.Now, log: logger, running: map[string]bool{}, wake: map[string]chan struct{}{}}
}

// Add registers a job; call before Start.
func (r *Runner) Add(j Job) {
	if j.Timeout == 0 {
		j.Timeout = j.Interval
	}
	r.jobs = append(r.jobs, j)
	r.wake[j.Name] = make(chan struct{}, 1)
}

// Start launches every job's loop.
func (r *Runner) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	for _, j := range r.jobs {
		r.wg.Add(1)
		go func(j Job) {
			defer r.wg.Done()
			r.loop(ctx, j)
		}(j)
	}
}

// RunNow asks a job to run at its next opportunity (e.g. an admin button).
func (r *Runner) RunNow(name string) bool {
	ch, ok := r.wake[name]
	if !ok {
		return false
	}
	select {
	case ch <- struct{}{}:
	default:
	}
	return true
}

// Shutdown cancels running jobs and waits for their loops to exit.
func (r *Runner) Shutdown(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jobs did not stop: %w", ctx.Err())
	}
}

func (r *Runner) jitter(j Job) time.Duration {
	if j.Jitter <= 0 {
		return 0
	}
	return rand.N(j.Jitter)
}

func (r *Runner) loop(ctx context.Context, j Job) {
	next, err := r.nextRun(ctx, j)
	if err != nil {
		r.log.Error("jobs: load schedule", "job", j.Name, "err", err)
		next = r.now().Add(j.FirstRun)
	}
	for {
		wait := next.Sub(r.now())
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-r.wake[j.Name]:
				timer.Stop()
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return
		}
		next = r.runOnce(ctx, j)
	}
}

// nextRun reads the persisted schedule, creating it on first sight.
func (r *Runner) nextRun(ctx context.Context, j Job) (time.Time, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT next_run_at FROM jobs WHERE name = ?`, j.Name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		next := r.now().Add(j.FirstRun + r.jitter(j))
		_, err = r.db.ExecContext(ctx, `INSERT INTO jobs (name, next_run_at) VALUES (?, ?) ON CONFLICT(name) DO NOTHING`,
			j.Name, next.UTC().Format(time.RFC3339Nano))
		return next, err
	}
	if err != nil {
		return time.Time{}, err
	}
	next, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return r.now(), nil
	}
	// A schedule further out than one interval (clock change, shortened
	// interval) is pulled in.
	if limit := r.now().Add(j.Interval + j.Jitter); next.After(limit) {
		next = limit
	}
	return next, nil
}

func (r *Runner) runOnce(ctx context.Context, j Job) time.Time {
	r.mu.Lock()
	r.running[j.Name] = true
	r.mu.Unlock()
	start := r.now()
	r.db.ExecContext(ctx, `UPDATE jobs SET last_started_at = ? WHERE name = ?`, start.UTC().Format(time.RFC3339Nano), j.Name)

	runCtx, cancel := context.WithTimeout(ctx, j.Timeout)
	err := safeRun(runCtx, j)
	cancel()

	r.mu.Lock()
	r.running[j.Name] = false
	r.mu.Unlock()
	end := r.now()
	next := end.Add(j.Interval + r.jitter(j))
	if ctx.Err() != nil {
		// Shutting down: keep the schedule so the run resumes after restart.
		return next
	}
	errText, failed := "", 0
	if err != nil {
		errText, failed = err.Error(), 1
		r.log.Warn("job failed", "job", j.Name, "err", err, "duration", end.Sub(start))
	} else {
		r.log.Debug("job finished", "job", j.Name, "duration", end.Sub(start))
	}
	if _, dbErr := r.db.ExecContext(context.WithoutCancel(ctx), `UPDATE jobs SET next_run_at = ?, last_finished_at = ?, last_duration_ms = ?,
			last_error = ?, runs = runs + 1, failures = failures + ? WHERE name = ?`,
		next.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano), end.Sub(start).Milliseconds(),
		errText, failed, j.Name); dbErr != nil {
		r.log.Error("jobs: save schedule", "job", j.Name, "err", dbErr)
	}
	return next
}

func safeRun(ctx context.Context, j Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return j.Run(ctx)
}

// Statuses reports every registered job.
func (r *Runner) Statuses(ctx context.Context) ([]Status, error) {
	out := make([]Status, 0, len(r.jobs))
	now := r.now()
	for _, j := range r.jobs {
		st := Status{Name: j.Name}
		var next string
		var started, finished sql.NullString
		err := r.db.QueryRowContext(ctx, `SELECT next_run_at, last_started_at, last_finished_at, last_duration_ms, last_error, runs, failures
			FROM jobs WHERE name = ?`, j.Name).Scan(&next, &started, &finished, &st.LastDurationMS, &st.LastError, &st.Runs, &st.Failures)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		st.NextRunAt, _ = time.Parse(time.RFC3339Nano, next)
		st.LastStartedAt, _ = time.Parse(time.RFC3339Nano, started.String)
		st.LastFinishedAt, _ = time.Parse(time.RFC3339Nano, finished.String)
		r.mu.Lock()
		st.Running = r.running[j.Name]
		r.mu.Unlock()
		if !st.NextRunAt.IsZero() && !st.Running {
			// Allow a minute of slack before calling a job late.
			if lag := now.Sub(st.NextRunAt) - time.Minute; lag > 0 {
				st.LagSeconds = lag.Seconds()
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// MaxLag is the largest lag across jobs, for /healthz.
func (r *Runner) MaxLag(ctx context.Context) (time.Duration, string) {
	sts, err := r.Statuses(ctx)
	if err != nil {
		return 0, ""
	}
	var worst time.Duration
	name := ""
	for _, st := range sts {
		if d := time.Duration(st.LagSeconds * float64(time.Second)); d > worst {
			worst, name = d, st.Name
		}
	}
	return worst, name
}
