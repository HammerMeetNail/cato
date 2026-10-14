package library

import (
	"context"
	"strings"
)

// MaxBulk caps how many items one bulk action touches.
const MaxBulk = 500

// BulkAction applies to every selected item; one undo restores them all.
type BulkAction struct {
	GameIDs    []int64  `json:"game_ids"`
	Status     *string  `json:"status"`
	AddTags    []string `json:"add_tags"`
	RemoveTags []string `json:"remove_tags"`
	Remove     bool     `json:"remove"`
}

type BulkResult struct {
	Updated int    `json:"updated"`
	UndoID  string `json:"undo_id"`
	Rev     int64  `json:"library_rev"`
}

// Bulk applies a to the selected items the user has; others are skipped.
func (s *Store) Bulk(ctx context.Context, userID string, a BulkAction) (*BulkResult, error) {
	ids := dedupeIDs(a.GameIDs)
	if len(ids) == 0 {
		return nil, invalid("invalid_selection", "game_ids", "Select at least one game")
	}
	if len(ids) > MaxBulk {
		return nil, invalid("invalid_selection", "game_ids", "Select at most 500 games")
	}
	if a.Remove {
		undo, err := s.deleteMany(ctx, userID, ids, false)
		if err != nil {
			return nil, err
		}
		rev, err := s.Rev(ctx, userID)
		if err != nil {
			return nil, err
		}
		n := 0
		if undo != "" {
			n = len(ids)
		}
		return &BulkResult{Updated: n, UndoID: undo, Rev: rev}, nil
	}
	if a.Status == nil && len(a.AddTags) == 0 && len(a.RemoveTags) == 0 {
		return nil, invalid("invalid_action", "", "Choose a status, tags or remove")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var snaps []itemSnapshot
	for _, id := range ids {
		ch := Change{Status: a.Status}
		if len(a.AddTags) > 0 || len(a.RemoveTags) > 0 {
			cur, err := s.get(ctx, tx, userID, id)
			if err == ErrNotFound {
				continue
			}
			if err != nil {
				return nil, err
			}
			tags := editTags(cur.Tags, a.AddTags, a.RemoveTags)
			ch.Tags = &tags
		}
		snap, _, err := s.applyChange(ctx, tx, userID, id, ch, modePatch)
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	res := &BulkResult{Updated: len(snaps)}
	if res.UndoID, err = saveUndo(ctx, tx, userID, snaps); err != nil {
		return nil, err
	}
	tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT library_rev FROM user_state WHERE user_id = ?), 0)`, userID).Scan(&res.Rev)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

func editTags(cur, add, remove []string) []string {
	drop := map[string]bool{}
	for _, t := range remove {
		drop[strings.ToLower(strings.TrimSpace(t))] = true
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range append(append([]string{}, cur...), add...) {
		t = strings.TrimSpace(t)
		k := strings.ToLower(t)
		if t == "" || drop[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

func dedupeIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Tags suggests the user's tags starting with prefix (case-insensitive).
func (s *Store) Tags(ctx context.Context, userID, prefix string, limit int) ([]string, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT MIN(tag) FROM library_tags
		WHERE user_id = ? AND LOWER(tag) LIKE ? ESCAPE '\'
		GROUP BY LOWER(tag) ORDER BY COUNT(*) DESC, LOWER(MIN(tag)) LIMIT ?`,
		userID, strings.ToLower(escapeLike(prefix))+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// OwnedPlatforms lists the platforms the user owns games on, most used first.
func (s *Store) OwnedPlatforms(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT je.value, COUNT(*) AS c
		FROM library_items li, json_each(li.owned_platforms_json) je
		WHERE li.user_id = ? AND je.value != ''
		GROUP BY LOWER(je.value) ORDER BY c DESC, je.value LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}
