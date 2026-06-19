package http

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cato/internal/auth"
)

type LibraryHandler struct {
	db *sql.DB
}

func NewLibraryHandler(db *sql.DB) *LibraryHandler {
	return &LibraryHandler{db: db}
}

func (h *LibraryHandler) Register(mux *http.ServeMux) {
	chain := auth.AuthRequired(h.db)
	csrfChain := auth.CSRFRequired(h.db)

	mux.Handle("/api/library", chain(csrfChain(http.HandlerFunc(h.handleLibrary))))
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

	var rows *sql.Rows
	var err error

	if status != "" && isValidStatus(status) {
		rows, err = h.db.Query(`SELECT li.game_id, li.status, li.rating, li.playtime_minutes, li.tags_json,
			li.notes, li.started_at, li.completed_at, li.created_at, li.updated_at,
			g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date
			FROM library_items li
			JOIN games g ON g.id = li.game_id
			WHERE li.user_id = ? AND li.status = ?
			ORDER BY li.updated_at DESC`, userID, status)
	} else {
		rows, err = h.db.Query(`SELECT li.game_id, li.status, li.rating, li.playtime_minutes, li.tags_json,
			li.notes, li.started_at, li.completed_at, li.created_at, li.updated_at,
			g.name, g.slug, g.cover_url, g.local_cover_path, g.first_release_date
			FROM library_items li
			JOIN games g ON g.id = li.game_id
			WHERE li.user_id = ?
			ORDER BY li.updated_at DESC`, userID)
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to fetch library"))
		return
	}
	defer rows.Close()

	items := make([]map[string]interface{}, 0)
	for rows.Next() {
		var gameID int64
		var liStatus, tagsJSON, notes string
		var rating, playtime int64
		var startedAt, completedAt, createdAt, updatedAt sql.NullString
		var name, slug, coverURL, localCoverPath string
		var firstReleaseDate int64

		if err := rows.Scan(&gameID, &liStatus, &rating, &playtime, &tagsJSON, &notes,
			&startedAt, &completedAt, &createdAt, &updatedAt,
			&name, &slug, &coverURL, &localCoverPath, &firstReleaseDate); err != nil {
			continue
		}

		var tags []string
		json.Unmarshal([]byte(tagsJSON), &tags)
		if tags == nil {
			tags = []string{}
		}

		item := map[string]interface{}{
			"game_id":           gameID,
			"status":            liStatus,
			"rating":            rating,
			"playtime_minutes":  playtime,
			"tags":              tags,
			"notes":             notes,
			"created_at":        nullStr(createdAt),
			"updated_at":        nullStr(updatedAt),
			"game_name":         name,
			"game_slug":         slug,
			"cover_url":         coverURL,
			"local_cover_path":  localCoverPath,
			"first_release_date": firstReleaseDate,
		}
		if startedAt.Valid {
			item["started_at"] = startedAt.String
		} else {
			item["started_at"] = nil
		}
		if completedAt.Valid {
			item["completed_at"] = completedAt.String
		} else {
			item["completed_at"] = nil
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, items)
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
	if err := h.db.QueryRow("SELECT 1 FROM games WHERE id = ?", gameID).Scan(&exists); err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, errResp("game_not_found", "Game not found"))
		return
	}

	tagsJSON := "[]"
	if req.Tags != nil {
		b, _ := json.Marshal(req.Tags)
		tagsJSON = string(b)
	}

	var startedAt, completedAt interface{}
	if req.StartedAt != nil {
		startedAt = *req.StartedAt
	}
	if req.CompletedAt != nil {
		completedAt = *req.CompletedAt
	}

	_, err := h.db.Exec(`INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes, started_at, completed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, game_id) DO UPDATE SET
			status = excluded.status,
			rating = excluded.rating,
			playtime_minutes = excluded.playtime_minutes,
			tags_json = excluded.tags_json,
			notes = excluded.notes,
			started_at = excluded.started_at,
			completed_at = excluded.completed_at,
			updated_at = excluded.updated_at`,
		userID, gameID, req.Status, req.Rating, req.PlaytimeMinutes,
		tagsJSON, req.Notes, startedAt, completedAt, time.Now().Format(time.RFC3339))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("db_error", "Failed to save library item"))
		return
	}

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

func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
