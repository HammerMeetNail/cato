package http

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cato/internal/auth"
	"cato/internal/db"
	"cato/internal/games"
)

// maxLibraryPageSize caps the per-page limit for GET /api/library.
const maxLibraryPageSize = 200

type LibraryHandler struct {
	db    *db.DB
	games *games.Store
}

func NewLibraryHandler(db *db.DB) *LibraryHandler {
	return &LibraryHandler{db: db, games: games.NewStore(db)}
}

// platformNames loads the IGDB ID→name lookup once per request; callers pass
// it down to scanLibraryItem. An empty map (table unfetched / DB error)
// degrades to omitting unknown IDs rather than failing the request.
func (h *LibraryHandler) platformNames(ctx context.Context) map[int64]string {
	names, err := h.games.PlatformNames(ctx)
	if err != nil {
		return map[int64]string{}
	}
	return names
}

func (h *LibraryHandler) Register(mux *http.ServeMux) {
	chain := auth.AuthRequired(h.db)
	csrfChain := auth.CSRFRequired(h.db)

	mux.Handle("/api/library", chain(csrfChain(http.HandlerFunc(h.handleLibrary))))
	mux.Handle("/api/library/tags", chain(http.HandlerFunc(h.handleLibraryTags)))
	// Platform suggestions for the @ search prefix: distinct resolved
	// platform names present in the caller's library.
	mux.Handle("/api/library/platforms", chain(http.HandlerFunc(h.handleLibraryPlatforms)))
	// Registered before the /api/library/ prefix so ServeMux picks the
	// more specific pattern.
	mux.Handle("/api/library/check", chain(http.HandlerFunc(h.handleLibraryCheck)))
	mux.Handle("/api/library/counts", chain(http.HandlerFunc(h.handleLibraryCounts)))
	mux.Handle("/api/library/export", chain(http.HandlerFunc(h.handleLibraryExport)))
	mux.Handle("/api/library/stats", chain(http.HandlerFunc(h.handleLibraryStats)))
	mux.Handle("/api/library/suggestions", chain(http.HandlerFunc(h.handleLibrarySuggestions)))
	mux.Handle("/api/library/", chain(csrfChain(http.HandlerFunc(h.handleLibraryItem))))
}

func (h *LibraryHandler) handleLibrary(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())

	switch r.Method {
	case http.MethodGet:
		h.listLibrary(w, r, userID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
	}
}

func (h *LibraryHandler) handleLibraryItem(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())

	gameID, err := extractGameID(r.URL.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_id", "Invalid game ID"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getLibraryItem(w, r, userID, gameID)
	case http.MethodPost, http.MethodPut:
		h.upsertLibraryItem(w, r, userID, gameID)
	case http.MethodPatch:
		h.patchLibraryItem(w, r, userID, gameID)
	case http.MethodDelete:
		h.deleteLibraryItem(w, r, userID, gameID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
	}
}

