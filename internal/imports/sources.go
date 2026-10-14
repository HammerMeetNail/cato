package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cato/internal/library"
)

// ---- Cato JSON export --------------------------------------------------------

// CatoExport is the part of /api/v1/export.json an import reads.
type CatoExport struct {
	Format  string `json:"format"`
	Library []struct {
		GameID      int64    `json:"game_id"`
		Status      string   `json:"status"`
		Rating      int64    `json:"rating"`
		Minutes     int64    `json:"minutes"`
		Tags        []string `json:"tags"`
		Notes       string   `json:"notes"`
		StartedAt   *string  `json:"started_at"`
		CompletedAt *string  `json:"completed_at"`
		Platforms   []string `json:"platforms"`
		Game        struct {
			Name        string `json:"name"`
			ReleaseDate int64  `json:"release_date"`
		} `json:"game"`
		Sessions []struct {
			PlayedOn string `json:"played_on"`
			Minutes  int64  `json:"minutes"`
			Note     string `json:"note"`
			Source   string `json:"source"`
		} `json:"sessions"`
	} `json:"library"`
	Reviews []ExportReview `json:"reviews"`
	Lists   []ExportList   `json:"lists"`
}

// ExportReview and ExportList are the people data in an export.
type ExportReview struct {
	GameID     int64  `json:"game_id"`
	GameName   string `json:"game_name"`
	Body       string `json:"body"`
	Spoiler    bool   `json:"spoiler"`
	Visibility string `json:"visibility"`
	CreatedAt  string `json:"created_at"`
}

type ExportList struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Ranked      bool   `json:"ranked"`
	Visibility  string `json:"visibility"`
	Items       []struct {
		GameID   int64  `json:"game_id"`
		GameName string `json:"game_name"`
		Note     string `json:"note"`
	} `json:"items"`
}

// ErrNotCatoExport means the JSON isn't a Cato export.
var ErrNotCatoExport = errors.New("that isn't a Cato export (cato-export.json)")

// ParseCatoJSON reads a Cato export. Games keep their IGDB IDs (custom
// games, which only exist on the server they were made on, match by name).
func ParseCatoJSON(data []byte) (*CatoExport, []Row, error) {
	var ex CatoExport
	if err := json.Unmarshal(data, &ex); err != nil || ex.Format != "cato-export" {
		return nil, nil, ErrNotCatoExport
	}
	rows := make([]Row, 0, len(ex.Library))
	for _, it := range ex.Library {
		r := Row{Title: it.Game.Name, Status: it.Status, Rating: it.Rating, Minutes: it.Minutes, Notes: it.Notes, Tags: it.Tags}
		if it.GameID > 0 {
			r.IGDBID = it.GameID
		}
		if it.Game.ReleaseDate > 0 {
			r.Year = time.Unix(it.Game.ReleaseDate, 0).UTC().Year()
		}
		if len(it.Platforms) > 0 {
			r.Platform = it.Platforms[0]
		}
		if it.StartedAt != nil {
			r.StartedAt = date(*it.StartedAt)
		}
		if it.CompletedAt != nil {
			r.CompletedAt = date(*it.CompletedAt)
		}
		for _, s := range it.Sessions {
			if s.Source != "adjust" && s.Source != "legacy" && s.Minutes > 0 {
				r.Sessions = append(r.Sessions, library.ImportSession{PlayedOn: s.PlayedOn, Minutes: s.Minutes, Note: s.Note})
			}
		}
		rows = append(rows, r)
	}
	return &ex, rows, nil
}

// ---- Steam ------------------------------------------------------------------

// Steam reads a person's owned games (plan §8.1). Their profile and game
// details must be public.
type Steam struct {
	Key      string
	HTTP     *http.Client
	Endpoint string // https://api.steampowered.com (tests override)
}

// SteamGame is one owned game.
type SteamGame struct {
	AppID          int64  `json:"appid"`
	Name           string `json:"name"`
	Minutes        int64  `json:"playtime_forever"`
	LastPlayedUnix int64  `json:"rtime_last_played"`
}

var (
	ErrSteamNotConfigured = errors.New("Steam import isn't set up on this server (STEAM_WEB_API_KEY)")
	ErrSteamPrivate       = errors.New("Steam didn't share any games. Set your Steam profile and “Game details” to Public, then try again")
	ErrSteamNotFound      = errors.New("no Steam profile found with that name or link")
)

var steamID64 = regexp.MustCompile(`^7656119\d{10}$`)

func (s *Steam) get(ctx context.Context, path string, q url.Values, into any) error {
	if s.Key == "" {
		return ErrSteamNotConfigured
	}
	base := s.Endpoint
	if base == "" {
		base = "https://api.steampowered.com"
	}
	q.Set("key", s.Key)
	q.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("steam: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return fmt.Errorf("steam rejected the API key (HTTP %d)", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("steam: HTTP %d", res.StatusCode)
	}
	return json.Unmarshal(body, into)
}

