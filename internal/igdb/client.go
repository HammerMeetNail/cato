package igdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cato/internal/games"
)

// maxBatchIDs is IGDB's hard cap on `where id = (...)` tuple size.
const maxBatchIDs = 500

type Client struct {
	clientID     string
	clientSecret string
	accessToken  string
	tokenExpiry  time.Time
	httpClient   *http.Client
	rateLimiter  *games.IGDBRateLimiter
	mu           sync.Mutex
}

type igdbCover struct {
	ID      int64  `json:"id"`
	ImageID string `json:"image_id"`
}

// igdbAltName is one entry of the games.alternative_names expansion
// (abbreviations like "botw", localized titles, etc.).
type igdbAltName struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type igdbGame struct {
	ID                    int64         `json:"id"`
	Name                  string        `json:"name"`
	Slug                  string        `json:"slug"`
	Summary               string        `json:"summary"`
	Storyline             string        `json:"storyline"`
	Cover                 *igdbCover    `json:"cover"`
	AlternativeNames      []igdbAltName `json:"alternative_names"`
	FirstReleaseDate      int64         `json:"first_release_date"`
	AggregatedRating      float64       `json:"aggregated_rating"`
	AggregatedRatingCount int64         `json:"aggregated_rating_count"`
	Platforms             []int64       `json:"platforms"`
	Genres                []int64       `json:"genres"`
	URL                   string        `json:"url"`
	UpdatedAt             int64         `json:"updated_at"`
	Rating                float64       `json:"rating"`
	RatingCount           int64         `json:"rating_count"`
	TotalRating           float64       `json:"total_rating"`
	TotalRatingCount      int64         `json:"total_rating_count"`
	Follows               int64         `json:"follows"`
	Hypes                 int64         `json:"hypes"`
	Category              int64         `json:"category"`
	GameType              int64         `json:"game_type"`
	Status                int64         `json:"status"`
	VersionParent         int64         `json:"version_parent"`
	VersionTitle          string        `json:"version_title"`
	ParentGame            int64         `json:"parent_game"`
}

// igdbFields is the IGDB API v4 fields clause requested on every games query.
// Extended to include popularity signals (follows, hypes,
// total_rating_count, etc.) used to compute Game.PopularityScore, and
// alternative_names.name for alias-aware search (SEARCH_IMPROVEMENTS.md §4.1).
//
// cover.image_id is requested as a nested field: game.cover is an ID into the
// covers table, and the CDN URL must be built from that record's image_id
// (e.g. "co213263"), NOT from the numeric ID itself — the two coincide often
// enough to look correct, but guessing co%05d.jpg from the cover ID produces
// 404s for a meaningful fraction of games.
//
// Note: IGDB's games endpoint does NOT accept a "popularity" field name
// (returns 400 "Invalid field name"); the raw IGDB popularity score is
// only available via the separate /popularity endpoint. We therefore store
// igdb_popularity as NULL and compute popularity_score solely from
// follows, hypes, total_rating_count, category, and status. Do not add
// `popularity` back to this list without first verifying via the
// /popularity endpoint integration.
const igdbFields = "id,name,slug,summary,storyline,cover.id,cover.image_id,alternative_names.name,first_release_date,aggregated_rating,aggregated_rating_count,platforms,genres,url,updated_at,rating,rating_count,total_rating,total_rating_count,follows,hypes,category,game_type,status,version_parent,version_title,parent_game"

