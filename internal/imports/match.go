package imports

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
	"cato/internal/library"
)

// SourceSteam is IGDB's external_game_source for Steam.
const SourceSteam = 1

// Game is a catalog game offered as a match.
type Game struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Year  int    `json:"year,omitempty"`
	Cover string `json:"cover"`
}

// Candidate is a row with what it matched.
type Candidate struct {
	Index        int     `json:"index"`
	Row          Row     `json:"row"`
	Match        *Game   `json:"match,omitempty"`
	Confidence   float64 `json:"confidence"`
	State        string  `json:"state"` // matched, review, none
	Alternatives []Game  `json:"alternatives,omitempty"`
	// InLibrary is the matched game's status in your library ("" if not).
	InLibrary string `json:"in_library,omitempty"`
}

// Matcher finds catalog games for rows.
type Matcher struct {
	DB      *db.DB
	Catalog *catalog.Service
}

// Confident is the score from which a match needs no review.
const Confident = 0.85

// Match matches every row: IGDB IDs exactly, Steam app IDs through the
// external IDs this server knows (asking IGDB for the rest when
// configured), then names against the local catalog with the year.
func (m *Matcher) Match(ctx context.Context, userID string, rows []Row) ([]Candidate, error) {
	out := make([]Candidate, len(rows))
	for i, r := range rows {
		out[i] = Candidate{Index: i, Row: r, State: "none"}
	}
	if err := m.byIGDB(ctx, out); err != nil {
		return nil, err
	}
	if err := m.bySteam(ctx, out); err != nil {
		return nil, err
	}
	// Names: a few at a time over the read pool.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range out {
		if out[i].Match != nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c *Candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			m.byName(ctx, c)
		}(&out[i])
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// What's already in the library.
	have := map[int64]string{}
	rowsQ, err := m.DB.QueryContext(ctx, `SELECT game_id, status FROM library_items WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	for rowsQ.Next() {
		var id int64
		var st string
		rowsQ.Scan(&id, &st)
		have[id] = st
	}
	rowsQ.Close()
	for i := range out {
		if out[i].Match != nil {
			out[i].InLibrary = have[out[i].Match.ID]
		}
	}
	return out, nil
}

func (m *Matcher) games(ctx context.Context, ids []int64) (map[int64]Game, error) {
	out := map[int64]Game{}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		q := `SELECT id, name, COALESCE(first_release_date, 0), COALESCE(local_cover_path, ''), COALESCE(cover_url, '') FROM games WHERE id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + `)`
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := m.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var g Game
			var rel int64
			var local, remote string
			if err := rows.Scan(&g.ID, &g.Name, &rel, &local, &remote); err != nil {
				rows.Close()
				return nil, err
			}
			if rel > 0 {
				g.Year = time.Unix(rel, 0).UTC().Year()
			}
			g.Cover = library.CoverURL(local, remote)
			out[g.ID] = g
		}
		rows.Close()
	}
	return out, nil
}

