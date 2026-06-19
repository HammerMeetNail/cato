package games

import "time"

type Game struct {
	ID                    int64  `json:"id"`
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	SafeName              string `json:"safe_name"`
	NormalizedName        string `json:"normalized_name"`
	Summary               string `json:"summary"`
	Storyline             string `json:"storyline"`
	CoverID               int64  `json:"cover_id"`
	CoverURL              string `json:"cover_url"`
	LocalCoverPath        string `json:"local_cover_path"`
	FirstReleaseDate      int64  `json:"first_release_date"`
	AggregatedRating      int64  `json:"aggregated_rating"`
	AggregatedRatingCount int64  `json:"aggregated_rating_count"`
	PlatformsJSON         string `json:"platforms_json"`
	GenresJSON            string `json:"genres_json"`
	Trailer              string `json:"trailer"`
	IGDBURL               string `json:"igdb_url"`
	SourceUpdatedAt       int64  `json:"source_updated_at"`
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
