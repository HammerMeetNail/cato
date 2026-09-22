package http

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/oauth2"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	id1, err := h.findOrCreateGoogleUser(&auth.GoogleUser{EmailVerified: true, Sub: "sub-1", Email: "g1@example.com", Name: "G One"})
	if err != nil || id1 == "" {
		t.Fatalf("create: (%q, %v)", id1, err)
	}
	// Same subject again → same user (no duplicate).
	id2, err := h.findOrCreateGoogleUser(&auth.GoogleUser{EmailVerified: true, Sub: "sub-1", Email: "g1@example.com", Name: "G One"})
	if err != nil || id2 != id1 {
		t.Errorf("subject lookup: (%q, %v), want %q", id2, err, id1)
	}
	// A password account sharing the email must never be linked.
	if _, err := database.Exec(`INSERT INTO users (id, email, password_hash) VALUES ('plain-1', 'plain@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	id3, err := h.findOrCreateGoogleUser(&auth.GoogleUser{EmailVerified: true, Sub: "sub-plain", Email: "PLAIN@example.com"})
	if !errors.Is(err, errGoogleEmailConflict) || id3 != "" {
		t.Fatalf("collision: (%q, %v)", id3, err)
	}
	var linked int
	if err := database.QueryRow(`SELECT google_subject IS NOT NULL FROM users WHERE id = 'plain-1'`).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 0 {
		t.Fatal("password account was linked")
	}
	// An email already owned by a different Google subject also cannot merge.
	if _, err := h.findOrCreateGoogleUser(&auth.GoogleUser{EmailVerified: true, Sub: "other", Email: "g1@example.com"}); !errors.Is(err, errGoogleEmailConflict) {
		t.Fatalf("Google collision: %v", err)
	}
	for _, profile := range []*auth.GoogleUser{nil, {}, {EmailVerified: true, Email: "x@example.com"}, {Sub: "bad", Email: "x@example.com"}, {Sub: " ", Email: "x@example.com", EmailVerified: true}} {
		if _, err := h.findOrCreateGoogleUser(profile); err == nil {
			t.Fatalf("accepted invalid profile: %+v", profile)
		}
	}
	if _, err := database.Exec("UPDATE users SET disabled = 1 WHERE id = ?", id1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.findOrCreateGoogleUser(&auth.GoogleUser{EmailVerified: true, Sub: "sub-1", Email: "g1@example.com"}); !errors.Is(err, errGoogleDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	// Google user without email and unknown subject → error, not a blank row.
	if _, err := h.findOrCreateGoogleUser(&auth.GoogleUser{EmailVerified: true, Sub: "sub-noemail"}); err == nil {
		t.Error("expected error for email-less unknown Google user")
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ''`).Scan(&n)
	if n != 0 {
		t.Errorf("%d blank-email users created", n)
	}
}

type googleTestTransport struct{ target *url.URL }

func (t googleTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestGoogleCallbackAccountProtection(t *testing.T) {
	for _, tc := range []struct {
		name, profile, setup string
		status               int
		code                 string
	}{
		{"email_collision", `{"sub":"new-sub","email":"same@example.com","email_verified":true}`, `INSERT INTO users(id,email,password_hash) VALUES('existing','same@example.com','hash')`, 409, "email_taken"},
		{"disabled", `{"sub":"known-sub","email":"same@example.com","email_verified":true}`, `INSERT INTO users(id,email,google_subject,disabled) VALUES('existing','same@example.com','known-sub',1)`, 403, "account_disabled"},
		{"existing_subject", `{"sub":"known-sub","email":"changed@example.com","email_verified":true}`, `INSERT INTO users(id,email,google_subject) VALUES('existing','same@example.com','known-sub')`, 302, ""},
		{"unverified", `{"sub":"new-sub","email":"same@example.com","email_verified":false}`, "", 500, "google_error"},
		{"missing_subject", `{"email":"same@example.com","email_verified":true}`, "", 500, "google_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := setupAuthTestDB(t)
			defer database.Close()
			if tc.setup != "" {
				if _, err := database.Exec(tc.setup); err != nil {
					t.Fatal(err)
				}
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/token" {
					fmt.Fprint(w, `{"access_token":"tok","token_type":"Bearer"}`)
				} else {
					fmt.Fprint(w, tc.profile)
				}
			}))
			defer provider.Close()
			target, err := url.Parse(provider.URL)
			if err != nil {
				t.Fatal(err)
			}
			handler := newTestAuthHandler(database)
			handler.googleCfg = auth.NewGoogleConfig("id", "secret", provider.URL+"/callback")
			handler.googleCfg.Endpoint.TokenURL = provider.URL + "/token"
			handler.googleCfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
			req := httptest.NewRequest(http.MethodGet, "/api/auth/google/callback?state=state&code=code", nil)
			req = req.WithContext(context.WithValue(req.Context(), oauth2.HTTPClient, &http.Client{Transport: googleTestTransport{target}}))
			req.AddCookie(&http.Cookie{Name: "cato_oauth_state", Value: "state"})
			rec := httptest.NewRecorder()
			handler.handleGoogleCallback(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if tc.code != "" && !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("missing error %s: %s", tc.code, rec.Body.String())
			}
			cookie := getCookie(rec.Result(), "cato_session")
			if tc.status == 302 {
				session, err := auth.GetSession(database, cookie)
				if err != nil || session == nil || session.UserID != "existing" {
					t.Fatalf("existing subject login: %+v %v", session, err)
				}
			} else {
				if cookie != "" {
					t.Fatal("rejected callback issued cookie")
				}
				var count int
				if err := database.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatal("rejected callback issued session")
				}
			}
		})
	}
}