func (h *LibraryHandler) listLibrary(w http.ResponseWriter, r *http.Request, userID string) {
	// status may be repeated (?status=wishlist&status=backlog) or comma-separated (?status=wishlist,backlog)
	var statuses []string
	seenStatus := map[string]bool{}
	for _, raw := range r.URL.Query()["status"] {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seenStatus[part] || !isValidStatus(part) {
				continue
			}
			seenStatus[part] = true
			statuses = append(statuses, part)
		}
	}
	tags := r.URL.Query()["tag"]
	tagOp := r.URL.Query().Get("tag_op")
	if tagOp != "or" {
		tagOp = "and"
	}

	// Parse pagination parameters.
	limit := 60 // default
	offset := 0

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if limit > maxLibraryPageSize {
		limit = maxLibraryPageSize
	}

	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	where, whereArgs := libraryFilter(statuses, tags, tagOp)

	// Availability filter ("show games I can play on X"): substring match
	// against the resolved names of games.platforms_json.
	if platform := strings.TrimSpace(r.URL.Query().Get("platform")); platform != "" && len(platform) <= 64 {
		frag, fargs := games.PlatformFilter("g", platform)
		where += " AND " + frag
		whereArgs = append(whereArgs, fargs...)
	}

	// Owned platform filter ("show games I own on X"): substring match
	// against library_items.owned_platforms_json and legacy platform column,
	// resolved via platforms table so shortnames (ps5, sw2) match canonical names.
	if ownedPlatform := strings.TrimSpace(r.URL.Query().Get("owned_platform")); ownedPlatform != "" && len(ownedPlatform) <= 64 {
		pattern := "%" + games.EscapeLike(strings.ToLower(ownedPlatform)) + "%"
		frag := `(
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
		)`
		where += " AND " + frag
		for i := 0; i < 8; i++ {
			whereArgs = append(whereArgs, pattern)
		}
	}

	// Format filter: "both" means either a physical or digital copy, while
	// "none" means the format has not been set.
	formatFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if formatFilter == "" {
		// Accept the storage-oriented name for API callers that use medium.
		formatFilter = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("medium")))
	}
	switch formatFilter {
	case "digital", "physical":
		where += " AND li.medium = ?"
		whereArgs = append(whereArgs, formatFilter)
	case "both":
		where += " AND li.medium IN (?, ?)"
		whereArgs = append(whereArgs, "physical", "digital")
	case "none":
		where += " AND (li.medium = '' OR li.medium IS NULL)"
	}

	// Release date range filters (on g.first_release_date, like search).
	if yf := parseLibraryYearParam(r.URL.Query().Get("year_from"), false); yf != 0 {
		where += " AND g.first_release_date >= ?"
		whereArgs = append(whereArgs, yf)
	}
	if yt := parseLibraryYearParam(r.URL.Query().Get("year_to"), true); yt != 0 {
		where += " AND g.first_release_date <= ?"
		whereArgs = append(whereArgs, yt)
	}
	if rf := parseLibraryDateParam(r.URL.Query().Get("release_from"), false); rf != 0 {
		where += " AND g.first_release_date >= ?"
		whereArgs = append(whereArgs, rf)
	}
	if rt := parseLibraryDateParam(r.URL.Query().Get("release_to"), true); rt != 0 {
		where += " AND g.first_release_date <= ?"
		whereArgs = append(whereArgs, rt)
	}

	// Sorting: library-specific sorts, default is updated (li.updated_at DESC).
	// rating = my rating (li.rating), critic_rating = aggregated critic score.
	sort := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	switch sort {
	case "name", "release_new", "release_old", "rating", "rating_low", "my_rating", "my_rating_low", "critic_rating", "critic_rating_low", "critic", "aggregated_rating", "added", "updated":
	default:
		sort = "updated"
	}
	orderBy := "li.updated_at DESC"
	switch sort {
	case "name":
		orderBy = "g.name COLLATE NOCASE ASC, li.updated_at DESC"
	case "release_new":
		orderBy = "CASE WHEN g.first_release_date = 0 THEN 1 ELSE 0 END, g.first_release_date DESC, li.updated_at DESC"
	case "release_old":
		orderBy = "CASE WHEN g.first_release_date = 0 THEN 1 ELSE 0 END, g.first_release_date ASC, li.updated_at DESC"
	case "rating", "my_rating":
		orderBy = "li.rating DESC, li.updated_at DESC"
	case "rating_low", "my_rating_low":
		orderBy = "CASE WHEN li.rating = 0 THEN 1 ELSE 0 END, li.rating ASC, li.updated_at DESC"
	case "critic_rating", "critic", "aggregated_rating":
		orderBy = "g.aggregated_rating DESC, g.aggregated_rating_count DESC, li.updated_at DESC"
	case "critic_rating_low":
		orderBy = "CASE WHEN g.aggregated_rating_count = 0 THEN 1 ELSE 0 END, g.aggregated_rating ASC, g.aggregated_rating_count DESC, li.updated_at DESC"
	case "added":
		orderBy = "li.created_at DESC, li.updated_at DESC"
	case "updated":
		orderBy = "li.updated_at DESC"
	}

	// Total matching items for the current filter — surfaced via the
	// X-Total-Count header so clients can show "N games" per tab and know
	// when infinite scroll is done without coming up one item short.
	var total int64
	countQuery := `SELECT COUNT(*) FROM library_items li JOIN games g ON g.id = li.game_id WHERE li.user_id = ?` + where
	if err := h.db.QueryRow(countQuery, append([]interface{}{userID}, whereArgs...)...).Scan(&total); err != nil {
		log.Printf("library list: count failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch library"))
		return
	}

	query := `SELECT ` + libraryItemSelect + `
		FROM library_items li
		JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ?` + where +
		` ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?`
	args := append([]interface{}{userID}, whereArgs...)
	// Fetch one extra row so hasMore is exact even when the total is a
	// multiple of the page size.
	args = append(args, limit+1, offset)

	rows, err := h.db.Query(query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch library"))
		return
	}
	defer rows.Close()

	platformNames := h.platformNames(r.Context())

	items := make([]map[string]interface{}, 0)
	hasMore := false
	for rows.Next() {
		item, err := scanLibraryItem(rows, platformNames)
		if err != nil {
			// A schema drift or corrupt row shouldn't silently shrink the
			// user's library; log it loudly.
			log.Printf("library list: skipping unscannable row: %v", err)
			continue
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		items = append(items, item)
	}

	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
	writeJSON(w, http.StatusOK, items)
}

// libraryFilter builds the shared WHERE fragment (status/tag filtering) used
// by both the page query and the COUNT query so they can never diverge.
func libraryFilter(statuses []string, tags []string, tagOp string) (string, []interface{}) {
	where := ""
	args := []interface{}{}

	if len(tags) > 0 {
		// Deduplicate tags case-insensitively: "RPG" and "rpg" are the same filter.
		seenLower := map[string]bool{}
		uniq := make([]string, 0, len(tags))
		for _, t := range tags {
			lt := strings.ToLower(t)
			if lt == "" || seenLower[lt] {
				continue
			}
			seenLower[lt] = true
			uniq = append(uniq, t)
		}
		tags = uniq
		if len(tags) == 0 {
			// All tags were empty duplicates — no filter.
		} else {
			placeholders := make([]string, len(tags))
			for i := range placeholders {
				placeholders[i] = "?"
			}
			inClause := strings.Join(placeholders, ", ")
			if tagOp == "and" {
				// All tags must be present — case-insensitive (RPG = rpg)
				where += ` AND (SELECT COUNT(DISTINCT LOWER(lt.tag)) FROM library_tags lt WHERE lt.user_id = li.user_id AND lt.game_id = li.game_id AND LOWER(lt.tag) IN (` + inClause + `)) = ?`
				for _, t := range tags {
					args = append(args, strings.ToLower(t))
				}
				args = append(args, len(tags))
			} else {
				// Any tag must be present — case-insensitive
				where += ` AND EXISTS (SELECT 1 FROM library_tags lt WHERE lt.user_id = li.user_id AND lt.game_id = li.game_id AND LOWER(lt.tag) IN (` + inClause + `))`
				for _, t := range tags {
					args = append(args, strings.ToLower(t))
				}
			}
		}
	}
	if len(statuses) > 0 {
		valid := make([]string, 0, len(statuses))
		seen := map[string]bool{}
		for _, s := range statuses {
			if !isValidStatus(s) || seen[s] {
				continue
			}
			seen[s] = true
			valid = append(valid, s)
		}
		if len(valid) == 1 {
			where += ` AND li.status = ?`
			args = append(args, valid[0])
		} else if len(valid) > 1 {
			placeholders := make([]string, len(valid))
			for i := range placeholders {
				placeholders[i] = "?"
			}
			where += ` AND li.status IN (` + strings.Join(placeholders, ", ") + `)`
			for _, s := range valid {
				args = append(args, s)
			}
		}
	}
	return where, args
}

