package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cato/internal/auth"
	"cato/internal/db"
)

// newAuthTestMux builds a mux with the auth handler registered (for /api/me).
func newAuthTestMux(database *db.DB) *http.ServeMux {
	return createTestMux(newTestAuthHandler(database))
}

func getJSON(t *testing.T, path string, mux *http.ServeMux, sessionID string) (map[string]interface{}, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp, rec.Code
}

func TestStatsEndpoint(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	database.Exec(`INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes,
		tags_json, platform, completed_at, started_at)
		VALUES
		('user-1', 1, 'completed', 90, 3600, '["rpg","co-op"]', 'PC', '2025-03-01T00:00:00Z', NULL),
		('user-1', 2, 'completed', 80, 1200, '["rpg"]', 'PC', '2026-02-15T00:00:00Z', '2026-01-10T00:00:00Z')`)

	resp, code := getJSON(t, "/api/library/stats", mux, sessionID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, resp)
	}

	if resp["total_games"].(float64) != 2 {
		t.Errorf("total_games = %v", resp["total_games"])
	}
	if resp["total_finished"].(float64) != 2 {
		t.Errorf("total_finished = %v", resp["total_finished"])
	}
	if resp["total_minutes"].(float64) != 4800 {
		t.Errorf("total_minutes = %v", resp["total_minutes"])
	}
	if resp["avg_rating"].(float64) != 85 {
		t.Errorf("avg_rating = %v, want 85 (mean of rated items)", resp["avg_rating"])
	}
	if resp["finished_this_year"].(float64) != 1 {
		t.Errorf("finished_this_year = %v, want 1 (only the 2026 completion)", resp["finished_this_year"])
	}

	byYear, _ := resp["by_year"].([]interface{})
	if len(byYear) != 2 {
		t.Fatalf("by_year = %v, want two entries (2025, 2026)", byYear)
	}
	firstYear := byYear[0].(map[string]interface{})
	if firstYear["year"] != "2026" || firstYear["count"].(float64) != 1 {
		t.Errorf("first by_year entry = %v, want 2026 x1 (desc order)", firstYear)
	}

	topTags, _ := resp["top_tags"].([]interface{})
	if len(topTags) != 2 {
		t.Fatalf("top_tags = %v, want 2 distinct tags", topTags)
	}
	tagEntry := topTags[0].(map[string]interface{})
	if tagEntry["tag"] != "rpg" || tagEntry["count"].(float64) != 2 {
		t.Errorf("top tag = %v, want rpg x2", tagEntry)
	}

	platforms, _ := resp["top_platforms"].([]interface{})
	if len(platforms) != 1 || platforms[0].(map[string]interface{})["platform"] != "PC" {
		t.Errorf("top_platforms = %v", platforms)
	}

	recent, _ := resp["recent"].([]interface{})
	if len(recent) != 2 {
		t.Errorf("recent = %v, want both items", recent)
	}
}

func TestStatsEmptyLibrary(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	resp, code := getJSON(t, "/api/library/stats", mux, sessionID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp["total_games"].(float64) != 0 {
		t.Errorf("total_games = %v, want 0", resp["total_games"])
	}
	if avg := resp["avg_rating"].(float64); avg != 0 {
		t.Errorf("avg_rating = %v, want 0 for empty library", avg)
	}
}

func TestUpdateDisplayName(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newAuthTestMux(database)

	patchMe := func(body string, withCSRF bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/me", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
		if withCSRF {
			session, _ := auth.GetSession(database, sessionID)
			req.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := patchMe(`{"display_name":"Dave"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["display_name"] != "Dave" {
		t.Errorf("display_name = %v, want Dave", resp["display_name"])
	}

	var stored string
	database.QueryRow(`SELECT display_name FROM users WHERE id = 'user-1'`).Scan(&stored)
	if stored != "Dave" {
		t.Errorf("stored display_name = %q", stored)
	}

	// CSRF must be enforced.
	rec = patchMe(`{"display_name":"Nope"}`, false)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 without CSRF token, got %d", rec.Code)
	}

	// Overlong names rejected.
	long := `{"display_name":"` + strings.Repeat("x", 65) + `"}`
	rec = patchMe(long, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for overlong display name, got %d", rec.Code)
	}
}
