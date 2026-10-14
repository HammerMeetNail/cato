// Package imports brings libraries in from elsewhere (plan §8.1): CSV files
// (with presets for Grouvee, HowLongToBeat, Infinite Backlog and Cato's own
// export, plus column mapping for anything else), Cato JSON exports, Steam
// and RetroAchievements. Every source becomes Rows; Match finds each row's
// game (exact IDs first, then the local catalog by name and year); the
// person reviews uncertain matches; library.Store.Import writes the batch.
package imports

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cato/internal/library"
)

// Row is one game as another service described it.
type Row struct {
	Title       string   `json:"title"`
	Year        int      `json:"year,omitempty"`
	Platform    string   `json:"platform,omitempty"`
	Status      string   `json:"status"`
	Rating      int64    `json:"rating,omitempty"` // 0–100
	Minutes     int64    `json:"minutes,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	StartedAt   string   `json:"started_at,omitempty"`
	CompletedAt string   `json:"completed_at,omitempty"`
	SteamAppID  int64    `json:"steam_appid,omitempty"`
	IGDBID      int64    `json:"igdb_id,omitempty"`
	// Played is set when the source knows the game was played even though
	// it's in the backlog (Steam playtime).
	Played bool `json:"played,omitempty"`
	// Sessions come with a Cato JSON export.
	Sessions []library.ImportSession `json:"sessions,omitempty"`
}

// Limits for uploaded files.
const (
	MaxCSVBytes = 8 << 20
	MaxRows     = 10000
)

// Fields Cato can fill from a column.
var Fields = []string{"title", "platform", "status", "rating", "hours", "minutes", "notes", "tags", "started", "finished", "year", "steam_appid", "igdb_id"}

// Mapping says which column (index) fills each field; -1 or missing means none.
type Mapping struct {
	Columns map[string]int `json:"columns"`
	// RatingScale is what a perfect score is in the file (5, 10 or 100);
	// 0 detects it from the values.
	RatingScale float64 `json:"rating_scale"`
	// Statuses maps the file's status values to Cato's; unknown values use
	// the synonyms below, then backlog.
	Statuses map[string]string `json:"statuses,omitempty"`
}

// Table is a parsed CSV file.
type Table struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"-"`
}

// ErrTooBig and ErrEmpty are file problems worth telling the person.
var (
	ErrTooBig = errors.New("the file is too big (8 MB at most)")
	ErrEmpty  = errors.New("the file has no rows")
)