func (m *Matcher) byIGDB(ctx context.Context, cs []Candidate) error {
	var ids []int64
	for _, c := range cs {
		if c.Row.IGDBID != 0 {
			ids = append(ids, c.Row.IGDBID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if m.Catalog != nil {
		m.Catalog.EnsureGames(ctx, positive(ids))
	}
	games, err := m.games(ctx, ids)
	if err != nil {
		return err
	}
	for i := range cs {
		if g, ok := games[cs[i].Row.IGDBID]; ok {
			cs[i].Match, cs[i].Confidence, cs[i].State = &g, 1, "matched"
		}
	}
	return nil
}

func positive(ids []int64) []int64 {
	var out []int64
	for _, id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func (m *Matcher) bySteam(ctx context.Context, cs []Candidate) error {
	byApp := map[string]int64{}
	var uids []string
	for _, c := range cs {
		if c.Row.SteamAppID > 0 {
			uids = append(uids, strconv.FormatInt(c.Row.SteamAppID, 10))
		}
	}
	if len(uids) == 0 {
		return nil
	}
	for start := 0; start < len(uids); start += 500 {
		chunk := uids[start:min(start+500, len(uids))]
		args := []any{SourceSteam}
		for _, u := range chunk {
			args = append(args, u)
		}
		rows, err := m.DB.QueryContext(ctx, `SELECT uid, MIN(game_id) FROM game_external_ids WHERE source = ? AND uid IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`) GROUP BY uid`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var uid string
			var id int64
			rows.Scan(&uid, &id)
			byApp[uid] = id
		}
		rows.Close()
	}
	// Ask IGDB about the app IDs this server hasn't seen, and remember them.
	if m.Catalog != nil {
		if ext, ok := m.Catalog.External(); ok {
			var unknown []string
			for _, u := range uids {
				if _, ok := byApp[u]; !ok {
					unknown = append(unknown, u)
				}
			}
			if found, err := ext.GamesByExternal(catalog.WithLane(ctx, catalog.LaneInteractive), SourceSteam, unknown); err == nil && len(found) > 0 {
				var ids []int64
				for _, id := range found {
					ids = append(ids, id)
				}
				if err := m.Catalog.EnsureGames(ctx, ids); err == nil {
					for u, id := range found {
						byApp[u] = id
						m.DB.ExecContext(ctx, `INSERT OR IGNORE INTO game_external_ids (game_id, source, uid) VALUES (?, ?, ?)`, id, SourceSteam, u)
					}
				}
			}
		}
	}
	var ids []int64
	for _, id := range byApp {
		ids = append(ids, id)
	}
	games, err := m.games(ctx, ids)
	if err != nil {
		return err
	}
	for i := range cs {
		if cs[i].Match != nil || cs[i].Row.SteamAppID == 0 {
			continue
		}
		if g, ok := games[byApp[strconv.FormatInt(cs[i].Row.SteamAppID, 10)]]; ok {
			cs[i].Match, cs[i].Confidence, cs[i].State = &g, 1, "matched"
		}
	}
	return nil
}

func (m *Matcher) byName(ctx context.Context, c *Candidate) {
	if m.Catalog == nil || strings.TrimSpace(c.Row.Title) == "" {
		return
	}
	results, err := m.Catalog.Search(catalog.LocalOnly(ctx), c.Row.Title, false)
	if err != nil || len(results) == 0 {
		return
	}
	want := catalog.NormalizeName(c.Row.Title)
	type scored struct {
		g     Game
		score float64
	}
	var best []scored
	for i, r := range results {
		if i >= 8 {
			break
		}
		g := Game{ID: r.ID, Name: r.Name, Cover: library.CoverURL(r.LocalCoverPath, r.CoverURL)}
		if r.FirstReleaseDate > 0 {
			g.Year = time.Unix(r.FirstReleaseDate, 0).UTC().Year()
		}
		best = append(best, scored{g, Score(want, c.Row.Year, catalog.NormalizeName(r.Name), g.Year, i)})
	}
	// Highest score first (stable: search order breaks ties).
	for i := 1; i < len(best); i++ {
		for j := i; j > 0 && best[j].score > best[j-1].score; j-- {
			best[j], best[j-1] = best[j-1], best[j]
		}
	}
	top := best[0]
	g := top.g
	c.Match, c.Confidence = &g, top.score
	c.State = "review"
	if top.score >= Confident && (len(best) == 1 || best[1].score < top.score-0.05) {
		c.State = "matched"
	}
	for _, b := range best[1:min(len(best), 4)] {
		c.Alternatives = append(c.Alternatives, b.g)
	}
}

// Score rates a catalog name against an imported title (both normalised):
// equal names score highest, the year confirms or contradicts, and later
// search results count slightly less.
func Score(want string, wantYear int, got string, gotYear int, rank int) float64 {
	var s float64
	switch {
	case want == got:
		s = 0.9
	case strings.HasPrefix(got, want+" ") || strings.HasPrefix(want, got+" "):
		s = 0.65
	case strings.Contains(got, want) || strings.Contains(want, got):
		s = 0.55
	default:
		s = 0.4 * similarity(want, got)
	}
	if wantYear > 0 && gotYear > 0 {
		switch d := wantYear - gotYear; {
		case d == 0:
			s += 0.1
		case d == 1 || d == -1:
			s += 0.03
		default:
			s -= 0.25
		}
	} else if want == got {
		s += 0.05
	}
	s -= 0.01 * float64(rank)
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	return s
}

// similarity is the share of words two names have in common.
func similarity(a, b string) float64 {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	common := 0
	for _, w := range wb {
		if set[w] {
			common++
		}
	}
	return float64(common) / float64(max(len(wa), len(wb)))
}
