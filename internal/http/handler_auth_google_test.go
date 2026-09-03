package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cato/internal/auth"
	"cato/internal/config"
)

// Google OAuth handlers were at 0% coverage despite guarding an
// authentication entry point (state validation, account linking). These
// tests pin the network-free paths: unconfigured 503s, state mismatch
// rejection, missing code, and the find-or-create linking logic.

func TestGoogleStartUnconfigured(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	h := newTestAuthHandler(database) // no GoogleKey/Secret → googleCfg nil
	mux := createTestMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/google/start", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("start unconfigured: got %d, want 503", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/google/callback?code=x&state=y", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("callback unconfigured: got %d, want 503", rec2.Code)
	}
}

func TestGoogleStartIssuesStateCookieAndRedirect(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	cfg := &config.Config{
		ListenAddr:   ":7080",
		CookieSecure: false,
		GoogleKey:    "test-key",
		GoogleSecret: "test-secret",
		BaseURL:      "http://example.com",
	}
	h := NewAuthHandler(database, cfg)
	if h.googleCfg == nil {
		t.Fatal("googleCfg not built despite key+secret")
	}
	mux := createTestMux(h)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/google/start", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("start: got %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "accounts.google.com") {
		t.Errorf("redirect not to Google: %q", loc)
	}
	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "cato_oauth_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("no cato_oauth_state cookie issued")
	}
	if !stateCookie.HttpOnly {
		t.Error("state cookie must be HttpOnly")
	}
	// The OAuth start must not clobber a logged-in session cookie.
	for _, c := range rec.Result().Cookies() {
		if c.Name == "cato_session" {
			t.Error("start overwrote cato_session (regression: logs out signed-in users)")
		}
	}
}

func TestGoogleCallbackRejectsBadStateAndMissingCode(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	cfg := &config.Config{
		ListenAddr: ":7080", GoogleKey: "k", GoogleSecret: "s",
		BaseURL: "http://example.com",
	}
	h := NewAuthHandler(database, cfg)
	mux := createTestMux(h)

	// State mismatch (attacker-forged or missing cookie) → 400.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/google/callback?code=abc&state=wrong", nil)
	req.AddCookie(&http.Cookie{Name: "cato_oauth_state", Value: "right"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad state: got %d, want 400", rec.Code)
	}

	// No state at all → 400.
	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/google/callback?code=abc&state=x", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("missing state cookie: got %d, want 400", rec2.Code)
	}

	// Valid state but missing code → 400 (before any network call).
	req3 := httptest.NewRequest(http.MethodGet, "/api/auth/google/callback?state=s", nil)
	req3.AddCookie(&http.Cookie{Name: "cato_oauth_state", Value: "s"})
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("missing code: got %d, want 400", rec3.Code)
	}
}

func TestFindOrCreateGoogleUser(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	h := newTestAuthHandler(database)

	// New Google user with fresh email → created.
	id1, err := h.findOrCreateGoogleUser(&auth.GoogleUser{Sub: "sub-1", Email: "g1@example.com", Name: "G One"})
	if err != nil || id1 == "" {
		t.Fatalf("create: (%q, %v)", id1, err)
	}
	// Same subject again → same user (no duplicate).
	id2, err := h.findOrCreateGoogleUser(&auth.GoogleUser{Sub: "sub-1", Email: "g1@example.com", Name: "G One"})
	if err != nil || id2 != id1 {
		t.Errorf("subject lookup: (%q, %v), want %q", id2, err, id1)
	}
	// Pre-existing password user with same email → linked, not duplicated.
	database.Exec(`INSERT INTO users (id, email, password_hash) VALUES ('plain-1', 'plain@example.com', 'x')`)
	id3, err := h.findOrCreateGoogleUser(&auth.GoogleUser{Sub: "sub-plain", Email: "plain@example.com", Name: "Plain"})
	if err != nil || id3 != "plain-1" {
		t.Errorf("email link: (%q, %v), want plain-1", id3, err)
	}
	var linked string
	database.QueryRow(`SELECT google_subject FROM users WHERE id = 'plain-1'`).Scan(&linked)
	if linked != "sub-plain" {
		t.Errorf("google_subject not linked: %q", linked)
	}
	// Google user without email and unknown subject → error, not a blank row.
	if _, err := h.findOrCreateGoogleUser(&auth.GoogleUser{Sub: "sub-noemail"}); err == nil {
		t.Error("expected error for email-less unknown Google user")
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ''`).Scan(&n)
	if n != 0 {
		t.Errorf("%d blank-email users created", n)
	}
}
