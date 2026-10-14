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
