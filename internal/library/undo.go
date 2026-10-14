package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cato/internal/auth"
)

// UndoWindow is how long a write can be rolled back.
const UndoWindow = 15 * time.Minute

// ErrUndoExpired means the undo ID is unknown, used, or too old.
var ErrUndoExpired = errors.New("nothing to undo")

// itemSnapshot is everything a write can change for one game: the library
// row (nil when the game wasn't in the library) and its journal.
type itemSnapshot struct {
	GameID       int64        `json:"game_id"`
	Row          *rowSnapshot `json:"row,omitempty"`
	Playthroughs []ptSnapshot `json:"playthroughs,omitempty"`
	Sessions     []ssSnapshot `json:"sessions,omitempty"`
}

type rowSnapshot struct {
	Status      string  `json:"status"`
	Rating      int64   `json:"rating"`
	TagsJSON    string  `json:"tags_json"`
	Notes       string  `json:"notes"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	Platform    string  `json:"platform"`
	Medium      string  `json:"medium"`
	OwnedJSON   string  `json:"owned_json"`
	CurrentPT   *int64  `json:"current_playthrough_id"`
}

type ptSnapshot struct {
	ID         int64   `json:"id"`
	Platform   string  `json:"platform"`
	StartedOn  *string `json:"started_on"`
	FinishedOn *string `json:"finished_on"`
	Completion *string `json:"completion"`
	Rating     *int64  `json:"rating"`
	Note       string  `json:"note"`
	CreatedAt  string  `json:"created_at"`
}

type ssSnapshot struct {
	ID            int64  `json:"id"`
	PlaythroughID *int64 `json:"playthrough_id"`
	PlayedOn      string `json:"played_on"`
	Minutes       int64  `json:"minutes"`
	Note          string `json:"note"`
	Source        string `json:"source"`
	CreatedAt     string `json:"created_at"`
}

func ptrString(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

func ptrInt(ni sql.NullInt64) *int64 {
	if ni.Valid {
		return &ni.Int64
	}
	return nil
}

func snapshotItem(ctx context.Context, q querier, userID string, gameID int64) (itemSnapshot, error) {
	snap := itemSnapshot{GameID: gameID}
	var r rowSnapshot
	var started, completed sql.NullString
	var cur sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT status, rating, tags_json, notes, started_at, completed_at, created_at, updated_at,
			platform, medium, owned_platforms_json, current_playthrough_id
		FROM library_items WHERE user_id = ? AND game_id = ?`, userID, gameID).
		Scan(&r.Status, &r.Rating, &r.TagsJSON, &r.Notes, &started, &completed, &r.CreatedAt, &r.UpdatedAt,
			&r.Platform, &r.Medium, &r.OwnedJSON, &cur)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	r.StartedAt, r.CompletedAt, r.CurrentPT = ptrString(started), ptrString(completed), ptrInt(cur)
	snap.Row = &r

	rows, err := q.QueryContext(ctx, `SELECT id, platform, started_on, finished_on, completion, rating, note, created_at
		FROM playthroughs WHERE user_id = ? AND game_id = ? ORDER BY id`, userID, gameID)
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var p ptSnapshot
		var so, fo, comp sql.NullString
		var rating sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Platform, &so, &fo, &comp, &rating, &p.Note, &p.CreatedAt); err != nil {
			rows.Close()
			return snap, err
		}
		p.StartedOn, p.FinishedOn, p.Completion, p.Rating = ptrString(so), ptrString(fo), ptrString(comp), ptrInt(rating)
		snap.Playthroughs = append(snap.Playthroughs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return snap, err
	}

	rows, err = q.QueryContext(ctx, `SELECT id, playthrough_id, played_on, minutes, note, source, created_at
		FROM play_sessions WHERE user_id = ? AND game_id = ? ORDER BY id`, userID, gameID)
	if err != nil {
		return snap, err
	}
	defer rows.Close()
	for rows.Next() {
		var ss ssSnapshot
		var pt sql.NullInt64
		if err := rows.Scan(&ss.ID, &pt, &ss.PlayedOn, &ss.Minutes, &ss.Note, &ss.Source, &ss.CreatedAt); err != nil {
			return snap, err
		}
		ss.PlaythroughID = ptrInt(pt)
		snap.Sessions = append(snap.Sessions, ss)
	}
	return snap, rows.Err()
}

