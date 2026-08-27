package games

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"cato/internal/db"
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
	6:   "win",             // PC (Microsoft Windows) — upstream abbr is just "PC"
	7:   "ps1 psx",         // PlayStation
	8:   "ps2",             // PlayStation 2
	9:   "ps3",             // PlayStation 3
	12:  "x360 360",        // Xbox 360
	38:  "psp",             // PlayStation Portable
	46:  "psvita vita psv", // PlayStation Vita
	48:  "ps4",             // PlayStation 4
	49:  "xb1 xone",        // Xbox One
	130: "ns swi switch1",  // Nintendo Switch
	167: "ps5",             // PlayStation 5
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

// platformTagMigration maps ownership tags the operator historically used
// onto canonical platform values for library_items.platform. Storefront and
// misc tags (Steam, Epic, GOTY, …) are deliberately absent — they describe
// where/how a game was bought, not what it runs on.
var platformTagMigration = map[string]string{
	"switch":        "Nintendo Switch",
	"switch 2":      "Nintendo Switch 2",
	"ps1":           "PlayStation",
	"ps2":           "PlayStation 2",
	"ps3":           "PlayStation 3",
	"ps4":           "PlayStation 4",
	"ps5":           "PlayStation 5",
	"psp":           "PlayStation Portable",
	"xbox":          "Xbox",
	"x1":            "Xbox One",
	"xbox one":      "Xbox One",
	"xsx":           "Xbox Series X|S",
	"xbox series x": "Xbox Series X|S",
	"x360":          "Xbox 360",
	"wii":           "Wii",
	"wiiu":          "Wii U",
	"gamecube":      "Nintendo GameCube",
	"n64":           "Nintendo 64",
	"3ds":           "Nintendo 3DS",
	"gba":           "Game Boy Advance",
	"gb":            "Game Boy",
	"dreamcast":     "Dreamcast",
	"ios":           "iOS",
	"windows":       "PC (Microsoft Windows)",
}

// MigratePlatformTags is a one-time data fixup: items whose tags encode
// platform ownership ("Switch", "PS5", …) get those values moved into
// library_items.owned_platforms_json (multi-ownership — an item tagged on
// several platforms is owned on several) and the tags are removed
// afterwards. The legacy singular platform column keeps the first entry.
// Idempotent by construction — after a successful pass no mappable tags
// remain. Returns the number of items migrated.
func MigratePlatformTags(database *db.DB) (int, error) {
	rows, err := database.Query(`SELECT user_id, game_id, tags_json, platform, owned_platforms_json FROM library_items`)
	if err != nil {
		return 0, fmt.Errorf("scan library items: %w", err)
	}

	type item struct {
		userID string
		gameID int64
		tags   []string
		owned  []string
	}
	var updates []item
	defer rows.Close()
	for rows.Next() {
		var userID, tagsJSON, platform, ownedJSON string
		var gameID int64
		if err := rows.Scan(&userID, &gameID, &tagsJSON, &platform, &ownedJSON); err != nil {
			continue
		}
		var tags []string
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil || len(tags) == 0 {
			continue
		}

		owned := []string{}
		json.Unmarshal([]byte(ownedJSON), &owned)
		if len(owned) == 0 && strings.TrimSpace(platform) != "" {
			owned = append(owned, strings.TrimSpace(platform))
		}
		have := map[string]bool{}
		for _, p := range owned {
			have[strings.ToLower(p)] = true
		}

		mapped := false
		var kept []string
		for _, t := range tags {
			if p, ok := platformTagMigration[strings.ToLower(strings.TrimSpace(t))]; ok {
				mapped = true
				if !have[strings.ToLower(p)] {
					owned = append(owned, p)
					have[strings.ToLower(p)] = true
				}
				continue // drop from kept list
			}
			kept = append(kept, t)
		}
		if !mapped {
			continue
		}

		updates = append(updates, item{userID: userID, gameID: gameID, tags: kept, owned: owned})
	}
	if err := rows.Err(); err != nil {
		return 0, rows.Err()
	}

	migrated := 0
	for _, u := range updates {
		if u.tags == nil {
			u.tags = []string{}
		}
		tagsJSON, _ := json.Marshal(u.tags)
		ownedJSON, _ := json.Marshal(u.owned)
		if _, err := database.Exec(`UPDATE library_items
			SET tags_json = ?, owned_platforms_json = ?, platform = ?, updated_at = CURRENT_TIMESTAMP
			WHERE user_id = ? AND game_id = ?`,
			string(tagsJSON), string(ownedJSON), firstOrEmpty(u.owned), u.userID, u.gameID); err != nil {
			return migrated, fmt.Errorf("migrate tags for %s/%d: %w", u.userID, u.gameID, err)
		}
		migrated++
	}
	return migrated, nil
}

func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
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
// whose normalized platform rows contain a platform whose name, abbreviation,
// or curated shortname contains the given text (case-insensitive substring),
// plus its bind args. Platform IDs are resolved through the small reference
// table first, then game_platforms uses its game/platform indexes. Legacy rows
// storing literal names in platforms_json are represented by
// game_platforms.platform_value and remain searchable too. alias is the games
// table alias used in the caller's query.
func PlatformFilter(alias, platform string) (string, []interface{}) {
	pat := "%" + strings.ToLower(EscapeLike(strings.TrimSpace(platform))) + "%"
	frag := fmt.Sprintf(`EXISTS (
		SELECT 1 FROM game_platforms gp
		WHERE gp.game_id = %s.id
		  AND (
		       gp.platform_id IN (
		         SELECT id FROM platforms
		          WHERE LOWER(name) LIKE ? ESCAPE '\'
		             OR LOWER(abbreviation) LIKE ? ESCAPE '\'
		             OR LOWER(shortname) LIKE ? ESCAPE '\'
		       )
		   OR (gp.platform_id = 0 AND LOWER(gp.platform_value) LIKE ? ESCAPE '\')
		  )
	)`, alias)
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
