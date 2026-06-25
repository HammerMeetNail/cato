package http

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"cato/internal/config"
	"cato/internal/games"
	"cato/internal/igdb"
)

type GameHandler struct {
	service *games.Service
}

func NewGameHandler(db *sql.DB, cfg *config.Config) *GameHandler {
	store := games.NewStore(db)
	var igdbClient games.IGDBClient
	if cfg.IGDBClientID != "" {
		igdbClient = igdb.NewClient(cfg.IGDBClientID, cfg.IGDBClientSecret)
	} else {
		igdbClient = &noopIGDBClient{}
	}
	svc := games.NewService(store, igdbClient, db)
	svc.EnqueueMissingCovers()
	if cfg.IGDBClientID != "" {
		svc.StartStaleRefresh()
	}
	return &GameHandler{service: svc}
}

func (h *GameHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/games/search", h.handleSearch)
	mux.HandleFunc("/api/games/", h.handleGameByID)
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

	results, err := h.service.Search(r.Context(), query)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("search_error", "Search failed"))
		return
	}

	if results == nil {
		results = []games.GameResult{}
	}
	writeJSON(w, http.StatusOK, results)
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

func (c *noopIGDBClient) SearchGames(ctx context.Context, query string, limit int) ([]games.Game, error) {
	return nil, nil
}

func (c *noopIGDBClient) GetGame(ctx context.Context, id int64) (*games.Game, error) {
	return nil, nil
}