func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		rateLimiter:  games.NewIGDBRateLimiter(),
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) SearchGames(ctx context.Context, query string, limit int, includeEditions bool) ([]games.Game, error) {
	if c.clientID == "" {
		return nil, nil
	}

	c.rateLimiter.Wait()

	// Hide IGDB editions/packs by default: `version_parent` marks
	// deluxe/collector editions, `game_type` packs/skins (13) etc. Bypass
	// when the query explicitly asks for them or the caller opts in.
	editionInclude := includeEditions || games.ContainsEditionKeyword(query)
	packInclude := includeEditions || games.ContainsPackKeyword(query)
	var whereParts []string
	if !editionInclude {
		whereParts = append(whereParts, "version_parent = null")
	}
	if !packInclude {
		whereParts = append(whereParts, "game_type = (0,1,2,4,8,9,10,11)")
	}
	where := ""
	if len(whereParts) > 0 {
		where = " where " + strings.Join(whereParts, " & ") + ";"
	}
	body := fmt.Sprintf(`search "%s"; fields %s;%s limit %d;`, query, igdbFields, where, limit)

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

// GetGamesBatch fetches many games in a single rate-limited request, asking
// for id/name/alternative_names plus cover.image_id. The extra cover fields
// let the cover-URL repair batch (RepairCovers) verify and correct URLs that
// were once guessed from the numeric cover ID (co%05d) rather than the
// authoritative covers.image_id. IGDB accepts up to 500 IDs per query, so
// 300k+ rows cost ~1 request per 500 games (~10 min at the ~1 req/s limiter)
// instead of one request per game.
func (c *Client) GetGamesBatch(ctx context.Context, ids []int64) ([]games.Game, error) {
	if c.clientID == "" {
		return nil, nil
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxBatchIDs {
		return nil, fmt.Errorf("batch size %d exceeds IGDB limit of %d", len(ids), maxBatchIDs)
	}

	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = strconv.FormatInt(id, 10)
	}

	c.rateLimiter.Wait()

	// Include version_parent/category/parent_game so edition/pack backfills
	// can correct legacy rows without a separate endpoint. Harmless for
	// alias/cover batches.
	body := fmt.Sprintf(`where id = (%s); fields id,name,alternative_names.name,cover.id,cover.image_id,version_parent,version_title,category,game_type,parent_game; limit %d;`,
		strings.Join(strs, ","), len(ids))

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

// GetPlatforms fetches the complete platform reference list (id, name,
// abbreviation) in a single rate-limited request — the table is small
// (~230 rows), well under IGDB's page cap.
func (c *Client) GetPlatforms(ctx context.Context) ([]games.Platform, error) {
	if c.clientID == "" {
		return nil, nil
	}

	c.rateLimiter.Wait()

	raw, err := c.do(ctx, "platforms", "fields id,name,abbreviation; limit 500;")
	if err != nil {
		return nil, err
	}
	return decodePlatforms(raw)
}

func decodePlatforms(raw []byte) ([]games.Platform, error) {
	var plats []struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Abbreviation string `json:"abbreviation"`
	}
	if err := json.Unmarshal(raw, &plats); err != nil {
		return nil, fmt.Errorf("decode platforms: %w", err)
	}
	out := make([]games.Platform, 0, len(plats))
	for _, p := range plats {
		out = append(out, games.Platform{ID: p.ID, Name: p.Name, Abbreviation: p.Abbreviation})
	}
	return out, nil
}

func (c *Client) toGame(g igdbGame) games.Game {
	var coverID int64
	var coverURL string
	if g.Cover != nil {
		coverID = g.Cover.ID
		coverURL = igdbCoverURL(g.Cover.ImageID)
	}
	var aliases []string
	for _, a := range g.AlternativeNames {
		if a.Name != "" {
			aliases = append(aliases, a.Name)
		}
	}
	return games.Game{
		ID:                    g.ID,
		Name:                  g.Name,
		Slug:                  g.Slug,
		SafeName:              g.Name,
		NormalizedName:        games.NormalizeName(g.Name),
		Summary:               g.Summary,
		Storyline:             g.Storyline,
		CoverID:               coverID,
		CoverURL:              coverURL,
		Aliases:               aliases,
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
		Category:              gameCategory(g),
		Status:                g.Status,
		VersionParent:         g.VersionParent,
		ParentGame:            g.ParentGame,
		PopularityScore: games.ComputePopularityScore(
			g.Follows, g.Hypes, g.TotalRatingCount, gameCategory(g), g.Status,
		),
	}
}

func gameCategory(g igdbGame) int64 {
	if g.GameType != 0 {
		return g.GameType
	}
	return g.Category
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

// do authenticates, POSTs an APIQL query to one IGDB endpoint, and returns
// the raw response body. Endpoint-specific wrappers decode it.
func (c *Client) do(ctx context.Context, endpoint, body string) ([]byte, error) {
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

	return io.ReadAll(resp.Body)
}

func (c *Client) post(ctx context.Context, endpoint, body string) ([]igdbGame, error) {
	raw, err := c.do(ctx, endpoint, body)
	if err != nil {
		return nil, err
	}
	var games []igdbGame
	if err := json.Unmarshal(raw, &games); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return games, nil
}

// igdbCoverURL builds the CDN URL for a cover from its covers-table image_id
// (e.g. "co213263"). The image_id is the authoritative source for the URL —
// it cannot be derived from the game or cover numeric ID.
func igdbCoverURL(imageID string) string {
	if imageID == "" {
		return ""
	}
	return fmt.Sprintf("https://images.igdb.com/igdb/image/upload/t_cover_big/%s.jpg", imageID)
}

func intsToJSON(ints []int64) string {
	if len(ints) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ints)
	return string(b)
}
