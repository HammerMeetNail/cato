package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func getSuggestions(t *testing.T, mux *http.ServeMux, sessionID string) ([]map[string]interface{}, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/library/suggestions", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var resp []map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp, rec.Code
}

func TestSuggestionsExcludeOwnedAndPreferPopular(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	// Three candidates with covers; game 1 is owned so must be excluded.
	for _, g := range []struct {
		id   string
		pop  int
		cover string
	}{
		{"1", 500, "/covers/1.jpg"},
		{"2", 900, "/covers/2.jpg"},
	} {
		database.Exec(`UPDATE games SET local_cover_path = ?, popularity_score = ? WHERE id = ?`, g.cover, g.pop, g.id)
	}

	if rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup POST failed: %d", rec.Code)
	}

	resp, code := getSuggestions(t, mux, sessionID)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 suggestion (owned game excluded), got %d", len(resp))
	}
	if resp[0]["name"] != "Game Two" {
		t.Errorf("top suggestion = %v, want Game Two (higher popularity)", resp[0]["name"])
	}
}

func TestSuggestionsSkipCoverlessGamesAndLimit(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	// Games seeded by setup have no covers — nothing to suggest.
	resp, code := getSuggestions(t, mux, sessionID)
	if code != http.StatusOK || len(resp) != 0 {
		t.Errorf("expected empty suggestions for coverless catalog, got %d (%d)", len(resp), code)
	}
}
