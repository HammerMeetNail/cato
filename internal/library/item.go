// Package library is the library domain: typed items, the shared filter and
// sort builder, writes (with playthrough and session bookkeeping), the
// compact index used for on-device search, stats and export. HTTP handlers
// stay thin and call into it.
package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
)

// Statuses, in shelf order. Display labels live in the frontend
// (format.js): wishlist is "Want", completed "Finished", abandoned "Dropped".
const (
	StatusWishlist  = "wishlist"
	StatusBacklog   = "backlog"
	StatusPlaying   = "playing"
	StatusPaused    = "paused"
	StatusCompleted = "completed"
	StatusAbandoned = "abandoned"
)

var Statuses = []string{StatusWishlist, StatusBacklog, StatusPlaying, StatusPaused, StatusCompleted, StatusAbandoned}

func ValidStatus(s string) bool {
	for _, v := range Statuses {
		if s == v {
			return true
		}
	}
	return false
}

// Completion types recorded on a finished playthrough.
var Completions = []string{"main", "main_extras", "completionist", "endless", "dropped"}

func validCompletion(c string) bool {
	for _, v := range Completions {
		if c == v {
			return true
		}
	}
	return false
}

var ErrNotFound = errors.New("not in library")

// GameNotFoundError means the catalog has no game with that ID.
var ErrGameNotFound = errors.New("game not found")

// ValidationError is a rejected input. Field names the offending JSON field.
type ValidationError struct {
	Code    string
	Message string
	Field   string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(code, field, msg string) error {
	return &ValidationError{Code: code, Field: field, Message: msg}
}

// Game is the catalog summary carried by library items.
type Game struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Cover       string   `json:"cover"`
	ReleaseDate int64    `json:"release_date"`
	Platforms   []string `json:"platforms"`
	// TTBMinutes is IGDB's "normally" time to beat (0 when unknown).
	TTBMinutes int64 `json:"ttb_minutes"`
}

// Item is one game in a user's library.
type Item struct {
	GameID               int64    `json:"game_id"`
	Status               string   `json:"status"`
	Rating               int64    `json:"rating"`
	Minutes              int64    `json:"minutes"`
	Tags                 []string `json:"tags"`
	Notes                string   `json:"notes"`
	StartedAt            *string  `json:"started_at"`
	CompletedAt          *string  `json:"completed_at"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
	Platforms            []string `json:"platforms"`
	Medium               string   `json:"medium"`
	CurrentPlaythroughID *int64   `json:"current_playthrough_id"`
	Rev                  int64    `json:"rev"`
	Game                 Game     `json:"game"`
}

// CoverURL picks the locally cached cover, falling back to the IGDB URL.
func CoverURL(local, remote string) string {
	if local != "" {
		return local
	}
	return remote
}

// itemSelect is the column list scanned by scanItem.
const itemSelect = `li.game_id, li.status, li.rating, li.playtime_minutes, li.tags_json,
	li.notes, li.started_at, li.completed_at, li.created_at, li.updated_at,
	li.owned_platforms_json, li.medium, li.current_playthrough_id, li.rev,
	g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date, g.platforms_json, g.ttb_normally`

type scanner interface{ Scan(...any) error }

func scanItem(row scanner, names map[int64]string) (Item, error) {
	var it Item
	var tagsJSON, ownedJSON, platformsJSON, coverURL, localCover string
	var started, completed sql.NullString
	var current sql.NullInt64
	if err := row.Scan(&it.GameID, &it.Status, &it.Rating, &it.Minutes, &tagsJSON,
		&it.Notes, &started, &completed, &it.CreatedAt, &it.UpdatedAt,
		&ownedJSON, &it.Medium, &current, &it.Rev,
		&it.Game.Name, &it.Game.Slug, &coverURL, &localCover, &it.Game.ReleaseDate, &platformsJSON, &it.Game.TTBMinutes); err != nil {
		return Item{}, err
	}
	it.Game.ID = it.GameID
	it.Game.Cover = CoverURL(localCover, coverURL)
	it.Tags = decodeStrings(tagsJSON)
	it.Platforms = decodeStrings(ownedJSON)
	it.Game.Platforms = catalog.ResolvePlatformNames(platformsJSON, names)
	if it.Game.Platforms == nil {
		it.Game.Platforms = []string{}
	}
	if started.Valid {
		it.StartedAt = &started.String
	}
	if completed.Valid {
		it.CompletedAt = &completed.String
	}
	if current.Valid {
		it.CurrentPlaythroughID = &current.Int64
	}
	return it, nil
}

func decodeStrings(raw string) []string {
	out := []string{}
	var vals []any
	if json.Unmarshal([]byte(raw), &vals) != nil {
		return out
	}
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func encodeStrings(vals []string) string {
	if len(vals) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(vals)
	return string(b)
}

// PlatformNamer resolves IGDB platform IDs (catalog.Store satisfies it).
type PlatformNamer interface {
	PlatformNames(ctx context.Context) (map[int64]string, error)
}

// Store reads and writes libraries.
type Store struct {
	db    *db.DB
	names PlatformNamer
	// now is the clock (tests pin it).
	now func() time.Time
	// OnWrite runs after a committed write that may have queued a cover job.
	OnWrite func()
	// Scorer ranks backlog games for Up Next (set by the catalog layer).
	Scorer func(ctx context.Context, userID string, n int) ([]UpNextPick, error)
}

func NewStore(database *db.DB, names PlatformNamer) *Store {
	return &Store{db: database, names: names, now: time.Now}
}

func (s *Store) platformNames(ctx context.Context) map[int64]string {
	if s.names == nil {
		return map[int64]string{}
	}
	names, err := s.names.PlatformNames(ctx)
	if err != nil {
		return map[int64]string{}
	}
	return names
}

// querier is a read/write handle: *db.DB or the *sql.Tx of a write.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) get(ctx context.Context, q querier, userID string, gameID int64) (*Item, error) {
	row := q.QueryRowContext(ctx, `SELECT `+itemSelect+`
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.game_id = ?`, userID, gameID)
	it, err := scanItem(row, s.platformNames(ctx))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// Get returns one library item.
func (s *Store) Get(ctx context.Context, userID string, gameID int64) (*Item, error) {
	return s.get(ctx, s.db, userID, gameID)
}

// Rev is the user's library revision: it changes whenever any of their
// library rows (or the catalog fields the index carries) change.
func (s *Store) Rev(ctx context.Context, userID string) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT library_rev FROM user_state WHERE user_id = ?`, userID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

// Statuses present in the library with their counts, plus "all".
func (s *Store) Counts(ctx context.Context, userID string) (map[string]int64, error) {
	counts := map[string]int64{"all": 0}
	for _, st := range Statuses {
		counts[st] = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM library_items WHERE user_id = ? GROUP BY status`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		counts[st] = n
		counts["all"] += n
	}
	return counts, rows.Err()
}

// Status returns the library status of each given game the user has.
func (s *Store) Statuses(ctx context.Context, userID string, gameIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(gameIDs) == 0 {
		return out, nil
	}
	args := []any{userID}
	ph := make([]string, 0, len(gameIDs))
	for _, id := range gameIDs {
		args = append(args, id)
		ph = append(ph, "?")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT game_id, status FROM library_items WHERE user_id = ? AND game_id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
