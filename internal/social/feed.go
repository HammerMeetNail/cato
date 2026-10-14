package social

import (
	"context"
	"encoding/json"
)

// FeedEntry is one line of the activity feed. Play sessions one person
// logged on one day are grouped: "Alex logged 6h across 2 games".
type FeedEntry struct {
	ID        int64          `json:"id"`
	Kind      string         `json:"kind"`
	Person    Person         `json:"person"`
	Games     []GameRef      `json:"games"`
	RefID     int64          `json:"ref_id,omitempty"`
	ListTitle string         `json:"list_title,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Minutes   int64          `json:"minutes,omitempty"`
	CreatedAt string         `json:"created_at"`
}

// Feed reads the activity of people viewer follows (fan-out on read through
// follows), newest first. before is the id cursor; it returns the next one
// (0 at the end).
func (s *Service) Feed(ctx context.Context, viewer string, before int64, limit int) ([]FeedEntry, int64, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if before <= 0 {
		before = 1 << 62
	}
	raw := limit * 4
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.kind, COALESCE(a.ref_id, 0), a.data_json, a.created_at,
			COALESCE(g.id, 0), COALESCE(g.name, ''), COALESCE(g.local_cover_path, ''), COALESCE(g.cover_url, ''),
			COALESCE(l.title, ''), `+personCols+`
		FROM follows f
		JOIN activity a ON a.user_id = f.followee_id
		JOIN users u ON u.id = a.user_id
		LEFT JOIN games g ON g.id = a.game_id
		LEFT JOIN lists l ON a.kind = 'listed' AND l.id = a.ref_id
		WHERE f.follower_id = ? AND f.status = 'accepted' AND a.id < ?
		  AND `+visibleSQL("a.user_id", "u.visibility")+`
		  AND (a.kind != 'listed' OR (l.id IS NOT NULL AND l.hidden_at IS NULL AND `+visibleSQL("l.user_id", `(CASE l.visibility WHEN 'inherit' THEN u.visibility ELSE l.visibility END)`)+`))
		  AND (a.kind != 'reviewed' OR EXISTS (SELECT 1 FROM reviews r WHERE r.id = a.ref_id AND r.hidden_at IS NULL
		       AND `+visibleSQL("r.user_id", reviewLevel)+`))
		ORDER BY a.id DESC LIMIT ?`,
		append(append(append(append([]any{viewer, before}, visibleArgs(viewer)...), visibleArgs(viewer)...), visibleArgs(viewer)...), raw)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []FeedEntry{}
	var next int64
	n := 0
	for rows.Next() {
		n++
		var e FeedEntry
		var dataJSON, local, remote string
		var g GameRef
		e.Person, err = scanPerson(rowPrefix{rows, []any{&e.ID, &e.Kind, &e.RefID, &dataJSON, &e.CreatedAt, &g.ID, &g.Name, &local, &remote, &e.ListTitle}})
		if err != nil {
			return nil, 0, err
		}
		g.Cover = coverURL(local, remote)
		json.Unmarshal([]byte(dataJSON), &e.Data)
		e.Games = []GameRef{}
		if g.ID != 0 {
			e.Games = append(e.Games, g)
		}
		if e.Kind == "logged" {
			if m, ok := e.Data["minutes"].(float64); ok {
				e.Minutes = int64(m)
			}
		}
		// A day's logging, adds, wishlists and additions to one list by
		// the same person become one line.
		if k := len(out) - 1; k >= 0 && groupable[e.Kind] && out[k].Kind == e.Kind && out[k].Person.ID == e.Person.ID &&
			out[k].CreatedAt[:10] == e.CreatedAt[:10] && (e.Kind != "listed" || out[k].RefID == e.RefID) {
			out[k].Minutes += e.Minutes
			if g.ID != 0 && !hasGame(out[k].Games, g.ID) {
				out[k].Games = append(out[k].Games, g)
			}
			next = e.ID
			continue
		}
		if len(out) == limit {
			break
		}
		out = append(out, e)
		next = e.ID
	}
	if n < raw && len(out) < limit {
		next = 0
	}
	return out, next, rows.Err()
}

var groupable = map[string]bool{"logged": true, "added": true, "wishlisted": true, "listed": true}

func hasGame(gs []GameRef, id int64) bool {
	for _, g := range gs {
		if g.ID == id {
			return true
		}
	}
	return false
}
