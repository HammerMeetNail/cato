package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestSecureCookiesUseHostPrefix(t *testing.T) {
	c := Cookies{Secure: true}
	rec := httptest.NewRecorder()
	c.SetSession(rec, "tok")
	got := rec.Result().Cookies()[0]
	if got.Name != "__Host-cato_session" || !got.Secure || got.Path != "/" || got.Domain != "" || !got.HttpOnly || got.SameSite != http.SameSiteLaxMode {
		t.Fatalf("secure session cookie = %+v", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-cato_session", Value: "tok"})
	if id := c.SessionID(req); id != "tok" {
		t.Fatalf("SessionID = %q", id)
	}
	// A legacy/unprefixed cookie (e.g. planted by a sibling subdomain) is
	// never read in HTTPS mode.
	legacy := httptest.NewRequest(http.MethodGet, "/", nil)
	legacy.AddCookie(&http.Cookie{Name: "cato_session", Value: "planted"})
	if id := c.SessionID(legacy); id != "" {
		t.Fatalf("unprefixed cookie accepted in HTTPS mode: %q", id)
	}

	rec = httptest.NewRecorder()
	c.ClearSession(rec)
	cleared := rec.Result().Cookies()[0]
	if cleared.Name != "__Host-cato_session" || cleared.MaxAge != -1 || !cleared.Secure || cleared.Path != "/" {
		t.Fatalf("cleared cookie = %+v", cleared)
	}

	rec = httptest.NewRecorder()
	c.SetOAuthState(rec, "state")
	state := rec.Result().Cookies()[0]
	if state.Name != "__Host-cato_oauth_state" || !state.Secure || state.Path != "/" || state.MaxAge != 600 {
		t.Fatalf("oauth state cookie = %+v", state)
	}
}

func TestInsecureCookiesKeepLegacyNames(t *testing.T) {
	c := Cookies{}
	rec := httptest.NewRecorder()
	c.SetSession(rec, "tok")
	got := rec.Result().Cookies()[0]
	if got.Name != "cato_session" || got.Secure {
		t.Fatalf("insecure session cookie = %+v", got)
	}
	rec = httptest.NewRecorder()
	c.SetOAuthState(rec, "s")
	if state := rec.Result().Cookies()[0]; state.Name != "cato_oauth_state" || state.Path != "/api/auth/google" {
		t.Fatalf("insecure oauth cookie = %+v", state)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "cato_oauth_state", Value: "s"})
	if c.OAuthState(req) != "s" {
		t.Fatal("OAuthState did not read the cookie")
	}
}

func TestClientIPHonoursOnlyTrustedProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	cases := []struct {
		name, remote, xff, proto string
		wantIP                   string
		wantHTTPS                bool
	}{
		{"direct client ignores headers", "203.0.113.9:5000", "198.51.100.1", "https", "203.0.113.9", false},
		{"trusted proxy", "10.1.2.3:443", "198.51.100.7", "https", "198.51.100.7", true},
		{"spoofed left entries ignored", "10.1.2.3:443", "1.1.1.1, 198.51.100.7", "http", "198.51.100.7", false},
		{"proxy chain", "10.1.2.3:443", "198.51.100.7, 10.9.9.9", "https", "198.51.100.7", true},
		{"all trusted uses leftmost", "10.1.2.3:443", "10.4.4.4, 10.5.5.5", "", "10.4.4.4", false},
		{"garbage header falls back to peer", "10.1.2.3:443", "not-an-ip", "", "10.1.2.3", false},
		{"ipv6 trusted peer", "[::1]:443", "2001:db8::5", "https", "2001:db8::5", true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.remote
		if tc.xff != "" {
			req.Header.Set("X-Forwarded-For", tc.xff)
		}
		if tc.proto != "" {
			req.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		req = ResolveClient(req, trusted)
		if got := ClientIP(req); got != tc.wantIP {
			t.Errorf("%s: ClientIP = %q, want %q", tc.name, got, tc.wantIP)
		}
		if got := RequestIsHTTPS(req); got != tc.wantHTTPS {
			t.Errorf("%s: RequestIsHTTPS = %v, want %v", tc.name, got, tc.wantHTTPS)
		}
	}
	// Without ResolveClient, the peer address is used.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if got := ClientIP(req); got != "192.0.2.1" {
		t.Errorf("unresolved ClientIP = %q", got)
	}
}

func TestRateLimiterKeysOnResolvedClientIP(t *testing.T) {
	rl := NewRateLimiter(1, 60_000_000_000)
	h := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	send := func(client string) int {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "10.0.0.2:1000"
		req.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, ResolveClient(req, trusted))
		return rec.Code
	}
	if send("198.51.100.1") != http.StatusOK || send("198.51.100.2") != http.StatusOK {
		t.Fatal("distinct clients behind one proxy shared a bucket")
	}
	if send("198.51.100.1") != http.StatusTooManyRequests {
		t.Fatal("repeat client was not limited")
	}
}