// ParseCSV reads a CSV file: UTF-8 (a BOM is fine), comma, semicolon or
// tab separated, a header row first. Ragged rows are padded.
func ParseCSV(data []byte) (*Table, error) {
	if len(data) > MaxCSVBytes {
		return nil, ErrTooBig
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		data = []byte(strings.ToValidUTF8(string(data), "�"))
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	sep := ','
	if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		sep = ';'
	}
	if bytes.Count(first, []byte("\t")) > bytes.Count(first, []byte(string(sep))) {
		sep = '\t'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = sep
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false
	t := &Table{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if t.Headers == nil {
			for _, h := range rec {
				t.Headers = append(t.Headers, strings.TrimSpace(h))
			}
			continue
		}
		blank := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		if len(t.Rows) >= MaxRows {
			break
		}
		for len(rec) < len(t.Headers) {
			rec = append(rec, "")
		}
		t.Rows = append(t.Rows, rec)
	}
	if len(t.Rows) == 0 {
		return nil, ErrEmpty
	}
	return t, nil
}

// synonyms maps normalised header names to fields.
var synonyms = map[string]string{
	"title": "title", "name": "title", "game": "title", "game name": "title", "game title": "title",
	"platform": "platform", "platforms": "platform", "system": "platform", "console": "platform", "platform name": "platform",
	"status": "status", "shelf": "status", "shelves": "status", "list": "status", "statuses": "status", "state": "status", "progress": "status",
	"rating": "rating", "score": "rating", "stars": "rating", "my rating": "rating", "your rating": "rating", "review score": "rating",
	"hours": "hours", "hours played": "hours", "playtime": "hours", "time played": "hours", "play time": "hours", "playtime hours": "hours", "hours_played": "hours", "playtime_hours": "hours",
	"minutes": "minutes", "minutes played": "minutes", "playtime minutes": "minutes", "playtime_forever": "minutes",
	"notes": "notes", "review": "notes", "comment": "notes", "comments": "notes", "note": "notes",
	"tags": "tags", "labels": "tags", "tag": "tags", "genres": "", "custom tags": "tags",
	"started": "started", "start date": "started", "date started": "started", "started at": "started", "started_at": "started",
	"finished": "finished", "completed": "finished", "completion date": "finished", "date completed": "finished", "finished at": "finished",
	"completed at": "finished", "completed_at": "finished", "date finished": "finished", "beaten": "finished",
	"year": "year", "release year": "year", "release date": "year", "released": "year", "release_date": "year",
	"steam appid": "steam_appid", "steam app id": "steam_appid", "appid": "steam_appid", "steam_appid": "steam_appid",
	"igdb id": "igdb_id", "igdb_id": "igdb_id", "game_id": "igdb_id",
}

// Preset names a recognised export format.
type Preset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

var (
	PresetCato      = Preset{ID: "cato", Label: "Cato export"}
	PresetGrouvee   = Preset{ID: "grouvee", Label: "Grouvee", Note: "Grouvee's CSV export (Settings → Export)."}
	PresetHLTB      = Preset{ID: "hltb", Label: "HowLongToBeat", Note: "HowLongToBeat's game list export; the columns are matched by name."}
	PresetIB        = Preset{ID: "infinite_backlog", Label: "Infinite Backlog", Note: "Infinite Backlog's CSV export; the columns are matched by name."}
	PresetGeneric   = Preset{ID: "generic", Label: "Spreadsheet", Note: "Columns were matched by name; check them below."}
	PresetBackloggd = Preset{ID: "backloggd", Label: "Backloggd", Note: "Backloggd has no export; community scripts produce a CSV with these columns."}
)

func normHeader(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.NewReplacer("(", " ", ")", " ", "#", " ", ":", " ").Replace(h)
	return strings.Join(strings.Fields(h), " ")
}

// Detect guesses the preset and the column mapping from the headers.
func Detect(headers []string) (Preset, Mapping) {
	m := Mapping{Columns: map[string]int{}}
	has := map[string]int{}
	for i, h := range headers {
		n := normHeader(h)
		has[n] = i
		if f, ok := synonyms[n]; ok && f != "" {
			if _, taken := m.Columns[f]; !taken {
				m.Columns[f] = i
			}
		}
	}
	_, igdbID := has["igdb_id"]
	_, playtimeHours := has["playtime_hours"]
	_, shelves := has["shelves"]
	_, giantbomb := has["giantbomb_id"]
	_, mainStory := has["main story"]
	_, ibStatus := has["completion status"]
	switch {
	case igdbID && playtimeHours && headerIs(headers, 0, "game"):
		m.RatingScale = 100
		return PresetCato, m
	case shelves && giantbomb:
		m.RatingScale = 5
		m.Columns["status"] = has["shelves"]
		return PresetGrouvee, m
	case mainStory:
		return PresetHLTB, m
	case ibStatus:
		m.Columns["status"] = has["completion status"]
		return PresetIB, m
	}
	return PresetGeneric, m
}

func headerIs(headers []string, i int, want string) bool {
	return i < len(headers) && normHeader(headers[i]) == want
}

var statusSynonyms = map[string]string{
	"wishlist": "wishlist", "want": "wishlist", "want to play": "wishlist", "wanted": "wishlist", "wish list": "wishlist", "want to buy": "wishlist",
	"backlog": "backlog", "to play": "backlog", "owned": "backlog", "unplayed": "backlog", "not started": "backlog", "plan to play": "backlog", "shelved": "backlog", "library": "backlog",
	"playing": "playing", "currently playing": "playing", "in progress": "playing", "now playing": "playing", "started": "playing", "replay": "playing", "replaying": "playing",
	"paused": "paused", "on hold": "paused", "on-hold": "paused",
	"completed": "completed", "complete": "completed", "finished": "completed", "beaten": "completed", "played": "completed", "done": "completed",
	"mastered": "completed", "100%": "completed", "retired": "completed", "continuous": "playing", "endless": "playing",
	"abandoned": "abandoned", "dropped": "abandoned", "quit": "abandoned", "gave up": "abandoned", "unfinished": "abandoned",
}

// StatusFor maps a source's status text to a Cato status ("" when unknown).
func StatusFor(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if s, ok := statusSynonyms[v]; ok {
		return s
	}
	// Grouvee shelves are a JSON object keyed by shelf name.
	if strings.HasPrefix(v, "{") {
		var shelves map[string]any
		if json.Unmarshal([]byte(v), &shelves) == nil {
			best := ""
			for k := range shelves {
				if s := StatusFor(k); rank[s] > rank[best] {
					best = s
				}
			}
			return best
		}
	}
	for _, sep := range []string{",", ";", "|"} {
		if strings.Contains(v, sep) {
			best := ""
			for _, part := range strings.Split(v, sep) {
				if s := StatusFor(part); rank[s] > rank[best] {
					best = s
				}
			}
			return best
		}
	}
	return ""
}

// rank picks the most telling status when a source lists several.
var rank = map[string]int{"": 0, "wishlist": 1, "backlog": 2, "paused": 3, "abandoned": 4, "playing": 5, "completed": 6}

// Rows turns a table into rows with a mapping.
func (t *Table) Apply(m Mapping) []Row {
	col := func(rec []string, f string) string {
		i, ok := m.Columns[f]
		if !ok || i < 0 || i >= len(rec) {
			return ""
		}
		return unsafeCSV(strings.TrimSpace(rec[i]))
	}
	scale := m.RatingScale
	if scale == 0 {
		scale = detectScale(t, m)
	}
	out := make([]Row, 0, len(t.Rows))
	for _, rec := range t.Rows {
		r := Row{Title: col(rec, "title")}
		if r.Title == "" {
			continue
		}
		if len([]rune(r.Title)) > 200 {
			r.Title = string([]rune(r.Title)[:200])
		}
		r.Platform = firstOf(col(rec, "platform"))
		raw := col(rec, "status")
		if s, ok := m.Statuses[strings.ToLower(raw)]; ok {
			r.Status = s
		} else {
			r.Status = StatusFor(raw)
		}
		if v, ok := number(col(rec, "rating")); ok && v > 0 && scale > 0 {
			r.Rating = int64(math.Round(math.Min(v/scale, 1) * 100))
		}
		if v, ok := number(col(rec, "minutes")); ok && v > 0 {
			r.Minutes = int64(v)
		} else if v, ok := hours(col(rec, "hours")); ok && v > 0 {
			r.Minutes = int64(math.Round(v * 60))
		}
		if r.Minutes > 1_000_000 {
			r.Minutes = 1_000_000
		}
		r.Notes = limit(col(rec, "notes"), 5000)
		r.Tags = splitTags(col(rec, "tags"))
		r.StartedAt = date(col(rec, "started"))
		r.CompletedAt = date(col(rec, "finished"))
		if y := year(col(rec, "year")); y > 0 {
			r.Year = y
		}
		r.SteamAppID, _ = strconv.ParseInt(col(rec, "steam_appid"), 10, 64)
		r.IGDBID, _ = strconv.ParseInt(col(rec, "igdb_id"), 10, 64)
		if r.Status == "" {
			if r.CompletedAt != "" {
				r.Status = "completed"
			} else {
				r.Status = "backlog"
			}
		}
		out = append(out, r)
	}
	return out
}

func detectScale(t *Table, m Mapping) float64 {
	i, ok := m.Columns["rating"]
	if !ok || i < 0 {
		return 0
	}
	max := 0.0
	for _, rec := range t.Rows {
		if i < len(rec) {
			if v, ok := number(rec[i]); ok && v > max {
				max = v
			}
		}
	}
	switch {
	case max == 0:
		return 0
	case max <= 5:
		return 5
	case max <= 10:
		return 10
	}
	return 100
}

func number(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, false
	}
	if a, _, ok := strings.Cut(s, "/"); ok {
		s = a
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// hours accepts "12.5", "12h 30m", "12:30" and "750 min".
func hours(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	if h, m, ok := strings.Cut(s, ":"); ok {
		hv, e1 := strconv.Atoi(strings.TrimSpace(h))
		mv, e2 := strconv.Atoi(strings.TrimSpace(m))
		if e1 == nil && e2 == nil && hv >= 0 && mv >= 0 {
			return float64(hv) + float64(mv)/60, true
		}
	}
	if strings.ContainsAny(s, "hm") {
		total := 0.0
		num := ""
		for _, r := range s {
			switch {
			case (r >= '0' && r <= '9') || r == '.' || r == ',':
				num += string(r)
			case r == 'h' || r == 'm':
				v, ok := number(num)
				if ok {
					if r == 'h' {
						total += v
					} else {
						total += v / 60
					}
				}
				num = ""
			}
		}
		if total > 0 {
			return total, true
		}
	}
	return number(s)
}

func firstOf(s string) string {
	for _, sep := range []string{",", ";", "|"} {
		if a, _, ok := strings.Cut(s, sep); ok {
			return strings.TrimSpace(a)
		}
	}
	return strings.TrimSpace(s)
}

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '|' }) {
		if t := limit(strings.TrimSpace(part), 40); t != "" && len(out) < 20 {
			out = append(out, t)
		}
	}
	return out
}

