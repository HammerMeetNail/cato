package catalog

import (
	"context"
	"sync"
	"time"
)

type Game struct {
	ID                    int64   `json:"id"`
	Name                  string  `json:"name"`
	Slug                  string  `json:"slug"`
	SafeName              string  `json:"safe_name"`
	NormalizedName        string  `json:"normalized_name"`
	Summary               string  `json:"summary"`
	Storyline             string  `json:"storyline"`
	CoverID               int64   `json:"cover_id"`
	CoverURL              string  `json:"cover_url"`
	LocalCoverPath        string  `json:"local_cover_path"`
	FirstReleaseDate      int64   `json:"first_release_date"`
	AggregatedRating      int64   `json:"aggregated_rating"`
	AggregatedRatingCount int64   `json:"aggregated_rating_count"`
	PlatformsJSON         string  `json:"platforms_json"`
	GenresJSON            string  `json:"genres_json"`
	Trailer               string  `json:"trailer"`
	IGDBURL               string  `json:"igdb_url"`
	SourceUpdatedAt       int64   `json:"source_updated_at"`
	Rating                float64 `json:"rating"`
	RatingCount           int64   `json:"rating_count"`
	TotalRating           float64 `json:"total_rating"`
	TotalRatingCount      int64   `json:"total_rating_count"`
	Follows               int64   `json:"follows"`
	Hypes                 int64   `json:"hypes"`
	IGDBPopularity        float64 `json:"igdb_popularity"`
	Category              int64   `json:"category"`
	Status                int64   `json:"status"`
	VersionParent         int64   `json:"version_parent"`
	ParentGame            int64   `json:"parent_game"`
	PopularityScore       int64   `json:"popularity_score"`

	// Aliases carries IGDB alternative_names (abbreviations, localized
	// titles) into UpsertIGDBGame, which persists them in game_aliases for
	// alias-aware search. Never serialized to API clients and never stored
	// on the games row itself.
	Aliases []string `json:"-"`

	// Platforms is the display form of PlatformsJSON (IDs resolved to names
	// via the platforms lookup table). Populated on read paths only —
	// never persisted.
	Platforms []string `json:"platforms,omitempty"`
}

// ComputePopularityScore blends IGDB signals into a single sortable integer.
// Weighting: follows*3 + hypes*2 + total_rating_count, plus a flat 10-point
// bonus for released main games (category==0, status==0). Follows is weighted
// highest because it tracks current community attention; total_rating_count is
// the vote-count floor that lets real games outrank obscure stubs. Stored on
// the row at upsert time so search ORDER BY stays a single indexed scan.
func ComputePopularityScore(follows, hypes, totalRatingCount, category, status int64) int64 {
	score := follows*3 + hypes*2 + totalRatingCount
	if category == 0 && status == 0 {
		score += 10
	}
	return score
}

type GameResult struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	Slug             string   `json:"slug"`
	CoverURL         string   `json:"cover_url"`
	LocalCoverPath   string   `json:"local_cover_path"`
	FirstReleaseDate int64    `json:"first_release_date"`
	Platforms        []string `json:"platforms,omitempty"`
}

// Lane is an IGDB request priority. Interactive requests (a person is
// waiting: search, opening a game page) are served before background ones
// (refresh, backfills, sync) whenever both are queued.
type Lane int

const (
	LaneBackground Lane = iota
	LaneInteractive
)

type laneKey struct{}

// WithLane tags ctx's IGDB requests with a priority lane.
func WithLane(ctx context.Context, lane Lane) context.Context {
	return context.WithValue(ctx, laneKey{}, lane)
}

// LaneOf reports ctx's lane (background unless tagged).
func LaneOf(ctx context.Context) Lane {
	if l, ok := ctx.Value(laneKey{}).(Lane); ok {
		return l
	}
	return LaneBackground
}

