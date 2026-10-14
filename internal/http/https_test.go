package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cato/internal/config"
	"cato/internal/devtls"
)

func newHTTPSTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	database := setupAuthTestDB(t)
	t.Cleanup(func() { database.Close() })
	if cfg.StaticDir == "" {
		cfg.StaticDir = t.TempDir()
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	return NewServer(cfg, database)
}

func TestHTTPSModeSecurityHeaders(t *testing.T) {
	cases := []struct {
		name      string
		cfg       config.Config
		wantHSTS  bool
		wantUpgr  bool
		wantPerms bool
	}{
		{"https base url", config.Config{BaseURL: "https://cato.example"}, true, true, true},
		{"native tls", config.Config{TLSCertFile: "c", TLSKeyFile: "k"}, true, true, true},
		// Self-signed dev certificates skip HSTS: it is per host, not per
		// port, so it would force https on every other localhost project.
		{"dev tls", config.Config{DevTLS: true}, false, true, true},
		{"insecure test mode", config.Config{AllowInsecure: true}, false, false, true},
	}
	for _, tc := range cases {
		cfg := tc.cfg
		srv := newHTTPSTestServer(t, &cfg)
		for _, path := range []string{"/healthz", "/api/me", "/"} {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			h := rec.Header()
			if got := h.Get("Strict-Transport-Security"); (got == "max-age=31536000") != tc.wantHSTS {
				t.Errorf("%s %s: HSTS = %q, want present=%v", tc.name, path, got, tc.wantHSTS)
			}
			if strings.Contains(h.Get("Strict-Transport-Security"), "preload") {
				t.Errorf("%s: HSTS must not opt into preload by default", tc.name)
			}
			if got := strings.Contains(h.Get("Content-Security-Policy"), "upgrade-insecure-requests"); got != tc.wantUpgr {
				t.Errorf("%s %s: upgrade-insecure-requests = %v, want %v", tc.name, path, got, tc.wantUpgr)
			}
			pp := h.Get("Permissions-Policy")
			if tc.wantPerms && (!strings.Contains(pp, "camera=(self)") || !strings.Contains(pp, "microphone=()") || !strings.Contains(pp, "geolocation=()")) {
				t.Errorf("%s %s: Permissions-Policy = %q", tc.name, path, pp)
			}
		}
	}
}

func TestHTTPSModeUsesHostPrefixedSessionCookie(t *testing.T) {
	srv := newHTTPSTestServer(t, &config.Config{BaseURL: "https://cato.example"})
	body := bytes.NewBufferString(`{"email":"host@example.com","password":"correct-password-1"}`)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/signup", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup = %d %s", rec.Code, rec.Body)
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "cato_session" {
			t.Fatal("legacy cookie name issued in HTTPS mode")
		}
		if c.Name == "__Host-cato_session" {
			session = c
		}
	}
	if session == nil || !session.Secure || session.Path != "/" || session.Domain != "" || !session.HttpOnly {
		t.Fatalf("session cookie = %+v", session)
	}

	me := func(name string) bool {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(&http.Cookie{Name: name, Value: session.Value})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return strings.Contains(rec.Body.String(), `"authenticated":true`)
	}
	if !me("__Host-cato_session") {
		t.Fatal("__Host- session not accepted")
	}
	if me("cato_session") {
		t.Fatal("unprefixed cookie accepted in HTTPS mode")
	}
	// The library API (AuthRequired) reads the same cookie.
	req := httptest.NewRequest(http.MethodGet, "/api/library", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("library with __Host- cookie = %d", rec.Code)
	}
}

func writeDevCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	certFile, keyFile, err := devtls.Ensure(t.TempDir(), []string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func tlsClient(t *testing.T, certFile string, h2 bool) *http.Client {
	t.Helper()
	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("bad cert")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: h2}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{
		Transport:     tr,
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func startServer(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Start() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if err := <-done; err != nil && err != http.ErrServerClosed {
			t.Errorf("Start: %v", err)
		}
	})
}

func TestNativeTLSServesHTTP2WithModernTLS(t *testing.T) {
	certFile, keyFile := writeDevCert(t)
	srv := newHTTPSTestServer(t, &config.Config{TLSCertFile: certFile, TLSKeyFile: keyFile})
	startServer(t, srv)
	base := "https://" + srv.Addr().String()

	resp, err := tlsClient(t, certFile, true).Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 {
		t.Fatalf("status=%d proto=%s, want 200 over HTTP/2", resp.StatusCode, resp.Proto)
	}
	if resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("TLS state = %+v", resp.TLS)
	}
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing on native TLS")
	}

	// TLS 1.1 clients are refused.
	old := &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS11}
	if conn, err := tls.Dial("tcp", srv.Addr().String(), old); err == nil {
		conn.Close()
		t.Fatal("TLS 1.1 handshake accepted")
	}

	// Plain HTTP on the TLS port gets no application response.
	plain := &http.Client{Timeout: 5 * time.Second}
	if r, err := plain.Get("http://" + srv.Addr().String() + "/healthz"); err == nil {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode == http.StatusOK || strings.Contains(string(b), `"status"`) {
			t.Fatal("plain HTTP served application content on the TLS port")
		}
	}
}

func TestNativeTLSReloadsRotatedCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := devtls.Ensure(dir, []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPSTestServer(t, &config.Config{TLSCertFile: certFile, TLSKeyFile: keyFile})
	srv.certReloadInterval = 0
	startServer(t, srv)
	serial := func() string {
		conn, err := tls.Dial("tcp", srv.Addr().String(), &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
	}
	first := serial()
	// A renewed certificate (new name forces regeneration) with a newer mtime.
	if _, _, err := devtls.Ensure(dir, []string{"127.0.0.1", "renewed.test"}); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	os.Chtimes(certFile, future, future)
	os.Chtimes(keyFile, future, future)
	if second := serial(); second == first {
		t.Fatal("rotated certificate was not picked up")
	}
}

func TestTLSListenerRejectsMissingCertificate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.pem")
	srv := newHTTPSTestServer(t, &config.Config{TLSCertFile: missing, TLSKeyFile: missing})
	if err := srv.Listen(); err == nil {
		t.Fatal("Listen accepted a missing certificate")
	}
}

func TestHTTPRedirectListener(t *testing.T) {
	certFile, keyFile := writeDevCert(t)
	cases := []struct {
		name string
		cfg  config.Config
		want func(srv *Server) string
	}{
		{
			"derives https origin from host",
			config.Config{TLSCertFile: certFile, TLSKeyFile: keyFile, HTTPRedirectAddr: "127.0.0.1:0"},
			func(srv *Server) string {
				_, port, _ := strings.Cut(srv.Addr().String(), ":")
				return "https://localhost:" + port + "/library?x=1"
			},
		},
		{
			"uses base url",
			config.Config{TLSCertFile: certFile, TLSKeyFile: keyFile, HTTPRedirectAddr: "127.0.0.1:0", BaseURL: "https://cato.example/"},
			func(*Server) string { return "https://cato.example/library?x=1" },
		},
	}
	for _, tc := range cases {
		cfg := tc.cfg
		srv := newHTTPSTestServer(t, &cfg)
		startServer(t, srv)
		client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, _ := http.NewRequest(method, "http://"+srv.RedirectAddr().String()+"/library?x=1", nil)
			req.Host = "localhost:9999"
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusPermanentRedirect {
				t.Fatalf("%s %s: status %d, want 308", tc.name, method, resp.StatusCode)
			}
			if got := resp.Header.Get("Location"); got != tc.want(srv) {
				t.Fatalf("%s: Location = %q, want %q", tc.name, got, tc.want(srv))
			}
			if resp.Header.Get("Set-Cookie") != "" {
				t.Fatal("redirect listener must not set cookies")
			}
		}
		// The redirect listener never serves the application.
		resp, err := client.Get("http://" + srv.RedirectAddr().String() + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusPermanentRedirect {
			t.Fatalf("%s: /healthz on redirect listener = %d", tc.name, resp.StatusCode)
		}
	}
}

func TestHTTPRedirectRejectsBadHost(t *testing.T) {
	certFile, keyFile := writeDevCert(t)
	srv := newHTTPSTestServer(t, &config.Config{TLSCertFile: certFile, TLSKeyFile: keyFile, HTTPRedirectAddr: "127.0.0.1:0"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.example/@x"
	srv.redirectHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad host = %d, want 400", rec.Code)
	}
}

func TestTrustedProxySeparatesRateLimitBuckets(t *testing.T) {
	trusted, _ := config.ParseTrustedProxies("10.0.0.0/8")
	srv := newHTTPSTestServer(t, &config.Config{BaseURL: "https://cato.example", AuthRateLimit: 1, TrustedProxies: trusted})
	login := func(remote, xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"x@example.com","password":"wrong-password"}`))
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if login("10.0.0.2:1", "198.51.100.1") == http.StatusTooManyRequests ||
		login("10.0.0.2:1", "198.51.100.2") == http.StatusTooManyRequests {
		t.Fatal("clients behind the trusted proxy shared one bucket")
	}
	if login("10.0.0.2:1", "198.51.100.1") != http.StatusTooManyRequests {
		t.Fatal("repeat client behind proxy was not limited")
	}
	// An untrusted peer cannot rotate identities with the header.
	if login("203.0.113.5:1", "198.51.100.50") == http.StatusTooManyRequests {
		t.Fatal("first untrusted request limited")
	}
	if login("203.0.113.5:1", "198.51.100.51") != http.StatusTooManyRequests {
		t.Fatal("untrusted peer bypassed the limiter with X-Forwarded-For")
	}
}

func TestGoogleRedirectURLUsesHTTPSWithoutBaseURL(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	h := NewAuthHandler(database, &config.Config{ListenAddr: ":7080", DevTLS: true, GoogleKey: "k", GoogleSecret: "s"})
	if got := h.googleCfg.RedirectURL; got != "https://localhost:7080/api/auth/google/callback" {
		t.Fatalf("RedirectURL = %q", got)
	}
}

func TestGoogleStateCookieInHTTPSMode(t *testing.T) {
	database := setupAuthTestDB(t)
	defer database.Close()
	h := NewAuthHandler(database, &config.Config{GoogleKey: "k", GoogleSecret: "s", BaseURL: "https://cato.example"})
	mux := createTestMux(h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/google/start", nil))
	var state *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-cato_oauth_state" {
			state = c
		}
	}
	if state == nil || !state.Secure || state.Path != "/" {
		t.Fatalf("state cookie = %+v", state)
	}
	if h.googleCfg.RedirectURL != "https://cato.example/api/auth/google/callback" {
		t.Fatalf("RedirectURL = %q", h.googleCfg.RedirectURL)
	}
	// The legacy name is not accepted for the state check; the prefixed one
	// gets past it (to the missing-code check).
	for name, want := range map[string]string{"cato_oauth_state": "link expired", "__Host-cato_oauth_state": "didn&#39;t complete"} {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/google/callback?state="+state.Value, nil)
		req.AddCookie(&http.Cookie{Name: name, Value: state.Value})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: body %s, want %s", name, rec.Body, want)
		}
	}
}
