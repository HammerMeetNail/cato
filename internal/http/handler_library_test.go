package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cato/internal/auth"
	"cato/internal/db"

	_ "modernc.org/sqlite"
)

func setupLibraryTestDB(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.Exec("INSERT INTO users (id, email) VALUES ('user-1', 'test@test.com')")
	database.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Test Game', 'test-game', 'test game')")
	database.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (2, 'Game Two', 'game-two', 'game two')")
	return database
}

func createLibrarySession(t *testing.T, db *db.DB, userID string) string {
	t.Helper()
	session, err := auth.CreateSession(db, userID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session.ID
}

func newTestLibraryMux(db *db.DB) *http.ServeMux {
	mux := http.NewServeMux()
	handler := NewLibraryHandler(db)
	handler.Register(mux)
	return mux
}

func TestLibraryAddItem(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	body := `{"status":"backlog","rating":85,"playtime_minutes":60,"tags":["rpg"],"notes":"Great game"}`
	req := httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	session, _ := auth.GetSession(database, sessionID)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify item in DB
	var status, tagsJSON string
	var rating, playtime int64
	database.QueryRow("SELECT status, rating, playtime_minutes, tags_json FROM library_items WHERE user_id = 'user-1' AND game_id = 1").Scan(&status, &rating, &playtime, &tagsJSON)
	if status != "backlog" {
		t.Errorf("expected status 'backlog', got %q", status)
	}
	if rating != 85 {
		t.Errorf("expected rating 85, got %d", rating)
	}
	if playtime != 60 {
		t.Errorf("expected playtime 60, got %d", playtime)
	}
	if tagsJSON != `["rpg"]` {
		t.Errorf("expected tags [\"rpg\"], got %q", tagsJSON)
	}
}

func TestLibraryUpdateItem(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	// Add first
	body := `{"status":"backlog","rating":50,"playtime_minutes":0,"tags":[],"notes":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	session, _ := auth.GetSession(database, sessionID)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Update
	body = `{"status":"playing","rating":90,"playtime_minutes":120,"tags":["favorite"],"notes":"Updated notes"}`
	req = httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var status, tagsJSON string
	var rating, playtime int64
	database.QueryRow("SELECT status, rating, playtime_minutes, tags_json FROM library_items WHERE user_id = 'user-1' AND game_id = 1").Scan(&status, &rating, &playtime, &tagsJSON)
	if status != "playing" {
		t.Errorf("expected status 'playing', got %q", status)
	}
	if rating != 90 {
		t.Errorf("expected rating 90, got %d", rating)
	}
	if playtime != 120 {
		t.Errorf("expected playtime 120, got %d", playtime)
	}
	if tagsJSON != `["favorite"]` {
		t.Errorf("expected tags [\"favorite\"], got %q", tagsJSON)
	}
}

func TestLibraryDeleteItem(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	// Add
	body := `{"status":"backlog","rating":0,"playtime_minutes":0,"tags":[],"notes":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	session, _ := auth.GetSession(database, sessionID)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Delete
	req = httptest.NewRequest(http.MethodDelete, "/api/library/1", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var count int
	database.QueryRow("SELECT COUNT(*) FROM library_items WHERE user_id = 'user-1' AND game_id = 1").Scan(&count)
	if count != 0 {
		t.Error("expected item to be deleted")
	}
}

func TestLibraryList(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	// Add two items
	session, _ := auth.GetSession(database, sessionID)
	for _, g := range []struct {
		id     int
		status string
		rating int
		tags   string
	}{
		{1, "backlog", 80, `["rpg","ps5"]`},
		{2, "playing", 90, `[]`},
	} {
		body := fmt.Sprintf(`{"status":"%s","rating":%d,"playtime_minutes":0,"tags":%s,"notes":""}`, g.status, g.rating, g.tags)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/library/%d", g.id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	// List all
	req := httptest.NewRequest(http.MethodGet, "/api/library", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var items []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
	for _, item := range items {
		tags, _ := item["tags"].([]interface{})
		if item["game_id"] == float64(1) && len(tags) != 2 {
			t.Errorf("expected 2 tags for game 1, got %v", item["tags"])
		}
	}

	// Filter by status
	req = httptest.NewRequest(http.MethodGet, "/api/library?status=playing", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 1 {
		t.Errorf("expected 1 'playing' item, got %d", len(items))
	}
}

func TestLibraryInvalidStatus(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	body := `{"status":"invalid_status","rating":0,"playtime_minutes":0,"tags":[],"notes":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	session, _ := auth.GetSession(database, sessionID)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid status, got %d", rec.Code)
	}
}

func TestLibraryInvalidRating(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	body := `{"status":"backlog","rating":101,"playtime_minutes":0,"tags":[],"notes":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	session, _ := auth.GetSession(database, sessionID)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid rating, got %d", rec.Code)
	}
}

func TestLibraryUnauthenticated(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	mux := newTestLibraryMux(database)

	req := httptest.NewRequest(http.MethodGet, "/api/library", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated, got %d", rec.Code)
	}
}

func TestLibraryFilterByTag(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	session, _ := auth.GetSession(database, sessionID)
	mux := newTestLibraryMux(database)

	// Add items with different tags
	for _, g := range []struct {
		id     int
		status string
		tags   string
	}{
		{1, "backlog", `["ps5","rpg"]`},
		{2, "backlog", `["steam","rpg"]`},
	} {
		body := fmt.Sprintf(`{"status":"%s","rating":80,"playtime_minutes":0,"tags":%s,"notes":""}`, g.status, g.tags)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/library/%d", g.id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	// Filter by tag "ps5" — only game 1 should match
	req := httptest.NewRequest(http.MethodGet, "/api/library?tag=ps5", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var items []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 1 {
		t.Fatalf("expected 1 item for tag=ps5, got %d", len(items))
	}
	if items[0]["game_id"] != float64(1) {
		t.Errorf("expected game_id 1, got %v", items[0]["game_id"])
	}

	// Filter by tag "rpg" — both games match
	req = httptest.NewRequest(http.MethodGet, "/api/library?tag=rpg", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 2 {
		t.Fatalf("expected 2 items for tag=rpg, got %d", len(items))
	}

	// Combine tag + status filter
	req = httptest.NewRequest(http.MethodGet, "/api/library?tag=rpg&status=backlog", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 2 {
		t.Fatalf("expected 2 items for tag=rpg+status=backlog, got %d", len(items))
	}

	// Non-existent tag returns empty
	req = httptest.NewRequest(http.MethodGet, "/api/library?tag=xbox", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 0 {
		t.Errorf("expected 0 items for tag=xbox, got %d", len(items))
	}
}

func TestLibraryFilterByMultipleTags(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	// Add extra games for the multi-tag test
	database.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (3, 'Game Three', 'game-three', 'game three')")
	database.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (4, 'Game Four', 'game-four', 'game four')")
	sessionID := createLibrarySession(t, database, "user-1")
	session, _ := auth.GetSession(database, sessionID)
	mux := newTestLibraryMux(database)

	// Game 1: switch + steam
	// Game 2: switch only
	// Game 3: steam only
	// Game 4: neither
	for _, g := range []struct {
		id     int
		status string
		tags   string
	}{
		{1, "backlog", `["switch","steam"]`},
		{2, "backlog", `["switch"]`},
		{3, "backlog", `["steam"]`},
		{4, "backlog", `["ps5"]`},
	} {
		body := fmt.Sprintf(`{"status":"%s","rating":80,"playtime_minutes":0,"tags":%s,"notes":""}`, g.status, g.tags)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/library/%d", g.id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	var items []map[string]interface{}

	// OR: switch | steam — games 1, 2, 3 (default tag_op=and, so use tag_op=or)
	req := httptest.NewRequest(http.MethodGet, "/api/library?tag=switch&tag=steam&tag_op=or", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 3 {
		t.Fatalf("expected 3 items for switch OR steam, got %d", len(items))
	}

	// AND: switch & steam — only game 1
	req = httptest.NewRequest(http.MethodGet, "/api/library?tag=switch&tag=steam&tag_op=and", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 1 {
		t.Fatalf("expected 1 item for switch AND steam, got %d", len(items))
	}
	if items[0]["game_id"] != float64(1) {
		t.Errorf("expected game_id 1 for switch AND steam, got %v", items[0]["game_id"])
	}

	// AND: switch & ps5 — no games have both
	req = httptest.NewRequest(http.MethodGet, "/api/library?tag=switch&tag=ps5&tag_op=and", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 0 {
		t.Errorf("expected 0 items for switch AND ps5, got %d", len(items))
	}

	// OR: switch & ps5 — games 1, 2, 4
	req = httptest.NewRequest(http.MethodGet, "/api/library?tag=switch&tag=ps5&tag_op=or", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 3 {
		t.Fatalf("expected 3 items for switch OR ps5, got %d", len(items))
	}

	// Single tag still works (defaults to AND which is same as OR for single tag)
	req = httptest.NewRequest(http.MethodGet, "/api/library?tag=switch", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 2 {
		t.Fatalf("expected 2 items for tag=switch, got %d", len(items))
	}
}

func TestLibraryTagAutocomplete(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	session, _ := auth.GetSession(database, sessionID)
	mux := newTestLibraryMux(database)

	// Add items with different tags
	for _, g := range []struct {
		id     int
		status string
		tags   string
	}{
		{1, "backlog", `["ps5","ps4","rpg"]`},
		{2, "backlog", `["steam","switch"]`},
	} {
		body := fmt.Sprintf(`{"status":"%s","rating":80,"playtime_minutes":0,"tags":%s,"notes":""}`, g.status, g.tags)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/library/%d", g.id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	// Autocomplete "ps" — should return ps4 and ps5
	req := httptest.NewRequest(http.MethodGet, "/api/library/tags?q=ps", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var tags []string
	json.NewDecoder(rec.Body).Decode(&tags)
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags matching 'ps', got %d: %v", len(tags), tags)
	}
	if tags[0] != "ps4" || tags[1] != "ps5" {
		t.Errorf("expected [ps4 ps5], got %v", tags)
	}

	// Autocomplete "s" — should return steam and switch
	req = httptest.NewRequest(http.MethodGet, "/api/library/tags?q=s", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&tags)
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags matching 's', got %d", len(tags))
	}

	// Autocomplete "xyz" — should return empty
	req = httptest.NewRequest(http.MethodGet, "/api/library/tags?q=xyz", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	json.NewDecoder(rec.Body).Decode(&tags)
	if len(tags) != 0 {
		t.Errorf("expected 0 tags matching 'xyz', got %d", len(tags))
	}
}
