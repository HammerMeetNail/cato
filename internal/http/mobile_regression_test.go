package http

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readStaticFile reads a repo file from either the package directory or
// the repository root.
func readStaticFile(t *testing.T, relPath string) string {
	t.Helper()
	for _, p := range []string{filepath.Join("..", "..", relPath), relPath} {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
	}
	t.Fatalf("could not read %s", relPath)
	return ""
}

// TestServiceWorkerVersionIsAutomatic: the worker's cache is named from
// the server-stamped asset version (internal/http/assets.go), so shipped
// asset changes never depend on a hand-bumped CACHE_NAME again.
func TestServiceWorkerVersionIsAutomatic(t *testing.T) {
	content := readStaticFile(t, "web/static/service-worker.js")
	if !strings.Contains(content, `const VERSION = "__CATO_ASSET_VERSION__";`) || strings.Contains(content, "CACHE_NAME") {
		t.Fatalf("service-worker.js must use the stamped VERSION, not a manual CACHE_NAME")
	}
}

// The app pages keep what installability, first paint and the CSP rely on:
// the head scripts (theme before paint, --app-h), the asset marker, the
// manifest, and no inline scripts or handlers.
func TestAppPagesShellContract(t *testing.T) {
	inline := regexp.MustCompile(`<script(?:\s+type="module")?>|\son[a-z]+="|javascript:`)
	for _, page := range []string{"web/static/index.html", "web/static/login.html"} {
		html := readStaticFile(t, page)
		for _, want := range []string{`<script src="/js/theme-init.js">`, `<script src="/js/head-init.js">`, "<!-- assets -->",
			`<link rel="manifest" href="/manifest.webmanifest">`, `viewport-fit=cover`} {
			if !strings.Contains(html, want) {
				t.Errorf("%s lacks %s", page, want)
			}
		}
		if m := inline.FindString(html); m != "" {
			t.Errorf("%s has inline script or handler %q (blocked by the CSP)", page, m)
		}
	}
}
