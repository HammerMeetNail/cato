package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cato/internal/auth"
	"cato/internal/config"
	"cato/internal/db"

	_ "modernc.org/sqlite"
)

func setupAuthTestDB(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

func newTestAuthHandler(database *db.DB) *AuthHandler {
	cfg := &config.Config{
		ListenAddr:   ":7080",
		CookieSecure: false,
	}
	return NewAuthHandler(database, cfg)
}

func createTestMux(h *AuthHandler) *http.ServeMux {
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func readJSON(t *testing.T, body io.Reader) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	return result
}

func getCookie(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func TestSignup(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	handler := newTestAuthHandler(database)
	mux := createTestMux(handler)

	body := `{"email":"newuser@example.com","password":"password123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := readJSON(t, rec.Body)
	if resp["authenticated"] != true {
		t.Error("expected authenticated=true")
	}
	if resp["user_id"] == nil || resp["user_id"] == "" {
		t.Error("expected user_id")
	}

	cookie := getCookie(rec.Result(), "cato_session")
	if cookie == "" {
		t.Error("expected session cookie")
	}
}

func TestSignupDuplicateEmail(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	handler := newTestAuthHandler(database)
	mux := createTestMux(handler)

	body := `{"email":"dupe@example.com","password":"password123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first signup failed: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d", rec.Code)
	}
}

func TestSignupInvalidEmail(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	handler := newTestAuthHandler(database)
	mux := createTestMux(handler)

	tests := []struct {
		body string
		code int
	}{
		{`{"email":"","password":"password123"}`, http.StatusBadRequest},
		{`{"email":"notanemail","password":"password123"}`, http.StatusBadRequest},
		{`{"email":"user@example.com","password":"short"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tt.code {
			t.Errorf("body=%s: expected %d, got %d", tt.body, tt.code, rec.Code)
		}
	}
}

func TestLogin(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	// Signup first
	handler := newTestAuthHandler(database)
	mux := createTestMux(handler)

	body := `{"email":"login@example.com","password":"password123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup failed: %d", rec.Code)
	}

	// Now login
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := readJSON(t, rec.Body)
	if resp["authenticated"] != true {
		t.Error("expected authenticated=true")
	}

	cookie := getCookie(rec.Result(), "cato_session")
	if cookie == "" {
		t.Error("expected session cookie")
	}
}

func TestLoginInvalidCredentials(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	handler := newTestAuthHandler(database)
	mux := createTestMux(handler)

	body := `{"email":"nobody@example.com","password":"wrongpass"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	handler := newTestAuthHandler(database)
	mux := createTestMux(handler)

	// Signup to get a session
	body := `{"email":"logout@example.com","password":"password123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	sessionCookie := getCookie(rec.Result(), "cato_session")

	// Logout
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionCookie})
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Cookie should be cleared
	clearCookie := getCookie(rec.Result(), "cato_session")
	if clearCookie != "" {
		t.Error("expected cookie to be cleared")
	}
}

func TestMe(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	handler := newTestAuthHandler(database)
	mux := createTestMux(handler)

	// Without session
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	resp := readJSON(t, rec.Body)
	if resp["authenticated"] != false {
		t.Error("expected authenticated=false without session")
	}

	// With session
	body := `{"email":"meuser@example.com","password":"password123"}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	sessionCookie := getCookie(rec.Result(), "cato_session")
	csrfToken := readJSON(t, rec.Body)["csrf_token"].(string)

	req = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionCookie})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	resp = readJSON(t, rec.Body)
	if resp["authenticated"] != true {
		t.Error("expected authenticated=true with session")
	}
	if resp["csrf_token"] != csrfToken {
		t.Error("expected matching CSRF token")
	}
	if resp["email"] != "meuser@example.com" {
		t.Errorf("expected email=meuser@example.com, got %v", resp["email"])
	}
}

func TestCSRFRequired(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	// Simulate a protected endpoint with CSRF middleware
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := auth.GetUserID(r.Context())
		if userID == "" {
			writeJSON(w, http.StatusUnauthorized, errResp("unauthorized", "Not authenticated"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	})

	handler = auth.AuthRequired(database)(handler)
	handler = auth.CSRFRequired(database)(handler)

	// Insert user first, then create session
	database.Exec("INSERT INTO users (id, email) VALUES ('test-user', 'csrf@test.com')")
	session, err := auth.CreateSession(database, "test-user")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	t.Run("GET passes without CSRF", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: session.ID})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for GET without CSRF, got %d", rec.Code)
		}
	})

	t.Run("POST fails without CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: session.ID})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for POST without CSRF, got %d", rec.Code)
		}
	})

	t.Run("POST passes with valid CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: session.ID})
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for POST with valid CSRF, got %d", rec.Code)
		}
	})

	t.Run("POST fails with wrong CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: session.ID})
		req.Header.Set("X-CSRF-Token", "wrong-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for wrong CSRF, got %d", rec.Code)
		}
	})
}

func TestAuthRequired(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := auth.GetUserID(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"user_id": userID})
	})

	handler = auth.AuthRequired(database)(handler)

	t.Run("rejects without session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("accepts with valid session", func(t *testing.T) {
		database.Exec("INSERT OR IGNORE INTO users (id, email) VALUES ('u-auth', 'auth@test.com')")
		session, err := auth.CreateSession(database, "u-auth")
		if err != nil {
			t.Fatalf("failed to create session: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "cato_session", Value: session.ID})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})
}
