package http

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cato/internal/auth"
	"cato/internal/catalog"
	"cato/internal/config"
	"cato/internal/db"
)

// --- S2: public search must not drive IGDB or grow the database -----------

type countingIGDB struct{ searches atomic.Int64 }

func (c *countingIGDB) SearchGames(ctx context.Context, q string, limit int, editions bool) ([]catalog.Game, error) {
	c.searches.Add(1)
	return []catalog.Game{{ID: 777, Name: "Remote " + q, Slug: "remote", CoverURL: "https://images.igdb.com/x.jpg"}}, nil
}
func (c *countingIGDB) GetGame(context.Context, int64) (*catalog.Game, error) { return nil, nil }
func (c *countingIGDB) GetGamesBatch(context.Context, []int64) ([]catalog.Game, error) {
	return nil, nil
}
func (c *countingIGDB) GetPlatforms(context.Context) ([]catalog.Platform, error) { return nil, nil }

func newSearchMux(t *testing.T, database *db.DB, cfg *config.Config, client catalog.IGDBClient) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	h := newGameHandlerWithClient(database, cfg, client)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.service.Shutdown(ctx)
	})
	h.Register(mux)
	return mux
}

func countRows(t *testing.T, database *db.DB, table string) int {
	t.Helper()
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnonymousSearchIsLocalOnly(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	client := &countingIGDB{}
	mux := newSearchMux(t, database, &config.Config{}, client)
	for _, q := range []string{"zelda", "metroid", "zelda&full=1"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/games/search?q="+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("anonymous search %s: %d", q, rec.Code)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := client.searches.Load(); n != 0 {
		t.Fatalf("anonymous searches triggered %d IGDB calls", n)
	}
	for _, table := range []string{"igdb_query_cache", "cover_jobs"} {
		if n := countRows(t, database, table); n != 0 {
			t.Fatalf("anonymous search wrote %d %s rows", n, table)
		}
	}
	if n := countRows(t, database, "games"); n != 2 {
		t.Fatalf("anonymous search grew the catalog to %d games", n)
	}

	// A signed-in search still refreshes from IGDB.
	sessionID := createLibrarySession(t, database, "user-1")
	req := httptest.NewRequest(http.MethodGet, "/api/games/search?q=zelda", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	mux.ServeHTTP(httptest.NewRecorder(), req)
	deadline := time.Now().Add(5 * time.Second)
	for client.searches.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if client.searches.Load() != 1 {
		t.Fatal("signed-in search did not refresh from IGDB")
	}
}

func TestSearchQueryLengthIsCapped(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	mux := newSearchMux(t, database, &config.Config{}, &countingIGDB{})
	for _, extra := range []string{"", "&full=1"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/games/search?q="+strings.Repeat("a", 101)+extra, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "query_too_long") {
			t.Fatalf("101-char query: %d %s", rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/games/search?q="+strings.Repeat("a", 100), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("100-char query: %d", rec.Code)
	}
}

func TestSearchIsRateLimitedPerIP(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	mux := newSearchMux(t, database, &config.Config{SearchRateLimit: 3}, &countingIGDB{})
	get := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/games/search?q=zz", nil)
		req.RemoteAddr = ip + ":1234"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		if code := get("192.0.2.1"); code != http.StatusOK {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	if code := get("192.0.2.1"); code != http.StatusTooManyRequests {
		t.Fatalf("4th request = %d, want 429", code)
	}
	if code := get("192.0.2.2"); code != http.StatusOK {
		t.Fatalf("other IP limited: %d", code)
	}
}

// --- S3: signup switch -------------------------------------------------------

func signupStatus(t *testing.T, mux *http.ServeMux, email string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(`{"email":"`+email+`","password":"password123"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestSignupModes(t *testing.T) {
	for _, mode := range []string{"closed", "invite", ""} {
		database := setupAuthTestDB(t)
		mux := createTestMux(NewAuthHandler(database, &config.Config{Signup: mode}))
		// The first account on an empty instance can always be created.
		if code := signupStatus(t, mux, "owner@example.com"); code != http.StatusCreated {
			t.Fatalf("mode %q: first signup = %d", mode, code)
		}
		if code := signupStatus(t, mux, "second@example.com"); code != http.StatusForbidden {
			t.Fatalf("mode %q: second signup = %d, want 403", mode, code)
		}
		database.Close()
	}
	database := setupAuthTestDB(t)
	defer database.Close()
	mux := createTestMux(NewAuthHandler(database, &config.Config{Signup: config.SignupOpen}))
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if code := signupStatus(t, mux, email); code != http.StatusCreated {
			t.Fatalf("open signup %s = %d", email, code)
		}
	}
}

func TestClosedSignupBlocksNewGoogleAccounts(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO users (id, email) VALUES ('existing', 'owner@example.com')`)
	h := NewAuthHandler(database, &config.Config{Signup: config.SignupClosed})
	_, err := h.findOrCreateGoogleUser(&auth.GoogleUser{Sub: "g-1", Email: "new@example.com", EmailVerified: true})
	if err == nil || err != errSignupClosed {
		t.Fatalf("err = %v, want errSignupClosed", err)
	}
	if n := countRows(t, database, "users"); n != 1 {
		t.Fatalf("users = %d", n)
	}
}

func TestLoadSignupMode(t *testing.T) {
	t.Setenv("CATO_SIGNUP", "")
	if got := config.Load().Signup; got != config.SignupClosed {
		t.Fatalf("default signup = %q, want closed until invites exist", got)
	}
	t.Setenv("CATO_SIGNUP", "OPEN")
	if got := config.Load().Signup; got != config.SignupOpen {
		t.Fatalf("signup = %q", got)
	}
	t.Setenv("CATO_SIGNUP", "sometimes")
	t.Setenv("CATO_DEV_TLS", "1")
	if err := config.Load().Validate(); err == nil || !strings.Contains(err.Error(), "CATO_SIGNUP") {
		t.Fatalf("bad CATO_SIGNUP accepted: %v", err)
	}
}

// --- S4: per-account login backoff ----------------------------------------------

func loginStatus(t *testing.T, mux *http.ServeMux, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestLoginBacksOffPerAccount(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	h := NewAuthHandler(database, &config.Config{Signup: config.SignupOpen, AuthRateLimit: 1000})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.loginThrottle.Now = func() time.Time { return now }
	mux := createTestMux(h)
	signupTestUser(t, mux, "victim@example.com")
	for i := 0; i < 5; i++ {
		if rec := loginStatus(t, mux, "victim@example.com", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d", i, rec.Code)
		}
	}
	// Locked: even the right password waits, and the response says how long.
	rec := loginStatus(t, mux, "Victim@Example.com", "password123")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("locked login = %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Another account is unaffected (per account, not global).
	signupTestUser(t, mux, "other@example.com")
	if rec := loginStatus(t, mux, "other@example.com", "password123"); rec.Code != http.StatusOK {
		t.Fatalf("other account = %d", rec.Code)
	}
	// Unknown emails back off identically (no account enumeration).
	for i := 0; i < 5; i++ {
		loginStatus(t, mux, "ghost@example.com", "wrong-password")
	}
	if rec := loginStatus(t, mux, "ghost@example.com", "wrong-password"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("unknown email not throttled: %d", rec.Code)
	}
	now = now.Add(2 * time.Second)
	if rec := loginStatus(t, mux, "victim@example.com", "password123"); rec.Code != http.StatusOK {
		t.Fatalf("after backoff = %d", rec.Code)
	}
	// Success resets the counter.
	if rec := loginStatus(t, mux, "victim@example.com", "wrong-password"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after reset = %d", rec.Code)
	}
}

// --- S5: CSV formula injection and size limits ------------------------------------

func TestCSVExportNeutralizesFormulas(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	database.Exec(`UPDATE games SET name = '=HYPERLINK("http://evil")' WHERE id = 1`)
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)
	if rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog","notes":"+cmd|' /C calc'!A0","tags":["@sum","-1","\tx","ok"]}`); rec.Code != http.StatusOK {
		t.Fatalf("setup %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/library/export", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(records) != 2 {
		t.Fatalf("csv = %v %v", records, err)
	}
	row := records[1]
	if row[0] != `'=HYPERLINK("http://evil")` {
		t.Errorf("game = %q", row[0])
	}
	if row[10] != `'+cmd|' /C calc'!A0` {
		t.Errorf("notes = %q", row[10])
	}
	// Tags are trimmed on save; only the cell's first character matters.
	if row[9] != "'@sum; -1; x; ok" {
		t.Errorf("tags = %q", row[9])
	}
	for i, cell := range row {
		if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
			t.Errorf("cell %d starts with a formula character: %q", i, cell)
		}
	}
}

func TestLibraryFieldLimits(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)
	many := make([]string, 33)
	for i := range many {
		many[i] = `"t` + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"`
	}
	cases := []struct {
		body, code string
	}{
		{`{"status":"backlog","notes":"` + strings.Repeat("é", 10001) + `"}`, "invalid_notes"},
		{`{"status":"backlog","tags":[` + strings.Join(many, ",") + `]}`, "invalid_tags"},
		{`{"status":"backlog","tags":["` + strings.Repeat("y", 41) + `"]}`, "invalid_tags"},
	}
	for _, tc := range cases {
		rec, _ := postLibraryItem(t, mux, database, sessionID, 1, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("POST %s…: %d %s", tc.body[:40], rec.Code, rec.Body)
		}
	}
	// The limits themselves are allowed (characters, not bytes).
	ok := `{"status":"backlog","notes":"` + strings.Repeat("é", 10000) + `","tags":["` + strings.Repeat("ü", 40) + `"]}`
	if rec, _ := postLibraryItem(t, mux, database, sessionID, 1, ok); rec.Code != http.StatusOK {
		t.Fatalf("at-limit POST = %d %s", rec.Code, rec.Body)
	}
	// PATCH goes through the same validation.
	if rec, _ := patchLibraryItem(t, mux, database, sessionID, 1, `{"notes":"`+strings.Repeat("n", 10001)+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH over limit = %d", rec.Code)
	}
}

// --- S6: no directory listings -------------------------------------------------------

func TestStaticDirectoriesAreNotListed(t *testing.T) {
	static := t.TempDir()
	os.MkdirAll(filepath.Join(static, "js", "sub"), 0o755)
	os.WriteFile(filepath.Join(static, "index.html"), []byte("<!doctype html>home"), 0o644)
	os.WriteFile(filepath.Join(static, "js", "app.js"), []byte("console.log(1)"), 0o644)
	srv := newHTTPSTestServer(t, &config.Config{StaticDir: static, AllowInsecure: true})
	for path, want := range map[string]int{"/js/": 404, "/js": 404, "/js/sub/": 404, "/js/app.js": 200, "/": 200} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
		if strings.Contains(rec.Body.String(), "app.js</a>") {
			t.Errorf("GET %s listed the directory", path)
		}
	}
}

// --- S7: CSRF tokens compare exactly --------------------------------------------------

func TestCSRFTokenComparisonIsExact(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	session, _ := auth.GetSession(database, sessionID)
	mux := newTestLibraryMux(database)
	authMux := createTestMux(newTestAuthHandler(database))
	for _, token := range []string{strings.ToUpper(session.CSRFToken), session.CSRFToken[:len(session.CSRFToken)-1]} {
		req := httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(`{"status":"backlog"}`))
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
		req.Header.Set("X-CSRF-Token", token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("library POST with token %q… = %d", token[:6], rec.Code)
		}
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			req := httptest.NewRequest(method, "/api/me", strings.NewReader(`{"display_name":"x"}`))
			req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
			req.Header.Set("X-CSRF-Token", token)
			rec := httptest.NewRecorder()
			authMux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s /api/me with altered token = %d", method, rec.Code)
			}
		}
	}
}