// libraryItemSelect is the column list for a library item joined with its game.
// g.platforms_json rides along so clients can offer platform choices in the
// edit form without an extra fetch.
const libraryItemSelect = `li.game_id, li.status, li.rating, li.playtime_minutes, li.tags_json,
	li.notes, li.started_at, li.completed_at, li.created_at, li.updated_at,
	li.platform, li.medium, li.owned_platforms_json,
	g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date,
	g.platforms_json`

// scanLibraryItem scans one row of libraryItemSelect into the API's JSON map.
// Timestamps are emitted as JSON null when unset (previously created_at/
// updated_at were mapped to "" while started/completed used null).
// platformNames resolves games.platforms_json IGDB IDs to display names —
// load it once per request via platformNames and pass it in.
func scanLibraryItem(row interface{ Scan(...interface{}) error }, platformNames map[int64]string) (map[string]interface{}, error) {
	var gameID int64
	var liStatus, tagsJSON, notes string
	var rating, playtime int64
	var startedAt, completedAt, createdAt, updatedAt sql.NullString
	var platform, medium string
	var ownedPlatformsJSON string
	var name, slug, coverURL, localCoverPath string
	var firstReleaseDate int64
	var platformsJSON string

	if err := row.Scan(&gameID, &liStatus, &rating, &playtime, &tagsJSON, &notes,
		&startedAt, &completedAt, &createdAt, &updatedAt,
		&platform, &medium, &ownedPlatformsJSON,
		&name, &slug, &coverURL, &localCoverPath, &firstReleaseDate,
		&platformsJSON); err != nil {
		return nil, err
	}

	tags := []string{}
	json.Unmarshal([]byte(tagsJSON), &tags)

	ownedPlatforms := []string{}
	json.Unmarshal([]byte(ownedPlatformsJSON), &ownedPlatforms)

	platforms := games.ResolvePlatformNames(platformsJSON, platformNames)
	if platforms == nil {
		platforms = []string{}
	}

	nullOrNil := func(ns sql.NullString) interface{} {
		if ns.Valid {
			return ns.String
		}
		return nil
	}

	return map[string]interface{}{
		"game_id":            gameID,
		"status":             liStatus,
		"rating":             rating,
		"playtime_minutes":   playtime,
		"tags":               tags,
		"notes":              notes,
		"started_at":         nullOrNil(startedAt),
		"completed_at":       nullOrNil(completedAt),
		"created_at":         nullOrNil(createdAt),
		"updated_at":         nullOrNil(updatedAt),
		"platform":           platform,
		"medium":             medium,
		"owned_platforms":    ownedPlatforms,
		"game_name":          name,
		"game_slug":          slug,
		"cover_url":          coverURL,
		"local_cover_path":   localCoverPath,
		"first_release_date": firstReleaseDate,
		"platforms":          platforms,
	}, nil
}

