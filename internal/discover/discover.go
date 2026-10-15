// Package discover answers "what should I play next?": the Discover
// shelves, Up Next scoring over your backlog and estimates at your own
// pace (plan §6.2). It reads the catalog and library tables directly.
package discover

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"cato/internal/db"
	"cato/internal/library"
)

type Service struct {
	db  *db.DB
	lib *library.Store
	now func() time.Time

	cache struct {
		mu      sync.Mutex
		at      time.Time
		shelves []Shelf
	}
}

func New(database *db.DB, lib *library.Store) *Service {
	return &Service{db: database, lib: lib, now: time.Now}
}

// Game is a shelf entry.
type Game struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Cover       string   `json:"cover"`
	ReleaseDate int64    `json:"release_date"`
	Platforms   []string `json:"platforms"`
	TTBMinutes  int64    `json:"ttb_minutes"`
	Rating      int64    `json:"critic_rating"`
	Status      string   `json:"status,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type Shelf struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Items    []Game `json:"items"`
}

const shelfSize = 18

// gameCols selects a shelf Game (plus the caller's status) from games g.
const gameCols = `g.id, g.name, g.local_cover_path, g.cover_url, g.first_release_date, g.platforms_json, g.ttb_normally,
	CAST(MAX(g.aggregated_rating, g.total_rating) AS INTEGER),
	COALESCE((SELECT status FROM library_items mine WHERE mine.user_id = ? AND mine.game_id = g.id), '')`

// notMine hides games already in the caller's library.
const notMine = `NOT EXISTS (SELECT 1 FROM library_items li WHERE li.user_id = ? AND li.game_id = g.id)`

// visible keeps editions, packs and covers-less stubs off the shelves.
//
// Shelf queries that start from a small set (popular here, trending,
// related, upcoming) join it to games with CROSS JOIN, which pins the join
// order: otherwise SQLite may walk every game through idx_games_version_parent
// and probe the small set, ~100× slower on a 300k-game catalog.
const visible = `g.id > 0 AND g.version_parent = 0 AND g.category IN (0, 2, 4, 8, 9, 10, 11) AND (g.local_cover_path != '' OR g.cover_url != '')`

func (s *Service) games(ctx context.Context, userID, query string, args ...any) ([]Game, error) {
	rows, err := s.db.QueryContext(ctx, query, append([]any{userID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, _ := s.platformNames(ctx)
	out := []Game{}
	for rows.Next() {
		var g Game
		var local, remote, platforms string
		if err := rows.Scan(&g.ID, &g.Name, &local, &remote, &g.ReleaseDate, &platforms, &g.TTBMinutes, &g.Rating, &g.Status); err != nil {
			return nil, err
		}
		g.Cover = library.CoverURL(local, remote)
		g.Platforms = resolve(platforms, names)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Service) platformNames(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM platforms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if rows.Scan(&id, &n) == nil {
			out[id] = n
		}
	}
	return out, rows.Err()
}

func resolve(raw string, names map[int64]string) []string {
	var ids []any
	_ = json.Unmarshal([]byte(raw), &ids)
	out := []string{}
	for _, v := range ids {
		if f, ok := v.(float64); ok {
			if n := names[int64(f)]; n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// ownedPlatformIDs maps the platform names on the user's copies (and
// default platforms) to IGDB platform IDs.
func (s *Service) ownedPlatformIDs(ctx context.Context, userID string, extra []string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT p.id FROM platforms p
		WHERE LOWER(p.name) IN (
			SELECT LOWER(je.value) FROM library_items li, json_each(li.owned_platforms_json) je WHERE li.user_id = ?
			UNION SELECT LOWER(value) FROM json_each(?))
		   OR LOWER(p.abbreviation) IN (
			SELECT LOWER(je.value) FROM library_items li, json_each(li.owned_platforms_json) je WHERE li.user_id = ?
			UNION SELECT LOWER(value) FROM json_each(?))`, userID, jsonList(extra), userID, jsonList(extra))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func inList(ids []int64) string {
	if len(ids) == 0 {
		return "0"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ",")
}