// ResolveID turns a SteamID64, a profile link or a custom URL name into a
// SteamID64.
func (s *Steam) ResolveID(ctx context.Context, input string) (string, error) {
	in := strings.TrimSpace(input)
	if u, err := url.Parse(in); err == nil && u.Host != "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] == "profiles" {
			in = parts[1]
		} else if len(parts) >= 2 && parts[0] == "id" {
			in = parts[1]
		}
	}
	if steamID64.MatchString(in) {
		return in, nil
	}
	if in == "" || len(in) > 64 {
		return "", ErrSteamNotFound
	}
	var r struct {
		Response struct {
			SteamID string `json:"steamid"`
			Success int    `json:"success"`
		} `json:"response"`
	}
	if err := s.get(ctx, "/ISteamUser/ResolveVanityURL/v1/", url.Values{"vanityurl": {in}}, &r); err != nil {
		return "", err
	}
	if r.Response.Success != 1 || !steamID64.MatchString(r.Response.SteamID) {
		return "", ErrSteamNotFound
	}
	return r.Response.SteamID, nil
}

// Persona is the profile's display name ("" if Steam doesn't say).
func (s *Steam) Persona(ctx context.Context, steamID string) string {
	var r struct {
		Response struct {
			Players []struct {
				Name string `json:"personaname"`
			} `json:"players"`
		} `json:"response"`
	}
	if s.get(ctx, "/ISteamUser/GetPlayerSummaries/v2/", url.Values{"steamids": {steamID}}, &r) != nil || len(r.Response.Players) == 0 {
		return ""
	}
	return r.Response.Players[0].Name
}

// OwnedGames lists the games on a Steam account.
func (s *Steam) OwnedGames(ctx context.Context, steamID string) ([]SteamGame, error) {
	var r struct {
		Response struct {
			Count *int        `json:"game_count"`
			Games []SteamGame `json:"games"`
		} `json:"response"`
	}
	q := url.Values{"steamid": {steamID}, "include_appinfo": {"1"}, "include_played_free_games": {"1"}}
	if err := s.get(ctx, "/IPlayerService/GetOwnedGames/v1/", q, &r); err != nil {
		return nil, err
	}
	if r.Response.Count == nil {
		return nil, ErrSteamPrivate
	}
	return r.Response.Games, nil
}

// SteamRows suggests a status for each owned game: played in the last 30
// days → Playing; played before → Backlog (marked played); never → Backlog.
func SteamRows(games []SteamGame, now time.Time) []Row {
	rows := make([]Row, 0, len(games))
	for _, g := range games {
		r := Row{Title: g.Name, SteamAppID: g.AppID, Minutes: g.Minutes, Status: "backlog", Platform: "PC (Microsoft Windows)"}
		if g.Minutes > 0 {
			r.Played = true
			if g.LastPlayedUnix > 0 && now.Sub(time.Unix(g.LastPlayedUnix, 0)) < 30*24*time.Hour {
				r.Status = "playing"
			}
		}
		if r.Title == "" {
			r.Title = "Steam app " + strconv.FormatInt(g.AppID, 10)
		}
		rows = append(rows, r)
	}
	return rows
}

// ---- RetroAchievements --------------------------------------------------------

// RetroAchievements reads a person's progress with their own web API key.
type RetroAchievements struct {
	HTTP     *http.Client
	Endpoint string // https://retroachievements.org (tests override)
}

type raProgress struct {
	Count   int `json:"Count"`
	Total   int `json:"Total"`
	Results []struct {
		GameID      int64   `json:"GameID"`
		Title       string  `json:"Title"`
		ConsoleName string  `json:"ConsoleName"`
		NumAwarded  int     `json:"NumAwarded"`
		MaxPossible int     `json:"MaxPossible"`
		Award       *string `json:"HighestAwardKind"`
		AwardDate   *string `json:"HighestAwardDate"`
		Recent      string  `json:"MostRecentAwardedDate"`
	} `json:"Results"`
}

var raTag = regexp.MustCompile(`~[^~]*~\s*`)

// Rows fetches every game the person has progress in (500 per page).
func (ra *RetroAchievements) Rows(ctx context.Context, user, key string, now time.Time) ([]Row, error) {
	base := ra.Endpoint
	if base == "" {
		base = "https://retroachievements.org"
	}
	client := ra.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	var rows []Row
	for offset := 0; offset < 5000; offset += 500 {
		q := url.Values{"u": {user}, "y": {key}, "c": {"500"}, "o": {strconv.Itoa(offset)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/API/API_GetUserCompletionProgress.php?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("retroachievements: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			return nil, errors.New("RetroAchievements rejected that user name and key")
		}
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("retroachievements: HTTP %d", res.StatusCode)
		}
		var p raProgress
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("retroachievements: %w", err)
		}
		for _, g := range p.Results {
			r := Row{Title: strings.TrimSpace(raTag.ReplaceAllString(g.Title, "")), Platform: g.ConsoleName, Status: "backlog"}
			award := ""
			if g.Award != nil {
				award = *g.Award
			}
			switch {
			case strings.HasPrefix(award, "mastered") || strings.HasPrefix(award, "completed") || strings.HasPrefix(award, "beaten"):
				r.Status = "completed"
				if g.AwardDate != nil {
					r.CompletedAt = date(*g.AwardDate)
				}
			case g.NumAwarded > 0:
				r.Status = "paused"
				if t, err := time.Parse("2006-01-02 15:04:05", g.Recent); err == nil && now.Sub(t) < 30*24*time.Hour {
					r.Status = "playing"
				}
			}
			if g.MaxPossible > 0 {
				r.Notes = fmt.Sprintf("RetroAchievements: %d of %d", g.NumAwarded, g.MaxPossible)
			}
			rows = append(rows, r)
		}
		if len(p.Results) < 500 || offset+500 >= p.Total {
			break
		}
	}
	return rows, nil
}
