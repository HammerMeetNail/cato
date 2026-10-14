package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Rich catalog data (plan §6.1): what a game page shows beyond the core row.

type NamedRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

type Company struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Developer bool   `json:"developer"`
	Publisher bool   `json:"publisher"`
}

type ReleaseDate struct {
	PlatformID int64  `json:"platform_id"`
	Date       int64  `json:"date"`
	Region     int64  `json:"region"`
	Human      string `json:"human"`
}

type Video struct {
	VideoID string `json:"video_id"`
	Name    string `json:"name"`
}

type Website struct {
	Type int64  `json:"type"`
	URL  string `json:"url"`
}

type ExternalID struct {
	Source int64  `json:"source"`
	UID    string `json:"uid"`
}

type Multiplayer struct {
	CampaignCoop bool  `json:"campaign_coop"`
	OfflineCoop  bool  `json:"offline_coop"`
	OnlineCoop   bool  `json:"online_coop"`
	Splitscreen  bool  `json:"splitscreen"`
	LAN          bool  `json:"lan"`
	OfflineMax   int64 `json:"offline_max"`
	OnlineMax    int64 `json:"online_max"`
}

// Relation kinds stored in game_relations.
var RelationKinds = []string{"similar", "dlc", "expansion", "standalone", "remake", "remaster", "port", "bundle"}

// Details is one game's expanded IGDB record.
type Details struct {
	GameID       int64              `json:"-"`
	Genres       []int64            `json:"-"`
	Themes       []int64            `json:"-"`
	Modes        []int64            `json:"-"`
	Perspectives []int64            `json:"-"`
	Companies    []Company          `json:"-"`
	Collections  []NamedRef         `json:"-"`
	Franchises   []NamedRef         `json:"-"`
	Related      map[string][]int64 `json:"-"`
	ReleaseDates []ReleaseDate      `json:"-"`
	External     []ExternalID       `json:"-"`

	// Display-only, kept as one JSON document in game_details.
	Screenshots []string     `json:"screenshots"`
	Artworks    []string     `json:"artworks"`
	Videos      []Video      `json:"videos"`
	Websites    []Website    `json:"websites"`
	AgeRatings  []string     `json:"age_ratings"`
	Languages   []string     `json:"languages"`
	Multiplayer *Multiplayer `json:"multiplayer,omitempty"`
}

// TimeToBeat is IGDB game_time_to_beats, in seconds.
type TimeToBeat struct {
	GameID     int64 `json:"game_id"`
	Hastily    int64 `json:"hastily"`
	Normally   int64 `json:"normally"`
	Completely int64 `json:"completely"`
	Count      int64 `json:"count"`
}

// Popularity is one IGDB popularity primitive (PopScore).
type Popularity struct {
	GameID int64   `json:"game_id"`
	Type   int64   `json:"popularity_type"`
	Value  float64 `json:"value"`
}

// Popularity types used for shelves (IGDB popularity_types).
const (
	PopularityVisits     = 1
	PopularityWantToPlay = 2
	PopularityPlaying    = 3
)

// DetailsClient is the optional IGDB surface for rich data; the live client
// implements it, fakes may not (detail features then stay off).
type DetailsClient interface {
	GetDetails(ctx context.Context, ids []int64) ([]Details, error)
	GetTimesToBeat(ctx context.Context, ids []int64) ([]TimeToBeat, error)
	GetLookup(ctx context.Context, kind string) ([]NamedRef, error)
	GetUpdatedSince(ctx context.Context, since int64, offset, limit int) ([]Game, error)
	GetPopular(ctx context.Context, popularityType int64, limit int) ([]Popularity, error)
	GetGamesFull(ctx context.Context, ids []int64) ([]Game, error)
}

// LookupKinds are the reference tables synced from IGDB.
var LookupKinds = []string{"genres", "themes", "game_modes", "player_perspectives", "game_types"}

