package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cato/internal/auth"
)

// postLibraryItem sends an authenticated POST to /api/library/{gameID}.
func postLibraryItem(t *testing.T, mux *http.ServeMux, db auth.Querier, sessionID string, gameID int, body string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/library/"+strconv.Itoa(gameID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	session, _ := auth.GetSession(db, sessionID)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

// patchLibraryItem sends an authenticated PATCH to /api/library/{gameID} and
// returns the recorder plus the decoded response body.
func patchLibraryItem(t *testing.T, mux *http.ServeMux, db auth.Querier, sessionID string, gameID int, body string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/library/"+strconv.Itoa(gameID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	session, _ := auth.GetSession(db, sessionID)
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestPatchUpdatesOnlyProvidedFields(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	postBody := `{"status":"backlog","rating":85,"playtime_minutes":120,"tags":["rpg","co-op"],"notes":"Great so far"}`
	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, postBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d %s", rec.Code, rec.Body.String())
	}

	rec, resp := patchLibraryItem(t, mux, database, sessionID, 1, `{"status":"playing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if resp["status"] != "playing" {
		t.Errorf("expected status 'playing' in response, got %v", resp["status"])
	}

	var status, tagsJSON, notes string
	var rating, playtime int64
	var startedAt *string
	err := database.QueryRow(`SELECT status, rating, playtime_minutes, tags_json, notes, started_at
		FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).
		Scan(&status, &rating, &playtime, &tagsJSON, &notes, &startedAt)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "playing" {
		t.Errorf("status = %q, want playing", status)
	}
	if rating != 85 {
		t.Errorf("rating = %d, want 85 (preserved)", rating)
	}
	if playtime != 120 {
		t.Errorf("playtime = %d, want 120 (preserved)", playtime)
	}
	if tagsJSON != `["rpg","co-op"]` {
		t.Errorf("tags_json = %s, want preserved", tagsJSON)
	}
	if notes != "Great so far" {
		t.Errorf("notes = %q, want preserved", notes)
	}
	if startedAt == nil || *startedAt == "" {
		t.Error("started_at should be auto-set when patching into playing")
	}
}

func TestPatchPlaytimeDeltaAccumulates(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"playing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d", rec.Code)
	}

	for _, delta := range []int64{30, 60} {
		body := `{"playtime_delta_minutes":` + jsonNumber(int(delta)) + `}`
		rec, _ = patchLibraryItem(t, mux, database, sessionID, 1, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH delta %d failed: %d %s", delta, rec.Code, rec.Body.String())
		}
	}

	var playtime int64
	database.QueryRow(`SELECT playtime_minutes FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&playtime)
	if playtime != 90 {
		t.Errorf("playtime = %d, want 90 (30+60 accumulated)", playtime)
	}
}

func TestPatchPlaytimeDeltaClampsAtZero(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog","playtime_minutes":20}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d", rec.Code)
	}

	rec, _ = patchLibraryItem(t, mux, database, sessionID, 1, `{"playtime_delta_minutes":-999}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var playtime int64
	database.QueryRow(`SELECT playtime_minutes FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&playtime)
	if playtime != 0 {
		t.Errorf("playtime = %d, want clamped to 0", playtime)
	}
}

func TestPatchCanClearRatingAndNotes(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"completed","rating":90,"notes":"loved it"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d", rec.Code)
	}

	rec, _ = patchLibraryItem(t, mux, database, sessionID, 1, `{"rating":0,"notes":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var rating int64
	var notes string
	database.QueryRow(`SELECT rating, notes FROM library_items WHERE user_id = 'user-1' AND game_id = 1`).Scan(&rating, &notes)
	if rating != 0 {
		t.Errorf("rating = %d, want explicit clear to 0", rating)
	}
	if notes != "" {
		t.Errorf("notes = %q, want explicit clear", notes)
	}
}

func TestPatchClearCompletedAtThenReplayDoesNotResurrectIt(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"completed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d", rec.Code)
	}

	rec, resp := patchLibraryItem(t, mux, database, sessionID, 1, `{"completed_at":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if v, ok := resp["completed_at"]; !ok || v != nil {
		t.Errorf("completed_at = %v, want null in response", v)
	}
}

func TestPatchNotFoundForMissingItem(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := patchLibraryItem(t, mux, database, sessionID, 2, `{"status":"playing"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for game not in library, got %d", rec.Code)
	}
}

func TestPatchRejectsInvalidStatus(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d", rec.Code)
	}

	rec, _ = patchLibraryItem(t, mux, database, sessionID, 1, `{"status":"bogus"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid status, got %d", rec.Code)
	}
}

func TestPatchRejectsConflictingPlaytimeFields(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST setup failed: %d", rec.Code)
	}

	rec, _ = patchLibraryItem(t, mux, database, sessionID, 1,
		`{"playtime_minutes":60,"playtime_delta_minutes":30}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for conflicting playtime inputs, got %d", rec.Code)
	}
}

func TestPatchRequiresCSRF(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	req := httptest.NewRequest(http.MethodPatch, "/api/library/1", strings.NewReader(`{"status":"playing"}`))
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 without CSRF token, got %d", rec.Code)
	}
}
