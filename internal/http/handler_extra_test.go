package http

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cato/internal/auth"
	"cato/internal/config"
	"cato/internal/db"
)

// --- /api/games/{id} (handleGameByID) --------------------------------------

func seedGames(t *testing.T, database *db.DB) {
	t.Helper()
	for _, g := range []struct {
		id   int64
		name string
	}{
		{1, "Test Game"},
		{2, "Game Two"},
	} {
		database.Exec(`INSERT OR IGNORE INTO games (id, name, slug, normalized_name) VALUES (?, ?, ?, ?)`,
			g.id, g.name, fmt.Sprintf("game-%d", g.id), fmt.Sprintf("game %d", g.id))
	}
}

func TestGetGameByIDFound(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	seedGames(t, database)
	database.Exec(`UPDATE games SET summary = 'A story', platforms_json = '[130]' WHERE id = 1`)
	mux := newTestGamesMux(database)

	req := httptest.NewRequest(http.MethodGet, "/api/games/1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var game map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &game); err != nil {
		t.Fatal(err)
	}
	if game["name"] != "Test Game" {
		t.Errorf("name = %v", game["name"])
	}
}

func TestGetGameByIDNotFound(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	seedGames(t, database)
	mux := newTestGamesMux(database)

	req := httptest.NewRequest(http.MethodGet, "/api/games/999", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetGameByIDErrors(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	seedGames(t, database)
	mux := newTestGamesMux(database)

	cases := []struct {
		path string
		want int
	}{
		{"/api/games/", http.StatusBadRequest},    // missing id
		{"/api/games/abc", http.StatusBadRequest}, // invalid id
		{"/api/games/1/", http.StatusOK},          // trailing slash tolerated
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.path, rec.Code, tc.want)
		}
	}

	// Method not allowed.
	req := httptest.NewRequest(http.MethodDelete, "/api/games/1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: got %d", rec.Code)
	}
}

// --- library CSV export -----------------------------------------------------

