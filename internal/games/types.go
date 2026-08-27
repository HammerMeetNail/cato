package games

import (
	"context"
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

type IGDBRateLimiter struct {
	lastRequest time.Time
	mu          chan struct{}
}

func NewIGDBRateLimiter() *IGDBRateLimiter {
	return &IGDBRateLimiter{
		mu: make(chan struct{}, 1),
	}
}

// WaitContext waits for the next rate-limit slot, returning early when the
// request has been canceled. Search requests are created on every debounced
// keystroke; without cancellation, browser-aborted requests still occupied a
// slot for a full second and delayed the query the user actually wants.
func (rl *IGDBRateLimiter) WaitContext(ctx context.Context) error {
	select {
	case rl.mu <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-rl.mu }()

	if elapsed := time.Since(rl.lastRequest); elapsed < time.Second {
		timer := time.NewTimer(time.Second - elapsed)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rl.lastRequest = time.Now()
	return nil
}

// Wait retains the non-cancelable form for callers that do not have a request
// context, such as maintenance jobs.
func (rl *IGDBRateLimiter) Wait() {
	_ = rl.WaitContext(context.Background())
}