// Shelves builds Discover for userID.
// Shelves builds Discover. The catalog-wide shelves (trending, new, short,
// gems, classics, popular) don't depend on the person: their candidates
// are computed by the hourly discover.refresh job (or on first use) and
// each request only drops the games already in the person's library. The
// per-person shelves (coming soon on your platforms, popular on this
// server, because you liked…) are cheap indexed reads.
func (s *Service) Shelves(ctx context.Context, userID string) ([]Shelf, error) {
	catalog, err := s.catalogShelves(ctx)
	if err != nil {
		return nil, err
	}
	mine, err := s.libraryIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	prefs, _ := s.lib.GetPrefs(ctx, userID)
	now := s.now().Unix()
	owned, err := s.ownedPlatformIDs(ctx, userID, prefs.DefaultPlatforms)
	if err != nil {
		return nil, err
	}
	var shelves []Shelf
	add := func(id, title, subtitle string, items []Game, err error) error {
		if err != nil {
			return err
		}
		if len(items) > 0 {
			shelves = append(shelves, Shelf{ID: id, Title: title, Subtitle: subtitle, Items: items})
		}
		return nil
	}
	fromCatalog := func(id string) {
		for _, c := range catalog {
			if c.ID == id {
				add(c.ID, c.Title, c.Subtitle, notIn(c.Items, mine), nil)
			}
		}
	}

	fromCatalog("trending")

	platformClause := ""
	sub := "On every platform"
	if len(owned) > 0 {
		platformClause = ` AND rd.platform_id IN (` + inList(owned) + `)`
		sub = "On your platforms"
	}
	// Each game's next release on your platforms in the next six months, in
	// one grouped pass over the date index. CROSS JOIN pins the join order:
	// otherwise SQLite may walk every game and probe the upcoming set.
	soon, err := s.games(ctx, userID, `SELECT `+gameCols+` FROM (
			SELECT rd.game_id, MIN(rd.date) AS next FROM game_release_dates rd
			WHERE rd.date > ? AND rd.date < ?`+platformClause+` GROUP BY rd.game_id
		) up CROSS JOIN games g ON g.id = up.game_id
		WHERE `+visible+` AND `+notMine+`
		ORDER BY up.next, g.popularity_score DESC LIMIT ?`,
		now, now+180*86400, userID, shelfSize)
	if err := add("coming-soon", "Coming soon", sub, soon, err); err != nil {
		return nil, err
	}

	fromCatalog("new")

	local, err := s.games(ctx, userID, `SELECT `+gameCols+` FROM (
			SELECT game_id, COUNT(*) AS n FROM library_items WHERE user_id != ? GROUP BY game_id HAVING n >= 1
		) pop CROSS JOIN games g ON g.id = pop.game_id
		WHERE `+visible+` AND `+notMine+` ORDER BY pop.n DESC, g.popularity_score DESC LIMIT ?`, userID, userID, shelfSize)
	if err := add("instance", "Popular on this server", "In other people's libraries here", local, err); err != nil {
		return nil, err
	}

	// Because you liked X: the similar games of your best recent finish.
	var seedID int64
	var seedName string
	err = s.db.QueryRowContext(ctx, `SELECT li.game_id, g.name FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.rating >= 80 AND EXISTS (SELECT 1 FROM game_relations r WHERE r.game_id = li.game_id AND r.kind = 'similar')
		ORDER BY li.rating DESC, li.updated_at DESC LIMIT 1`, userID).Scan(&seedID, &seedName)
	if err == nil {
		like, err := s.games(ctx, userID, `SELECT `+gameCols+` FROM game_relations r CROSS JOIN games g ON g.id = r.related_id
			WHERE r.game_id = ? AND r.kind = 'similar' AND `+visible+` AND `+notMine+`
			ORDER BY g.total_rating DESC LIMIT ?`, seedID, userID, shelfSize)
		if err := add("because", "Because you liked "+seedName, "", like, err); err != nil {
			return nil, err
		}
	} else if err != sql.ErrNoRows {
		return nil, err
	}

	fromCatalog("short")
	fromCatalog("gems")
	fromCatalog("classics")
	// Popular (with art) as the fallback when the richer data isn't there yet.
	if len(shelves) < 2 {
		fromCatalog("popular")
	}
	return shelves, nil
}

