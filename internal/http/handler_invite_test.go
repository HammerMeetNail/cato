package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cato/internal/auth"
	"cato/internal/config"
	"cato/internal/social"
)

func post(mux http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestInviteSignupAndResetLink(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	h := NewAuthHandler(database, &config.Config{ListenAddr: ":7080", Signup: config.SignupInvite})
	mux := createTestMux(h)
	ctx := context.Background()
	soc := social.New(database, t.TempDir())

	// Bootstrap: the first account is allowed and becomes the admin.
	if rec := post(mux, "/api/auth/signup", `{"email":"owner@example.com","password":"password123"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first signup: %d %s", rec.Code, rec.Body)
	}
	var ownerID, role string
	database.QueryRow(`SELECT id, role FROM users WHERE email = 'owner@example.com'`).Scan(&ownerID, &role)
	if role != "admin" {
		t.Fatalf("first account role %q", role)
	}
	// Invite mode: no invite, no account.
	if rec := post(mux, "/api/auth/signup", `{"email":"x@example.com","password":"password123"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("signup without invite: %d", rec.Code)
	}
	inv, err := soc.CreateInvite(ctx, ownerID, "invite", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/config?invite="+inv.Token, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	cfg := readJSON(t, rec.Body)
	if cfg["invite_valid"] != true || cfg["signup"] != "invite" {
		t.Fatalf("config: %v", cfg)
	}
	if rec := post(mux, "/api/auth/signup", `{"email":"sam@example.com","password":"password123","invite":"`+inv.Token+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("signup with invite: %d %s", rec.Code, rec.Body)
	}
	if rec := post(mux, "/api/auth/signup", `{"email":"sam2@example.com","password":"password123","invite":"`+inv.Token+`"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("invite reuse: %d", rec.Code)
	}

	// Reset link: new password works, old one doesn't, link is single-use.
	var samID string
	database.QueryRow(`SELECT id FROM users WHERE email = 'sam@example.com'`).Scan(&samID)
	tok, err := soc.CreateResetLink(ctx, ownerID, samID)
	if err != nil {
		t.Fatal(err)
	}
	if rec := post(mux, "/api/auth/reset", `{"token":"`+tok+`","password":"short"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("weak reset password: %d", rec.Code)
	}
	if rec := post(mux, "/api/auth/reset", `{"token":"`+tok+`","password":"brandnew123"}`); rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if rec := post(mux, "/api/auth/reset", `{"token":"`+tok+`","password":"brandnew456"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("reset reuse: %d", rec.Code)
	}
	if rec := post(mux, "/api/auth/login", `{"email":"sam@example.com","password":"password123"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password: %d", rec.Code)
	}
	if rec := post(mux, "/api/auth/login", `{"email":"sam@example.com","password":"brandnew123"}`); rec.Code != http.StatusOK {
		t.Fatalf("new password: %d", rec.Code)
	}
}

func TestAvatarsNeedASessionAndCSPHasTrustedTypes(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	dir := t.TempDir()
	cfg := &config.Config{ListenAddr: ":0", DBPath: dir + "/cato.db", CoverDir: dir + "/covers", StaticDir: "../../web/static", AllowInsecure: true}
	srv := NewServer(cfg, database)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/avatars/x.jpg", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous avatar: %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "require-trusted-types-for 'script'") || !strings.Contains(csp, "trusted-types lit-html cato-sw") {
		t.Fatalf("CSP lacks Trusted Types: %s", csp)
	}
	database.Exec(`INSERT INTO users (id, email) VALUES ('u', 'u@example.com')`)
	s, _ := auth.CreateSession(database, "u")
	req := httptest.NewRequest(http.MethodGet, "/avatars/../cato.db", nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: s.ID})
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("path traversal served a file")
	}
}
