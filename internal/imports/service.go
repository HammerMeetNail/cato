package imports

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"cato/internal/db"
	"cato/internal/library"
	"cato/internal/social"
)

// Service runs imports and the Steam playtime sync.
type Service struct {
	DB      *db.DB
	Library *library.Store
	Social  *social.Service
	Matcher *Matcher
	Steam   *Steam
	RA      *RetroAchievements
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SteamLink is a person's connected Steam account.
type SteamLink struct {
	SteamID    string `json:"steam_id"`
	Persona    string `json:"persona"`
	Sync       bool   `json:"sync"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

func (s *Service) SteamLinkOf(ctx context.Context, userID string) (*SteamLink, error) {
	var l SteamLink
	var last sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT steam_id, persona, sync, last_sync_at, last_error FROM steam_links WHERE user_id = ?`, userID).
		Scan(&l.SteamID, &l.Persona, &l.Sync, &last, &l.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	l.LastSyncAt = last.String
	return &l, err
}

// SteamPreview resolves the profile, remembers it, and matches its games.
func (s *Service) SteamPreview(ctx context.Context, userID, profile string) (*SteamLink, []Candidate, error) {
	id, err := s.Steam.ResolveID(ctx, profile)
	if err != nil {
		return nil, nil, err
	}
	games, err := s.Steam.OwnedGames(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	persona := s.Steam.Persona(ctx, id)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO steam_links (user_id, steam_id, persona) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET steam_id = excluded.steam_id, persona = excluded.persona, last_error = ''`, userID, id, persona); err != nil {
		return nil, nil, err
	}
	cands, err := s.Matcher.Match(ctx, userID, SteamRows(games, s.now()))
	if err != nil {
		return nil, nil, err
	}
	link, err := s.SteamLinkOf(ctx, userID)
	return link, cands, err
}

// SetSteamSync turns the daily playtime sync on or off.
func (s *Service) SetSteamSync(ctx context.Context, userID string, on bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE steam_links SET sync = ? WHERE user_id = ?`, on, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return library.ErrNotFound
	}
	return nil
}

// UnlinkSteam forgets the Steam account and its playtime baseline.
func (s *Service) UnlinkSteam(ctx context.Context, userID string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM steam_links WHERE user_id = ?`, userID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM steam_playtime WHERE user_id = ?`, userID)
	return err
}

// CommitEntry is a reviewed row with the game the person chose.
type CommitEntry struct {
	GameID int64 `json:"game_id"`
	Row    Row   `json:"row"`
}

// Extras are the people data of a Cato JSON export.
type Extras struct {
	Reviews []ExportReview `json:"reviews"`
	Lists   []ExportList   `json:"lists"`
}

// CommitResult adds what the extras created.
type CommitResult struct {
	library.ImportResult
	Reviews int `json:"reviews"`
	Lists   int `json:"lists"`
}

// Commit writes the chosen games as one undoable batch.
func (s *Service) Commit(ctx context.Context, userID, source string, entries []CommitEntry, extras *Extras) (*CommitResult, error) {
	var ie []library.ImportEntry
	idFor := map[string]int64{} // the export's game (by IGDB ID or name) → chosen game
	for _, e := range entries {
		if e.GameID == 0 {
			continue
		}
		r := e.Row
		status := r.Status
		if status == "" {
			status = "backlog"
		}
		tags := r.Tags
		if r.Played && status == "backlog" {
			tags = append(append([]string{}, tags...), "played")
		}
		ie = append(ie, library.ImportEntry{GameID: e.GameID, Status: status, Rating: r.Rating, Minutes: r.Minutes, Notes: r.Notes,
			Tags: tags, Platform: r.Platform, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, Sessions: r.Sessions})
		if r.IGDBID != 0 {
			idFor["id:"+strconv.FormatInt(r.IGDBID, 10)] = e.GameID
		}
		idFor["name:"+strings.ToLower(r.Title)] = e.GameID
	}
	if len(ie) == 0 {
		return nil, errors.New("choose at least one game to import")
	}
	res, err := s.Library.Import(ctx, userID, source, ie)
	if err != nil {
		return nil, err
	}
	out := &CommitResult{ImportResult: *res}
	if source == "steam" {
		s.baselineSteam(ctx, userID, entries)
	}
	if extras != nil && s.Social != nil {
		resolve := func(id int64, name string) int64 {
			if g, ok := idFor["id:"+strconv.FormatInt(id, 10)]; ok {
				return g
			}
			return idFor["name:"+strings.ToLower(name)]
		}
		out.Reviews, out.Lists = s.importExtras(ctx, userID, extras, resolve)
	}
	return out, nil
}

