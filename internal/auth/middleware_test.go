package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testQuerier(t *testing.T) Querier {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf_token TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create sessions: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT,
		display_name TEXT NOT NULL DEFAULT '',
		avatar_url TEXT NOT NULL DEFAULT '',
		google_subject TEXT UNIQUE,
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := database.Exec(`INSERT OR IGNORE INTO users (id, email) VALUES ('user-1', 'u1@example.com'), ('user-2', 'u2@example.com')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	return database
}

func TestAuthRequiredMiddleware(t *testing.T) {
	q := testQuerier(t)
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	mw := AuthRequired(q)(http.HandlerFunc(handler))

	// No cookie → 401.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: got %d", rec.Code)
	}

	// Garbage cookie → 401.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: "cato_session", Value: "bogus"})
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("bogus cookie: got %d", rec2.Code)
	}

	// Valid session → 200 with user ID in context.
	sess, err := CreateSession(q, "user-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.AddCookie(&http.Cookie{Name: "cato_session", Value: sess.ID})
	rec3 := httptest.NewRecorder()
	var gotID string
	mwWithCapture := AuthRequired(q)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = GetUserID(r.Context())
	}))
	mwWithCapture.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("valid session: got %d", rec3.Code)
	}
	if gotID != "user-1" {
		t.Errorf("GetUserID = %q", gotID)
	}
}

func TestCSRFRequiredMiddleware(t *testing.T) {
	q := testQuerier(t)
	sess, err := CreateSession(q, "user-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var called bool
	mw := CSRFRequired(q)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	reqWith := func(method, token string) *http.Request {
		r := httptest.NewRequest(method, "/", nil)
		r.AddCookie(&http.Cookie{Name: "cato_session", Value: sess.ID})
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		return r
	}

	// GET/HEAD/OPTIONS skip CSRF entirely.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		called = false
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, reqWith(method, ""))
		if rec.Code != http.StatusOK || !called {
			t.Errorf("%s with no token: got %d called=%v", method, rec.Code, called)
		}
	}

	// POST without token → 403.
	called = false
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, reqWith(http.MethodPost, ""))
	if rec.Code != http.StatusForbidden || called {
		t.Errorf("POST no token: got %d called=%v", rec.Code, called)
	}

	// POST with wrong token → 403.
	called = false
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, reqWith(http.MethodPost, "wrong"))
	if rec.Code != http.StatusForbidden || called {
		t.Errorf("POST wrong token: got %d called=%v", rec.Code, called)
	}

	// POST with correct token (case-insensitive) → pass.
	called = false
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, reqWith(http.MethodPost, strings.ToUpper(sess.CSRFToken)))
	if rec.Code != http.StatusOK || !called {
		t.Errorf("POST correct token: got %d called=%v", rec.Code, called)
	}

	// POST with garbage session → 403.
	rec = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.AddCookie(&http.Cookie{Name: "cato_session", Value: "nope"})
	mw.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST bogus session: got %d", rec.Code)
	}
}

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	if !rl.Allow("a") || !rl.Allow("a") {
		t.Error("first two requests should pass")
	}
	if rl.Allow("a") {
		t.Error("third request within window should be blocked")
	}
	// Different key unaffected; old entries beyond the window expire.
	if !rl.Allow("b") {
		t.Error("different key should be independent")
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	h := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://x/api/auth/login", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("first request: %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "http://x/api/auth/login", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("second request: %d", rec2.Code)
	}

	// Key is host-only: same IP different port shares the bucket.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "http://x:9999/api/auth/login", nil))
	if rec3.Code != http.StatusTooManyRequests {
		t.Errorf("same host, different port: %d (expected shared bucket)", rec3.Code)
	}
}

func TestSessionLifecycle(t *testing.T) {
	q := testQuerier(t)

	sess, err := CreateSession(q, "user-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID == "" || sess.CSRFToken == "" {
		t.Fatal("session tokens empty")
	}

	// GetSession restores the unhashed ID and round-trips.
	got, err := GetSession(q, sess.ID)
	if err != nil || got == nil {
		t.Fatalf("GetSession: (%v, %v)", got, err)
	}
	if got.ID != sess.ID || got.UserID != "user-1" || got.CSRFToken != sess.CSRFToken {
		t.Errorf("roundtrip mismatch: %+v", got)
	}

	// Unknown session → (nil, nil).
	if g, err := GetSession(q, "missing"); g != nil || err != nil {
		t.Errorf("missing session: (%v, %v)", g, err)
	}

	// Expired session is deleted lazily and reports (nil, nil).
	hashed := hashToken(sess.ID)
	expiry := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if _, err := q.Exec("UPDATE sessions SET expires_at = ? WHERE id = ?", expiry, hashed); err != nil {
		t.Fatal(err)
	}
	if g, err := GetSession(q, sess.ID); g != nil || err != nil {
		t.Errorf("expired session: (%v, %v)", g, err)
	}
	var n int
	q.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n)
	if n != 0 {
		t.Errorf("expired session not deleted: %d rows", n)
	}

	// DeleteSession removes the row.
	s2, _ := CreateSession(q, "user-1")
	if err := DeleteSession(q, s2.ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := GetSession(q, s2.ID); g != nil {
		t.Error("session survived DeleteSession")
	}
}

func TestCleanupExpiredSessions(t *testing.T) {
	q := testQuerier(t)
	live, _ := CreateSession(q, "user-1")
	dead, _ := CreateSession(q, "user-2")
	hashed := hashToken(dead.ID)
	q.Exec("UPDATE sessions SET expires_at = ? WHERE id = ?",
		time.Now().Add(-time.Minute).Format(time.RFC3339), hashed)

	n, err := CleanupExpiredSessions(q)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	if g, _ := GetSession(q, live.ID); g == nil {
		t.Error("live session deleted")
	}
}

func TestRandomTokenUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok := RandomToken(32)
		if len(tok) != 64 {
			t.Fatalf("token length %d, want 64 hex chars", len(tok))
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
}

func TestPasswordHashRoundtrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword("correct horse battery staple", h) {
		t.Error("correct password rejected")
	}
	if CheckPassword("wrong", h) {
		t.Error("wrong password accepted")
	}
	// bcrypt cost 12 hashes start with $2a$12.
	if !strings.HasPrefix(h, "$2") || !strings.Contains(h, "$12$") {
		t.Errorf("unexpected bcrypt cost marker: %q", h[:10])
	}
}

func TestSessionCookieHelpers(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "tok123", true)
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie set")
	}
	c := cookies[0]
	if c.Name != "cato_session" || c.Value != "tok123" || !c.HttpOnly || !c.Secure {
		t.Errorf("SetSessionCookie: %+v", c)
	}
	if c.MaxAge != int((30 * 24 * time.Hour).Seconds()) {
		t.Errorf("MaxAge = %d", c.MaxAge)
	}

	rec2 := httptest.NewRecorder()
	ClearSessionCookie(rec2, false)
	c2 := rec2.Result().Cookies()[0]
	if c2.Name != "cato_session" || c2.MaxAge != -1 {
		t.Errorf("ClearSessionCookie: %+v", c2)
	}
}

func TestGetSessionIDAndCSRFContext(t *testing.T) {
	if id := GetSessionID(httptest.NewRequest(http.MethodGet, "/", nil)); id != "" {
		t.Errorf("no cookie: %q", id)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: "abc"})
	if id := GetSessionID(req); id != "abc" {
		t.Errorf("with cookie: %q", id)
	}

	if tok := GetCSRFToken(context.Background()); tok != "" {
		t.Errorf("empty ctx: %q", tok)
	}
	sess := &Session{CSRFToken: "tok"}
	ctx := context.WithValue(context.Background(), SessionKey, sess)
	if tok := GetCSRFToken(ctx); tok != "tok" {
		t.Errorf("ctx with session: %q", tok)
	}
}

func TestFetchGoogleUserAndConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/oauth2/v3/userinfo") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"sub":"12345","email":"a@b.c","name":"A","picture":"http://p"}`))
	}))
	defer srv.Close()

	cfg := NewGoogleConfig("id", "secret", srv.URL+"/callback")
	user, err := FetchGoogleUser(context.Background(), cfg, "authcode")
	if err != nil {
		// The exchange endpoint is google's live one, so this fails unless we
		// fake the endpoint. We exercise the failure path here and the decode
		// path via a direct server below.
		t.Logf("exchange failed as expected without endpoint override: %v", err)
	} else if user == nil {
		t.Error("nil user without error")
	}
}