// IGDBRateLimiter spaces IGDB requests to the documented limit (4 requests
// per second) with two priority lanes: a waiting interactive request always
// takes the next slot before any background one.
type IGDBRateLimiter struct {
	interval time.Duration

	mu      sync.Mutex
	next    time.Time        // earliest time the next request may start
	queues  [2][]*laneWaiter // indexed by Lane
	changed chan struct{}    // closed and replaced whenever a slot is taken or a waiter leaves
	stats   [2]laneStats
}

type laneWaiter struct{}

type laneStats struct {
	requests int64
	waited   time.Duration
}

// LaneStatus is a snapshot for the admin System page.
type LaneStatus struct {
	Lane       string  `json:"lane"`
	Queued     int     `json:"queued"`
	Requests   int64   `json:"requests"`
	AvgWaitSec float64 `json:"avg_wait_seconds"`
}

func NewIGDBRateLimiter() *IGDBRateLimiter {
	return NewIGDBRateLimiterWithInterval(250 * time.Millisecond)
}

// NewIGDBRateLimiterWithInterval builds a limiter with a custom request
// spacing. Production uses 250ms (IGDB's 4 req/s); tests pass 0 to run
// unthrottled against a local server.
func NewIGDBRateLimiterWithInterval(interval time.Duration) *IGDBRateLimiter {
	return &IGDBRateLimiter{interval: interval, changed: make(chan struct{})}
}

// WaitContext waits for the next slot in ctx's lane, returning early when
// ctx is canceled (search requests are made per keystroke and abandoned).
func (rl *IGDBRateLimiter) WaitContext(ctx context.Context) error {
	lane := LaneOf(ctx)
	w := &laneWaiter{}
	start := time.Now()
	rl.mu.Lock()
	rl.queues[lane] = append(rl.queues[lane], w)
	for {
		if err := ctx.Err(); err != nil {
			rl.remove(lane, w)
			rl.mu.Unlock()
			return err
		}
		ahead := len(rl.queues[lane]) > 0 && rl.queues[lane][0] == w
		if lane == LaneBackground && len(rl.queues[LaneInteractive]) > 0 {
			ahead = false
		}
		now := time.Now()
		if ahead && !now.Before(rl.next) {
			rl.queues[lane] = rl.queues[lane][1:]
			rl.next = now.Add(rl.interval)
			rl.stats[lane].requests++
			rl.stats[lane].waited += now.Sub(start)
			rl.broadcast()
			rl.mu.Unlock()
			return nil
		}
		changed := rl.changed
		var timer *time.Timer
		var timeout <-chan time.Time
		if ahead {
			timer = time.NewTimer(rl.next.Sub(now))
			timeout = timer.C
		}
		rl.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		case <-timeout:
		}
		if timer != nil {
			timer.Stop()
		}
		rl.mu.Lock()
	}
}

func (rl *IGDBRateLimiter) remove(lane Lane, w *laneWaiter) {
	q := rl.queues[lane]
	for i, x := range q {
		if x == w {
			rl.queues[lane] = append(q[:i:i], q[i+1:]...)
			break
		}
	}
	rl.broadcast()
}

func (rl *IGDBRateLimiter) broadcast() {
	close(rl.changed)
	rl.changed = make(chan struct{})
}

// Wait is the non-cancelable form for callers without a request context.
func (rl *IGDBRateLimiter) Wait() {
	_ = rl.WaitContext(context.Background())
}

// Status reports queue depth and average wait per lane.
func (rl *IGDBRateLimiter) Status() []LaneStatus {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	out := make([]LaneStatus, 0, 2)
	for _, lane := range []Lane{LaneInteractive, LaneBackground} {
		st := rl.stats[lane]
		avg := 0.0
		if st.requests > 0 {
			avg = st.waited.Seconds() / float64(st.requests)
		}
		name := "background"
		if lane == LaneInteractive {
			name = "interactive"
		}
		out = append(out, LaneStatus{Lane: name, Queued: len(rl.queues[lane]), Requests: st.requests, AvgWaitSec: avg})
	}
	return out
}
