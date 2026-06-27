package games

import "time"

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
	PopularityScore       int64   `json:"popularity_score"`
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
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Slug             string `json:"slug"`
	CoverURL         string `json:"cover_url"`
	LocalCoverPath   string `json:"local_cover_path"`
	FirstReleaseDate int64  `json:"first_release_date"`
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

func (rl *IGDBRateLimiter) Wait() {
	rl.mu <- struct{}{}
	defer func() { <-rl.mu }()

	elapsed := time.Since(rl.lastRequest)
	if elapsed < time.Second {
		time.Sleep(time.Second - elapsed)
	}
	rl.lastRequest = time.Now()
}