func saveUndo(ctx context.Context, tx *sql.Tx, userID string, snaps []itemSnapshot) (string, error) {
	if len(snaps) == 0 {
		return "", nil
	}
	b, err := json.Marshal(snaps)
	if err != nil {
		return "", err
	}
	id := auth.RandomToken(16)
	if _, err := tx.ExecContext(ctx, `DELETE FROM undo_log WHERE user_id = ? AND created_at < ?`,
		userID, time.Now().Add(-UndoWindow).UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO undo_log (id, user_id, snapshot_json) VALUES (?, ?, ?)`, id, userID, string(b)); err != nil {
		return "", err
	}
	return id, nil
}

// Undo restores the rows a write replaced. It returns the restored items
// (games that weren't in the library before the write are removed again).
func (s *Store) Undo(ctx context.Context, userID, undoID string) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM undo_log WHERE id = ? AND user_id = ? AND created_at >= ?`,
		undoID, userID, time.Now().Add(-UndoWindow).UTC().Format(time.RFC3339)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUndoExpired
	}
	if err != nil {
		return nil, err
	}
	var snaps []itemSnapshot
	if err := json.Unmarshal([]byte(raw), &snaps); err != nil {
		return nil, err
	}
	var ids []int64
	for _, snap := range snaps {
		if err := restoreItem(ctx, tx, userID, snap); err != nil {
			return nil, err
		}
		ids = append(ids, snap.GameID)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM undo_log WHERE id = ?`, undoID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func restoreItem(ctx context.Context, tx *sql.Tx, userID string, snap itemSnapshot) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_items WHERE user_id = ? AND game_id = ?`, userID, snap.GameID); err != nil {
		return err
	}
	r := snap.Row
	if r == nil {
		return nil
	}
	// The row starts at zero minutes: re-inserting the sessions restores the
	// total through the session triggers.
	if _, err := tx.ExecContext(ctx, `INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes,
			started_at, completed_at, created_at, updated_at, platform, medium, owned_platforms_json)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, snap.GameID, r.Status, r.Rating, r.TagsJSON, r.Notes, r.StartedAt, r.CompletedAt,
		r.CreatedAt, r.UpdatedAt, r.Platform, r.Medium, r.OwnedJSON); err != nil {
		return err
	}
	for _, p := range snap.Playthroughs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO playthroughs (id, user_id, game_id, platform, started_on, finished_on, completion, minutes, rating, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			p.ID, userID, snap.GameID, p.Platform, p.StartedOn, p.FinishedOn, p.Completion, p.Rating, p.Note, p.CreatedAt); err != nil {
			return err
		}
	}
	for _, ss := range snap.Sessions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO play_sessions (id, user_id, game_id, playthrough_id, played_on, minutes, note, source, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ss.ID, userID, snap.GameID, ss.PlaythroughID, ss.PlayedOn, ss.Minutes, ss.Note, ss.Source, ss.CreatedAt); err != nil {
			return err
		}
	}
	// Session inserts moved updated_at; put it (and the current playthrough)
	// back so the item returns to its old place in "recent".
	_, err := tx.ExecContext(ctx, `UPDATE library_items SET updated_at = ?, current_playthrough_id = ? WHERE user_id = ? AND game_id = ?`,
		r.UpdatedAt, r.CurrentPT, userID, snap.GameID)
	return err
}

// PruneUndo deletes expired undo snapshots (maintenance sweep).
func PruneUndo(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) (int64, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM undo_log WHERE created_at < ?`, time.Now().Add(-UndoWindow).UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