// getLibraryItem handles GET /api/library/{gameID} — returns the item or 404
// if it isn't in the caller's library. Lets clients check membership before
// opening an "Add" form that would overwrite existing data.
func (h *LibraryHandler) getLibraryItem(w http.ResponseWriter, r *http.Request, userID string, gameID int64) {
	row := h.db.QueryRow(`SELECT `+libraryItemSelect+`
		FROM library_items li
		JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.game_id = ?`, userID, gameID)

	item, err := scanLibraryItem(row, h.platformNames(r.Context()))
	if err == sql.ErrNoRows || (err == nil && item["status"] == nil) {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "Game is not in your library"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch library item"))
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// libraryUpsertRequest is the body of POST/PUT /api/library/{gameID}.
type libraryUpsertRequest struct {
	Status          string   `json:"status"`
	Rating          int64    `json:"rating"`
	PlaytimeMinutes int64    `json:"playtime_minutes"`
	Tags            []string `json:"tags"`
	Notes           string   `json:"notes"`
	StartedAt       *string  `json:"started_at"`
	CompletedAt     *string  `json:"completed_at"`
	// Platforms is the authoritative multi-ownership list ("owned on PS4
	// AND Switch"); Platform is the legacy singular field, kept working as
	// a one-element shorthand. When both are sent, Platforms wins.
	Platforms []string `json:"platforms"`
	Platform  string   `json:"platform"`
	Medium    string   `json:"medium"`
}

// libraryPatchRequest is the body of PATCH /api/library/{gameID}. Pointer
// fields distinguish "absent" (preserve stored value) from zero values, so a
// patch can both clear a rating (0) and leave one untouched.
type libraryPatchRequest struct {
	Status               *string   `json:"status"`
	Rating               *int64    `json:"rating"`
	PlaytimeMinutes      *int64    `json:"playtime_minutes"`
	PlaytimeDeltaMinutes *int64    `json:"playtime_delta_minutes"`
	Tags                 *[]string `json:"tags"`
	Notes                *string   `json:"notes"`
	StartedAt            *string   `json:"started_at"`
	CompletedAt          *string   `json:"completed_at"`
	Platforms            *[]string `json:"platforms"`
	Platform             *string   `json:"platform"`
	Medium               *string   `json:"medium"`
}

func (h *LibraryHandler) upsertLibraryItem(w http.ResponseWriter, r *http.Request, userID string, gameID int64) {
	var req libraryUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}

	h.writeLibraryItem(w, userID, gameID, req)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// patchLibraryItem handles PATCH /api/library/{gameID} — a partial update
// used by quick actions (mark playing/finished, +time buttons). Unlike the
// POST upsert (which zeroes omitted fields), absent fields preserve their
// stored values, so a one-tap status change can never wipe rating/tags/notes.
// Returns the updated item JSON so clients can refresh cache without a
// refetch. 404 when the game isn't in the caller's library — adding games
// still goes through POST.
func (h *LibraryHandler) patchLibraryItem(w http.ResponseWriter, r *http.Request, userID string, gameID int64) {
	var req libraryPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}
	if req.PlaytimeMinutes != nil && req.PlaytimeDeltaMinutes != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_playtime",
			"Use either playtime_minutes or playtime_delta_minutes, not both"))
		return
	}

	existing, err := h.fetchLibraryItem(userID, gameID, h.platformNames(r.Context()))
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "Game is not in your library"))
		return
	}
	if err != nil {
		log.Printf("library patch: fetch failed for user %s game %d: %v", userID, gameID, err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch library item"))
		return
	}

	// Start from the stored state, overlay whatever the client sent.
	existingOwned, _ := existing["owned_platforms"].([]string)
	merged := libraryUpsertRequest{
		Status:          existing["status"].(string),
		Rating:          toInt64(existing["rating"]),
		PlaytimeMinutes: toInt64(existing["playtime_minutes"]),
		Notes:           existing["notes"].(string),
		Tags:            existing["tags"].([]string),
		Platforms:       existingOwned,
		Platform:        existing["platform"].(string),
		Medium:          existing["medium"].(string),
	}

	if req.Status != nil {
		merged.Status = *req.Status
	}
	if req.Rating != nil {
		merged.Rating = *req.Rating
	}
	switch {
	case req.PlaytimeDeltaMinutes != nil:
		delta := *req.PlaytimeDeltaMinutes
		merged.PlaytimeMinutes += delta
		if merged.PlaytimeMinutes < 0 {
			merged.PlaytimeMinutes = 0
		}
	case req.PlaytimeMinutes != nil:
		merged.PlaytimeMinutes = *req.PlaytimeMinutes
	}
	if req.Tags != nil {
		merged.Tags = *req.Tags
	}
	if req.Notes != nil {
		merged.Notes = *req.Notes
	}
	switch {
	case req.Platforms != nil:
		merged.Platforms = *req.Platforms
	case req.Platform != nil:
		// Legacy singular setter — empty clears, a value replaces the list.
		p := strings.TrimSpace(*req.Platform)
		if p == "" {
			merged.Platforms = []string{}
		} else {
			merged.Platforms = []string{p}
		}
	}
	if req.Medium != nil {
		merged.Medium = *req.Medium
	}
	// Keep the legacy singular column in step with the authoritative list.
	if merged.Platforms != nil {
		if len(merged.Platforms) > 0 {
			merged.Platform = strings.TrimSpace(merged.Platforms[0])
		} else {
			merged.Platform = ""
		}
	}
	// Timestamps keep POST semantics: nil preserves/auto-tracks, "" clears,
	// a value sets. parseTimestampInput in writeLibraryItem handles all three.
	merged.StartedAt = req.StartedAt
	merged.CompletedAt = req.CompletedAt

	if !h.writeLibraryItem(w, userID, gameID, merged) {
		return
	}

	updated, err := h.fetchLibraryItem(userID, gameID, h.platformNames(r.Context()))
	if err != nil {
		// The row was just written by us through the writer pool; a read
		// hiccup shouldn't turn a successful patch into an error response.
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// fetchLibraryItem loads one library item via libraryItemSelect, returning the
// same JSON shape as the list endpoint (scanLibraryItem).
func (h *LibraryHandler) fetchLibraryItem(userID string, gameID int64, platformNames map[int64]string) (map[string]interface{}, error) {
	row := h.db.QueryRow(`SELECT `+libraryItemSelect+`
		FROM library_items li
		JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.game_id = ?`, userID, gameID)
	item, err := scanLibraryItem(row, platformNames)
	if err != nil {
		return nil, err
	}
	if item["status"] == nil {
		return nil, sql.ErrNoRows
	}
	return item, nil
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

// writeLibraryItem validates and persists a full library item state. Both the
// POST upsert and the PATCH merge funnel through here so validation and
// timestamp semantics can never diverge. Returns false if it already wrote an
// error response.
func (h *LibraryHandler) writeLibraryItem(w http.ResponseWriter, userID string, gameID int64, req libraryUpsertRequest) bool {
	if !isValidStatus(req.Status) {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_status",
			"Status must be wishlist, backlog, playing, completed, or abandoned"))
		return false
	}

	// Validate rating
	if req.Rating < 0 || req.Rating > 100 {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_rating", "Rating must be between 0 and 100"))
		return false
	}

	// Validate playtime
	if req.PlaytimeMinutes < 0 {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_playtime", "Playtime must be non-negative"))
		return false
	}

	// Validate ownership fields
	req.Medium = strings.TrimSpace(req.Medium)
	if req.Medium != "" && req.Medium != "physical" && req.Medium != "digital" {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_medium", "Medium must be empty, physical, or digital"))
		return false
	}
	req.Platform = strings.TrimSpace(req.Platform)
	if len(req.Platform) > 64 {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_platform", "Platform must be at most 64 characters"))
		return false
	}

	// Normalize multi-ownership: Platforms is authoritative when present;
	// otherwise the legacy singular platform acts as a one-element list.
	// Trimmed, de-duplicated (case-insensitive), capped at 32 entries.
	ownedPlatforms := make([]string, 0, len(req.Platforms))
	if req.Platforms != nil || req.Platform != "" {
		seen := map[string]bool{}
		list := req.Platforms
		if req.Platforms == nil {
			list = []string{req.Platform}
		}
		for _, p := range list {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if len(p) > 64 {
				writeJSON(w, http.StatusBadRequest, errResp("invalid_platform", "Platform must be at most 64 characters"))
				return false
			}
			key := strings.ToLower(p)
			if seen[key] {
				continue
			}
			if len(ownedPlatforms) >= 32 {
				writeJSON(w, http.StatusBadRequest, errResp("invalid_platform", "At most 32 platforms per game"))
				return false
			}
			seen[key] = true
			ownedPlatforms = append(ownedPlatforms, p)
		}
	}
	platformPrimary := ""
	if len(ownedPlatforms) > 0 {
		platformPrimary = ownedPlatforms[0]
	}

	// Verify game exists
	var exists int
	err := h.db.QueryRow("SELECT 1 FROM games WHERE id = ?", gameID).Scan(&exists)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errResp("game_not_found", "Game not found"))
		return false
	}
	if err != nil {
		// Previously a transient error here fell through to the INSERT and
		// surfaced as a confusing FK 500.
		log.Printf("library upsert: game existence check failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to save library item"))
		return false
	}

	tagsJSON := "[]"
	if req.Tags != nil {
		// Deduplicate tags case-insensitively ("RPG" and "rpg" are the same).
		seenLower := map[string]bool{}
		uniq := make([]string, 0, len(req.Tags))
		for _, t := range req.Tags {
			trimmed := strings.TrimSpace(t)
			if trimmed == "" {
				continue
			}
			lt := strings.ToLower(trimmed)
			if seenLower[lt] {
				continue
			}
			seenLower[lt] = true
			uniq = append(uniq, trimmed)
		}
		b, _ := json.Marshal(uniq)
		tagsJSON = string(b)
	}

	// Timestamp semantics:
	//   absent/null -> preserve the existing value; auto-set on status
	//                  transition into playing/completed if still unset
	//   ""          -> explicit clear
	//   value       -> validated RFC3339 or YYYY-MM-DD, normalized to UTC
	startClear, startVal, err := parseTimestampInput(req.StartedAt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_timestamp", err.Error()))
		return false
	}
	endClear, endVal, err := parseTimestampInput(req.CompletedAt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_timestamp", err.Error()))
		return false
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// VALUES-side timestamp: what a brand-new row gets. An unspecified value
	// auto-tracks when the initial status is playing/completed; the CASE in
	// the ON CONFLICT branch handles updates (preserving existing values).
	insertStarted := insertTimestamp(startClear, startVal, req.Status == "playing", now)
	insertCompleted := insertTimestamp(endClear, endVal, req.Status == "completed", now)

	ownedPlatformsJSON := "[]"
	if len(ownedPlatforms) > 0 {
		b, _ := json.Marshal(ownedPlatforms)
		ownedPlatformsJSON = string(b)
	}

	// The ON CONFLICT DO UPDATE never writes NULL for an unspecified timestamp
	// — that's the fix for the data-loss bug where a UI edit (which sends no
	// timestamps) wiped started_at/completed_at via excluded.<col>.
	_, err = h.db.Exec(`INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes, started_at, completed_at, platform, owned_platforms_json, medium, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, game_id) DO UPDATE SET
			status = excluded.status,
			rating = excluded.rating,
			playtime_minutes = excluded.playtime_minutes,
			tags_json = excluded.tags_json,
			notes = excluded.notes,
			started_at = CASE
				WHEN ? THEN NULL
				WHEN ? THEN ?
				WHEN library_items.started_at IS NOT NULL THEN library_items.started_at
				WHEN excluded.status = 'playing' THEN excluded.updated_at
				ELSE NULL
			END,
			completed_at = CASE
				WHEN ? THEN NULL
				WHEN ? THEN ?
				WHEN library_items.completed_at IS NOT NULL THEN library_items.completed_at
				WHEN excluded.status = 'completed' THEN excluded.updated_at
				ELSE NULL
			END,
			platform = excluded.platform,
			owned_platforms_json = excluded.owned_platforms_json,
			medium = excluded.medium,
			updated_at = excluded.updated_at`,
		userID, gameID, req.Status, req.Rating, req.PlaytimeMinutes,
		tagsJSON, req.Notes, insertStarted, insertCompleted, platformPrimary, ownedPlatformsJSON, req.Medium, now,
		startClear, startVal != "", nullStrOr(startVal),
		endClear, endVal != "", nullStrOr(endVal))
	if err != nil {
		log.Printf("library upsert failed for user %s game %d: %v", userID, gameID, err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to save library item"))
		return false
	}

	// Enqueue a cover job for this game (best-effort, ignore errors).
	h.db.Exec(`INSERT INTO cover_jobs (game_id, source_url)
		SELECT id, cover_url FROM games WHERE id = ? AND cover_url != ''
		ON CONFLICT(game_id) DO NOTHING`, gameID)

	return true
}

func (h *LibraryHandler) deleteLibraryItem(w http.ResponseWriter, r *http.Request, userID string, gameID int64) {
	result, err := h.db.Exec("DELETE FROM library_items WHERE user_id = ? AND game_id = ?", userID, gameID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to remove library item"))
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, errResp("not_found", "Library item not found"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *LibraryHandler) handleLibraryTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())
	q := r.URL.Query().Get("q")

	// The game-form datalist wants the whole tag vocabulary, search only needs
	// the top matches; both come through here with an optional capped limit.
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	// Escape LIKE wildcards so searching "100%" doesn't match "1000 things".
	// Case-insensitive prefix match: "RPG" matches "rpg". Group by LOWER(tag)
	// so "RPG" and "rpg" dedupe to one suggestion (the lexicographically first
	// variant is returned — the user's most canonical casing is preserved in
	// filtering, but autocomplete doesn't spam duplicates).
	rows, err := h.db.Query(`SELECT MIN(tag) AS tag
		FROM library_tags
		WHERE user_id = ? AND LOWER(tag) LIKE ? ESCAPE '\'
		GROUP BY LOWER(tag)
		ORDER BY LOWER(MIN(tag)), MIN(tag)
		LIMIT ?`, userID, strings.ToLower(games.EscapeLike(q))+"%", limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch tags"))
		return
	}
	defer rows.Close()

	tags := make([]string, 0)
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			continue
		}
		tags = append(tags, tag)
	}

	writeJSON(w, http.StatusOK, tags)
}