func idsJSON(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// SaveDetails replaces a game's rich data in one transaction.
func (s *Store) SaveDetails(ctx context.Context, d Details, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM games WHERE id = ?`, d.GameID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not in the catalog (yet): nothing to attach to
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE games SET genres_json = ?, themes_json = ?, modes_json = ?, perspectives_json = ?,
			details_fetched_at = ? WHERE id = ?`,
		idsJSON(d.Genres), idsJSON(d.Themes), idsJSON(d.Modes), idsJSON(d.Perspectives), now.Unix(), d.GameID); err != nil {
		return err
	}
	for _, table := range []string{"game_relations", "game_release_dates", "game_companies", "game_series", "game_external_ids"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE game_id = ?`, d.GameID); err != nil {
			return err
		}
	}
	for kind, ids := range d.Related {
		for _, rid := range ids {
			if rid == 0 || rid == d.GameID {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO game_relations (game_id, related_id, kind) VALUES (?, ?, ?)`, d.GameID, rid, kind); err != nil {
				return err
			}
		}
	}
	for _, r := range d.ReleaseDates {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO game_release_dates (game_id, platform_id, date, region, human) VALUES (?, ?, ?, ?, ?)`,
			d.GameID, r.PlatformID, r.Date, r.Region, r.Human); err != nil {
			return err
		}
	}
	for _, c := range d.Companies {
		if c.Name == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO game_companies (game_id, company_id, name, developer, publisher) VALUES (?, ?, ?, ?, ?)`,
			d.GameID, c.ID, c.Name, c.Developer, c.Publisher); err != nil {
			return err
		}
	}
	for kind, refs := range map[string][]NamedRef{"collection": d.Collections, "franchise": d.Franchises} {
		for _, r := range refs {
			if r.ID == 0 || r.Name == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO game_series (game_id, series_id, kind, name) VALUES (?, ?, ?, ?)`,
				d.GameID, r.ID, kind, r.Name); err != nil {
				return err
			}
		}
	}
	for _, e := range d.External {
		if e.UID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO game_external_ids (game_id, source, uid) VALUES (?, ?, ?)`, d.GameID, e.Source, e.UID); err != nil {
			return err
		}
	}
	doc, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO game_details (game_id, json, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT(game_id) DO UPDATE SET json = excluded.json, fetched_at = excluded.fetched_at`, d.GameID, string(doc), now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveTimesToBeat stores time to beat in minutes.
func (s *Store) SaveTimesToBeat(ctx context.Context, ttbs []TimeToBeat) error {
	for _, t := range ttbs {
		if _, err := s.db.ExecContext(ctx, `UPDATE games SET ttb_hastily = ?, ttb_normally = ?, ttb_completely = ?, ttb_count = ? WHERE id = ?`,
			t.Hastily/60, t.Normally/60, t.Completely/60, t.Count, t.GameID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveLookups(ctx context.Context, kind string, refs []NamedRef) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lookups (kind, id, name, slug) VALUES (?, ?, ?, ?)
			ON CONFLICT(kind, id) DO UPDATE SET name = excluded.name, slug = excluded.slug`, kind, r.ID, r.Name, r.Slug); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LookupNames maps IDs of one lookup kind to names.
func (s *Store) LookupNames(ctx context.Context, kind string) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM lookups WHERE kind = ?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// SavePopularity replaces one popularity type's ranking and copies the
// visits score into games.igdb_popularity (used for search ranking).
func (s *Store) SavePopularity(ctx context.Context, popType int64, list []Popularity, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_popularity WHERE type = ?`, popType); err != nil {
		return err
	}
	for _, p := range list {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO game_popularity (game_id, type, value, updated_at) VALUES (?, ?, ?, ?)`,
			p.GameID, popType, p.Value, now.Unix()); err != nil {
			return err
		}
		if popType == PopularityVisits {
			if _, err := tx.ExecContext(ctx, `UPDATE games SET igdb_popularity = ? WHERE id = ?`, p.Value, p.GameID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// MissingGames returns which of ids aren't in the catalog.
func (s *Store) MissingGames(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	have := map[int64]bool{}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM games WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		have[id] = true
	}
	var missing []int64
	for _, id := range ids {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	return missing, rows.Err()
}

// StaleDetails lists library games whose details are older than cutoff.
func (s *Store) StaleDetails(ctx context.Context, cutoff time.Time, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT g.id FROM games g
		WHERE g.id > 0 AND g.details_fetched_at < ? AND EXISTS (SELECT 1 FROM library_items li WHERE li.game_id = g.id)
		ORDER BY g.details_fetched_at LIMIT ?`, cutoff.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DetailsAge reports when a game's details were last fetched (zero: never).
func (s *Store) DetailsAge(ctx context.Context, id int64) (time.Time, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT details_fetched_at FROM games WHERE id = ?`, id).Scan(&at)
	if err != nil {
		return time.Time{}, err
	}
	if at == 0 {
		return time.Time{}, nil
	}
	return time.Unix(at, 0), nil
}

func (s *Store) syncState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM igdb_sync_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) setSyncState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO igdb_sync_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, key, value)
	return err
}

var errNoDetailsClient = fmt.Errorf("IGDB details are not available")
