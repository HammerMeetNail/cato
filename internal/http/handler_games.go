package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/config"
	"cato/internal/db"
	"cato/internal/games"
	"cato/internal/igdb"
)

type GameHandler struct {
	service *games.Service
	db      *db.DB
}

func NewGameHandler(db *db.DB, cfg *config.Config) *GameHandler {
	store := games.NewStore(db)
	var igdbClient games.IGDBClient
	if cfg.IGDBClientID != "" {
		igdbClient = igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret)
	} else {
		igdbClient = &noopIGDBClient{}
	}
	svc := games.NewService(store, igdbClient, db)
	// Fix legacy accent-bearing normalized_name values (e.g. "pokémon go"
	// stored with é) so searching "pokemon go" without the accent finds
	// the game. Idempotent and cheap; runs even without IGDB.
	svc.StartNormalizationRepair()
	if cfg.IGDBClientID != "" {
		// Populates the platform ID→name lookup table once (single IGDB
		// request) so game platforms render as names, not bare IDs.
		svc.StartPlatformSync()
		svc.StartStaleRefresh()
		svc.StartQueryCacheRefresh()
		// One-shot startup repair of covers broken by the old URL guessing
		// and the dead cato host (needs a real IGDB client).
		svc.StartCoverRepair()
	}
	return &GameHandler{service: svc, db: db}
}

func (h *GameHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/games/search", h.handleSearch)
	mux.HandleFunc("/api/platforms", h.handlePlatforms)
	mux.HandleFunc("/api/games/", h.handleGameByID)
}

// handlePlatforms serves GET /api/platforms?q= — global platform name
// suggestions (distinct names from the platforms lookup table), matched
// against name/abbreviation/shortname. Public (no auth) so the search
// filter can autocomplete even for anonymous users or fresh libraries.
func (h *GameHandler) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	pattern := "%" + games.EscapeLike(q) + "%"
	prefix := games.EscapeLike(q) + "%"
	rows, err := h.db.Query(`SELECT name, COALESCE(shortname,'') FROM platforms
		WHERE name != '' AND (LOWER(name) LIKE ? ESCAPE '\'
		   OR LOWER(COALESCE(abbreviation,'')) LIKE ? ESCAPE '\'
		   OR LOWER(COALESCE(shortname,'')) LIKE ? ESCAPE '\')
		ORDER BY
		  CASE WHEN LOWER(COALESCE(shortname,'')) LIKE ? ESCAPE '\' THEN 0
		       WHEN LOWER(COALESCE(abbreviation,'')) LIKE ? ESCAPE '\' THEN 1
		       WHEN LOWER(name) LIKE ? ESCAPE '\' THEN 2
		       ELSE 3 END,
		  id DESC
		LIMIT 16`, pattern, pattern, pattern, prefix, prefix, prefix)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch platforms"))
		return
	}
	defer rows.Close()
	// Collect names and matching shortname tokens (e.g. "ps5" for PlayStation 5)
	// so the datalist offers both forms and shortname filtering is discoverable.
	// Prioritize contemporary shortnames: for "ps", ps5/ps4 before generic names.
	seen := make(map[string]bool, 16)
	platforms := make([]string, 0, 8)
	for rows.Next() {
		var name, sn string
		if err := rows.Scan(&name, &sn); err != nil {
			continue
		}
		// First, add shortname tokens that are prefix matches (ps5 for ps) — contemporary first
		for _, tok := range strings.Fields(sn) {
			tok = strings.TrimSpace(tok)
			if tok == "" || seen[tok] {
				continue
			}
			if strings.HasPrefix(strings.ToLower(tok), q) {
				seen[tok] = true
				platforms = append(platforms, tok)
				if len(platforms) >= 8 {
					break
				}
			}
		}
		if len(platforms) >= 8 {
			break
		}
		if !seen[name] {
			seen[name] = true
			platforms = append(platforms, name)
			if len(platforms) >= 8 {
				break
			}
		}
		// Then add remaining shortname tokens that contain query as substring
		for _, tok := range strings.Fields(sn) {
			tok = strings.TrimSpace(tok)
			if tok == "" || seen[tok] {
				continue
			}
			if strings.Contains(strings.ToLower(tok), q) {
				seen[tok] = true
				platforms = append(platforms, tok)
				if len(platforms) >= 8 {
					break
				}
			}
		}
		if len(platforms) >= 8 {
			break
		}
	}
	writeJSON(w, http.StatusOK, platforms)
}

