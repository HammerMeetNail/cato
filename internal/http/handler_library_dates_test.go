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

// setupDatesTestDB provisions a user + games and returns the DB, mux and
// session ID needed for library API tests.
func setupDatesTestDB(t *testing.T) (*db.DB, *http.ServeMux, string) {
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
	for i := int64(1); i <= 3; i++ {
		database.Exec(fmt.Sprintf(
			"INSERT INTO games (id, name, slug, normalized_name) VALUES (%d, 'Game %d', 'game-%d', 'game %d')", i, i, i, i))
	}
	sessionID := createLibrarySession(t, database, "user-1")
	return database, newTestLibraryMux(database), sessionID
}

// csrfFor looks up the CSRF token for a session (tests only).
func csrfFor(t *testing.T, database *db.DB, sessionID string) string {
	t.Helper()
	session, err := auth.GetSession(database, sessionID)
	if err != nil || session == nil {
		t.Fatalf("lookup session: %v", err)
	}
	return session.CSRFToken
}

// libReq performs an authenticated request against the library mux.
func libReq(mux *http.ServeMux, sessionID, csrf, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestLibraryUpdatePreservesTimestamps is the regression test for the
// data-loss bug where a UI-style edit sends no timestamps and the upsert
// used to write NULL over started_at/completed_at.
func TestLibraryUpdatePreservesTimestamps(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	// Seed directly so we control the timestamps.
	database.Exec(`INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes, started_at, completed_at)
		VALUES ('user-1', 1, 'completed', 90, 600, '[]', '', '2026-01-05T00:00:00Z', '2026-02-10T00:00:00Z')`)

	// UI-style edit: no timestamps at all.
	rec := libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1",
		`{"status":"completed","rating":95,"playtime_minutes":700,"tags":["done"],"notes":"great"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var startedAt, completedAt string
	err := database.QueryRow(
		"SELECT started_at, completed_at FROM library_items WHERE user_id='user-1' AND game_id=1",
	).Scan(&startedAt, &completedAt)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if startedAt != "2026-01-05T00:00:00Z" {
		t.Errorf("started_at was clobbered: got %q", startedAt)
	}
	if completedAt != "2026-02-10T00:00:00Z" {
		t.Errorf("completed_at was clobbered: got %q", completedAt)
	}
}

// TestLibraryAutoTracksDates covers the §2.1 auto-tracking: entering
// `playing` sets started_at when empty; entering `completed` sets
// completed_at when empty; existing values are never overwritten.
func TestLibraryAutoTracksDates(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	// New item straight into playing -> started_at auto-set.
	rec := libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1",
		`{"status":"playing","rating":0,"playtime_minutes":0,"tags":[],"notes":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var startedAt, completedAt interface{}
	database.QueryRow("SELECT started_at, completed_at FROM library_items WHERE user_id='user-1' AND game_id=1").Scan(&startedAt, &completedAt)
	if startedAt == nil {
		t.Error("expected started_at to be auto-set when adding a playing game")
	}

	// Transition into completed -> completed_at auto-set.
	rec = libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1",
		`{"status":"completed","rating":80,"playtime_minutes":120,"tags":[],"notes":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	database.QueryRow("SELECT started_at, completed_at FROM library_items WHERE user_id='user-1' AND game_id=1").Scan(&startedAt, &completedAt)
	if completedAt == nil {
		t.Error("expected completed_at to be auto-set on transition into completed")
	}

	// A later unrelated edit must not change either date.
	firstStarted := startedAt.(string)
	firstCompleted := completedAt.(string)
	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1",
		`{"status":"completed","rating":85,"playtime_minutes":130,"tags":[],"notes":"edited"}`)
	database.QueryRow("SELECT started_at, completed_at FROM library_items WHERE user_id='user-1' AND game_id=1").Scan(&startedAt, &completedAt)
	if startedAt.(string) != firstStarted || completedAt.(string) != firstCompleted {
		t.Errorf("auto-tracked dates changed on unrelated edit: %v/%v -> %v/%v",
			firstStarted, firstCompleted, startedAt, completedAt)
	}
}

// TestLibraryInvalidTimestampRejected covers §3.2: garbage timestamps must be
// rejected rather than stored.
func TestLibraryInvalidTimestampRejected(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	for _, bad := range []string{"not-a-date", "2026-13-45", "08/01/2026"} {
		body := fmt.Sprintf(`{"status":"backlog","rating":0,"playtime_minutes":0,"tags":[],"notes":"","completed_at":%q}`, bad)
		rec := libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("timestamp %q: expected 400, got %d", bad, rec.Code)
		}
	}

	var count int
	database.QueryRow("SELECT COUNT(*) FROM library_items WHERE user_id='user-1'").Scan(&count)
	if count != 0 {
		t.Errorf("expected no items created by rejected requests, got %d", count)
	}
}

// TestLibraryTimestampNormalization covers §2.2: accepted values are
// normalized to UTC RFC3339 regardless of input form.
func TestLibraryTimestampNormalization(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	cases := []struct{ input, want string }{
		{"2026-08-01", "2026-08-01T00:00:00Z"},
		{"2026-08-01T12:30:00+02:00", "2026-08-01T10:30:00Z"},
	}
	for _, tc := range cases {
		body := fmt.Sprintf(`{"status":"completed","rating":0,"playtime_minutes":0,"tags":[],"notes":"","completed_at":%q}`, tc.input)
		rec := libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/2", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("input %q: expected 200, got %d: %s", tc.input, rec.Code, rec.Body.String())
		}
		var stored string
		database.QueryRow("SELECT completed_at FROM library_items WHERE user_id='user-1' AND game_id=2").Scan(&stored)
		if stored != tc.want {
			t.Errorf("input %q: stored %q, want %q", tc.input, stored, tc.want)
		}
		database.Exec("DELETE FROM library_items WHERE user_id='user-1' AND game_id=2")
	}
}

// TestLibraryClearTimestampExplicitly verifies "" clears a timestamp while
// omitting the field preserves it.
func TestLibraryClearTimestampExplicitly(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	database.Exec(`INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes, completed_at)
		VALUES ('user-1', 3, 'completed', 0, 0, '[]', '', '2026-02-02T00:00:00Z')`)

	// Omit -> preserved.
	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/3",
		`{"status":"completed","rating":10,"playtime_minutes":0,"tags":[],"notes":""}`)
	var completed interface{}
	database.QueryRow("SELECT completed_at FROM library_items WHERE user_id='user-1' AND game_id=3").Scan(&completed)
	if completed == nil {
		t.Fatal("expected completed_at preserved when field omitted")
	}

	// Explicit "" -> cleared, even while staying in completed (auto-tracking
	// applies on transition into a status; an explicit clear always wins).
	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/3",
		`{"status":"completed","rating":10,"playtime_minutes":0,"tags":[],"notes":"","completed_at":""}`)
	database.QueryRow("SELECT completed_at FROM library_items WHERE user_id='user-1' AND game_id=3").Scan(&completed)
	if completed != nil {
		t.Errorf("expected completed_at NULL after explicit clear, got %v", completed)
	}

	// Explicit "" + leaving completed -> stays NULL.
	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/3",
		`{"status":"backlog","rating":10,"playtime_minutes":0,"tags":[],"notes":"","completed_at":""}`)
	database.QueryRow("SELECT completed_at FROM library_items WHERE user_id='user-1' AND game_id=3").Scan(&completed)
	if completed != nil {
		t.Errorf("expected completed_at NULL after explicit clear + status change, got %v", completed)
	}
}

// TestLibraryGetSingleItem covers the GET /api/library/{id} membership check.
func TestLibraryGetSingleItem(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	// Not present -> 404.
	rec := libReq(mux, sessionID, "", http.MethodGet, "/api/library/1", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for absent item, got %d", rec.Code)
	}

	// Add then fetch.
	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1",
		`{"status":"playing","rating":70,"playtime_minutes":60,"tags":["now"],"notes":"hi"}`)
	rec = libReq(mux, sessionID, "", http.MethodGet, "/api/library/1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var item map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&item)
	if item["status"] != "playing" || item["game_id"] != float64(1) {
		t.Errorf("unexpected item payload: %v", item)
	}
	if item["started_at"] == nil {
		t.Error("expected started_at in single-item payload (auto-tracked)")
	}
}

// TestLibraryCheckEndpoint covers GET /api/library/check?ids=...
func TestLibraryCheckEndpoint(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1",
		`{"status":"playing","rating":0,"playtime_minutes":0,"tags":[],"notes":""}`)

	rec := libReq(mux, sessionID, "", http.MethodGet, "/api/library/check?ids=1,2,999", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var items []struct {
		GameID int64  `json:"game_id"`
		Status string `json:"status"`
	}
	json.NewDecoder(rec.Body).Decode(&items)
	if len(items) != 1 || items[0].GameID != 1 || items[0].Status != "playing" {
		t.Errorf("expected [{1 playing}], got %+v", items)
	}
}

// TestLibraryCountsEndpoint covers GET /api/library/counts.
func TestLibraryCountsEndpoint(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()
	csrf := csrfFor(t, database, sessionID)

	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/1",
		`{"status":"playing","rating":0,"playtime_minutes":0,"tags":[],"notes":""}`)
	libReq(mux, sessionID, csrf, http.MethodPost, "/api/library/2",
		`{"status":"backlog","rating":0,"playtime_minutes":0,"tags":[],"notes":""}`)

	rec := libReq(mux, sessionID, "", http.MethodGet, "/api/library/counts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var counts map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&counts)
	if counts["all"] != float64(2) || counts["playing"] != float64(1) || counts["backlog"] != float64(1) {
		t.Errorf("unexpected counts: %v", counts)
	}
	if counts["wishlist"] != float64(0) {
		t.Errorf("expected all statuses present with zero defaults, got %v", counts)
	}
}

// TestLibraryListHasMoreHeader verifies X-Total-Count/X-Has-More exactness,
// including the exact-multiple edge case.
func TestLibraryListHasMoreHeader(t *testing.T) {
	database, mux, sessionID := setupDatesTestDB(t)
	defer database.Close()

	for i := int64(1); i <= 3; i++ {
		database.Exec(fmt.Sprintf(`INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes)
			VALUES ('user-1', %d, 'backlog', 0, 0, '[]', '')`, i))
	}

	rec := libReq(mux, sessionID, "", http.MethodGet, "/api/library?limit=3", "")
	if got := rec.Header().Get("X-Has-More"); got != "false" {
		t.Errorf("exact multiple of page size: X-Has-More=%s, want false", got)
	}
	if got := rec.Header().Get("X-Total-Count"); got != "3" {
		t.Errorf("X-Total-Count=%s, want 3", got)
	}

	rec = libReq(mux, sessionID, "", http.MethodGet, "/api/library?limit=2", "")
	if got := rec.Header().Get("X-Has-More"); got != "true" {
		t.Errorf("partial page: X-Has-More=%s, want true", got)
	}
}
