package catalog

import (
	"context"
	"strings"
)

// Moods are the suggested tags seeded from IGDB data (plan §10): they
// power suggestions on the game page and the Up Next mood filter.
var Moods = []string{"cozy", "co-op", "short", "story-rich", "challenging", "spooky"}

// IsMood reports whether m is one of Moods.
func IsMood(m string) bool {
	for _, x := range Moods {
		if x == m {
			return true
		}
	}
	return false
}

// MoodsFor suggests moods for each game from its genres, themes, modes and
// time to beat.
func (s *Store) MoodsFor(ctx context.Context, ids []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	names := map[string]map[int64]string{}
	for _, k := range []string{"genres", "themes", "game_modes"} {
		n, err := s.LookupNames(ctx, k)
		if err != nil {
			return nil, err
		}
		names[k] = n
	}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, genres_json, themes_json, modes_json, ttb_normally FROM games WHERE id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, ttb int64
			var g, t, m string
			if err := rows.Scan(&id, &g, &t, &m, &ttb); err != nil {
				rows.Close()
				return nil, err
			}
			set := func(kind, raw string) map[string]bool {
				out := map[string]bool{}
				for _, n := range namesOf(raw, names[kind]) {
					out[strings.ToLower(n)] = true
				}
				return out
			}
			out[id] = moodsOf(set("genres", g), set("themes", t), set("game_modes", m), ttb)
		}
		rows.Close()
	}
	return out, nil
}

func moodsOf(genres, themes, modes map[string]bool, ttbMinutes int64) []string {
	any := func(set map[string]bool, keys ...string) bool {
		for _, k := range keys {
			if set[k] {
				return true
			}
		}
		return false
	}
	dark := any(themes, "horror", "survival", "warfare", "thriller", "erotic")
	var out []string
	if !dark && (any(genres, "simulator", "puzzle", "card & board game", "point-and-click") || any(themes, "kids", "comedy", "romance", "party")) {
		out = append(out, "cozy")
	}
	if any(modes, "co-operative", "split screen") {
		out = append(out, "co-op")
	}
	if ttbMinutes > 0 && ttbMinutes <= 8*60 {
		out = append(out, "short")
	}
	if genres["visual novel"] || (any(genres, "adventure", "role-playing (rpg)", "point-and-click") && any(themes, "drama", "mystery", "romance", "historical")) {
		out = append(out, "story-rich")
	}
	if any(genres, "fighting", "hack and slash/beat 'em up", "arcade") || (genres["platform"] && genres["indie"]) {
		out = append(out, "challenging")
	}
	if any(themes, "horror", "thriller") {
		out = append(out, "spooky")
	}
	return out
}