func (h *GameHandler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	includeEditions := parseIncludeEditions(r)

	// Branch on full=1 for paginated full results vs. dropdown.
	if r.URL.Query().Get("full") == "1" {
		h.handleSearchFull(w, r, query, includeEditions)
		return
	}

	results, err := h.service.Search(r.Context(), query, includeEditions)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("search_error", "Search failed"))
		return
	}

	if results == nil {
		results = []games.GameResult{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleSearchFull handles paginated full-results search with the relevance floor.
// Parses limit (default 24, clamped [1,60]) and offset (default 0, clamped >=0).
// Optional sort/year_from/year_to/min_rating filters are applied server-side;
// the total match count is returned in X-Total-Count, mirroring the library
// list endpoint's convention.
func (h *GameHandler) handleSearchFull(w http.ResponseWriter, r *http.Request, query string, includeEditions bool) {
	limit := 24
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil {
			limit = parsed
		}
	}
	// Clamp limit to [1, 60]
	if limit < 1 {
		limit = 1
	}
	if limit > 60 {
		limit = 60
	}

	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil {
			offset = parsed
		}
	}
	// Clamp offset to >= 0
	if offset < 0 {
		offset = 0
	}

	sort := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	if !games.ValidSorts[sort] {
		sort = ""
	}

	// Availability filter: substring of a platform name/abbreviation
	// ("switch", "pc", "xbox"). Mirrors the library endpoint's platform param.
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))
	if len(platform) > 64 {
		platform = ""
	}
	// Owned platform filter: substring of owned platform name (library only,
	// e.g. ps5 + completed = completed games owned on ps5, not just available).
	ownedPlatform := strings.TrimSpace(r.URL.Query().Get("owned_platform"))
	if ownedPlatform == "" {
		ownedPlatform = strings.TrimSpace(r.URL.Query().Get("ownedPlatform"))
	}
	if len(ownedPlatform) > 64 {
		ownedPlatform = ""
	}

	// Tag filtering (personal library tags).
	tags := r.URL.Query()["tag"]
	if len(tags) == 0 {
		// Also accept comma-separated "tags" param for convenience.
		if raw := strings.TrimSpace(r.URL.Query().Get("tags")); raw != "" {
			for _, part := range strings.Split(raw, ",") {
				if t := strings.TrimSpace(part); t != "" {
					tags = append(tags, t)
				}
			}
		}
	}
	// Cap tag list to avoid abuse.
	if len(tags) > 16 {
		tags = tags[:16]
	}
	tagOp := r.URL.Query().Get("tag_op")
	if tagOp != "or" {
		tagOp = "and"
	}

	// Library membership filters.
	inLibrary := parseInLibraryParam(r)
	libraryStatus := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("library_status")))
	if libraryStatus == "" {
		libraryStatus = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("libraryStatus")))
	}
	if libraryStatus != "" && !games.ValidLibraryStatuses[libraryStatus] {
		libraryStatus = ""
	}
	// Also allow filtering by library status via plain "status" when it looks
	// like a library status (keeps old clients working that sent status=...).
	if libraryStatus == "" {
		if s := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))); games.ValidLibraryStatuses[s] {
			libraryStatus = s
		}
	}

	// Release date range: support both year_* and exact date params.
	yearFrom := parseYearParam(r.URL.Query().Get("year_from"), false)
	yearTo := parseYearParam(r.URL.Query().Get("year_to"), true)
	if df := parseDateParam(r.URL.Query().Get("release_from"), false); df != 0 {
		if df > yearFrom {
			yearFrom = df
		} else if yearFrom == 0 {
			yearFrom = df
		}
	}
	if dt := parseDateParam(r.URL.Query().Get("release_to"), true); dt != 0 {
		if yearTo == 0 || dt < yearTo {
			yearTo = dt
		}
	}

	// Resolve optional user for personal filters (tags / library).
	libraryUserID := ""
	if len(tags) > 0 || inLibrary != nil || libraryStatus != "" || ownedPlatform != "" {
		if uid := tryGetUserID(r, h.db); uid != "" {
			libraryUserID = uid
		} else {
			writeJSON(w, http.StatusUnauthorized, errResp("auth_required", "Login required for tag/library filters"))
			return
		}
		// Normalize tags: trim, drop empties.
		filtered := tags[:0]
		for _, t := range tags {
			if t = strings.TrimSpace(t); t != "" {
				filtered = append(filtered, t)
			}
		}
		tags = filtered
	} else {
		// Still try to populate libraryUserID if a session exists, so
		// authenticated users can filter by owned status without extra hop,
		// but don't require it.
		libraryUserID = tryGetUserID(r, h.db)
		// If they sent library filters but we stripped them (invalid), ignore.
	}

	results, total, err := h.service.SearchPagedFullWithFilters(r.Context(), query,
		limit, offset, sort,
		yearFrom, yearTo,
		parseMinRatingParam(r.URL.Query().Get("min_rating")),
		platform,
		tags, tagOp, libraryUserID, inLibrary, libraryStatus,
		includeEditions,
		ownedPlatform,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("search_error", "Search failed"))
		return
	}

	if results == nil {
		results = []games.GameResult{}
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSON(w, http.StatusOK, results)
}

