package igdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cato/internal/catalog"
)

// Rich data endpoints (plan §6.1). Field names follow IGDB API v4; where
// IGDB renamed a field (category → game_type / external_game_source / type,
// region → release_region, collection → collections) both are requested
// and the new one wins.

const detailsFields = "id,genres,themes,game_modes,player_perspectives," +
	"involved_companies.company.name,involved_companies.developer,involved_companies.publisher," +
	"collections.name,collection.name,franchises.name,franchise.name," +
	"similar_games,dlcs,expansions,standalone_expansions,remakes,remasters,ports,bundles," +
	"release_dates.date,release_dates.platform,release_dates.release_region,release_dates.region,release_dates.human," +
	"screenshots.image_id,artworks.image_id,videos.video_id,videos.name," +
	"websites.url,websites.type,websites.category," +
	"external_games.uid,external_games.external_game_source,external_games.category," +
	"age_ratings.organization.name,age_ratings.rating_category.rating,age_ratings.category,age_ratings.rating," +
	"language_supports.language.name,language_supports.language_support_type," +
	"multiplayer_modes.campaigncoop,multiplayer_modes.offlinecoop,multiplayer_modes.onlinecoop,multiplayer_modes.splitscreen," +
	"multiplayer_modes.lancoop,multiplayer_modes.offlinecoopmax,multiplayer_modes.onlinemax"

type namedObj struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type igdbDetails struct {
	ID                   int64      `json:"id"`
	Genres               []int64    `json:"genres"`
	Themes               []int64    `json:"themes"`
	GameModes            []int64    `json:"game_modes"`
	PlayerPerspectives   []int64    `json:"player_perspectives"`
	Collections          []namedObj `json:"collections"`
	Collection           *namedObj  `json:"collection"`
	Franchises           []namedObj `json:"franchises"`
	Franchise            *namedObj  `json:"franchise"`
	SimilarGames         []int64    `json:"similar_games"`
	DLCs                 []int64    `json:"dlcs"`
	Expansions           []int64    `json:"expansions"`
	StandaloneExpansions []int64    `json:"standalone_expansions"`
	Remakes              []int64    `json:"remakes"`
	Remasters            []int64    `json:"remasters"`
	Ports                []int64    `json:"ports"`
	Bundles              []int64    `json:"bundles"`
	InvolvedCompanies    []struct {
		Company   namedObj `json:"company"`
		Developer bool     `json:"developer"`
		Publisher bool     `json:"publisher"`
	} `json:"involved_companies"`
	ReleaseDates []struct {
		Date          int64  `json:"date"`
		Platform      int64  `json:"platform"`
		ReleaseRegion int64  `json:"release_region"`
		Region        int64  `json:"region"`
		Human         string `json:"human"`
	} `json:"release_dates"`
	Screenshots []struct {
		ImageID string `json:"image_id"`
	} `json:"screenshots"`
	Artworks []struct {
		ImageID string `json:"image_id"`
	} `json:"artworks"`
	Videos []struct {
		VideoID string `json:"video_id"`
		Name    string `json:"name"`
	} `json:"videos"`
	Websites []struct {
		URL      string `json:"url"`
		Type     int64  `json:"type"`
		Category int64  `json:"category"`
	} `json:"websites"`
	ExternalGames []struct {
		UID                string `json:"uid"`
		ExternalGameSource int64  `json:"external_game_source"`
		Category           int64  `json:"category"`
	} `json:"external_games"`
	AgeRatings []struct {
		Organization   *namedObj `json:"organization"`
		RatingCategory *struct {
			Rating string `json:"rating"`
		} `json:"rating_category"`
		Category int64 `json:"category"`
		Rating   int64 `json:"rating"`
	} `json:"age_ratings"`
	LanguageSupports []struct {
		Language            *namedObj `json:"language"`
		LanguageSupportType int64     `json:"language_support_type"`
	} `json:"language_supports"`
	MultiplayerModes []struct {
		CampaignCoop   bool  `json:"campaigncoop"`
		OfflineCoop    bool  `json:"offlinecoop"`
		OnlineCoop     bool  `json:"onlinecoop"`
		Splitscreen    bool  `json:"splitscreen"`
		LANCoop        bool  `json:"lancoop"`
		OfflineCoopMax int64 `json:"offlinecoopmax"`
		OnlineMax      int64 `json:"onlinemax"`
	} `json:"multiplayer_modes"`
}

// Deprecated age rating enums (category 1 = ESRB, 2 = PEGI; rating values).
var legacyRatingOrg = map[int64]string{1: "ESRB", 2: "PEGI", 3: "CERO", 4: "USK", 5: "GRAC", 6: "CLASS_IND", 7: "ACB"}
var legacyRating = map[int64]string{1: "3", 2: "7", 3: "12", 4: "16", 5: "18", 6: "RP", 7: "EC", 8: "E", 9: "E10+", 10: "T", 11: "M", 12: "AO"}

