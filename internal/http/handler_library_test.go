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
	var status string
	var rating, playtime int64
	database.QueryRow("SELECT status, rating, playtime_minutes FROM library_items WHERE user_id = 'user-1' AND game_id = 1").Scan(&status, &rating, &playtime)
	if status != "backlog" {
		t.Errorf("expected status 'backlog', got %q", status)
	}
	if rating != 85 {
		t.Errorf("expected rating 85, got %d", rating)
	}
	if playtime != 60 {
		t.Errorf("expected playtime 60, got %d", playtime)
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

	var status string
	var rating, playtime int64
	database.QueryRow("SELECT status, rating, playtime_minutes FROM library_items WHERE user_id = 'user-1' AND game_id = 1").Scan(&status, &rating, &playtime)
	if status != "playing" {
		t.Errorf("expected status 'playing', got %q", status)
	}
	if rating != 90 {
		t.Errorf("expected rating 90, got %d", rating)
	}
	if playtime != 120 {
		t.Errorf("expected playtime 120, got %d", playtime)
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
	}{
		{1, "backlog", 80},
		{2, "playing", 90},
	} {
		body := fmt.Sprintf(`{"status":"%s","rating":%d,"playtime_minutes":0,"tags":[],"notes":""}`, g.status, g.rating)
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