// --- S8: no account enumeration -------------------------------------------------------

func TestSignupDoesNotRevealTakenEmail(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	mux := createTestMux(newTestAuthHandler(database))
	signupTestUser(t, mux, "taken@example.com")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(`{"email":"TAKEN@example.com","password":"password123"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := strings.ToLower(rec.Body.String())
	if rec.Code == http.StatusConflict || strings.Contains(body, "taken") || strings.Contains(body, "exists") {
		t.Fatalf("duplicate signup reveals the account: %d %s", rec.Code, body)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate signup = %d, want 400", rec.Code)
	}
}

func TestLoginComparesPasswordForUnknownAndDisabledAccounts(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	h := newTestAuthHandler(database)
	var compares atomic.Int64
	real := h.checkPassword
	h.checkPassword = func(pw, hash string) bool { compares.Add(1); return real(pw, hash) }
	mux := createTestMux(h)
	signupTestUser(t, mux, "known@example.com")
	signupTestUser(t, mux, "disabled@example.com")
	database.Exec(`UPDATE users SET disabled = 1 WHERE email = 'disabled@example.com'`)

	unknown := loginStatus(t, mux, "nobody@example.com", "password123")
	if compares.Load() != 1 {
		t.Fatalf("unknown email skipped bcrypt (%d compares)", compares.Load())
	}
	wrong := loginStatus(t, mux, "known@example.com", "wrong-password")
	if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown %d %s vs wrong password %d %s", unknown.Code, unknown.Body, wrong.Code, wrong.Body)
	}
	// A disabled account is only revealed to someone who knows its password.
	disabledWrong := loginStatus(t, mux, "disabled@example.com", "wrong-password")
	if disabledWrong.Code != wrong.Code || disabledWrong.Body.String() != wrong.Body.String() {
		t.Fatalf("disabled account revealed without password: %d %s", disabledWrong.Code, disabledWrong.Body)
	}
}

// --- S9: account deletion re-authenticates ---------------------------------------------

func deleteMe(t *testing.T, mux *http.ServeMux, cookie, csrfToken, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/me", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestDeleteAccountRequiresPassword(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	mux := createTestMux(newTestAuthHandler(database))
	cookie, csrfToken, _ := signupTestUser(t, mux, "leaver@example.com")
	if code := deleteMe(t, mux, cookie, csrfToken, `{"confirm":"DELETE"}`); code != http.StatusUnauthorized {
		t.Fatalf("delete without password = %d", code)
	}
	if code := deleteMe(t, mux, cookie, csrfToken, `{"confirm":"DELETE","password":"wrong-password"}`); code != http.StatusUnauthorized {
		t.Fatalf("delete with wrong password = %d", code)
	}
	if countRows(t, database, "users") != 1 {
		t.Fatal("account deleted without re-authentication")
	}
	if code := deleteMe(t, mux, cookie, csrfToken, `{"confirm":"DELETE","password":"password123"}`); code != http.StatusOK {
		t.Fatalf("delete with password = %d", code)
	}
	if countRows(t, database, "users") != 0 {
		t.Fatal("account not deleted")
	}
}

func TestDeleteOAuthAccountRequiresRecentSignIn(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	mux := createTestMux(newTestAuthHandler(database))
	database.Exec(`INSERT INTO users (id, email, google_subject) VALUES ('g-user', 'g@example.com', 'sub-1')`)
	session, err := auth.CreateSession(database, "g-user")
	if err != nil {
		t.Fatal(err)
	}
	database.Exec(`UPDATE sessions SET created_at = datetime('now', '-11 minutes')`)
	if code := deleteMe(t, mux, session.ID, session.CSRFToken, `{"confirm":"DELETE"}`); code != http.StatusUnauthorized {
		t.Fatalf("stale OAuth session deleted the account: %d", code)
	}
	database.Exec(`UPDATE sessions SET created_at = datetime('now', '-1 minutes')`)
	if code := deleteMe(t, mux, session.ID, session.CSRFToken, `{"confirm":"DELETE"}`); code != http.StatusOK {
		t.Fatalf("recent OAuth sign-in = %d", code)
	}
}

// --- S11: Google button and HTML callback errors ---------------------------------------------

func TestPublicConfigExposesGoogleAndSignup(t *testing.T) {
	for _, tc := range []struct {
		cfg    config.Config
		google bool
		signup bool
	}{
		{config.Config{}, false, true},
		{config.Config{GoogleKey: "k", GoogleSecret: "s", Signup: config.SignupOpen}, true, true},
	} {
		database := setupAuthTestDB(t)
		cfg := tc.cfg
		mux := createTestMux(NewAuthHandler(database, &cfg))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		resp := readJSON(t, rec.Body)
		if resp["google_enabled"] != tc.google || resp["signup_allowed"] != tc.signup {
			t.Errorf("config %+v → %v", tc.cfg, resp)
		}
		database.Close()
	}
	// Once an account exists, a closed instance reports signup as unavailable.
	database := setupAuthTestDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO users (id, email) VALUES ('u', 'u@example.com')`)
	mux := createTestMux(NewAuthHandler(database, &config.Config{Signup: config.SignupClosed}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if resp := readJSON(t, rec.Body); resp["signup_allowed"] != false || resp["signup"] != "closed" {
		t.Fatalf("closed config = %v", resp)
	}
}

func TestGoogleErrorsRenderHTML(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	for _, cfg := range []*config.Config{{}, {GoogleKey: "k", GoogleSecret: "s", BaseURL: "https://cato.example"}} {
		mux := createTestMux(NewAuthHandler(database, cfg))
		for _, path := range []string{"/api/auth/google/start", "/api/auth/google/callback?state=<script>x</script>&code=c"} {
			if cfg.GoogleKey != "" && strings.HasSuffix(path, "/start") {
				continue
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			ct := rec.Header().Get("Content-Type")
			body := rec.Body.String()
			if !strings.HasPrefix(ct, "text/html") || !strings.Contains(body, `href="/login"`) || strings.Contains(body, `{"error"`) {
				t.Errorf("%s: %d %s %s", path, rec.Code, ct, body)
			}
			if strings.Contains(body, "<script>x") {
				t.Errorf("%s: unescaped input in error page", path)
			}
		}
	}
}
