package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cato/internal/config"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestRequestsAreLoggedWithRouteAndRequestID(t *testing.T) {
	logs := captureLogs(t)
	srv, _ := staticServer(t)
	rec := get(t, srv, "/healthz", nil)
	id := rec.Header().Get("X-Request-ID")
	if len(id) < 8 {
		t.Fatalf("X-Request-ID %q", id)
	}
	line := logs.String()
	for _, want := range []string{"msg=request", "id=" + id, "route=/healthz", "status=200"} {
		if !strings.Contains(line, want) {
			t.Errorf("log missing %q:\n%s", want, line)
		}
	}
}

func TestMetricsAccess(t *testing.T) {
	srv, _ := staticServer(t)
	get(t, srv, "/healthz", nil)
	scrape := func(remote, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = remote
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := scrape("203.0.113.9:1234", ""); rec.Code != http.StatusForbidden {
		t.Errorf("public scrape without token: %d", rec.Code)
	}
	rec := scrape("10.0.0.5:1234", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("private-network scrape: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`cato_http_requests_total{route="/healthz",code="200"} 1`, "cato_users 0", "cato_db_statement_seconds_count"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}

	tokened := newHTTPSTestServer(t, &config.Config{StaticDir: t.TempDir(), AllowInsecure: true, MetricsToken: "s3cret"})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec = httptest.NewRecorder()
	tokened.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("token configured, none sent: %d", rec.Code)
	}
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	tokened.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("right token: %d", rec.Code)
	}
}

func TestSlowQueriesAreLogged(t *testing.T) {
	logs := captureLogs(t)
	srv, _ := staticServer(t)
	srv.observeQuery("SELECT   *\n FROM games WHERE id = ?", 80*time.Millisecond)
	srv.observeQuery("SELECT 1", time.Millisecond)
	out := logs.String()
	if !strings.Contains(out, "slow query") || !strings.Contains(out, `sql="SELECT * FROM games WHERE id = ?"`) {
		t.Errorf("slow log:\n%s", out)
	}
	if strings.Count(out, "slow query") != 1 {
		t.Errorf("fast statement logged as slow")
	}
}

func TestHashUserIsStableAndShort(t *testing.T) {
	if hashUser("") != "" || hashUser("a") != hashUser("a") || len(hashUser("user-1")) != 8 || hashUser("a") == hashUser("b") {
		t.Error("hashUser")
	}
}