// baselineSteam remembers Steam's playtime for the imported apps, so the
// daily sync logs only what's played from now on.
func (s *Service) baselineSteam(ctx context.Context, userID string, entries []CommitEntry) {
	for _, e := range entries {
		if e.GameID != 0 && e.Row.SteamAppID > 0 {
			s.DB.ExecContext(ctx, `INSERT INTO steam_playtime (user_id, appid, minutes) VALUES (?, ?, ?)
				ON CONFLICT(user_id, appid) DO UPDATE SET minutes = MAX(minutes, excluded.minutes)`, userID, e.Row.SteamAppID, e.Row.Minutes)
			s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO game_external_ids (game_id, source, uid) VALUES (?, ?, ?)`, e.GameID, SourceSteam, strconv.FormatInt(e.Row.SteamAppID, 10))
		}
	}
}

// importExtras recreates reviews (one per game you haven't reviewed) and
// lists (by title, adding missing games) from an export.
func (s *Service) importExtras(ctx context.Context, userID string, ex *Extras, resolve func(int64, string) int64) (reviews, lists int) {
	for _, r := range ex.Reviews {
		g := resolve(r.GameID, r.GameName)
		if g == 0 {
			continue
		}
		var n int
		s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM reviews WHERE user_id = ? AND game_id = ?`, userID, g).Scan(&n)
		if n > 0 {
			continue
		}
		if _, err := s.Social.WriteReview(ctx, userID, social.ReviewInput{GameID: g, Body: r.Body, Spoiler: r.Spoiler, Visibility: r.Visibility}); err == nil {
			reviews++
		}
	}
	for _, l := range ex.Lists {
		var id int64
		err := s.DB.QueryRowContext(ctx, `SELECT id FROM lists WHERE user_id = ? AND title = ?`, userID, l.Title).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			title, desc, vis := l.Title, l.Description, l.Visibility
			created, err := s.Social.CreateList(ctx, userID, social.ListInput{Title: &title, Description: &desc, Ranked: &l.Ranked, Visibility: &vis})
			if err != nil {
				continue
			}
			id = created.ID
			lists++
		} else if err != nil {
			continue
		}
		for _, it := range l.Items {
			if g := resolve(it.GameID, it.GameName); g != 0 {
				s.Social.AddToList(ctx, userID, id, g, it.Note)
			}
		}
	}
	return reviews, lists
}

// SyncSteam is the daily job: for everyone who turned the sync on, the
// change in each game's Steam playtime becomes a "steam" session on
// yesterday's date (games not in their library are skipped).
func (s *Service) SyncSteam(ctx context.Context) error {
	if s.Steam == nil || s.Steam.Key == "" {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT user_id, steam_id FROM steam_links WHERE sync = 1`)
	if err != nil {
		return err
	}
	type link struct{ user, steam string }
	var links []link
	for rows.Next() {
		var l link
		rows.Scan(&l.user, &l.steam)
		links = append(links, l)
	}
	rows.Close()
	for _, l := range links {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg := ""
		if err := s.syncOne(ctx, l.user, l.steam); err != nil {
			msg = err.Error()
			slog.WarnContext(ctx, "steam sync", "user", l.user, "err", err)
		}
		s.DB.ExecContext(ctx, `UPDATE steam_links SET last_sync_at = ?, last_error = ? WHERE user_id = ?`, s.now().UTC().Format(time.RFC3339), msg, l.user)
	}
	return nil
}

func (s *Service) syncOne(ctx context.Context, userID, steamID string) error {
	games, err := s.Steam.OwnedGames(ctx, steamID)
	if err != nil {
		return err
	}
	day := s.now().Add(-12 * time.Hour).Format("2006-01-02")
	for _, g := range games {
		var before int64
		err := s.DB.QueryRowContext(ctx, `SELECT minutes FROM steam_playtime WHERE user_id = ? AND appid = ?`, userID, g.AppID).Scan(&before)
		known := err == nil
		if !known && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if known && g.Minutes > before {
			var gameID int64
			s.DB.QueryRowContext(ctx, `SELECT li.game_id FROM game_external_ids x JOIN library_items li ON li.game_id = x.game_id AND li.user_id = ?
				WHERE x.source = ? AND x.uid = ? LIMIT 1`, userID, SourceSteam, strconv.FormatInt(g.AppID, 10)).Scan(&gameID)
			if gameID != 0 {
				if _, err := s.Library.LogSynced(ctx, userID, gameID, g.Minutes-before, day, "steam"); err != nil {
					return err
				}
			}
		}
		if !known || g.Minutes != before {
			if _, err := s.DB.ExecContext(ctx, `INSERT INTO steam_playtime (user_id, appid, minutes) VALUES (?, ?, ?)
				ON CONFLICT(user_id, appid) DO UPDATE SET minutes = excluded.minutes`, userID, g.AppID, g.Minutes); err != nil {
				return err
			}
		}
	}
	return nil
}

// RAPreview reads RetroAchievements progress (remembering the key when asked).
func (s *Service) RAPreview(ctx context.Context, userID, raUser, key string, remember bool) ([]Candidate, error) {
	raUser, key = strings.TrimSpace(raUser), strings.TrimSpace(key)
	if key == "" {
		s.DB.QueryRowContext(ctx, `SELECT ra_user, api_key FROM ra_links WHERE user_id = ?`, userID).Scan(&raUser, &key)
	}
	if raUser == "" || key == "" {
		return nil, errors.New("enter your RetroAchievements user name and web API key")
	}
	rows, err := s.RA.Rows(ctx, raUser, key, s.now())
	if err != nil {
		return nil, err
	}
	if remember {
		s.DB.ExecContext(ctx, `INSERT INTO ra_links (user_id, ra_user, api_key) VALUES (?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET ra_user = excluded.ra_user, api_key = excluded.api_key`, userID, raUser, key)
	}
	return s.Matcher.Match(ctx, userID, rows)
}