// candidates is how many games each catalog shelf keeps, so that after
// dropping a person's library there are still enough to fill it.
const candidates = 150

// shelfTTL is how old the catalog shelves may get before a request
// recomputes them (the job refreshes them hourly).
const shelfTTL = 2 * time.Hour

// catalogShelves returns the cached catalog-wide shelves, computing them on
// first use or when stale. Concurrent callers wait for one computation.
func (s *Service) catalogShelves(ctx context.Context) ([]Shelf, error) {
	s.cache.mu.Lock()
	defer s.cache.mu.Unlock()
	if s.cache.shelves != nil && s.now().Sub(s.cache.at) < shelfTTL {
		return s.cache.shelves, nil
	}
	shelves, err := s.computeCatalogShelves(ctx)
	if err != nil {
		return nil, err
	}
	// An empty catalog (a new server before its first import) is cheap to
	// ask again and mustn't stick for hours.
	if !emptyShelves(shelves) {
		s.cache.shelves, s.cache.at = shelves, s.now()
	}
	return shelves, nil
}

func emptyShelves(shelves []Shelf) bool {
	for _, sh := range shelves {
		if len(sh.Items) > 0 {
			return false
		}
	}
	return true
}

// RefreshShelves recomputes the catalog shelves (the discover.refresh job).
func (s *Service) RefreshShelves(ctx context.Context) error {
	shelves, err := s.computeCatalogShelves(ctx)
	if err != nil {
		return err
	}
	if emptyShelves(shelves) {
		return nil
	}
	s.cache.mu.Lock()
	s.cache.shelves, s.cache.at = shelves, s.now()
	s.cache.mu.Unlock()
	return nil
}