// handleLibraryPlatforms serves GET /api/library/platforms?q= — the distinct
// platform names appearing in the caller's library (both IGDB availability
// data and manually-set ownership platforms), most-used first. Feeds the
// @ prefix autocomplete in the search bar, mirroring /api/library/tags.
func (h *LibraryHandler) handleLibraryPlatforms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	pattern := "%" + games.EscapeLike(q) + "%"
	prefix := games.EscapeLike(q) + "%"

	// Availability names resolve through the lookup table (matched against
	// name, IGDB abbreviation, and curated shortname); ownership platforms
	// are literal text. Union keeps all sources suggestable. For contemporary
	// queries like "ps", prioritize ps5/ps4 via shortname prefix and recency.
	rows, err := h.db.Query(`SELECT t.name, t.sn, COUNT(*) AS c FROM (
		SELECT COALESCE(NULLIF(p.name, ''), gp.platform_value) AS name,
		       COALESCE(p.abbreviation, '') AS abbr,
		       COALESCE(p.shortname, '') AS sn,
		       p.id AS pid
		FROM library_items li
		JOIN game_platforms gp ON gp.game_id = li.game_id
		LEFT JOIN platforms p ON p.id = gp.platform_id
		WHERE li.user_id = ?
		UNION ALL
		SELECT je.value AS name, '' AS abbr, '' AS sn, 0 AS pid
		FROM library_items li, json_each(li.owned_platforms_json) je
		WHERE li.user_id = ? AND je.value != ''
	) t
	WHERE t.name != '' AND (LOWER(t.name) LIKE ? ESCAPE '\'
	   OR LOWER(t.abbr) LIKE ? ESCAPE '\'
	   OR LOWER(t.sn) LIKE ? ESCAPE '\')
	GROUP BY t.name, t.sn ORDER BY c DESC,
	  CASE WHEN LOWER(t.sn) LIKE ? ESCAPE '\' THEN 0 ELSE 1 END,
	  MAX(t.pid) DESC
	LIMIT 16`,
		userID, userID, pattern, pattern, pattern, prefix)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch platforms"))
		return
	}
	defer rows.Close()

	platforms := make([]string, 0, 8)
	seen := make(map[string]bool, 16)
	for rows.Next() {
		var name, sn string
		var count int
		if err := rows.Scan(&name, &sn, &count); err != nil {
			continue
		}
		// Prioritize contemporary shortname prefix matches (ps5 for "ps")
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

// libCheckItem is one entry of the /api/library/check response: the game ID
// plus which list (status) it's in.
type libCheckItem struct {
	GameID int64  `json:"game_id"`
	Status string `json:"status"`
}

// handleLibraryCheck handles GET /api/library/check?ids=1,2,3 — returns the
// JSON array of {game_id, status} for those game IDs that are in the caller's
// library, so clients can show WHICH list a game is in ("Completed", …).
// Used by the frontend to badge search results and to avoid opening a
// destructive "Add to Library" form for an owned game.
func (h *LibraryHandler) handleLibraryCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())
	raw := r.URL.Query().Get("ids")
	ids := []interface{}{}
	seen := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) >= maxLibraryPageSize {
			break
		}
	}

	inLibrary := make([]libCheckItem, 0)
	if len(ids) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
		query := `SELECT game_id, status FROM library_items WHERE user_id = ? AND game_id IN (` + placeholders + `)`
		args := append([]interface{}{userID}, ids...)
		rows, err := h.db.Query(query, args...)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to check library"))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var it libCheckItem
			if err := rows.Scan(&it.GameID, &it.Status); err == nil {
				inLibrary = append(inLibrary, it)
			}
		}
	}

	writeJSON(w, http.StatusOK, inLibrary)
}