func limit(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var dateLayouts = []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05", "2006/01/02", "01/02/2006", "1/2/2006", "02.01.2006", "Jan 2, 2006", "January 2, 2006", "2 Jan 2006", "2006-01"}

// date normalises a date to YYYY-MM-DD ("" when it can't be read).
func date(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		// Grouvee keeps dates as JSON; take the first date in it.
		for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '"' || r == ',' || r == ' ' }) {
			if d := date(part); d != "" {
				return d
			}
		}
		return ""
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil && t.Year() > 1950 && t.Year() < 2100 {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func year(s string) int {
	s = strings.TrimSpace(s)
	if len(s) >= 4 {
		if y, err := strconv.Atoi(s[:4]); err == nil && y > 1950 && y < 2100 {
			return y
		}
	}
	if d := date(s); d != "" {
		y, _ := strconv.Atoi(d[:4])
		return y
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1e8 {
		return time.Unix(n, 0).UTC().Year() // Unix seconds
	}
	return 0
}

// unsafeCSV undoes the apostrophe Cato's export puts before cells that a
// spreadsheet would read as a formula.
func unsafeCSV(s string) string {
	if len(s) >= 2 && s[0] == '\'' && strings.ContainsRune("=+-@\t\r", rune(s[1])) {
		return s[1:]
	}
	return s
}
