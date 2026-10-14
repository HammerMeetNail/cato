package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"cato/internal/auth"
)

// ImportEntry is one game to bring in from another service or a file.
type ImportEntry struct {
	GameID      int64    `json:"game_id"`
	Status      string   `json:"status"`
	Rating      int64    `json:"rating"`
	Minutes     int64    `json:"minutes"`
	Notes       string   `json:"notes"`
	Tags        []string `json:"tags"`
	Platform    string   `json:"platform"`
	StartedAt   string   `json:"started_at"`
	CompletedAt string   `json:"completed_at"`
	// Sessions are added only when the game is new to the library (a Cato
	// JSON re-import), so importing twice doesn't double them.
	Sessions []ImportSession `json:"sessions,omitempty"`
}

type ImportSession struct {
	PlayedOn string `json:"played_on"`
	Minutes  int64  `json:"minutes"`
	Note     string `json:"note"`
}

// ImportResult counts what an import did.
type ImportResult struct {
	BatchID   string `json:"batch_id"`
	Added     int    `json:"added"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
	Rev       int64  `json:"library_rev"`
}

// MaxImport bounds one import batch.
const MaxImport = 10000

// Import writes entries as one batch that can be undone as a whole later
// (not just within the usual undo window). It never overwrites what you
// already recorded: for a game already in your library it only fills an
// empty rating, notes, platform or dates, adds tags, and raises the play
// time when the import knows more. Running the same import twice changes
// nothing the second time. Imports don't appear in friends' feeds.
func (s *Store) Import(ctx context.Context, userID, source string, entries []ImportEntry) (*ImportResult, error) {
	if len(entries) == 0 {
		return nil, invalid("empty_import", "entries", "Nothing to import")
	}
	if len(entries) > MaxImport {
		return nil, invalid("too_many", "entries", "Import at most 10,000 games at a time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res := &ImportResult{}
	var snaps []itemSnapshot
	seen := map[int64]bool{}
	for _, e := range entries {
		if e.GameID == 0 || seen[e.GameID] {
			continue
		}
		seen[e.GameID] = true
		if e.Status != "" && !isStatus(e.Status) {
			return nil, invalid("invalid_status", "status", "Unknown status "+e.Status)
		}
		prev, err := s.get(ctx, tx, userID, e.GameID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		ch := importChange(prev, e)
		if ch == nil {
			res.Unchanged++
			continue
		}
		snap, _, err := s.applyChange(ctx, tx, userID, e.GameID, *ch, modeUpsert)
		if errors.Is(err, ErrGameNotFound) {
			res.Unchanged++
			continue
		}
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
		if prev == nil {
			res.Added++
			if err := addImportedSessions(ctx, tx, userID, e); err != nil {
				return nil, err
			}
		} else {
			res.Updated++
		}
	}
	if len(snaps) > 0 {
		b, err := json.Marshal(snaps)
		if err != nil {
			return nil, err
		}
		res.BatchID = auth.RandomToken(12)
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_batches (id, user_id, source, items, snapshot_json) VALUES (?, ?, ?, ?, ?)`,
			res.BatchID, userID, source, len(snaps), string(b)); err != nil {
			return nil, err
		}
	}
	tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT library_rev FROM user_state WHERE user_id = ?), 0)`, userID).Scan(&res.Rev)
	return res, tx.Commit()
}

func isStatus(s string) bool {
	switch s {
	case StatusWishlist, StatusBacklog, StatusPlaying, StatusPaused, StatusCompleted, StatusAbandoned:
		return true
	}
	return false
}

// importChange is what an entry changes, or nil when nothing would.
func importChange(prev *Item, e ImportEntry) *Change {
	ch := Change{}
	changed := false
	status := e.Status
	if status == "" {
		status = StatusBacklog
	}
	day := func(v string) *string {
		if len(v) >= 10 {
			d := v[:10]
			return &d
		}
		return nil
	}
	if prev == nil {
		ch.Status = &status
		changed = true
		if e.Rating > 0 {
			r := clampRating(e.Rating)
			ch.Rating = &r
		}
		if e.Minutes > 0 {
			m := e.Minutes
			ch.Minutes = &m
		}
		if e.Notes != "" {
			n := e.Notes
			ch.Notes = &n
		}
		if len(e.Tags) > 0 {
			t := e.Tags
			ch.Tags = &t
		}
		if e.Platform != "" {
			p := []string{e.Platform}
			ch.Platforms = &p
		}
		ch.StartedAt, ch.CompletedAt = day(e.StartedAt), day(e.CompletedAt)
		return &ch
	}
	if prev.Rating == 0 && e.Rating > 0 {
		r := clampRating(e.Rating)
		ch.Rating, changed = &r, true
	}
	if e.Minutes > prev.Minutes {
		m := e.Minutes
		ch.Minutes, changed = &m, true
	}
	if prev.Notes == "" && e.Notes != "" {
		n := e.Notes
		ch.Notes, changed = &n, true
	}
	if len(e.Tags) > 0 {
		tags := editTags(prev.Tags, e.Tags, nil)
		if len(tags) != len(prev.Tags) {
			ch.Tags, changed = &tags, true
		}
	}
	if len(prev.Platforms) == 0 && e.Platform != "" {
		p := []string{e.Platform}
		ch.Platforms, changed = &p, true
	}
	if prev.StartedAt == nil && day(e.StartedAt) != nil {
		ch.StartedAt, changed = day(e.StartedAt), true
	}
	if prev.CompletedAt == nil && day(e.CompletedAt) != nil {
		ch.CompletedAt, changed = day(e.CompletedAt), true
	}
	if !changed {
		return nil
	}
	return &ch
}

func clampRating(r int64) int64 {
	if r < 0 {
		return 0
	}
	if r > 100 {
		return 100
	}
	return r
}

// addImportedSessions adds a new item's sessions from the import and moves
// its "set total" adjustment so the total still matches.
func addImportedSessions(ctx context.Context, tx *sql.Tx, userID string, e ImportEntry) error {
	var sum int64
	for _, ss := range e.Sessions {
		if ss.Minutes <= 0 {
			continue
		}
		if _, ok := parseDay(ss.PlayedOn); !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO play_sessions (user_id, game_id, played_on, minutes, note, source) VALUES (?, ?, ?, ?, ?, 'import')`,
			userID, e.GameID, ss.PlayedOn, ss.Minutes, strings.TrimSpace(ss.Note)); err != nil {
			return err
		}
		sum += ss.Minutes
	}
	if sum == 0 {
		return nil
	}
	// The import's total already became an adjustment: shrink it by what
	// the sessions now account for (removing it when they cover it all).
	var adjID, adj int64
	err := tx.QueryRowContext(ctx, `SELECT id, minutes FROM play_sessions WHERE user_id = ? AND game_id = ? AND source = 'adjust' ORDER BY id DESC LIMIT 1`,
		userID, e.GameID).Scan(&adjID, &adj)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if rest := adj - sum; rest == 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM play_sessions WHERE id = ?`, adjID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE play_sessions SET minutes = ? WHERE id = ?`, rest, adjID)
	}
	return err
}

// ImportBatch is one past import.
type ImportBatch struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Items     int    `json:"items"`
	CreatedAt string `json:"created_at"`
	Undone    bool   `json:"undone"`
}

func (s *Store) ImportBatches(ctx context.Context, userID string) ([]ImportBatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source, items, created_at, undone_at IS NOT NULL FROM import_batches
		WHERE user_id = ? ORDER BY created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImportBatch{}
	for rows.Next() {
		var b ImportBatch
		if err := rows.Scan(&b.ID, &b.Source, &b.Items, &b.CreatedAt, &b.Undone); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UndoImport puts every game the batch touched back the way it was before
// the import (games it added are removed).
func (s *Store) UndoImport(ctx context.Context, userID, batchID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM import_batches WHERE id = ? AND user_id = ? AND undone_at IS NULL`, batchID, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	var snaps []itemSnapshot
	if err := json.Unmarshal([]byte(raw), &snaps); err != nil {
		return 0, err
	}
	for _, snap := range snaps {
		if err := restoreItem(ctx, tx, userID, snap); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE import_batches SET undone_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`, batchID); err != nil {
		return 0, err
	}
	return len(snaps), tx.Commit()
}