// handleLibraryCounts handles GET /api/library/counts — per-status item
// counts for the caller's library, used for "Backlog (43)" style tab labels.
func (h *LibraryHandler) handleLibraryCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())
	rows, err := h.db.Query(`SELECT status, COUNT(*) FROM library_items WHERE user_id = ? GROUP BY status`, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to count library"))
		return
	}
	defer rows.Close()

	counts := map[string]int64{"all": 0}
	for _, s := range []string{"wishlist", "backlog", "playing", "completed", "abandoned"} {
		counts[s] = 0
	}
	var completed, totalMinutes int64
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			continue
		}
		counts[status] = n
		counts["all"] += n
		if status == "completed" {
			completed = n
		}
	}

	// Lifetime playtime for stats displays ("~120h logged"). Best-effort —
	// a failure just leaves the stat out.
	if err := h.db.QueryRow(`SELECT COALESCE(SUM(playtime_minutes), 0) FROM library_items WHERE user_id = ?`, userID).Scan(&totalMinutes); err != nil {
		totalMinutes = -1
	}
	counts["completed_count"] = completed
	counts["total_minutes"] = totalMinutes

	writeJSON(w, http.StatusOK, counts)
}

// handleLibraryExport handles GET /api/library/export — the caller's entire
// library as a CSV download (spreadsheet-friendly backup). Auth via session
// cookie, so a plain link works; no CSRF needed for a read-only GET.
func (h *LibraryHandler) handleLibraryExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())
	rows, err := h.db.Query(`SELECT li.game_id, li.status, li.rating, li.playtime_minutes,
			li.platform, li.owned_platforms_json, li.medium, li.tags_json, li.notes,
			COALESCE(li.started_at, ''), COALESCE(li.completed_at, ''), COALESCE(li.created_at, ''),
			g.name
		FROM library_items li
		JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ?
		ORDER BY g.name`, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to export library"))
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="cato-library-%s.csv"`, time.Now().UTC().Format("2006-01-02")))

	csv := csv.NewWriter(w)
	_ = csv.Write([]string{"game", "status", "platform", "medium", "rating",
		"playtime_hours", "started_at", "completed_at", "added_at", "tags", "notes"})

	for rows.Next() {
		var gameID int64
		var status, platform, ownedJSON, medium, tagsJSON, notes string
		var rating, playtime int64
		var startedAt, completedAt, createdAt, name string
		var tags []string
		if err := rows.Scan(&gameID, &status, &rating, &playtime, &platform, &ownedJSON,
			&medium, &tagsJSON, &notes, &startedAt, &completedAt, &createdAt, &name); err != nil {
			continue
		}
		json.Unmarshal([]byte(tagsJSON), &tags)
		ownedPlatforms := []string{}
		json.Unmarshal([]byte(ownedJSON), &ownedPlatforms)

		hours := fmt.Sprintf("%.2f", float64(playtime)/60)
		_ = csv.Write([]string{name, status, strings.Join(ownedPlatforms, "; "), medium,
			strconv.FormatInt(rating, 10), hours,
			startedAt, completedAt, createdAt,
			strings.Join(tags, "; "), notes})
	}
	csv.Flush()
}

// handleLibrarySuggestions handles GET /api/library/suggestions?limit=8 —
// popular catalog games (by popularity_score) the caller doesn't own yet,
// covers only, for the "start your library" empty state.
func (h *LibraryHandler) handleLibrarySuggestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())
	limit := 8
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= maxLibraryPageSize {
			limit = l
		}
	}

	// Only games with a locally cached cover — a suggestion without art is
	// just text and won't tempt anyone.
	rows, err := h.db.Query(`SELECT g.id, g.name, g.cover_url, g.local_cover_path, g.first_release_date
		FROM games g
		LEFT JOIN library_items li ON li.game_id = g.id AND li.user_id = ?
		WHERE li.game_id IS NULL
		  AND g.local_cover_path != ''
		ORDER BY g.popularity_score DESC, g.rating_count DESC, g.name
		LIMIT ?`, userID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch suggestions"))
		return
	}
	defer rows.Close()

	suggestions := make([]map[string]interface{}, 0)
	for rows.Next() {
		var id int64
		var name, coverURL, localCoverPath string
		var releaseDate int64
		if err := rows.Scan(&id, &name, &coverURL, &localCoverPath, &releaseDate); err != nil {
			continue
		}
		suggestions = append(suggestions, map[string]interface{}{
			"id":                 id,
			"name":               name,
			"cover_url":          coverURL,
			"local_cover_path":   localCoverPath,
			"first_release_date": releaseDate,
		})
	}
	writeJSON(w, http.StatusOK, suggestions)
}

// handleLibraryStats handles GET /api/library/stats — the numbers behind the
// stats dialog: lifetime totals, this-year activity, finished-per-year
// breakdown, top tags/platforms, and the most recently updated items.
func (h *LibraryHandler) handleLibraryStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
		return
	}

	userID := auth.GetUserID(r.Context())
	stats := map[string]interface{}{
		"total_games":    0,
		"total_finished": 0,
		"total_minutes":  0,
		"avg_rating":     0,
	}

	var total, finished, minutes int64
	var avgRating sql.NullFloat64
	if err := h.db.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(playtime_minutes), 0),
			AVG(NULLIF(rating, 0))
		FROM library_items WHERE user_id = ?`, userID).
		Scan(&total, &finished, &minutes, &avgRating); err == nil {
		stats["total_games"] = total
		stats["total_finished"] = finished
		stats["total_minutes"] = minutes
		if avgRating.Valid {
			stats["avg_rating"] = float64(int64(avgRating.Float64*10+0.5)) / 10 // round to 0.1
		}
	}

	var startedThisYear, addedThisYear int64
	ytd := ` WHERE user_id = ? AND ` + "substr(started_at, 1, 4) = strftime('%Y', 'now')"
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM library_items`+ytd, userID).Scan(&startedThisYear); err == nil {
		stats["started_this_year"] = startedThisYear
	}
	ytdCreated := ` WHERE user_id = ? AND substr(created_at, 1, 4) = strftime('%Y', 'now')`
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM library_items`+ytdCreated, userID).Scan(&addedThisYear); err == nil {
		stats["added_this_year"] = addedThisYear
	}
	finishedYTD := ` WHERE user_id = ? AND status = 'completed' AND substr(completed_at, 1, 4) = strftime('%Y', 'now')`
	var finishedThisYear int64
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM library_items`+finishedYTD, userID).Scan(&finishedThisYear); err == nil {
		stats["finished_this_year"] = finishedThisYear
	}

	// Finished games per year — the spine of the year-in-review view.
	rows, err := h.db.Query(`SELECT substr(completed_at, 1, 4), COUNT(*)
		FROM library_items
		WHERE user_id = ? AND completed_at IS NOT NULL AND completed_at != ''
		GROUP BY 1 ORDER BY 1 DESC LIMIT 10`, userID)
	if err == nil {
		defer rows.Close()
		byYear := make([]map[string]interface{}, 0)
		for rows.Next() {
			var year string
			var n int64
			if rows.Scan(&year, &n) == nil {
				byYear = append(byYear, map[string]interface{}{"year": year, "count": n})
			}
		}
		stats["by_year"] = byYear
	}

	rows2, err := h.db.Query(`SELECT MIN(tag) AS tag, COUNT(*) AS c
		FROM library_tags
		WHERE user_id = ?
		GROUP BY LOWER(tag) ORDER BY c DESC LIMIT 8`, userID)
	if err == nil {
		defer rows2.Close()
		topTags := make([]map[string]interface{}, 0)
		for rows2.Next() {
			var tag string
			var n int64
			if rows2.Scan(&tag, &n) == nil {
				topTags = append(topTags, map[string]interface{}{"tag": tag, "count": n})
			}
		}
		stats["top_tags"] = topTags
	}

	rows3, err := h.db.Query(`SELECT j.value, COUNT(*) AS c
		FROM library_items li, json_each(li.owned_platforms_json) j
		WHERE li.user_id = ?
		GROUP BY j.value ORDER BY c DESC LIMIT 5`, userID)
	if err == nil {
		defer rows3.Close()
		topPlatforms := make([]map[string]interface{}, 0)
		for rows3.Next() {
			var p string
			var n int64
			if rows3.Scan(&p, &n) == nil {
				topPlatforms = append(topPlatforms, map[string]interface{}{"platform": p, "count": n})
			}
		}
		stats["top_platforms"] = topPlatforms
	}

	rows4, err := h.db.Query(`SELECT li.game_id, g.name, li.status, li.updated_at
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ?
		ORDER BY li.updated_at DESC LIMIT 8`, userID)
	if err == nil {
		defer rows4.Close()
		recent := make([]map[string]interface{}, 0)
		for rows4.Next() {
			var gameID int64
			var name, status string
			var updatedAt string
			if rows4.Scan(&gameID, &name, &status, &updatedAt) == nil {
				recent = append(recent, map[string]interface{}{
					"game_id":    gameID,
					"game_name":  name,
					"status":     status,
					"updated_at": updatedAt,
				})
			}
		}
		stats["recent"] = recent
	}

	writeJSON(w, http.StatusOK, stats)
}

// parseTimestampInput interprets an optional client-supplied timestamp.
// nil -> not provided; "" -> explicit clear; otherwise it must parse as
// RFC3339 or YYYY-MM-DD and is normalized to UTC RFC3339 so stored values
// are consistent regardless of client timezone.
func parseTimestampInput(v *string) (clear bool, value string, err error) {
	if v == nil {
		return false, "", nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return true, "", nil
	}
	t, perr := time.Parse(time.RFC3339, s)
	if perr != nil {
		t, perr = time.Parse("2006-01-02", s)
	}
	if perr != nil {
		return false, "", fmt.Errorf("timestamp must be RFC3339 or YYYY-MM-DD, got %q", s)
	}
	return false, t.UTC().Format(time.RFC3339), nil
}

// insertTimestamp computes the timestamp written by the INSERT arm of the
// upsert: explicit clear wins, then explicit value, then auto-track when the
// initial status is playing/completed, else NULL.
func insertTimestamp(clear bool, value string, autoTrack bool, now string) interface{} {
	switch {
	case clear:
		return nil
	case value != "":
		return value
	case autoTrack:
		return now
	default:
		return nil
	}
}

func nullStrOr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func extractGameID(path string) (int64, error) {
	idStr := strings.TrimPrefix(path, "/api/library/")
	idStr = strings.TrimSuffix(idStr, "/")
	return strconv.ParseInt(idStr, 10, 64)
}

func isValidStatus(status string) bool {
	switch status {
	case "wishlist", "backlog", "playing", "completed", "abandoned":
		return true
	}
	return false
}

// parseLibraryYearParam mirrors handler_games.parseYearParam for library filtering.
func parseLibraryYearParam(raw string, end bool) int64 {
	if raw == "" {
		return 0
	}
	y, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || y < 1900 || y > 2100 {
		return 0
	}
	if !end {
		return time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
	return time.Date(y+1, time.January, 1, 0, 0, 0, 0, time.UTC).Unix() - 1
}

// parseLibraryDateParam parses YYYY-MM-DD or RFC3339 into unix seconds.
// When end is true, bare dates are inclusive to end-of-day (23:59:59 UTC).
func parseLibraryDateParam(raw string, end bool) int64 {
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
