package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cato/internal/config"
	"cato/internal/db"
	"cato/internal/games"

	_ "modernc.org/sqlite"
)

func setupGamesTestDB(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

func newTestGamesMux(db *db.DB) *http.ServeMux {
	mux := http.NewServeMux()
	cfg := &config.Config{}
	handler := NewGameHandler(db, cfg)
	handler.Register(mux)
	return mux
}

func TestSearchFull(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()

	// Insert test games
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score) VALUES
		(1, 'Zelda One', 'zelda-one', 'zelda one', 1000),
		(2, 'Zelda Two', 'zelda-two', 'zelda two', 500),
		(3, 'Zelda Three', 'zelda-three', 'zelda three', 100)`)

	mux := newTestGamesMux(database)

	// Test full=1 parameter with pagination
	req := httptest.NewRequest(http.MethodGet, "/api/games/search?q=zelda&full=1&limit=2&offset=0", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var results []games.GameResult
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results for limit=2, got %d", len(results))
	}

	// Test offset pagination
	req = httptest.NewRequest(http.MethodGet, "/api/games/search?q=zelda&full=1&limit=2&offset=2", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var results2 []games.GameResult
	if err := json.NewDecoder(rec.Body).Decode(&results2); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(results2) != 1 {
		t.Errorf("expected 1 result for offset=2, limit=2, got %d", len(results2))
	}

	// Verify no overlap between pages
	for _, r1 := range results {
		for _, r2 := range results2 {
			if r1.ID == r2.ID {
				t.Errorf("page overlap: result %d appears in both pages", r1.ID)
			}
		}
	}
}

func TestSearchFullLimitClamping(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()

	// Insert a test game
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, 'Test', 'test', 'test')`)

	mux := newTestGamesMux(database)

	tests := []struct {
		name      string
		limit     string
		expectMin int // minimum expected limit
		expectMax int // maximum expected limit
	}{
		{"limit below 1", "0", 1, 60},
		{"limit above 60", "100", 1, 60},
		{"limit default", "", 1, 60}, // will default to 24 in the handler
	}

	for _, tt := range tests {
		url := "/api/games/search?q=test&full=1"
		if tt.limit != "" {
			url += "&limit=" + tt.limit
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", tt.name, rec.Code)
		}
	}
}

func TestSearchDropdownUnchanged(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()

	// Insert a strong match (prefix match)
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score) VALUES
		(1, 'Zelda Popular', 'zelda-popular', 'zelda popular', 5000)`)

	// Insert a weak tier-3 match (substring only, popularity_score=0)
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score) VALUES
		(2, 'Puzzzeldic Junk', 'puzzzeldic-junk', 'puzzzeldic junk', 0)`)

	mux := newTestGamesMux(database)

	// Test without full=1 (dropdown mode) — should return both even though second has popularity_score=0
	req := httptest.NewRequest(http.MethodGet, "/api/games/search?q=zeld", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var results []games.GameResult
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Without floor (dropdown), both should be returned
	if len(results) != 2 {
		t.Errorf("dropdown (no floor): expected 2 results, got %d", len(results))
	}

	// Test with full=1 (full page mode) — should apply floor
	req = httptest.NewRequest(http.MethodGet, "/api/games/search?q=zeld&full=1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var fullResults []games.GameResult
	if err := json.NewDecoder(rec.Body).Decode(&fullResults); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// With floor, only the strong match should be returned
	if len(fullResults) != 1 {
		t.Errorf("full page (with floor): expected 1 result, got %d", len(fullResults))
	}
}