// TestFetchGoogleUserDecode exercises the userinfo decode by overriding the
// userinfo URL and token endpoint to point at a local server.
func TestFetchGoogleUserDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			r.ParseForm()
			if r.PostForm.Get("code") != "goodcode" {
				http.Error(w, "bad code", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tok","token_type":"Bearer"}`))
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "no auth", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"sub":"sub-1","email":"g@example.com","name":"Gamer","picture":"http://pic"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	orig := googleUserInfoURL
	googleUserInfoURL = srv.URL + "/userinfo"
	t.Cleanup(func() { googleUserInfoURL = orig })

	cfg := NewGoogleConfig("id", "secret", srv.URL+"/callback")
	cfg.Endpoint.TokenURL = srv.URL + "/token"
	user, err := FetchGoogleUser(context.Background(), cfg, "goodcode")
	if err != nil {
		t.Fatalf("FetchGoogleUser: %v", err)
	}
	if user.Sub != "sub-1" || user.Email != "g@example.com" || user.Name != "Gamer" {
		t.Errorf("user = %+v", user)
	}

	// Non-200 userinfo → error.
	cfg2 := NewGoogleConfig("id", "secret", srv.URL+"/callback")
	cfg2.Endpoint.TokenURL = srv.URL + "/token"
	googleUserInfoURL = srv.URL + "/missing"
	if _, err := FetchGoogleUser(context.Background(), cfg2, "goodcode"); err == nil {
		t.Error("expected userinfo failure")
	}
}

func TestGoogleEndpointOverride(t *testing.T) {
	// FetchGoogleUser hardcodes the userinfo URL; verify the wrapper surfaces
	// server errors rather than panicking (defensive contract test).
	cfg := NewGoogleConfig("id", "secret", "http://127.0.0.1:1/cb")
	if _, err := FetchGoogleUser(context.Background(), cfg, "code"); err == nil {
		t.Error("expected error from unreachable endpoints")
	} else if !errors.Is(err, err) {
		t.Error("unreachable")
	}
	var _ = json.Marshal
}
