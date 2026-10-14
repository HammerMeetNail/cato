package library

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"cato/internal/catalog"
)

// MaxPageSize caps List's limit.
const MaxPageSize = 200

// Filter selects library items. The zero value matches everything.
type Filter struct {
	Statuses []string
	Tags     []string
	// TagOp is "and" (default: every tag) or "or" (any tag).
	TagOp string
	// Platform matches games available on a platform (name, abbreviation
	// or shortname substring); OwnedPlatform matches the user's own copies.
	Platform      string
	OwnedPlatform string
	// Format is digital, physical, both or none.
	Format string
	// ReleaseFrom/ReleaseTo are unix seconds, inclusive; 0 = unset.
	ReleaseFrom int64
	ReleaseTo   int64
	// Q matches the game name, a tag or the notes (case-insensitive).
	Q string
}

// ListQuery is one page request.
type ListQuery struct {
	Filter
	Sort   string
	Cursor string
	Offset int
	Limit  int
}

type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"-"`
	// Total is only computed for the first page.
	Total *int64 `json:"total,omitempty"`
}

// sorts maps sort names to ORDER BY clauses. "updated" is the default and
// the only keyset-paged order.
var sorts = map[string]string{
	"updated":                 "li.updated_at DESC, li.game_id DESC",
	"name":                    "g.name COLLATE NOCASE ASC, li.updated_at DESC",
	"name_desc":               "g.name COLLATE NOCASE DESC, li.updated_at DESC",
	"rating":                  "li.rating DESC, li.updated_at DESC",
	"rating_low":              "CASE WHEN li.rating = 0 THEN 1 ELSE 0 END, li.rating ASC, li.updated_at DESC",
	"hours":                   "li.playtime_minutes DESC, li.updated_at DESC",
	"hours_low":               "li.playtime_minutes ASC, li.updated_at DESC",
	"release_new":             "CASE WHEN g.first_release_date = 0 THEN 1 ELSE 0 END, g.first_release_date DESC, li.updated_at DESC",
	"release_old":             "CASE WHEN g.first_release_date = 0 THEN 1 ELSE 0 END, g.first_release_date ASC, li.updated_at DESC",
	"added":                   "li.created_at DESC, li.updated_at DESC",
	"added_old":               "li.created_at ASC, li.updated_at DESC",
	"critic_rating":           "g.aggregated_rating DESC, g.aggregated_rating_count DESC, li.updated_at DESC",
	"critic_rating_low":       "CASE WHEN g.aggregated_rating_count = 0 THEN 1 ELSE 0 END, g.aggregated_rating ASC, g.aggregated_rating_count DESC, li.updated_at DESC",
	"platform":                "CASE WHEN li.platform = '' THEN 1 ELSE 0 END, li.platform COLLATE NOCASE ASC, g.name COLLATE NOCASE ASC, li.updated_at DESC",
	"platform_desc":           "CASE WHEN li.platform = '' THEN 1 ELSE 0 END, li.platform COLLATE NOCASE DESC, g.name COLLATE NOCASE ASC, li.updated_at DESC",
	"available_platform":      "CASE WHEN ap.available_platform IS NULL OR ap.available_platform = '' THEN 1 ELSE 0 END, ap.available_platform COLLATE NOCASE ASC, g.name COLLATE NOCASE ASC, li.updated_at DESC",
	"available_platform_desc": "CASE WHEN ap.available_platform IS NULL OR ap.available_platform = '' THEN 1 ELSE 0 END, ap.available_platform COLLATE NOCASE DESC, g.name COLLATE NOCASE ASC, li.updated_at DESC",
}

// sortAliases keeps older sort names working.
var sortAliases = map[string]string{
	"my_rating": "rating", "my_rating_low": "rating_low", "critic": "critic_rating",
	"aggregated_rating": "critic_rating", "owned_platform": "platform", "owned_platform_desc": "platform_desc",
}

// NormalizeSort returns a known sort name ("updated" for anything else).
func NormalizeSort(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if a, ok := sortAliases[name]; ok {
		name = a
	}
	if _, ok := sorts[name]; ok {
		return name
	}
	return "updated"
}

// ErrBadCursor is returned for a malformed cursor or one used with a sort
// that pages by offset.
var ErrBadCursor = errors.New("invalid pagination cursor")

type cursor struct {
	updatedAt string
	gameID    int64
}

func (c cursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.updatedAt + "|" + strconv.FormatInt(c.gameID, 10)))
}

func parseCursor(raw string) (cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor{}, ErrBadCursor
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok || at == "" {
		return cursor{}, ErrBadCursor
	}
	gameID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return cursor{}, ErrBadCursor
	}
	return cursor{updatedAt: at, gameID: gameID}, nil
}

// availablePlatformJoin resolves each library game's alphabetically first
// available platform, aggregating only the caller's library (first ? = user).
const availablePlatformJoin = `
		LEFT JOIN (
			SELECT gp.game_id,
				MIN(CASE
					WHEN gp.platform_id = 0 THEN NULLIF(gp.platform_value, '')
					ELSE COALESCE(NULLIF(p.name, ''), CAST(gp.platform_id AS TEXT))
				END) AS available_platform
			FROM library_items mine
			JOIN game_platforms gp ON gp.game_id = mine.game_id
			LEFT JOIN platforms p ON p.id = gp.platform_id
			WHERE mine.user_id = ? AND (gp.platform_id != 0 OR gp.platform_value != '')
			GROUP BY gp.game_id
		) ap ON ap.game_id = g.id`

// where builds the shared WHERE fragment (leading " AND …") for f.
func (f Filter) where() (string, []any) {
	var b strings.Builder
	var args []any

	if tags := dedupeFold(f.Tags); len(tags) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(tags)), ", ")
		lower := make([]any, len(tags))
		for i, t := range tags {
			lower[i] = strings.ToLower(t)
		}
		if f.TagOp == "or" {
			b.WriteString(` AND EXISTS (SELECT 1 FROM library_tags lt WHERE lt.user_id = li.user_id AND lt.game_id = li.game_id AND LOWER(lt.tag) IN (` + ph + `))`)
			args = append(args, lower...)
		} else {
			b.WriteString(` AND (SELECT COUNT(DISTINCT LOWER(lt.tag)) FROM library_tags lt WHERE lt.user_id = li.user_id AND lt.game_id = li.game_id AND LOWER(lt.tag) IN (` + ph + `)) = ?`)
			args = append(args, lower...)
			args = append(args, len(tags))
		}
	}

	var statuses []string
	seen := map[string]bool{}
	for _, st := range f.Statuses {
		if ValidStatus(st) && !seen[st] {
			seen[st] = true
			statuses = append(statuses, st)
		}
	}
	if len(statuses) > 0 {
		b.WriteString(` AND li.status IN (` + strings.TrimSuffix(strings.Repeat("?, ", len(statuses)), ", ") + `)`)
		for _, st := range statuses {
			args = append(args, st)
		}
	}

	if p := strings.TrimSpace(f.Platform); p != "" && len(p) <= 64 {
		frag, fargs := catalog.PlatformFilter("g", p)
		b.WriteString(" AND " + frag)
		args = append(args, fargs...)
	}

	// Owned platform: the user's copies, matched literally or through the
	// platforms table so shortnames (ps5, sw2) match canonical names.
	if op := strings.TrimSpace(f.OwnedPlatform); op != "" && len(op) <= 64 {
		pattern := "%" + catalog.EscapeLike(strings.ToLower(op)) + "%"
		b.WriteString(` AND (
			EXISTS (
				SELECT 1 FROM json_each(li.owned_platforms_json) je
				WHERE LOWER(je.value) LIKE ? ESCAPE '\'
				   OR EXISTS (
				       SELECT 1 FROM platforms p
				       WHERE (LOWER(p.name) = LOWER(je.value) OR LOWER(p.abbreviation) = LOWER(je.value))
				         AND (LOWER(p.name) LIKE ? ESCAPE '\' OR LOWER(p.abbreviation) LIKE ? ESCAPE '\' OR LOWER(p.shortname) LIKE ? ESCAPE '\')
				   )
			)
			OR LOWER(li.platform) LIKE ? ESCAPE '\'
			OR EXISTS (
			    SELECT 1 FROM platforms p2
			    WHERE LOWER(p2.name) = LOWER(li.platform)
			      AND (LOWER(p2.name) LIKE ? ESCAPE '\' OR LOWER(p2.abbreviation) LIKE ? ESCAPE '\' OR LOWER(p2.shortname) LIKE ? ESCAPE '\')
			)
		)`)
		for i := 0; i < 8; i++ {
			args = append(args, pattern)
		}
	}

	switch strings.ToLower(strings.TrimSpace(f.Format)) {
	case "digital", "physical":
		b.WriteString(" AND li.medium = ?")
		args = append(args, strings.ToLower(strings.TrimSpace(f.Format)))
	case "both":
		b.WriteString(" AND li.medium IN ('physical', 'digital')")
	case "none":
		b.WriteString(" AND (li.medium = '' OR li.medium IS NULL)")
	}

	if f.ReleaseFrom != 0 {
		b.WriteString(" AND g.first_release_date >= ?")
		args = append(args, f.ReleaseFrom)
	}
	if f.ReleaseTo != 0 {
		b.WriteString(" AND g.first_release_date <= ?")
		args = append(args, f.ReleaseTo)
	}

	if q := strings.ToLower(strings.TrimSpace(f.Q)); q != "" {
		if len([]rune(q)) > 100 {
			q = string([]rune(q)[:100])
		}
		pat := "%" + catalog.EscapeLike(q) + "%"
		b.WriteString(` AND (LOWER(g.name) LIKE ? ESCAPE '\'
			OR LOWER(li.notes) LIKE ? ESCAPE '\'
			OR EXISTS (SELECT 1 FROM library_tags lt WHERE lt.user_id = li.user_id AND lt.game_id = li.game_id AND LOWER(lt.tag) LIKE ? ESCAPE '\')
			OR EXISTS (SELECT 1 FROM game_aliases ga WHERE ga.game_id = g.id AND ga.normalized_alias LIKE ? ESCAPE '\'))`)
		args = append(args, pat, pat, pat, "%"+catalog.EscapeLike(catalog.NormalizeName(q))+"%")
	}
	return b.String(), args
}

func dedupeFold(vals []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		k := strings.ToLower(v)
		if v == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

// List returns one page of the user's library.
func (s *Store) List(ctx context.Context, userID string, q ListQuery) (Page, error) {
	if q.Limit <= 0 {
		q.Limit = 60
	}
	if q.Limit > MaxPageSize {
		q.Limit = MaxPageSize
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	sort := NormalizeSort(q.Sort)
	var cur *cursor
	if q.Cursor != "" {
		if sort != "updated" {
			return Page{}, ErrBadCursor
		}
		c, err := parseCursor(q.Cursor)
		if err != nil {
			return Page{}, err
		}
		cur = &c
	}
	where, whereArgs := q.Filter.where()
	page := Page{Items: []Item{}}

	if q.Offset == 0 && cur == nil {
		var total int64
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_items li JOIN games g ON g.id = li.game_id WHERE li.user_id = ?`+where,
			append([]any{userID}, whereArgs...)...).Scan(&total); err != nil {
			return Page{}, err
		}
		page.Total = &total
	}

	var args []any
	join := ""
	if strings.HasPrefix(sort, "available_platform") {
		join = availablePlatformJoin
		args = append(args, userID)
	}
	args = append(args, userID)
	args = append(args, whereArgs...)
	offset := q.Offset
	if cur != nil {
		where += " AND (li.updated_at, li.game_id) < (?, ?)"
		args = append(args, cur.updatedAt, cur.gameID)
		offset = 0
	}
	args = append(args, q.Limit+1, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemSelect+`
		FROM library_items li JOIN games g ON g.id = li.game_id`+join+`
		WHERE li.user_id = ?`+where+` ORDER BY `+sorts[sort]+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	names := s.platformNames(ctx)
	for rows.Next() {
		it, err := scanItem(rows, names)
		if err != nil {
			return Page{}, err
		}
		if len(page.Items) == q.Limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, it)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if page.HasMore && sort == "updated" && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = cursor{updatedAt: last.UpdatedAt, gameID: last.GameID}.String()
	}
	return page, nil
}

// ParseYear converts a year to the first (or, with end, last) unix second
// of that year; 0 when invalid.
func ParseYear(raw string, end bool) int64 {
	y, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || y < 1900 || y > 2100 {
		return 0
	}
	if end {
		return time.Date(y+1, time.January, 1, 0, 0, 0, 0, time.UTC).Unix() - 1
	}
	return time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
}

// ParseDate accepts YYYY-MM-DD, RFC3339 or a bare year; end makes bare
// dates inclusive to the end of the day (or year).
func ParseDate(raw string, end bool) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if end {
			return t.Add(24*time.Hour - time.Second).Unix()
		}
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix()
	}
	return ParseYear(raw, end)
}
