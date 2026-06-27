package igdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cato/internal/games"
)

type Client struct {
	clientID     string
	clientSecret string
	accessToken  string
	tokenExpiry  time.Time
	httpClient   *http.Client
	rateLimiter  *games.IGDBRateLimiter
	mu           sync.Mutex
}

type igdbGame struct {
	ID                    int64   `json:"id"`
	Name                  string  `json:"name"`
	Slug                  string  `json:"slug"`
	Summary               string  `json:"summary"`
	Storyline             string  `json:"storyline"`
	Cover                 int64   `json:"cover"`
	FirstReleaseDate      int64   `json:"first_release_date"`
	AggregatedRating      float64 `json:"aggregated_rating"`
	AggregatedRatingCount int64   `json:"aggregated_rating_count"`
	Platforms             []int64 `json:"platforms"`
	Genres                []int64 `json:"genres"`
	URL                   string  `json:"url"`
	UpdatedAt             int64   `json:"updated_at"`
	Rating                float64 `json:"rating"`
	RatingCount           int64   `json:"rating_count"`
	TotalRating           float64 `json:"total_rating"`
	TotalRatingCount      int64   `json:"total_rating_count"`
	Follows               int64   `json:"follows"`
	Hypes                 int64   `json:"hypes"`
	Category              int64   `json:"category"`
	Status                int64   `json:"status"`
	VersionParent         int64   `json:"version_parent"`
}

// igdbFields is the IGDB API v4 fields clause requested on every games query.
// Extended to include popularity signals (follows, hypes,
// total_rating_count, etc.) used to compute Game.PopularityScore.
//
// Note: IGDB's games endpoint does NOT accept a "popularity" field name
// (returns 400 "Invalid field name"); the raw IGDB popularity score is
// only available via the separate /popularity endpoint. We therefore store
// igdb_popularity as NULL and compute popularity_score solely from
// follows, hypes, total_rating_count, category, and status. Do not add
// `popularity` back to this list without first verifying via the
// /popularity endpoint integration.
const igdbFields = "id,name,slug,summary,storyline,cover,first_release_date,aggregated_rating,aggregated_rating_count,platforms,genres,url,updated_at,rating,rating_count,total_rating,total_rating_count,follows,hypes,category,status,version_parent"

func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		rateLimiter:  games.NewIGDBRateLimiter(),
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) SearchGames(ctx context.Context, query string, limit int) ([]games.Game, error) {
	if c.clientID == "" {
		return nil, nil
	}

	c.rateLimiter.Wait()

	body := fmt.Sprintf(`search "%s"; fields %s; limit %d;`, query, igdbFields, limit)

	igdbGames, err := c.post(ctx, "games", body)
	if err != nil {
		return nil, err
	}

	result := make([]games.Game, 0, len(igdbGames))
	for _, g := range igdbGames {
		result = append(result, c.toGame(g))
	}

	return result, nil
}

func (c *Client) GetGame(ctx context.Context, id int64) (*games.Game, error) {
	if c.clientID == "" {
		return nil, nil
	}

	c.rateLimiter.Wait()

	body := fmt.Sprintf(`where id = %d; fields %s;`, id, igdbFields)

	igdbGames, err := c.post(ctx, "games", body)
	if err != nil {
		return nil, err
	}

	if len(igdbGames) == 0 {
		return nil, nil
	}

	g := c.toGame(igdbGames[0])
	return &g, nil
}

func (c *Client) toGame(g igdbGame) games.Game {
	return games.Game{
		ID:                    g.ID,
		Name:                  g.Name,
		Slug:                  g.Slug,
		SafeName:              g.Name,
		NormalizedName:        games.NormalizeName(g.Name),
		Summary:               g.Summary,
		Storyline:             g.Storyline,
		CoverID:               g.Cover,
		CoverURL:              igdbCoverURL(g.Cover),
		FirstReleaseDate:      g.FirstReleaseDate,
		AggregatedRating:      int64(g.AggregatedRating),
		AggregatedRatingCount: g.AggregatedRatingCount,
		PlatformsJSON:         intsToJSON(g.Platforms),
		GenresJSON:            intsToJSON(g.Genres),
		IGDBURL:               g.URL,
		SourceUpdatedAt:       g.UpdatedAt,
		Rating:                g.Rating,
		RatingCount:           g.RatingCount,
		TotalRating:           g.TotalRating,
		TotalRatingCount:      g.TotalRatingCount,
		Follows:               g.Follows,
		Hypes:                 g.Hypes,
		IGDBPopularity:        0,
		Category:              g.Category,
		Status:                g.Status,
		VersionParent:         g.VersionParent,
		PopularityScore: games.ComputePopularityScore(
			g.Follows, g.Hypes, g.TotalRatingCount, g.Category, g.Status,
		),
	}
}

func (c *Client) authenticate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-5*time.Minute)) {
		return nil
	}

	if c.clientSecret == "" {
		return nil
	}

	data := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"grant_type":    {"client_credentials"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://id.twitch.tv/oauth2/token", strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}

	c.accessToken = tokenResp.AccessToken
	if tokenResp.ExpiresIn > 0 {
		c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}

	return nil
}

func (c *Client) post(ctx context.Context, endpoint, body string) ([]igdbGame, error) {
	if err := c.authenticate(ctx); err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.igdb.com/v4/"+endpoint, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Client-ID", c.clientID)
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("igdb rate limited (429)")
	}
	if resp.StatusCode == http.StatusUnauthorized && c.clientSecret != "" {
		c.mu.Lock()
		c.accessToken = ""
		c.mu.Unlock()
		return nil, fmt.Errorf("igdb unauthorized — token may have expired, will retry")
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("igdb server error: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("igdb returned %d: %s", resp.StatusCode, string(respBody))
	}

	var games []igdbGame
	if err := json.NewDecoder(resp.Body).Decode(&games); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return games, nil
}

func igdbCoverURL(coverID int64) string {
	if coverID == 0 {
		return ""
	}
	return fmt.Sprintf("https://images.igdb.com/igdb/image/upload/t_cover_big/co%05d.jpg", coverID)
}

func intsToJSON(ints []int64) string {
	if len(ints) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ints)
	return string(b)
}
