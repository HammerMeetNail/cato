package v1

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestReloadReportIsLogged(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	rec := c.do(http.MethodPost, "/api/v1/diagnostics/reload", map[string]any{
		"nav_type": "reload", "since_nav_ms": 84, "clean_exit": false,
		"from": "#home", "to": "#discover", "uptime_ms": 1320,
		"persisted": true, "next_url": "/#discover", "redirects": 0,
		"trail": "1210 tap #discover; 1214 navigate push /#discover hash user; 1215 hashchange #discover; 1290 pagehide",
	}, "User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 26_1 like Mac OS X)")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := buf.String()
	for _, want := range []string{`msg="unexpected reload"`, "nav_type=reload", "since_nav_ms=84", "clean_exit=false", "to=#discover", "uptime_ms=1320", "iPhone OS 26_1",
		"persisted=true", "next_url=/#discover", "redirects=0", `trail="1210 tap #discover; 1214 navigate push /#discover hash user`} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}

	// Free text is bounded, and nonsense is refused.
	if rec := c.do(http.MethodPost, "/api/v1/diagnostics/reload", map[string]any{"nav_type": strings.Repeat("x", 5000)}); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized report: status %d", rec.Code)
	}
	if rec := c.do(http.MethodPost, "/api/v1/diagnostics/reload", map[string]any{"since_nav_ms": -5}); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative timing: status %d", rec.Code)
	}
	if rec := c.do(http.MethodPost, "/api/v1/diagnostics/reload", map[string]any{"trail": strings.Repeat("x", 5000)}); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized trail: status %d", rec.Code)
	}
}

func TestSwipeReportIsLogged(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	rec := c.do(http.MethodPost, "/api/v1/diagnostics/swipe", map[string]any{
		"outcome": "cancelled by the browser", "dx": 132, "dy": -18, "start_x": 60, "width": 402, "ms": 210,
	}, "User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) Version/27.0.1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := buf.String()
	for _, want := range []string{`msg="swipe gesture"`, `outcome="cancelled by the browser"`, "dx=132", "dy=-18", "start_x=60", "width=402", "ms=210", "Version/27.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if rec := c.do(http.MethodPost, "/api/v1/diagnostics/swipe", map[string]any{"outcome": strings.Repeat("x", 5000)}); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized report: status %d", rec.Code)
	}
}
