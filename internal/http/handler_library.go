package http

import (
	"database/sql"
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
	db *db.DB
}

func NewLibraryHandler(db *db.DB) *LibraryHandler {
	return &LibraryHandler{db: db}
}

func (h *LibraryHandler) Register(mux *http.ServeMux) {
	chain := auth.AuthRequired(h.db)
	csrfChain := auth.CSRFRequired(h.db)

	mux.Handle("/api/library", chain(csrfChain(http.HandlerFunc(h.handleLibrary))))
	mux.Handle("/api/library/tags", chain(http.HandlerFunc(h.handleLibraryTags)))
	// Registered before the /api/library/ prefix so ServeMux picks the
	// more specific pattern.
	mux.Handle("/api/library/check", chain(http.HandlerFunc(h.handleLibraryCheck)))
	mux.Handle("/api/library/counts", chain(http.HandlerFunc(h.handleLibraryCounts)))
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
	case http.MethodDelete:
		h.deleteLibraryItem(w, r, userID, gameID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errResp("method_not_allowed", "Method not allowed"))
	}
}

func (h *LibraryHandler) listLibrary(w http.ResponseWriter, r *http.Request, userID string) {
	status := r.URL.Query().Get("status")
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

	where, whereArgs := libraryFilter(status, tags, tagOp)

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

	query := `SELECT li.game_id, li.status, li.rating, li.playtime_minutes, li.tags_json,
		li.notes, li.started_at, li.completed_at, li.created_at, li.updated_at,
		g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date
		FROM library_items li
		JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ?` + where +
		` ORDER BY li.updated_at DESC LIMIT ? OFFSET ?`
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

	items := make([]map[string]interface{}, 0)
	hasMore := false
	for rows.Next() {
		item, err := scanLibraryItem(rows)
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
func libraryFilter(status string, tags []string, tagOp string) (string, []interface{}) {
	where := ""
	args := []interface{}{}

	if len(tags) > 0 {
		placeholders := make([]string, len(tags))
		for i := range placeholders {
			placeholders[i] = "?"
		}
		inClause := strings.Join(placeholders, ", ")
		if tagOp == "and" {
			// All tags must be present
			where += ` AND (SELECT COUNT(DISTINCT value) FROM json_each(li.tags_json) WHERE value IN (` + inClause + `)) = ?`
			for _, t := range tags {
				args = append(args, t)
			}
			args = append(args, len(tags))
		} else {
			// Any tag must be present
			where += ` AND EXISTS (SELECT 1 FROM json_each(li.tags_json) WHERE value IN (` + inClause + `))`
			for _, t := range tags {
				args = append(args, t)
			}
		}
	}
	if status != "" && isValidStatus(status) {
		where += ` AND li.status = ?`
		args = append(args, status)
	}
	return where, args
}

// libraryItemSelect is the column list for a library item joined with its game.
const libraryItemSelect = `li.game_id, li.status, li.rating, li.playtime_minutes, li.tags_json,
	li.notes, li.started_at, li.completed_at, li.created_at, li.updated_at,
	g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date`

// scanLibraryItem scans one row of libraryItemSelect into the API's JSON map.
// Timestamps are emitted as JSON null when unset (previously created_at/
// updated_at were mapped to "" while started/completed used null).
func scanLibraryItem(row interface{ Scan(...interface{}) error }) (map[string]interface{}, error) {
	var gameID int64
	var liStatus, tagsJSON, notes string
	var rating, playtime int64
	var startedAt, completedAt, createdAt, updatedAt sql.NullString
	var name, slug, coverURL, localCoverPath string
	var firstReleaseDate int64

	if err := row.Scan(&gameID, &liStatus, &rating, &playtime, &tagsJSON, &notes,
		&startedAt, &completedAt, &createdAt, &updatedAt,
		&name, &slug, &coverURL, &localCoverPath, &firstReleaseDate); err != nil {
		return nil, err
	}

	tags := []string{}
	json.Unmarshal([]byte(tagsJSON), &tags)

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
		"game_name":          name,
		"game_slug":          slug,
		"cover_url":          coverURL,
		"local_cover_path":   localCoverPath,
		"first_release_date": firstReleaseDate,
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

	item, err := scanLibraryItem(row)
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

func (h *LibraryHandler) upsertLibraryItem(w http.ResponseWriter, r *http.Request, userID string, gameID int64) {
	var req struct {
		Status          string   `json:"status"`
		Rating          int64    `json:"rating"`
		PlaytimeMinutes int64    `json:"playtime_minutes"`
		Tags            []string `json:"tags"`
		Notes           string   `json:"notes"`
		StartedAt       *string  `json:"started_at"`
		CompletedAt     *string  `json:"completed_at"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_body", "Invalid request body"))
		return
	}

	// Validate status
	if !isValidStatus(req.Status) {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_status",
			"Status must be wishlist, backlog, playing, completed, or abandoned"))
		return
	}

	// Validate rating
	if req.Rating < 0 || req.Rating > 100 {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_rating", "Rating must be between 0 and 100"))
		return
	}

	// Validate playtime
	if req.PlaytimeMinutes < 0 {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_playtime", "Playtime must be non-negative"))
		return
	}

	// Verify game exists
	var exists int
	err := h.db.QueryRow("SELECT 1 FROM games WHERE id = ?", gameID).Scan(&exists)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errResp("game_not_found", "Game not found"))
		return
	}
	if err != nil {
		// Previously a transient error here fell through to the INSERT and
		// surfaced as a confusing FK 500.
		log.Printf("library upsert: game existence check failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to save library item"))
		return
	}

	tagsJSON := "[]"
	if req.Tags != nil {
		b, _ := json.Marshal(req.Tags)
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
		return
	}
	endClear, endVal, err := parseTimestampInput(req.CompletedAt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp("invalid_timestamp", err.Error()))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// VALUES-side timestamp: what a brand-new row gets. An unspecified value
	// auto-tracks when the initial status is playing/completed; the CASE in
	// the ON CONFLICT branch handles updates (preserving existing values).
	insertStarted := insertTimestamp(startClear, startVal, req.Status == "playing", now)
	insertCompleted := insertTimestamp(endClear, endVal, req.Status == "completed", now)

	// The ON CONFLICT DO UPDATE never writes NULL for an unspecified timestamp
	// — that's the fix for the data-loss bug where a UI edit (which sends no
	// timestamps) wiped started_at/completed_at via excluded.<col>.
	_, err = h.db.Exec(`INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes, started_at, completed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			updated_at = excluded.updated_at`,
		userID, gameID, req.Status, req.Rating, req.PlaytimeMinutes,
		tagsJSON, req.Notes, insertStarted, insertCompleted, now,
		startClear, startVal != "", nullStrOr(startVal),
		endClear, endVal != "", nullStrOr(endVal))
	if err != nil {
		log.Printf("library upsert failed for user %s game %d: %v", userID, gameID, err)
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to save library item"))
		return
	}

	// Enqueue a cover job for this game (best-effort, ignore errors).
	h.db.Exec(`INSERT INTO cover_jobs (game_id, source_url)
		SELECT id, cover_url FROM games WHERE id = ? AND cover_url != ''
		ON CONFLICT(game_id) DO NOTHING`, gameID)

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
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

	// Escape LIKE wildcards so searching "100%" doesn't match "1000 things".
	rows, err := h.db.Query(`SELECT DISTINCT j.value
		FROM library_items li, json_each(li.tags_json) j
		WHERE li.user_id = ? AND j.value LIKE ? ESCAPE '\'
		ORDER BY j.value
		LIMIT 10`, userID, games.EscapeLike(q)+"%")
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

// handleLibraryCheck handles GET /api/library/check?ids=1,2,3 — returns the
// JSON array of those game IDs that are in the caller's library. Used by the
// frontend to badge search results and to avoid opening a destructive
// "Add to Library" form for an owned game.
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

	inLibrary := make([]int64, 0)
	if len(ids) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
		query := `SELECT game_id FROM library_items WHERE user_id = ? AND game_id IN (` + placeholders + `)`
		args := append([]interface{}{userID}, ids...)
		rows, err := h.db.Query(query, args...)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to check library"))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err == nil {
				inLibrary = append(inLibrary, id)
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
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			continue
		}
		counts[status] = n
		counts["all"] += n
	}

	writeJSON(w, http.StatusOK, counts)
}

// parseTimestampInput interprets an optional client-supplied timestamp.
// nil -> not provided; "" -> explicit clear; otherwise it must parse as
// RFC3339 or YYYY-MM-DD and is normalized to UTC RFC3339 so stored values
// are consistent regardless of client timezone (FINDINGS §2.2/§3.2).
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
