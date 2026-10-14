package v1

import (
	"net/http"
	"strings"

	"cato/internal/auth"
	"cato/internal/catalog"
	"cato/internal/library"
)

// GameDetail is the game page's catalog data.
type GameDetail struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	Summary       string   `json:"summary"`
	Storyline     string   `json:"storyline"`
	Cover         string   `json:"cover"`
	ReleaseDate   int64    `json:"release_date"`
	Platforms     []string `json:"platforms"`
	CriticRating  int64    `json:"critic_rating"`
	CriticCount   int64    `json:"critic_count"`
	UserRating    float64  `json:"user_rating"`
	UserCount     int64    `json:"user_rating_count"`
	IGDBURL       string   `json:"igdb_url"`
	VersionParent int64    `json:"version_parent,omitempty"`
	ParentGame    int64    `json:"parent_game,omitempty"`
}

func detailOf(g *catalog.Game) GameDetail {
	platforms := g.Platforms
	if platforms == nil {
		platforms = []string{}
	}
	return GameDetail{
		ID: g.ID, Name: g.Name, Slug: g.Slug, Summary: g.Summary, Storyline: g.Storyline,
		Cover: library.CoverURL(g.LocalCoverPath, g.CoverURL), ReleaseDate: g.FirstReleaseDate, Platforms: platforms,
		CriticRating: g.AggregatedRating, CriticCount: g.AggregatedRatingCount,
		UserRating: g.Rating, UserCount: g.RatingCount, IGDBURL: g.IGDBURL,
		VersionParent: g.VersionParent, ParentGame: g.ParentGame,
	}
}

// getGame is the game page: catalog data, your item and journal.
func (a *API) getGame(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := catalog.WithLane(r.Context(), catalog.LaneInteractive)
	g, err := a.Catalog.GetGame(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if g == nil {
		writeError(w, http.StatusNotFound, "game_not_found", "Game not found", nil)
		return
	}
	resp := map[string]any{"game": detailOf(g), "item": nil, "playthroughs": []library.Playthrough{}, "sessions": []library.Session{}}
	item, err := a.Library.Get(ctx, user(r), id)
	switch {
	case err == nil:
		resp["item"] = item
		if resp["playthroughs"], err = a.Library.Playthroughs(ctx, user(r), id); err != nil {
			fail(w, r, err)
			return
		}
		if resp["sessions"], err = a.Library.Sessions(ctx, user(r), library.SessionRange{GameID: id, Limit: 50}); err != nil {
			fail(w, r, err)
			return
		}
	case err != library.ErrNotFound:
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) platforms(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	names, err := a.Games.PlatformNames(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	out := []map[string]any{}
	for id, name := range names {
		if q == "" || strings.Contains(strings.ToLower(name), q) {
			out = append(out, map[string]any{"id": id, "name": name})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// CatalogResult is a search hit with your library status.
type CatalogResult struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Cover       string   `json:"cover"`
	ReleaseDate int64    `json:"release_date"`
	Platforms   []string `json:"platforms"`
	Status      string   `json:"status,omitempty"`
}

func (a *API) annotate(r *http.Request, results []catalog.GameResult) ([]CatalogResult, error) {
	ids := make([]int64, len(results))
	for i, g := range results {
		ids[i] = g.ID
	}
	statuses, err := a.Library.Statuses(r.Context(), user(r), ids)
	if err != nil {
		return nil, err
	}
	out := make([]CatalogResult, 0, len(results))
	for _, g := range results {
		p := g.Platforms
		if p == nil {
			p = []string{}
		}
		out = append(out, CatalogResult{ID: g.ID, Name: g.Name, Cover: library.CoverURL(g.LocalCoverPath, g.CoverURL),
			ReleaseDate: g.FirstReleaseDate, Platforms: p, Status: statuses[g.ID]})
	}
	return out, nil
}

func (a *API) allowSearch(w http.ResponseWriter, r *http.Request) bool {
	if a.SearchLimiter != nil && !a.SearchLimiter.Allow(auth.ClientIP(r)) {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many searches. Try again shortly.", nil)
		return false
	}
	return true
}

const maxQueryChars = 100

func searchQuery(r *http.Request) string {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if rs := []rune(q); len(rs) > maxQueryChars {
		q = string(rs[:maxQueryChars])
	}
	return q
}

// typeahead backs the ⊕ sheet and the search box: your library first,
// then catalog games you don't have.
func (a *API) typeahead(w http.ResponseWriter, r *http.Request) {
	q := searchQuery(r)
	resp := map[string]any{"library": []library.Item{}, "catalog": []CatalogResult{}}
	if len([]rune(q)) < 2 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !a.allowSearch(w, r) {
		return
	}
	page, err := a.Library.List(r.Context(), user(r), library.ListQuery{Filter: library.Filter{Q: q}, Limit: 5})
	if err != nil {
		fail(w, r, err)
		return
	}
	resp["library"] = page.Items
	results, err := a.Catalog.Search(catalog.WithLane(r.Context(), catalog.LaneInteractive), q, r.URL.Query().Get("editions") == "1")
	if err != nil {
		fail(w, r, err)
		return
	}
	annotated, err := a.annotate(r, results)
	if err != nil {
		fail(w, r, err)
		return
	}
	resp["catalog"] = annotated
	writeJSON(w, http.StatusOK, resp)
}

// search is the full catalog search (Discover's results page).
func (a *API) search(w http.ResponseWriter, r *http.Request) {
	q := searchQuery(r)
	if len([]rune(q)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []CatalogResult{}, "total": 0})
		return
	}
	if !a.allowSearch(w, r) {
		return
	}
	vals := r.URL.Query()
	limit := min(max(queryInt(r, "limit", 24), 1), 60)
	offset := max(queryInt(r, "offset", 0), 0)
	var inLibrary *bool
	switch vals.Get("in_library") {
	case "1", "true":
		t := true
		inLibrary = &t
	case "0", "false":
		f := false
		inLibrary = &f
	}
	sort := vals.Get("sort")
	if !catalog.ValidSorts[sort] {
		sort = ""
	}
	minRating := int64(queryInt(r, "min_rating", 0))
	results, total, err := a.Catalog.SearchPagedFullWithFilters(catalog.WithLane(r.Context(), catalog.LaneInteractive), q, limit, offset, sort,
		library.ParseYear(vals.Get("year_from"), false), library.ParseYear(vals.Get("year_to"), true), minRating,
		vals.Get("platform"), nil, "", user(r), inLibrary, vals.Get("status"), vals.Get("editions") == "1")
	if err != nil {
		fail(w, r, err)
		return
	}
	annotated, err := a.annotate(r, results)
	if err != nil {
		fail(w, r, err)
		return
	}
	resp := map[string]any{"items": annotated, "total": total}
	if offset+len(results) < int(total) {
		resp["next_offset"] = offset + len(results)
	}
	writeJSON(w, http.StatusOK, resp)
}