func TestLibraryExportCSV(t *testing.T) {
	database := setupLibraryTestDB(t)
	defer database.Close()
	sessionID := createLibrarySession(t, database, "user-1")
	mux := newTestLibraryMux(database)

	body := `{"status":"completed","rating":90,"playtime_minutes":900,"tags":["rpg","jrpg"],"notes":"great","platforms":["Nintendo Switch"],"medium":"physical"}`
	req := httptest.NewRequest(http.MethodPost, "/api/library/1", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	sess, _ := auth.GetSession(database, sessionID)
	req.Header.Set("X-CSRF-Token", sess.CSRFToken)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	req2 := httptest.NewRequest(http.MethodGet, "/api/library/export", nil)
	req2.AddCookie(&http.Cookie{Name: "cato_session", Value: sessionID})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req2)

	if rec.Code != http.StatusOK {
		t.Fatalf("export: got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	csvBody := rec.Body.String()
	for _, want := range []string{"game,status", "Test Game,completed", "Nintendo Switch", "physical", "rpg; jrpg", "15.00"} {
		if !strings.Contains(csvBody, want) {
			t.Errorf("CSV missing %q in:\n%s", want, csvBody)
		}
	}
}

// --- search query-parameter parsing -----------------------------------------

func TestSearchFullFiltersAndParams(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	seedGames(t, database)
	// Popularity/rating spread for sort tests.
	database.Exec(`UPDATE games SET aggregated_rating = 91, aggregated_rating_count = 50, first_release_date = 1262304000, popularity_score = 5 WHERE id = 1`)
	database.Exec(`UPDATE games SET aggregated_rating = 75, aggregated_rating_count = 10, first_release_date = 1577836800, popularity_score = 2 WHERE id = 2`)
	mux := newTestGamesMux(database)

	get := func(qs string) (int, string, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/games/search?"+qs, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), rec.Header().Get("X-Total-Count")
	}

	// Sanity: base search returns both.
	code, body, total := get("q=game&full=1")
	if code != 200 {
		t.Fatalf("base search: %d %s", code, body)
	}
	if total != "2" {
		t.Errorf("X-Total-Count = %q", total)
	}

	// Sorts accepted.
	for _, sort := range []string{"release_new", "release_old", "rating", "popularity", "name", "relevance"} {
		code, body, _ = get("q=game&full=1&sort=" + sort)
		if code != 200 {
			t.Errorf("sort %s: %d %s", sort, code, body)
		}
	}

	// Year bounds.
	code, _, _ = get("q=game&full=1&year_from=2020")
	if code != 200 {
		t.Errorf("year_from: %d", code)
	}
	// Date bounds (YYYY-MM-DD).
	code, _, _ = get("q=game&full=1&release_from=2020-01-01&release_to=2020-12-31")
	if code != 200 {
		t.Errorf("release dates: %d", code)
	}
	// min_rating clamp: above 100 should still 200.
	code, _, _ = get("q=game&full=1&min_rating=150")
	if code != 200 {
		t.Errorf("min_rating: %d", code)
	}
	// Invalid params are tolerated (clamped/ignored).
	for _, qs := range []string{
		"q=game&full=1&limit=-5", "q=game&full=1&limit=9999", "q=game&full=1&offset=-1",
		"q=game&full=1&year_from=abcd", "q=game&full=1&min_rating=abc", "q=game&full=1&sort=bogus",
		"q=game&full=1&year_from=1850", "q=game&full=1&year_to=2150",
	} {
		code, body, _ = get(qs)
		if code != 200 {
			t.Errorf("%s: %d %s", qs, code, body)
		}
	}

	// include_editions variants.
	for _, qs := range []string{"q=game&include_editions=1", "q=game&editions=true", "q=game&includeEditions=yes", "q=game&editions=no"} {
		code, _, _ = get(qs)
		if code != 200 {
			t.Errorf("%s: %d", qs, code)
		}
	}
}

func TestParseYearAndDateParams(t *testing.T) {
	// parseYearParam: from/start and end bounds; invalid and out-of-range → 0.
	if got := parseYearParam("1998", false); got <= 0 {
		t.Errorf("start 1998: %d", got)
	}
	if got := parseYearParam("1998", true); got <= 0 {
		t.Errorf("end 1998: %d", got)
	}
	if got := parseYearParam("1998", false); parseYearParam("1998", true) <= got {
		t.Error("end bound must exceed start bound")
	}
	for _, bad := range []string{"", "abcd", "1850", "2150"} {
		if got := parseYearParam(bad, false); got != 0 {
			t.Errorf("parseYearParam(%q) = %d, want 0", bad, got)
		}
	}

	// parseMinRatingParam clamps.
	if got := parseMinRatingParam(""); got != 0 {
		t.Errorf("empty: %d", got)
	}
	if got := parseMinRatingParam("-10"); got != 0 {
		t.Errorf("negative: %d", got)
	}
	if got := parseMinRatingParam("500"); got != 100 {
		t.Errorf("above 100: %d", got)
	}
	if got := parseMinRatingParam("42"); got != 42 {
		t.Errorf("42: %d", got)
	}
	if got := parseMinRatingParam("xx"); got != 0 {
		t.Errorf("garbage: %d", got)
	}
}

func TestParseDateParam(t *testing.T) {
	// YYYY-MM-DD start vs end (end is inclusive to 23:59:59).
	start := parseDateParam("2020-06-01", false)
	end := parseDateParam("2020-06-01", true)
	if start <= 0 || end <= start {
		t.Errorf("start=%d end=%d", start, end)
	}
	// RFC3339 accepted.
	if got := parseDateParam("2020-06-01T12:00:00Z", false); got <= 0 {
		t.Errorf("rfc3339: %d", got)
	}
	// Bare year accepted within range.
	if got := parseDateParam("2020", false); got <= 0 {
		t.Errorf("year: %d", got)
	}
	// Out-of-range year and garbage → 0.
	for _, bad := range []string{"", "junk", "1850", "2150"} {
		if got := parseDateParam(bad, false); got != 0 {
			t.Errorf("parseDateParam(%q) = %d, want 0", bad, got)
		}
	}
}

func TestParseLibraryDateAndYearParams(t *testing.T) {
	if got := parseLibraryYearParam("1998", false); got <= 0 {
		t.Errorf("year: %d", got)
	}
	if parseLibraryYearParam("", false) != 0 || parseLibraryYearParam("nope", true) != 0 || parseLibraryYearParam("1850", false) != 0 {
		t.Error("invalid years must yield 0")
	}
	start := parseLibraryDateParam("2021-03-05", false)
	end := parseLibraryDateParam("2021-03-05", true)
	if start <= 0 || end <= start {
		t.Errorf("date bounds: %d/%d", start, end)
	}
	if got := parseLibraryDateParam("2021-03-05T10:00:00Z", false); got <= 0 {
		t.Errorf("rfc3339: %d", got)
	}
	if got := parseLibraryDateParam("1999", false); got <= 0 {
		t.Errorf("bare year: %d", got)
	}
	if parseLibraryDateParam("", true) != 0 || parseLibraryDateParam("junk", true) != 0 || parseLibraryDateParam("1800", true) != 0 {
		t.Error("invalid dates must yield 0")
	}
}

func TestParseInLibraryParam(t *testing.T) {
	cases := []struct {
		qs   string
		want *bool
	}{
		{"", nil},
		{"?in_library=1", boolPtr(true)},
		{"?in_library=true", boolPtr(true)},
		{"?owned=yes", boolPtr(true)},
		{"?in_library=0", boolPtr(false)},
		{"?in_library=false", boolPtr(false)},
		{"?owned=no", boolPtr(false)},
		{"?in_library=exclude", boolPtr(false)},
		{"?in_library", boolPtr(true)}, // bare presence
	}
	for _, tc := range cases {
		got := parseInLibraryParam(httptest.NewRequest(http.MethodGet, "http://x/"+tc.qs, nil))
		if tc.want == nil {
			if got != nil {
				t.Errorf("%q: got %v, want nil", tc.qs, *got)
			}
			continue
		}
		if got == nil || *got != *tc.want {
			t.Errorf("%q: got %v, want %v", tc.qs, got, tc.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// --- gzip middleware ---------------------------------------------------------

func TestGzipMiddlewareAPIResponses(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	cfg := &config.Config{StaticDir: "../../web/static", CoverDir: t.TempDir()}
	srv := NewServer(cfg, database)

	// API + Accept-Encoding: gzip → gzipped body.
	req := httptest.NewRequest(http.MethodGet, "/api/games/search?q=game", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("API response not gzipped: %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	// Empty result list is fine; decompressing at all proves the round-trip.
	var decoded []interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Errorf("decoded body = %q (%v)", body, err)
	}

	// API without Accept-Encoding → plain.
	req2 := httptest.NewRequest(http.MethodGet, "/api/games/search?q=game", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Header().Get("Content-Encoding") != "" {
		t.Error("plain response tagged as gzipped")
	}
	if rec2.Header().Get("Vary") == "" {
		t.Error("Vary missing on plain API response")
	}
}

func TestGzipMiddlewareSkips204AndStatic(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	cfg := &config.Config{StaticDir: "../../web/static", CoverDir: t.TempDir()}
	_ = NewServer(cfg, database)

	// 204 responses must not be tagged Content-Encoding: gzip.
	mux := http.NewServeMux()
	mux.Handle("/204", gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/204", nil))
	if rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("204 tagged with Content-Encoding: %q", rec.Header().Get("Content-Encoding"))
	}
}

func TestStaticCacheHeaders(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	cfg := &config.Config{StaticDir: "../../web/static", CoverDir: t.TempDir()}
	srv := NewServer(cfg, database)

	// JS/CSS get no-cache.
	req := httptest.NewRequest(http.MethodGet, "/js/api.js", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("js Cache-Control = %q", cc)
	}

	// HTML pages get no-cache.
	req2 := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if cc := rec2.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("login Cache-Control = %q", cc)
	}
}

func TestServerStartShutdown(t *testing.T) {
	database := setupGamesTestDB(t)
	defer database.Close()
	cfg := &config.Config{ListenAddr: "127.0.0.1:0", StaticDir: "web/static", CoverDir: t.TempDir()}
	srv := NewServer(cfg, database)

	// Shutdown before Start is a no-op.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown before Start: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	// Wait for the listener to come up, then drain it.
	var up bool
	for i := 0; i < 100; i++ {
		if srv.httpServer != nil {
			up = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !up {
		t.Fatal("server did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Logf("Start returned: %v", err)
	}
}