func (s *Service) computeCatalogShelves(ctx context.Context) ([]Shelf, error) {
	now := s.now().Unix()
	// No person: the status column is empty and notMine matches every game.
	const anyone = ""
	var out []Shelf
	add := func(id, title, subtitle, query string, args ...any) error {
		items, err := s.games(ctx, anyone, query, args...)
		if err != nil {
			return err
		}
		out = append(out, Shelf{ID: id, Title: title, Subtitle: subtitle, Items: items})
		return nil
	}
	steps := []func() error{
		func() error {
			return add("trending", "Trending", "What people are looking at on IGDB", `SELECT `+gameCols+` FROM game_popularity gp CROSS JOIN games g ON g.id = gp.game_id
				WHERE gp.type = 1 AND `+visible+` ORDER BY gp.value DESC LIMIT ?`, candidates)
		},
		func() error {
			return add("new", "New releases", "Out in the last two months", `SELECT `+gameCols+` FROM games g
				WHERE g.first_release_date BETWEEN ? AND ? AND `+visible+`
				ORDER BY g.igdb_popularity DESC, g.popularity_score DESC LIMIT ?`, now-60*86400, now, candidates)
		},
		func() error {
			return add("short", "Short and great", "Under 8 hours, rated 85 or more", `SELECT `+gameCols+` FROM games g
				WHERE g.ttb_normally > 0 AND g.ttb_normally <= 480 AND MAX(g.aggregated_rating, g.total_rating) >= 85
				  AND `+visible+` ORDER BY MAX(g.aggregated_rating, g.total_rating) DESC, g.total_rating_count DESC LIMIT ?`, candidates)
		},
		func() error {
			return add("gems", "Hidden gems", "Loved by the few who played them", `SELECT `+gameCols+` FROM games g
				WHERE g.total_rating >= 85 AND g.total_rating_count BETWEEN 5 AND 60 AND `+visible+`
				ORDER BY g.total_rating DESC LIMIT ?`, candidates)
		},
		func() error {
			return add("classics", "Classics you haven't played", "Released before 2005", `SELECT `+gameCols+` FROM games g
				WHERE g.first_release_date > 0 AND g.first_release_date < ? AND g.total_rating >= 88 AND g.total_rating_count >= 50
				  AND `+visible+` ORDER BY g.total_rating_count DESC LIMIT ?`,
				time.Date(2005, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), candidates)
		},
		func() error {
			return add("popular", "Popular", "", `SELECT `+gameCols+` FROM games g
				WHERE g.local_cover_path != '' AND g.id > 0
				ORDER BY g.popularity_score DESC, g.rating_count DESC, g.name LIMIT ?`, candidates)
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// libraryIDs is the set of games in the person's library.
func (s *Service) libraryIDs(ctx context.Context, userID string) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT game_id FROM library_items WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// notIn keeps the first shelfSize games that aren't in mine.
func notIn(items []Game, mine map[int64]bool) []Game {
	out := make([]Game, 0, shelfSize)
	for _, g := range items {
		if !mine[g.ID] {
			out = append(out, g)
			if len(out) == shelfSize {
				break
			}
		}
	}
	return out
}

// ComingSoonForYou lists wishlist games with a release date ahead (Home).
func (s *Service) ComingSoonForYou(ctx context.Context, userID string, limit int) ([]Game, error) {
	now := s.now().Unix()
	items, err := s.games(ctx, userID, `SELECT `+gameCols+` FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.status = 'wishlist'
		  AND (g.first_release_date > ? OR EXISTS (SELECT 1 FROM game_release_dates rd WHERE rd.game_id = g.id AND rd.date > ?))
		ORDER BY COALESCE((SELECT MIN(rd.date) FROM game_release_dates rd WHERE rd.game_id = g.id AND rd.date > ?), g.first_release_date)
		LIMIT ?`, userID, now, now, now, limit)
	if err != nil {
		return nil, err
	}
	for i := range items {
		var next sql.NullInt64
		s.db.QueryRowContext(ctx, `SELECT MIN(date) FROM game_release_dates WHERE game_id = ? AND date > ?`, items[i].ID, now).Scan(&next)
		if next.Valid && next.Int64 > 0 {
			items[i].ReleaseDate = next.Int64
		}
	}
	return items, nil
}

// ---- Estimates at your pace ------------------------------------------------------

// Pace is how you play compared with IGDB's "normally" time to beat.
type Pace struct {
	// Multiplier is your hours ÷ IGDB hours over finished games (1 when
	// there's too little history).
	Multiplier float64 `json:"multiplier"`
	// WeeklyMinutes is your average play per week over the last 8 weeks.
	WeeklyMinutes int64 `json:"weekly_minutes"`
	Samples       int   `json:"samples"`
}

// Estimate is a game's expected length for you.
type Estimate struct {
	Minutes int64   `json:"minutes"`
	Weeks   float64 `json:"weeks"`
	Pace    Pace    `json:"pace"`
}

func (s *Service) PaceFor(ctx context.Context, userID string) (Pace, error) {
	p := Pace{Multiplier: 1}
	var mine, igdb sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT SUM(li.playtime_minutes), SUM(g.ttb_normally), COUNT(*)
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.status = 'completed' AND g.ttb_normally > 0 AND li.playtime_minutes > 0`, userID).
		Scan(&mine, &igdb, &p.Samples); err != nil {
		return p, err
	}
	if p.Samples >= 2 && igdb.Float64 > 0 {
		p.Multiplier = math.Max(0.3, math.Min(3, mine.Float64/igdb.Float64))
	}
	since := s.now().AddDate(0, 0, -56).Format("2006-01-02")
	var total sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT SUM(minutes) FROM play_sessions
		WHERE user_id = ? AND played_on >= ? AND source IN ('manual', 'timer', 'steam')`, userID, since).Scan(&total); err != nil {
		return p, err
	}
	p.WeeklyMinutes = total.Int64 / 8
	return p, nil
}

func (p Pace) estimate(ttbMinutes int64) *Estimate {
	if ttbMinutes <= 0 {
		return nil
	}
	e := &Estimate{Minutes: int64(float64(ttbMinutes) * p.Multiplier), Pace: p}
	if p.WeeklyMinutes > 0 {
		e.Weeks = math.Round(float64(e.Minutes)/float64(p.WeeklyMinutes)*10) / 10
	}
	return e
}

// EstimateFor estimates gameID's length for userID.
func (s *Service) EstimateFor(ctx context.Context, userID string, gameID int64) (*Estimate, error) {
	var ttb int64
	if err := s.db.QueryRowContext(ctx, `SELECT ttb_normally FROM games WHERE id = ?`, gameID).Scan(&ttb); err != nil {
		return nil, err
	}
	p, err := s.PaceFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	return p.estimate(ttb), nil
}

// ---- Up Next ------------------------------------------------------------------------

// Fit is how much time the person has: "tonight" (an evening), "weekend"
// or "long" (a long haul); "" means no preference.
type Fit string

type candidate struct {
	item    library.Item
	score   float64
	reasons []reason
}

type reason struct {
	weight float64
	text   string
}

func (c *candidate) add(w float64, text string) {
	c.score += w
	if text != "" {
		c.reasons = append(c.reasons, reason{w, text})
	}
}

// UpNext ranks the backlog (and paused games) for userID. It returns up to
// n picks with a short reason each ("~9h · on Switch · you loved Celeste").
func (s *Service) UpNext(ctx context.Context, userID string, n int, fit Fit) ([]library.UpNextPick, error) {
	page, err := s.lib.List(ctx, userID, library.ListQuery{Filter: library.Filter{Statuses: []string{library.StatusBacklog, library.StatusPaused}}, Limit: library.MaxPageSize})
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return []library.UpNextPick{}, nil
	}
	pace, err := s.PaceFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	recentPlatforms, err := s.recentPlatforms(ctx, userID)
	if err != nil {
		return nil, err
	}
	affinity, err := s.affinity(ctx, userID)
	if err != nil {
		return nil, err
	}
	continuations, err := s.seriesContinuations(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var cands []*candidate
	for _, it := range page.Items {
		c := &candidate{item: it}
		ttb := it.Game.TTBMinutes
		if ttb > 0 {
			mine := int64(float64(ttb) * pace.Multiplier)
			label := fmt.Sprintf("~%dh", max(1, (mine+30)/60))
			switch fit {
			case "tonight":
				if mine <= 180 {
					c.add(4, label)
				} else if mine <= 480 {
					c.add(1, label)
				} else {
					c.add(-3, "")
				}
			case "weekend":
				if mine <= 900 {
					c.add(3, label)
				} else {
					c.add(-1, "")
				}
			case "long":
				if mine >= 1800 {
					c.add(3, label)
				}
			default:
				c.add(0.5, label)
			}
		}
		for _, p := range it.Platforms {
			if recentPlatforms[strings.ToLower(p)] {
				c.add(2, "on "+shortPlatform(p))
				break
			}
		}
		if a := affinity[it.GameID]; a.score > 0 {
			c.add(math.Min(4, a.score), a.because)
		}
		if seq, ok := continuations[it.GameID]; ok {
			c.add(4, "next in "+seq)
		}
		if it.Status == library.StatusPaused {
			c.add(1.5, "paused, not finished")
		}
		if added, err := time.Parse(time.RFC3339, it.CreatedAt); err == nil {
			years := now.Sub(added).Hours() / 24 / 365
			if years >= 1 {
				c.add(math.Min(2, years), fmt.Sprintf("waiting %d year%s", int(years), plural(int(years))))
			} else {
				c.add(years, "")
			}
		}
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	out := []library.UpNextPick{}
	for _, c := range cands[:min(n, len(cands))] {
		sort.SliceStable(c.reasons, func(i, j int) bool { return c.reasons[i].weight > c.reasons[j].weight })
		var parts []string
		for _, r := range c.reasons {
			if len(parts) == 3 {
				break
			}
			parts = append(parts, r.text)
		}
		text := strings.Join(parts, " · ")
		if text == "" {
			text = "From your backlog"
		}
		out = append(out, library.UpNextPick{Item: c.item, Reason: text})
	}
	return out, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// shortPlatform turns "Nintendo Switch" into "Switch", "PC (Microsoft
// Windows)" into "PC".
func shortPlatform(p string) string {
	if i := strings.Index(p, " ("); i > 0 {
		p = p[:i]
	}
	return strings.TrimPrefix(p, "Nintendo ")
}

// recentPlatforms: platforms of games with sessions in the last 30 days.
func (s *Service) recentPlatforms(ctx context.Context, userID string) (map[string]bool, error) {
	since := s.now().AddDate(0, 0, -30).Format("2006-01-02")
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT LOWER(je.value) FROM play_sessions ps
		JOIN library_items li ON li.user_id = ps.user_id AND li.game_id = ps.game_id, json_each(li.owned_platforms_json) je
		WHERE ps.user_id = ? AND ps.played_on >= ? AND ps.source != 'legacy'`, userID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out[p] = true
		}
	}
	return out, rows.Err()
}

type affinityScore struct {
	score   float64
	because string
}

// affinity scores backlog games by shared genres, themes and developers
// with games the user rated 80 or more.
func (s *Service) affinity(ctx context.Context, userID string) (map[int64]affinityScore, error) {
	loved := map[string]float64{}
	lovedName := map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT g.name, g.genres_json, g.themes_json, li.rating,
			COALESCE((SELECT GROUP_CONCAT(company_id) FROM game_companies gc WHERE gc.game_id = g.id AND gc.developer = 1), '')
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.rating >= 80 ORDER BY li.rating DESC, li.updated_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	keys := func(genres, themes, devs string) []string {
		var out []string
		var ids []int64
		json.Unmarshal([]byte(genres), &ids)
		for _, id := range ids {
			out = append(out, fmt.Sprintf("g%d", id))
		}
		ids = nil
		json.Unmarshal([]byte(themes), &ids)
		for _, id := range ids {
			out = append(out, fmt.Sprintf("t%d", id))
		}
		for _, d := range strings.Split(devs, ",") {
			if d != "" {
				out = append(out, "d"+d)
			}
		}
		return out
	}
	for rows.Next() {
		var name, genres, themes, devs string
		var rating int64
		if err := rows.Scan(&name, &genres, &themes, &rating, &devs); err != nil {
			rows.Close()
			return nil, err
		}
		w := float64(rating-70) / 30
		for _, k := range keys(genres, themes, devs) {
			loved[k] += w
			if _, ok := lovedName[k]; !ok {
				lovedName[k] = name
			}
		}
	}
	rows.Close()
	out := map[int64]affinityScore{}
	if len(loved) == 0 {
		return out, nil
	}
	rows, err = s.db.QueryContext(ctx, `SELECT g.id, g.genres_json, g.themes_json,
			COALESCE((SELECT GROUP_CONCAT(company_id) FROM game_companies gc WHERE gc.game_id = g.id AND gc.developer = 1), '')
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.status IN ('backlog', 'paused')`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var genres, themes, devs string
		if err := rows.Scan(&id, &genres, &themes, &devs); err != nil {
			return nil, err
		}
		var a affinityScore
		best := 0.0
		for _, k := range keys(genres, themes, devs) {
			w := loved[k]
			if strings.HasPrefix(k, "d") {
				w *= 2 // same studio is a strong signal
			} else if strings.HasPrefix(k, "t") {
				w *= 0.5
			}
			a.score += w
			if w > best {
				best = w
				a.because = "you loved " + lovedName[k]
			}
		}
		if a.score > 0 {
			out[id] = a
		}
	}
	return out, rows.Err()
}

// seriesContinuations finds backlog games that follow (by release date) a
// game the user finished in the same collection.
func (s *Service) seriesContinuations(ctx context.Context, userID string) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT nxt.game_id, sd.name FROM library_items done
		JOIN games dg ON dg.id = done.game_id
		JOIN game_series sd ON sd.game_id = done.game_id AND sd.kind = 'collection'
		JOIN game_series nxt ON nxt.kind = 'collection' AND nxt.series_id = sd.series_id AND nxt.game_id != done.game_id
		JOIN games ng ON ng.id = nxt.game_id
		JOIN library_items li ON li.user_id = done.user_id AND li.game_id = nxt.game_id AND li.status IN ('backlog', 'paused')
		WHERE done.user_id = ? AND done.status = 'completed' AND ng.first_release_date > dg.first_release_date`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			out[id] = "the " + name + " series"
		}
	}
	return out, rows.Err()
}