// parseYearParam converts a year (e.g. "1998") to unix seconds: start of that
// year for from-bounds, end of it for to-bounds. Returns 0 (= unset) for
// empty or out-of-range input.
func parseYearParam(raw string, end bool) int64 {
	if raw == "" {
		return 0
	}
	y, err := strconv.Atoi(raw)
	if err != nil || y < 1900 || y > 2100 {
		return 0
	}
	if !end {
		return time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
	return time.Date(y+1, time.January, 1, 0, 0, 0, 0, time.UTC).Unix() - 1
}

// parseMinRatingParam clamps to [0, 100]; 0 means unset.
func parseMinRatingParam(raw string) int64 {
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	if n < 0 {
		n = 0
	}
	if n > 100 {
		n = 100
	}
	return n
}

func parseIncludeEditions(r *http.Request) bool {
	for _, key := range []string{"include_editions", "editions", "includeEditions"} {
		v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
		if v == "1" || v == "true" || v == "yes" {
			return true
		}
	}
	return false
}

func parseInLibraryParam(r *http.Request) *bool {
	for _, key := range []string{"in_library", "inLibrary", "owned", "library"} {
		raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
		if raw == "" {
			continue
		}
		v := raw == "1" || raw == "true" || raw == "yes" || raw == "owned" || raw == "only"
		if raw == "0" || raw == "false" || raw == "no" || raw == "exclude" || raw == "not" {
			v = false
		} else if v == false && raw != "0" && raw != "false" && raw != "no" && raw != "exclude" && raw != "not" {
			// Unknown value: treat as true if param present but not recognized false
			v = true
		}
		// Distinguish "only owned" vs "exclude owned": "0"/false means not in library
		if raw == "0" || raw == "false" || raw == "no" || raw == "exclude" || raw == "not" {
			v = false
			return &v
		}
		if raw == "1" || raw == "true" || raw == "yes" || raw == "owned" || raw == "only" {
			v = true
			return &v
		}
		// Bare presence like ?in_library (no value) -> true
		if raw == "" {
			v = true
			return &v
		}
		return &v
	}
	// Also support ?in_library without value (Has check)
	for _, key := range []string{"in_library", "inLibrary", "owned"} {
		if _, ok := r.URL.Query()[key]; ok {
			v := true
			// If the value is explicitly falsy, handled above; otherwise true
			return &v
		}
	}
	return nil
}

func tryGetUserID(r *http.Request, database *db.DB) string {
	cookie, err := r.Cookie("cato_session")
	if err != nil || cookie.Value == "" {
		return ""
	}
	sess, err := auth.GetSession(database, cookie.Value)
	if err != nil || sess == nil {
		return ""
	}
	return sess.UserID
}

// parseDateParam parses YYYY-MM-DD or RFC3339 (or a year) into unix seconds.
// When end is true, bare dates are inclusive to end-of-day.
func parseDateParam(raw string, end bool) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if end {
			return t.Add(24*time.Hour - time.Second).Unix()
		}
		return t.UTC().Unix()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix()
	}
	if y, err := strconv.Atoi(raw); err == nil && y >= 1900 && y <= 2100 {
		if end {
			return time.Date(y+1, time.January, 1, 0, 0, 0, 0, time.UTC).Unix() - 1
		}
		return time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
	return 0
}

func (h *GameHandler) handleGameByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	idStr := strings.TrimPrefix(r.URL.Path, "/api/games/")
	idStr = strings.TrimSuffix(idStr, "/")
	if idStr == "" {
		writeJSON(w, http.StatusBadRequest, errResp("missing_id", "Game ID is required"))
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_id", "Invalid game ID"))
		return
	}

	game, err := h.service.GetGame(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("game_error", "Failed to fetch game"))
		return
	}
	if game == nil {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "Game not found"))
		return
	}

	writeJSON(w, http.StatusOK, game)
}

type noopIGDBClient struct{}

func (c *noopIGDBClient) SearchGames(ctx context.Context, query string, limit int, includeEditions bool) ([]games.Game, error) {
	return nil, nil
}

func (c *noopIGDBClient) GetGame(ctx context.Context, id int64) (*games.Game, error) {
	return nil, nil
}

func (c *noopIGDBClient) GetGamesBatch(ctx context.Context, ids []int64) ([]games.Game, error) {
	return nil, nil
}

func (c *noopIGDBClient) GetPlatforms(ctx context.Context) ([]games.Platform, error) {
	return nil, nil
}
