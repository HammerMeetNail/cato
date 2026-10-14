package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cato/internal/auth"
	"cato/internal/config"
)

func TestHandlePlatforms(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO platforms (id, name, abbreviation, shortname) VALUES
		(6, 'PC (Microsoft Windows)', 'PC', 'win'),
		(167, 'PlayStation 5', 'PS5', 'ps5'),
		(163, 'PlayStation 4', 'PS4', 'ps4'),
		(130, 'Nintendo Switch', 'NS', 'ns sw2')`)

	mux := newTestGamesMux(database)

	// Method guard.
	req := httptest.NewRequest(http.MethodPost, "/api/platforms?q=ps", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}

	// Empty query returns an empty array (not null).
	req = httptest.NewRequest(http.MethodGet, "/api/platforms", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var empty []string
	if err := json.NewDecoder(rec.Body).Decode(&empty); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected empty list, got %v", empty)
	}

	// Shortname prefix matches are prioritized: "ps" → ps5/ps4 before
	// substring-only matches (psvita, psp). The handler interleaves each
	// row's shortname tokens with its full name.
	req = httptest.NewRequest(http.MethodGet, "/api/platforms?q=ps", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var platforms []string
	if err := json.NewDecoder(rec.Body).Decode(&platforms); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(platforms) < 2 || platforms[0] != "ps5" {
		t.Fatalf("expected ps5 first, got %v", platforms)
	}
	idxPS4, idxPSVita := -1, -1
	for i, p := range platforms {
		if p == "ps4" && idxPS4 < 0 {
			idxPS4 = i
		}
		if p == "psvita" && idxPSVita < 0 {
			idxPSVita = i
		}
	}
	if idxPS4 < 0 || (idxPSVita >= 0 && idxPS4 > idxPSVita) {
		t.Errorf("expected ps4 ranked before psvita, got %v", platforms)
	}

	// Abbreviation/shortname match finds PlayStation 5 for "ps5".
	req = httptest.NewRequest(http.MethodGet, "/api/platforms?q=ps5", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	platforms = nil
	if err := json.NewDecoder(rec.Body).Decode(&platforms); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, p := range platforms {
		if p == "ps5" || p == "PlayStation 5" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a PlayStation 5 match for q=ps5, got %v", platforms)
	}

	// LIKE wildcards in the query are escaped: "100%" must not match everything.
	req = httptest.NewRequest(http.MethodGet, "/api/platforms?q=100%25", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	platforms = nil
	if err := json.NewDecoder(rec.Body).Decode(&platforms); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(platforms) != 0 {
		t.Errorf("expected no matches for literal 100%%, got %v", platforms)
	}
}

func TestNoopIGDBClient(t *testing.T) {
	// The noop client is what production uses when IGDB is unconfigured;
	// every method must return empty results without an error so search
	// falls back to local results.
	c := &noopIGDBClient{}
	found, err := c.SearchGames(context.Background(), "zelda", 10, false)
	if err != nil || len(found) != 0 {
		t.Errorf("SearchGames: got %v, %v", found, err)
	}
	game, err := c.GetGame(context.Background(), 1)
	if err != nil || game != nil {
		t.Errorf("GetGame: got %v, %v", game, err)
	}
	batch, err := c.GetGamesBatch(context.Background(), []int64{1, 2})
	if err != nil || len(batch) != 0 {
		t.Errorf("GetGamesBatch: got %v, %v", batch, err)
	}
	plats, err := c.GetPlatforms(context.Background())
	if err != nil || len(plats) != 0 {
		t.Errorf("GetPlatforms: got %v, %v", plats, err)
	}
}

func TestTryGetUserID(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO users (id, email) VALUES ('u-1', 'u1@example.com')`)
	session, err := auth.CreateSession(database, "u-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// No cookie → empty.
	req := httptest.NewRequest(http.MethodGet, "/api/games/search?q=zelda", nil)
	if uid := tryGetUserID(req, database, auth.Cookies{}); uid != "" {
		t.Errorf("expected empty for missing cookie, got %q", uid)
	}

	// Valid cookie → user id.
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: session.ID})
	if uid := tryGetUserID(req, database, auth.Cookies{}); uid != "u-1" {
		t.Errorf("expected u-1, got %q", uid)
	}

	// Bogus cookie → empty (no panic, no error propagation).
	req = httptest.NewRequest(http.MethodGet, "/api/games/search?q=zelda", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: "not-a-real-session"})
	if uid := tryGetUserID(req, database, auth.Cookies{}); uid != "" {
		t.Errorf("expected empty for bogus session, got %q", uid)
	}
}

func TestCatalogJobsWithoutIGDB(t *testing.T) {
	// Without IGDB config only the normalization repair is scheduled; the
	// IGDB-backed jobs stay off (no network calls).
	database := setupGamesTestDB(t)
	defer database.Close()
	handler := NewGameHandler(database, &config.Config{})
	var names []string
	for _, j := range handler.Jobs(&config.Config{}) {
		names = append(names, j.Name)
	}
	if len(names) != 1 || names[0] != "catalog.normalization-repair" {
		t.Errorf("jobs without IGDB = %v", names)
	}
	with := handler.Jobs(&config.Config{IGDBClientID: "id"})
	if len(with) < 4 {
		t.Errorf("IGDB jobs missing: %d", len(with))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handler.service.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
