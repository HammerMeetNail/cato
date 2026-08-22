package games

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Platform is one row of the IGDB platform reference table (migration v9).
type Platform struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Abbreviation string `json:"abbreviation"`
}

// platformShortnames curates gamer-style codes the @platform search should
// honor but IGDB's abbreviation field lacks (upstream: "Switch 2", "Series
// X|S", plain "PC"). Space-separated aliases; applied over the reference
// table on every boot so re-syncs and new rows stay covered. IDs are stable
// IGDB platform identifiers.
var platformShortnames = map[int64]string{
	6:   "win",           // PC (Microsoft Windows) — upstream abbr is just "PC"
	7:   "ps1 psx",       // PlayStation
	8:   "ps2",           // PlayStation 2
	9:   "ps3",           // PlayStation 3
	12:  "x360 360",      // Xbox 360
	38:  "psp",           // PlayStation Portable
	46:  "psvita vita psv", // PlayStation Vita
	48:  "ps4",           // PlayStation 4
	49:  "xb1 xone",      // Xbox One
	130: "ns swi switch1", // Nintendo Switch
	167: "ps5",           // PlayStation 5
	169: "xsx xss seriesx", // Xbox Series X|S
	508: "sw2 ns2 switch2", // Nintendo Switch 2
}

// ApplyPlatformShortnames stamps the curated codes onto the lookup table.
// Idempotent, cheap (~13 UPDATEs), and safe on an empty table.
func (s *Store) ApplyPlatformShortnames(ctx context.Context) error {
	for id, sn := range platformShortnames {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE platforms SET shortname = ? WHERE id = ?`, sn, id); err != nil {
			return fmt.Errorf("apply shortname for platform %d: %w", id, err)
		}
	}
	return nil
}

// UpsertPlatforms replaces-or-inserts reference rows in one statement.
// Called once at startup from IGDB's /platforms endpoint (~230 rows).
func (s *Store) UpsertPlatforms(ctx context.Context, plats []Platform) error {
	if len(plats) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`INSERT INTO platforms (id, name, abbreviation) VALUES `)
	args := make([]interface{}, 0, len(plats)*3)
	for i, p := range plats {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?, ?, ?)")
		args = append(args, p.ID, p.Name, p.Abbreviation)
	}
	b.WriteString(` ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		abbreviation = excluded.abbreviation`)
	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("upsert platforms: %w", err)
	}
	return nil
}

// CountPlatforms reports how many lookup rows exist; SyncPlatforms uses it to
// decide whether the table still needs its one-time IGDB fetch.
func (s *Store) CountPlatforms(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM platforms").Scan(&n)
	return n, err
}

// PlatformNames loads the ID → display-name map. The table is tiny (~230
// rows), so callers may load it per request rather than cache it.
func (s *Store) PlatformNames(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name FROM platforms")
	if err != nil {
		return nil, fmt.Errorf("load platforms: %w", err)
	}
	defer rows.Close()

	names := make(map[int64]string)
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// PlatformFilter returns a WHERE fragment (no leading AND/OR) matching games
// whose platforms_json includes a platform whose name, abbreviation, or
// curated shortname contains the given text (case-insensitive substring),
// plus its bind args. Legacy rows storing literal name strings instead of IDs
// are matched too; numeric IDs missing from the lookup table match nothing
// (and raw ID text never matches — the interface is names, not numbers).
// alias is the games table alias used in the caller's query.
func PlatformFilter(alias, platform string) (string, []interface{}) {
	pat := "%" + strings.ToLower(EscapeLike(strings.TrimSpace(platform))) + "%"
	frag := fmt.Sprintf(`EXISTS (
		SELECT 1 FROM json_each(%s.platforms_json) je
		LEFT JOIN platforms p ON p.id = je.value
		WHERE LOWER(COALESCE(p.name, '')) LIKE ? ESCAPE '\'
		   OR LOWER(COALESCE(p.abbreviation, '')) LIKE ? ESCAPE '\'
		   OR LOWER(COALESCE(p.shortname, '')) LIKE ? ESCAPE '\'
		   OR (typeof(je.value) != 'integer' AND LOWER(CAST(je.value AS TEXT)) LIKE ? ESCAPE '\'))`, alias)
	return frag, []interface{}{pat, pat, pat, pat}
}

// ResolvePlatformNames translates a games.platforms_json blob into display
// names. The column historically stores raw IGDB platform IDs ("[6,130]");
// legacy rows written by tests/edits hold name strings — both are handled.
// IDs missing from the lookup map are dropped (never shown as bare numbers).
// Returns nil for empty/unparsable input so JSON serializes as null/omitted.
func ResolvePlatformNames(raw string, names map[int64]string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "null" {
		return nil
	}
	var items []interface{}
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if v != "" {
				out = append(out, v)
			}
		case float64:
			id := int64(v)
			if name, ok := names[id]; ok && name != "" {
				out = append(out, name)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