func idList(ids []int64) string {
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(strs, ",")
}

func (c *Client) query(ctx context.Context, endpoint, body string, into any) error {
	if c.clientID == "" {
		return nil
	}
	if err := c.rateLimiter.WaitContext(ctx); err != nil {
		return err
	}
	raw, err := c.do(ctx, endpoint, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

// GetDetails fetches the expanded record of up to 500 games.
func (c *Client) GetDetails(ctx context.Context, ids []int64) ([]catalog.Details, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxBatchIDs {
		return nil, fmt.Errorf("batch size %d exceeds IGDB limit of %d", len(ids), maxBatchIDs)
	}
	var raw []igdbDetails
	if err := c.query(ctx, "games", fmt.Sprintf("fields %s; where id = (%s); limit %d;", detailsFields, idList(ids), len(ids)), &raw); err != nil {
		return nil, err
	}
	out := make([]catalog.Details, 0, len(raw))
	for _, g := range raw {
		out = append(out, toDetails(g))
	}
	return out, nil
}

func toDetails(g igdbDetails) catalog.Details {
	d := catalog.Details{
		GameID: g.ID, Genres: g.Genres, Themes: g.Themes, Modes: g.GameModes, Perspectives: g.PlayerPerspectives,
		Related: map[string][]int64{
			"similar": g.SimilarGames, "dlc": g.DLCs, "expansion": g.Expansions, "standalone": g.StandaloneExpansions,
			"remake": g.Remakes, "remaster": g.Remasters, "port": g.Ports, "bundle": g.Bundles,
		},
		Screenshots: []string{}, Artworks: []string{}, Videos: []catalog.Video{}, Websites: []catalog.Website{},
		AgeRatings: []string{}, Languages: []string{},
	}
	for _, ic := range g.InvolvedCompanies {
		d.Companies = append(d.Companies, catalog.Company{ID: ic.Company.ID, Name: ic.Company.Name, Developer: ic.Developer, Publisher: ic.Publisher})
	}
	seen := map[int64]bool{}
	for _, col := range append(g.Collections, ptrList(g.Collection)...) {
		if !seen[col.ID] {
			seen[col.ID] = true
			d.Collections = append(d.Collections, catalog.NamedRef{ID: col.ID, Name: col.Name})
		}
	}
	seen = map[int64]bool{}
	for _, f := range append(g.Franchises, ptrList(g.Franchise)...) {
		if !seen[f.ID] {
			seen[f.ID] = true
			d.Franchises = append(d.Franchises, catalog.NamedRef{ID: f.ID, Name: f.Name})
		}
	}
	for _, r := range g.ReleaseDates {
		region := r.ReleaseRegion
		if region == 0 {
			region = r.Region
		}
		d.ReleaseDates = append(d.ReleaseDates, catalog.ReleaseDate{PlatformID: r.Platform, Date: r.Date, Region: region, Human: r.Human})
	}
	for _, s := range g.Screenshots {
		if s.ImageID != "" {
			d.Screenshots = append(d.Screenshots, s.ImageID)
		}
	}
	for _, a := range g.Artworks {
		if a.ImageID != "" {
			d.Artworks = append(d.Artworks, a.ImageID)
		}
	}
	for _, v := range g.Videos {
		if v.VideoID != "" {
			d.Videos = append(d.Videos, catalog.Video{VideoID: v.VideoID, Name: v.Name})
		}
	}
	for _, w := range g.Websites {
		t := w.Type
		if t == 0 {
			t = w.Category
		}
		d.Websites = append(d.Websites, catalog.Website{Type: t, URL: w.URL})
	}
	for _, e := range g.ExternalGames {
		src := e.ExternalGameSource
		if src == 0 {
			src = e.Category
		}
		d.External = append(d.External, catalog.ExternalID{Source: src, UID: e.UID})
	}
	ratings := map[string]bool{}
	for _, a := range g.AgeRatings {
		var label string
		switch {
		case a.Organization != nil && a.RatingCategory != nil && a.RatingCategory.Rating != "":
			label = a.Organization.Name + " " + a.RatingCategory.Rating
		case legacyRatingOrg[a.Category] != "" && legacyRating[a.Rating] != "":
			label = legacyRatingOrg[a.Category] + " " + legacyRating[a.Rating]
		}
		if label != "" && !ratings[label] {
			ratings[label] = true
			d.AgeRatings = append(d.AgeRatings, label)
		}
	}
	langs := map[string]bool{}
	for _, l := range g.LanguageSupports {
		if l.Language != nil && l.Language.Name != "" && !langs[l.Language.Name] {
			langs[l.Language.Name] = true
			d.Languages = append(d.Languages, l.Language.Name)
		}
	}
	if len(g.MultiplayerModes) > 0 {
		m := &catalog.Multiplayer{}
		for _, mm := range g.MultiplayerModes {
			m.CampaignCoop = m.CampaignCoop || mm.CampaignCoop
			m.OfflineCoop = m.OfflineCoop || mm.OfflineCoop
			m.OnlineCoop = m.OnlineCoop || mm.OnlineCoop
			m.Splitscreen = m.Splitscreen || mm.Splitscreen
			m.LAN = m.LAN || mm.LANCoop
			m.OfflineMax = max(m.OfflineMax, mm.OfflineCoopMax)
			m.OnlineMax = max(m.OnlineMax, mm.OnlineMax)
		}
		d.Multiplayer = m
	}
	return d
}

func ptrList(n *namedObj) []namedObj {
	if n == nil || n.ID == 0 {
		return nil
	}
	return []namedObj{*n}
}

// GetTimesToBeat fetches game_time_to_beats for up to 500 games.
func (c *Client) GetTimesToBeat(ctx context.Context, ids []int64) ([]catalog.TimeToBeat, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []catalog.TimeToBeat
	err := c.query(ctx, "game_time_to_beats", fmt.Sprintf("fields game_id,hastily,normally,completely,count; where game_id = (%s); limit %d;", idList(ids), min(len(ids), maxBatchIDs)), &out)
	return out, err
}

// GetLookup fetches a whole reference table (genres, themes, …).
func (c *Client) GetLookup(ctx context.Context, kind string) ([]catalog.NamedRef, error) {
	switch kind {
	case "genres", "themes", "game_modes", "player_perspectives", "game_types":
	default:
		return nil, fmt.Errorf("unknown lookup %q", kind)
	}
	var raw []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
		Slug string `json:"slug"`
	}
	fields := "id,name,slug"
	if kind == "game_types" {
		fields = "id,type"
	}
	if err := c.query(ctx, kind, "fields "+fields+"; limit 500;", &raw); err != nil {
		return nil, err
	}
	out := make([]catalog.NamedRef, 0, len(raw))
	for _, r := range raw {
		name := r.Name
		if name == "" {
			name = r.Type
		}
		if name != "" {
			out = append(out, catalog.NamedRef{ID: r.ID, Name: name, Slug: r.Slug})
		}
	}
	return out, nil
}

// GetUpdatedSince pages through games changed after since (unix seconds),
// oldest change first.
func (c *Client) GetUpdatedSince(ctx context.Context, since int64, offset, limit int) ([]catalog.Game, error) {
	if limit <= 0 || limit > maxBatchIDs {
		limit = maxBatchIDs
	}
	games, err := c.postGames(ctx, fmt.Sprintf("fields %s; where updated_at > %d; sort updated_at asc; limit %d; offset %d;", igdbFields, since, limit, offset))
	if err != nil {
		return nil, err
	}
	out := make([]catalog.Game, 0, len(games))
	for _, g := range games {
		out = append(out, c.toGame(g))
	}
	return out, nil
}

// GetGamesFull fetches complete core records for up to 500 games.
func (c *Client) GetGamesFull(ctx context.Context, ids []int64) ([]catalog.Game, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxBatchIDs {
		return nil, fmt.Errorf("batch size %d exceeds IGDB limit of %d", len(ids), maxBatchIDs)
	}
	games, err := c.postGames(ctx, fmt.Sprintf("fields %s; where id = (%s); limit %d;", igdbFields, idList(ids), len(ids)))
	if err != nil {
		return nil, err
	}
	out := make([]catalog.Game, 0, len(games))
	for _, g := range games {
		out = append(out, c.toGame(g))
	}
	return out, nil
}

func (c *Client) postGames(ctx context.Context, body string) ([]igdbGame, error) {
	if c.clientID == "" {
		return nil, nil
	}
	if err := c.rateLimiter.WaitContext(ctx); err != nil {
		return nil, err
	}
	return c.post(ctx, "games", body)
}

// GetPopular returns the top games for one popularity type (PopScore).
func (c *Client) GetPopular(ctx context.Context, popularityType int64, limit int) ([]catalog.Popularity, error) {
	if limit <= 0 || limit > maxBatchIDs {
		limit = maxBatchIDs
	}
	var out []catalog.Popularity
	err := c.query(ctx, "popularity_primitives", fmt.Sprintf("fields game_id,value,popularity_type; where popularity_type = %d; sort value desc; limit %d;", popularityType, limit), &out)
	return out, err
}

var _ catalog.DetailsClient = (*Client)(nil)
