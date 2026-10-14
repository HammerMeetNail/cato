package http

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cato/internal/auth"
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

func TestLibraryAddWakesCoverWorker(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	database.Exec(`UPDATE games SET cover_url = 'https://images.igdb.com/c.jpg' WHERE id = 1`)
	sessionID := createLibrarySession(t, database, "user-1")
	h := NewLibraryHandler(database, auth.Cookies{})
	woken := 0
	h.coverQueued = func() { woken++ }
	mux := http.NewServeMux()
	h.Register(mux)
	if rec, _ := postLibraryItem(t, mux, database, sessionID, 1, `{"status":"backlog"}`); rec.Code != http.StatusOK {
		t.Fatalf("POST %d", rec.Code)
	}
	if rec, _ := patchLibraryItem(t, mux, database, sessionID, 1, `{"rating":50}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH %d", rec.Code)
	}
	if woken != 2 {
		t.Fatalf("worker woken %d times", woken)
	}
}

// --- F5 / F6: library listing ------------------------------------------------------------

func listLibrary(t *testing.T, mux *http.ServeMux, sessionID, query string) (*httptest.ResponseRecorder, []map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/library?"+query, nil)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var items []map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &items)
	return rec, items
}

func TestLibraryKeysetPagination(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)
	for id := 10; id < 35; id++ {
		database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (?, ?, ?, ?)`, id, "G"+strconv.Itoa(id), "g"+strconv.Itoa(id), "g"+strconv.Itoa(id))
		// Several rows share one timestamp, so the game_id tie-breaker matters.
		database.Exec(`INSERT INTO library_items (user_id, game_id, status, updated_at) VALUES ('user-1', ?, 'backlog', ?)`,
			id, "2026-01-0"+strconv.Itoa(1+id%3)+"T00:00:00Z")
	}
	seen := map[float64]bool{}
	cursor := ""
	pages := 0
	for {
		q := "limit=10"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		rec, items := listLibrary(t, mux, sessionID, q)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", pages, rec.Code, rec.Body)
		}
		if pages == 0 && rec.Header().Get("X-Total-Count") != "25" {
			t.Fatalf("first page total = %q", rec.Header().Get("X-Total-Count"))
		}
		if pages > 0 && rec.Header().Get("X-Total-Count") != "" {
			t.Fatal("later pages must not pay for a COUNT")
		}
		for _, it := range items {
			id := it["game_id"].(float64)
			if seen[id] {
				t.Fatalf("game %v repeated across pages", id)
			}
			seen[id] = true
		}
		pages++
		cursor = rec.Header().Get("X-Next-Cursor")
		if rec.Header().Get("X-Has-More") != "true" {
			if cursor != "" {
				t.Fatal("cursor on the last page")
			}
			break
		}
		if cursor == "" || pages > 5 {
			t.Fatalf("missing cursor on page %d", pages)
		}
		// An edit between pages moves an already-seen item to the top; the
		// cursor still continues where the client left off.
		if pages == 1 {
			database.Exec(`UPDATE library_items SET updated_at = '2027-01-01T00:00:00Z' WHERE game_id = 34`)
		}
	}
	if len(seen) != 25 || pages != 3 {
		t.Fatalf("saw %d items over %d pages", len(seen), pages)
	}
	// Offset paging still works for other sorts, with the count on page 1 only.
	rec, items := listLibrary(t, mux, sessionID, "limit=10&offset=10&sort=name")
	if rec.Code != http.StatusOK || len(items) != 10 || rec.Header().Get("X-Total-Count") != "" {
		t.Fatalf("offset page: %d items=%d total=%q", rec.Code, len(items), rec.Header().Get("X-Total-Count"))
	}
	// A malformed cursor is rejected rather than silently restarting.
	if rec, _ := listLibrary(t, mux, sessionID, "cursor=bm9waXBl"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor = %d", rec.Code)
	}
}

func TestAvailablePlatformSortOnlyAggregatesTheLibrary(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)
	database.Exec(`INSERT INTO platforms (id, name) VALUES (6, 'PC (Microsoft Windows)'), (130, 'Nintendo Switch')`)
	database.Exec(`UPDATE games SET platforms_json = '[130]' WHERE id = 1`)
	database.Exec(`UPDATE games SET platforms_json = '[6]' WHERE id = 2`)
	for _, id := range []int{1, 2} {
		postLibraryItem(t, mux, database, sessionID, id, `{"status":"backlog"}`)
	}
	_, items := listLibrary(t, mux, sessionID, "sort=available_platform")
	if len(items) != 2 || items[0]["game_id"].(float64) != 1 {
		t.Fatalf("order = %v", items)
	}
	plan := explain(t, database, `SELECT li.game_id FROM library_items li JOIN games g ON g.id = li.game_id`+
		availablePlatformJoin+` WHERE li.user_id = ? ORDER BY ap.available_platform`, "user-1", "user-1")
	if strings.Contains(plan, "SCAN gp") {
		t.Fatalf("available-platform aggregate scans every game's platforms:\n%s", plan)
	}
}

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

// F5: suggestions walk an index in ORDER BY order instead of sorting the catalog.
func TestSuggestionsQueryUsesIndexOrder(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	plan := explain(t, database, suggestionsQuery, "user-1", 8)
	if strings.Contains(plan, "TEMP B-TREE") || !strings.Contains(plan, "idx_games_suggest") {
		t.Fatalf("suggestions sort the catalog:\n%s", plan)
	}
}

// --- F10: cacheable catalog responses -------------------------------------------------------

func TestCatalogResponsesAreCacheable(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO platforms (id, name, abbreviation) VALUES (130, 'Nintendo Switch', 'Switch')`)
	srv := NewServer(&config.Config{StaticDir: t.TempDir(), AllowInsecure: true}, database)
	for _, path := range []string{"/api/games/1", "/api/platforms?q=swi"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		etag := rec.Header().Get("ETag")
		if rec.Code != http.StatusOK || etag == "" || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=") || strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("%s: %d etag=%q cache=%q", path, rec.Code, etag, rec.Header().Get("Cache-Control"))
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", etag)
		req.Header.Set("Accept-Encoding", "gzip")
		again := httptest.NewRecorder()
		srv.Handler().ServeHTTP(again, req)
		if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s: revalidation = %d %q", path, again.Code, again.Header().Get("Content-Encoding"))
		}
	}
	// Personal API responses stay uncacheable.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("/api/me Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}
