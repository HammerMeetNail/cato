package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// IndexFields names the columns of each Index entry, in order. Entries are
// arrays rather than objects to keep a whole library small enough to sync
// to the device (about 5,000 items in tens of kilobytes gzipped).
var IndexFields = []string{
	"id", "name", "status", "rating", "minutes", "updated", "added",
	"platforms", "tags", "release", "ttb", "cover", "medium", "notes", "game_platforms",
}

// indexNotesChars is how much of the private notes the index carries for
// on-device search.
const indexNotesChars = 140

// Index is the compact library snapshot (Full) or the changes since a
// revision. Deleted lists game IDs removed since then.
type Index struct {
	Rev     int64    `json:"rev"`
	Full    bool     `json:"full"`
	Fields  []string `json:"fields"`
	Items   [][]any  `json:"items"`
	Deleted []int64  `json:"deleted"`
	// PlatformNames resolves the IDs in game_platforms.
	PlatformNames map[int64]string `json:"platform_names"`
}

// Index returns the whole library (since <= 0) or what changed after since.
func (s *Store) Index(ctx context.Context, userID string, since int64) (*Index, error) {
	// One read transaction so rev, rows and tombstones are consistent.
	tx, err := s.db.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	idx := &Index{Fields: IndexFields, Items: [][]any{}, Deleted: []int64{}, PlatformNames: map[int64]string{}}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT library_rev FROM user_state WHERE user_id = ?), 0)`, userID).Scan(&idx.Rev); err != nil {
		return nil, err
	}
	if since > idx.Rev {
		// A client ahead of the server (restored database): resync fully.
		since = 0
	}
	idx.Full = since <= 0
	rows, err := tx.QueryContext(ctx, `SELECT li.game_id, g.name, li.status, li.rating, li.playtime_minutes, li.updated_at, li.created_at,
			li.owned_platforms_json, li.tags_json, g.first_release_date, g.local_cover_path, g.cover_url, li.medium,
			substr(li.notes, 1, ?), g.platforms_json
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.rev > ?
		ORDER BY li.updated_at DESC, li.game_id DESC`, indexNotesChars, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := s.platformNames(ctx)
	for rows.Next() {
		var id, rating, minutes, release int64
		var name, status, updated, added, owned, tags, local, remote, medium, notes, platformsJSON string
		if err := rows.Scan(&id, &name, &status, &rating, &minutes, &updated, &added, &owned, &tags, &release,
			&local, &remote, &medium, &notes, &platformsJSON); err != nil {
			return nil, err
		}
		ids := platformIDs(platformsJSON)
		for _, pid := range ids {
			if n, ok := names[pid]; ok {
				idx.PlatformNames[pid] = n
			}
		}
		idx.Items = append(idx.Items, []any{
			id, name, status, rating, minutes, unixOf(updated), unixOf(added),
			decodeStrings(owned), decodeStrings(tags), release, 0, CoverURL(local, remote), medium, notes, ids,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !idx.Full {
		del, err := tx.QueryContext(ctx, `SELECT game_id FROM library_tombstones WHERE user_id = ? AND rev > ?`, userID, since)
		if err != nil {
			return nil, err
		}
		defer del.Close()
		for del.Next() {
			var id int64
			if err := del.Scan(&id); err != nil {
				return nil, err
			}
			idx.Deleted = append(idx.Deleted, id)
		}
		if err := del.Err(); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

// platformIDs extracts the numeric IGDB platform IDs from platforms_json.
func platformIDs(raw string) []int64 {
	var vals []any
	out := []int64{}
	if json.Unmarshal([]byte(raw), &vals) != nil {
		return out
	}
	for _, v := range vals {
		if f, ok := v.(float64); ok && f > 0 {
			out = append(out, int64(f))
		}
	}
	return out
}

// unixOf parses the timestamp formats stored in library_items (RFC3339 from
// Go, "YYYY-MM-DD HH:MM:SS" from SQLite defaults).
func unixOf(ts string) int64 {
	ts = strings.TrimSpace(ts)
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse("2006-01-02 15:04:05", ts); err == nil {
		return t.Unix()
	}
	return 0
}
