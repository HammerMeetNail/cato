package http

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cato/internal/config"
	"cato/internal/db"
)

// --- F1: compressed static assets -------------------------------------------------

func staticServer(t *testing.T) (*Server, string) {
	t.Helper()
	static := t.TempDir()
	os.MkdirAll(filepath.Join(static, "js"), 0o755)
	os.MkdirAll(filepath.Join(static, "css"), 0o755)
	os.WriteFile(filepath.Join(static, "index.html"), []byte("<!doctype html><title>Cato</title>"+strings.Repeat("<p>shell</p>", 200)), 0o644)
	os.WriteFile(filepath.Join(static, "login.html"), []byte("<!doctype html>"+strings.Repeat("<p>login</p>", 200)), 0o644)
	os.WriteFile(filepath.Join(static, "js", "app.js"), []byte(strings.Repeat("export const x = 1;\n", 500)), 0o644)
	os.WriteFile(filepath.Join(static, "css", "app.css"), []byte(strings.Repeat("body { color: red; }\n", 500)), 0o644)
	os.WriteFile(filepath.Join(static, "icon.png"), []byte("\x89PNG not really"), 0o644)
	return newHTTPSTestServer(t, &config.Config{StaticDir: static, AllowInsecure: true}), static
}

func get(t *testing.T, srv *Server, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestStaticTextAssetsAreGzipped(t *testing.T) {
	srv, static := staticServer(t)
	for _, path := range []string{"/js/app.js", "/css/app.css", "/", "/login"} {
		rec := get(t, srv, path, map[string]string{"Accept-Encoding": "gzip, deflate, br"})
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("%s: %d encoding=%q", path, rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") || rec.Header().Get("ETag") == "" {
			t.Fatalf("%s: vary=%q etag=%q", path, rec.Header().Get("Vary"), rec.Header().Get("ETag"))
		}
		if cl, _ := strconv.Atoi(rec.Header().Get("Content-Length")); cl != rec.Body.Len() {
			t.Fatalf("%s: Content-Length %d for %d bytes", path, cl, rec.Body.Len())
		}
		body := gunzip(t, rec.Body.Bytes())
		file := map[string]string{"/js/app.js": "js/app.js", "/css/app.css": "css/app.css", "/": "index.html", "/login": "login.html"}[path]
		want, _ := os.ReadFile(filepath.Join(static, file))
		if body != string(want) {
			t.Fatalf("%s: decompressed body differs", path)
		}
		if rec.Body.Len() >= len(want)/3 {
			t.Fatalf("%s: %d bytes compressed from %d", path, rec.Body.Len(), len(want))
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: Cache-Control %q", path, rec.Header().Get("Cache-Control"))
		}
		// Revalidation is a cheap 304.
		etag := rec.Header().Get("ETag")
		again := get(t, srv, path, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
		if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
			t.Fatalf("%s: revalidation = %d (%d bytes)", path, again.Code, again.Body.Len())
		}
	}
	// Clients without gzip get the identity representation, with a different ETag.
	plain := get(t, srv, "/js/app.js", nil)
	gz := get(t, srv, "/js/app.js", map[string]string{"Accept-Encoding": "gzip"})
	if plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("ETag") == gz.Header().Get("ETag") {
		t.Fatalf("identity: encoding=%q etag=%q gz etag=%q", plain.Header().Get("Content-Encoding"), plain.Header().Get("ETag"), gz.Header().Get("ETag"))
	}
	// Binary assets are not recompressed.
	if png := get(t, srv, "/icon.png", map[string]string{"Accept-Encoding": "gzip"}); png.Header().Get("Content-Encoding") != "" {
		t.Fatal("PNG was gzipped")
	}
}

// Range requests apply to the gzip representation consistently: a 206 with
// Content-Encoding gzip carries bytes of the gzip stream (the old
// gzipMiddleware produced gzip-labelled raw bytes).
func TestStaticGzipRangeIsConsistent(t *testing.T) {
	srv, _ := staticServer(t)
	full := get(t, srv, "/js/app.js", map[string]string{"Accept-Encoding": "gzip"})
	part := get(t, srv, "/js/app.js", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-9"})
	if part.Code != http.StatusPartialContent || part.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("range: %d %q", part.Code, part.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(part.Body.Bytes(), full.Body.Bytes()[:10]) {
		t.Fatal("range bytes are not a slice of the gzip representation")
	}
}

func TestStaticGzipPicksUpEditedFiles(t *testing.T) {
	srv, static := staticServer(t)
	first := get(t, srv, "/js/app.js", map[string]string{"Accept-Encoding": "gzip"})
	path := filepath.Join(static, "js", "app.js")
	os.WriteFile(path, []byte(strings.Repeat("export const y = 2;\n", 500)), 0o644)
	future := time.Now().Add(time.Minute)
	os.Chtimes(path, future, future)
	second := get(t, srv, "/js/app.js", map[string]string{"Accept-Encoding": "gzip"})
	if !strings.Contains(gunzip(t, second.Body.Bytes()), "const y") || first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Fatal("edited asset served stale")
	}
}

// --- F4: library writes wake the cover worker ----------------------------------------

// --- F5 / F6: library listing ------------------------------------------------------------

func explain(t *testing.T, database *db.DB, q string, args ...any) string {
	t.Helper()
	rows, err := database.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		rows.Scan(&id, &parent, &notused, &detail)
		b.WriteString(detail + "\n")
	}
	return b.String()
}

// --- F10: cacheable catalog responses -------------------------------------------------------
