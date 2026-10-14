package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cato/internal/auth"
	"cato/internal/catalog"
	"cato/internal/config"
	"cato/internal/db"
	"cato/internal/discover"
	"cato/internal/library"
	"cato/internal/social"
)

type client struct {
	t       *testing.T
	mux     *http.ServeMux
	session *auth.Session
}

type fixture struct {
	db  *db.DB
	mux *http.ServeMux
	api *API
}

// noIGDB is an IGDB client that knows nothing (search stays local).
type noIGDB struct{}

func (noIGDB) SearchGames(context.Context, string, int, bool) ([]catalog.Game, error) {
	return nil, nil
}
func (noIGDB) GetGame(context.Context, int64) (*catalog.Game, error)          { return nil, nil }
func (noIGDB) GetGamesBatch(context.Context, []int64) ([]catalog.Game, error) { return nil, nil }
func (noIGDB) GetPlatforms(context.Context) ([]catalog.Platform, error)       { return nil, nil }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "v1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, summary, platforms_json, first_release_date, aggregated_rating, aggregated_rating_count) VALUES
		(1, 'Hades', 'hades', 'hades', 'Escape the underworld.', '[130]', 1600000000, 93, 40),
		(2, 'Hades II', 'hades-ii', 'hades ii', '', '[]', 1715000000, 0, 0),
		(3, 'Celeste', 'celeste', 'celeste', '', '[]', 1516000000, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	games := catalog.NewStore(database)
	lib := library.NewStore(database, games)
	mux := http.NewServeMux()
	api := Register(mux, Deps{
		DB: database, Cfg: &config.Config{}, Cookies: auth.Cookies{}, Library: lib,
		Catalog: catalog.NewService(games, noIGDB{}, database), Games: games,
		Discover: discover.New(database, lib), CoverDir: t.TempDir(),
		Social: social.New(database, t.TempDir()),
	})
	return &fixture{db: database, mux: mux, api: api}
}

func (f *fixture) user(t *testing.T, id string) *client {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO users (id, email, username) VALUES (?, ?, ?)`, id, id+"@example.com", id); err != nil {
		t.Fatal(err)
	}
	s, err := auth.CreateSession(f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, mux: f.mux, session: s}
}

func (c *client) do(method, path string, body any, header ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: c.session.ID})
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", c.session.CSRFToken)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	c.mux.ServeHTTP(rec, req)
	return rec
}

func (c *client) json(method, path string, body any, wantStatus int) map[string]any {
	c.t.Helper()
	rec := c.do(method, path, body)
	if rec.Code != wantStatus {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		c.t.Fatalf("%s %s: not JSON: %s", method, path, rec.Body.String())
	}
	return out
}

func TestAuthAndCSRFAreRequired(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/library", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	c := f.user(t, "u1")
	req := httptest.NewRequest(http.MethodPut, "/api/v1/library/1", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: c.session.ID})
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("mutation without CSRF: %d", rec.Code)
	}
	if rec := c.do(http.MethodGet, "/api/v1/nope", nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error":"not_found"`) {
		t.Errorf("unknown endpoint: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLibraryContract(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")

	added := c.json(http.MethodPut, "/api/v1/library/1", map[string]any{"status": "playing", "tags": []string{"roguelike"}}, http.StatusCreated)
	for _, k := range []string{"item", "undo_id", "library_rev", "created"} {
		if _, ok := added[k]; !ok {
			t.Errorf("mutation response missing %q: %v", k, added)
		}
	}
	item := added["item"].(map[string]any)
	if item["status"] != "playing" || item["game"].(map[string]any)["name"] != "Hades" {
		t.Errorf("item %v", item)
	}
	c.json(http.MethodPut, "/api/v1/library/1", map[string]any{"rating": 90}, http.StatusOK)

	bad := c.json(http.MethodPatch, "/api/v1/library/1", map[string]any{"rating": 120}, http.StatusBadRequest)
	if bad["error"] != "invalid_rating" || bad["fields"].(map[string]any)["rating"] == nil {
		t.Errorf("validation error shape: %v", bad)
	}
	if rec := c.do(http.MethodPatch, "/api/v1/library/2", map[string]any{"rating": 50}); rec.Code != http.StatusNotFound {
		t.Errorf("patch of an item not in the library: %d", rec.Code)
	}
	if rec := c.do(http.MethodPatch, "/api/v1/library/1", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: %d", rec.Code)
	}

	c.json(http.MethodPut, "/api/v1/library/2", map[string]any{"status": "backlog"}, http.StatusCreated)
	c.json(http.MethodPut, "/api/v1/library/3", map[string]any{"status": "backlog"}, http.StatusCreated)
	list := c.json(http.MethodGet, "/api/v1/library?limit=2", nil, http.StatusOK)
	if list["total"].(float64) != 3 || len(list["items"].([]any)) != 2 || list["next_cursor"] == "" {
		t.Fatalf("first page %v", list)
	}
	next := c.json(http.MethodGet, "/api/v1/library?limit=2&cursor="+list["next_cursor"].(string), nil, http.StatusOK)
	if _, has := next["total"]; has || len(next["items"].([]any)) != 1 {
		t.Errorf("second page %v", next)
	}
	shelf := c.json(http.MethodGet, "/api/v1/library?shelf=backlog&q=celes", nil, http.StatusOK)
	if len(shelf["items"].([]any)) != 1 {
		t.Errorf("shelf + q filter %v", shelf)
	}
	counts := c.json(http.MethodGet, "/api/v1/library/counts", nil, http.StatusOK)
	if counts["backlog"].(float64) != 2 || counts["all"].(float64) != 3 {
		t.Errorf("counts %v", counts)
	}
}

func TestLibraryETagsFollowLibraryRev(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	c.json(http.MethodPut, "/api/v1/library/1", map[string]any{}, http.StatusCreated)
	first := c.do(http.MethodGet, "/api/v1/library", nil)
	tag := first.Header().Get("ETag")
	if !strings.HasPrefix(tag, `W/"lib-`) {
		t.Fatalf("ETag %q", tag)
	}
	if rec := c.do(http.MethodGet, "/api/v1/library", nil, "If-None-Match", tag); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("unchanged library: %d", rec.Code)
	}
	// Same rev, different query: different tag.
	if other := c.do(http.MethodGet, "/api/v1/library?shelf=backlog", nil).Header().Get("ETag"); other == tag {
		t.Errorf("query not part of the ETag")
	}
	c.json(http.MethodPatch, "/api/v1/library/1", map[string]any{"notes": "x"}, http.StatusOK)
	if rec := c.do(http.MethodGet, "/api/v1/library", nil, "If-None-Match", tag); rec.Code != http.StatusOK {
		t.Errorf("after a write the old ETag must not match: %d", rec.Code)
	}
	// Non-library GETs get a content ETag.
	game := c.do(http.MethodGet, "/api/v1/games/3", nil)
	if gt := game.Header().Get("ETag"); gt == "" {
		t.Error("game page has no ETag")
	} else if rec := c.do(http.MethodGet, "/api/v1/games/3", nil, "If-None-Match", gt); rec.Code != http.StatusNotModified {
		t.Errorf("game page revalidation: %d", rec.Code)
	}
}

func TestIndexDeltaAndUndo(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	c.json(http.MethodPut, "/api/v1/library/1", map[string]any{"status": "playing"}, http.StatusCreated)
	full := c.json(http.MethodGet, "/api/v1/library/index", nil, http.StatusOK)
	if full["full"] != true || len(full["items"].([]any)) != 1 || len(full["fields"].([]any)) < 10 {
		t.Fatalf("index %v", full)
	}
	rev := int64(full["rev"].(float64))
	del := c.json(http.MethodDelete, "/api/v1/library/1", nil, http.StatusOK)
	delta := c.json(http.MethodGet, "/api/v1/library/index?since="+itoa(rev), nil, http.StatusOK)
	if delta["full"] != false || len(delta["deleted"].([]any)) != 1 {
		t.Fatalf("delta %v", delta)
	}
	undone := c.json(http.MethodPost, "/api/v1/undo/"+del["undo_id"].(string), nil, http.StatusOK)
	if len(undone["game_ids"].([]any)) != 1 {
		t.Errorf("undo %v", undone)
	}
	c.json(http.MethodGet, "/api/v1/library/1", nil, http.StatusOK)
	gone := c.json(http.MethodPost, "/api/v1/undo/"+del["undo_id"].(string), nil, http.StatusGone)
	if gone["error"] != "undo_expired" {
		t.Errorf("second undo %v", gone)
	}
}

func TestSessionsTimerAndOwnership(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	other := f.user(t, "u2")
	c.json(http.MethodPut, "/api/v1/library/1", map[string]any{"status": "playing"}, http.StatusCreated)
	logged := c.json(http.MethodPost, "/api/v1/sessions", map[string]any{"game_id": 1, "minutes": 60, "played_on": "2026-10-13"}, http.StatusCreated)
	sess := logged["session"].(map[string]any)
	if logged["item"].(map[string]any)["minutes"].(float64) != 60 || sess["played_on"] != "2026-10-13" {
		t.Fatalf("log %v", logged)
	}
	sid := itoa(int64(sess["id"].(float64)))
	if rec := other.do(http.MethodDelete, "/api/v1/sessions/"+sid, nil); rec.Code != http.StatusNotFound {
		t.Errorf("another user deleted my session: %d", rec.Code)
	}
	if rec := other.do(http.MethodGet, "/api/v1/library/1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("another user read my item: %d", rec.Code)
	}
	bad := c.json(http.MethodPost, "/api/v1/sessions", map[string]any{"game_id": 1, "minutes": 2000}, http.StatusBadRequest)
	if bad["fields"].(map[string]any)["minutes"] == nil {
		t.Errorf("session validation %v", bad)
	}
	diary := c.json(http.MethodGet, "/api/v1/sessions?from=2026-10-01&to=2026-10-31", nil, http.StatusOK)
	if len(diary["items"].([]any)) != 1 {
		t.Errorf("diary %v", diary)
	}
	c.json(http.MethodPatch, "/api/v1/sessions/"+sid, map[string]any{"minutes": 90}, http.StatusOK)
	game := c.json(http.MethodGet, "/api/v1/games/1", nil, http.StatusOK)
	if game["item"].(map[string]any)["minutes"].(float64) != 90 || len(game["playthroughs"].([]any)) != 1 || len(game["sessions"].([]any)) != 1 {
		t.Errorf("game page %v", game)
	}

	started := c.json(http.MethodPost, "/api/v1/timer", map[string]any{"game_id": 1}, http.StatusOK)
	if started["timer"].(map[string]any)["game_id"].(float64) != 1 {
		t.Fatalf("timer %v", started)
	}
	if got := c.json(http.MethodGet, "/api/v1/timer", nil, http.StatusOK); got["timer"] == nil {
		t.Errorf("timer not visible on another device")
	}
	stopped := c.json(http.MethodPost, "/api/v1/timer/stop", map[string]any{"log": true, "minutes": 25}, http.StatusOK)
	if stopped["session"].(map[string]any)["source"] != "timer" {
		t.Errorf("stop %v", stopped)
	}
	c.json(http.MethodPost, "/api/v1/timer/stop", map[string]any{"log": true}, http.StatusConflict)

	home := c.json(http.MethodGet, "/api/v1/home", nil, http.StatusOK)
	for _, k := range []string{"playing", "up_next", "recent", "counts", "streak"} {
		if _, ok := home[k]; !ok {
			t.Errorf("home missing %q", k)
		}
	}
	stats := c.json(http.MethodGet, "/api/v1/stats?year=2026", nil, http.StatusOK)
	if stats["year"].(float64) != 2026 {
		t.Errorf("stats %v", stats)
	}
}

func TestPrefs(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	me := c.json(http.MethodGet, "/api/v1/me", nil, http.StatusOK)
	prefs := me["prefs"].(map[string]any)
	if prefs["theme"] != "system" || prefs["rating_display"] != "stars" || me["csrf_token"] == "" {
		t.Fatalf("me %v", me)
	}
	got := c.json(http.MethodPatch, "/api/v1/me/prefs", map[string]any{"theme": "dark", "up_next": []int{3, 3, 1}}, http.StatusOK)
	p := got["prefs"].(map[string]any)
	if p["theme"] != "dark" || len(p["up_next"].([]any)) != 2 || p["rating_display"] != "stars" {
		t.Errorf("merged prefs %v", p)
	}
	c.json(http.MethodPatch, "/api/v1/me/prefs", map[string]any{"theme": "neon"}, http.StatusBadRequest)
	c.json(http.MethodPatch, "/api/v1/me/prefs", map[string]any{"is_admin": true}, http.StatusBadRequest)
}

func TestBulkAndExport(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	for _, id := range []string{"1", "2", "3"} {
		c.json(http.MethodPut, "/api/v1/library/"+id, map[string]any{"status": "backlog"}, http.StatusCreated)
	}
	res := c.json(http.MethodPost, "/api/v1/library/bulk", map[string]any{"game_ids": []int{1, 2}, "status": "wishlist"}, http.StatusOK)
	if res["updated"].(float64) != 2 {
		t.Errorf("bulk %v", res)
	}
	rec := c.do(http.MethodGet, "/api/v1/export.csv", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "game,igdb_id,status") {
		t.Errorf("csv %d %s", rec.Code, rec.Body.String())
	}
	exp := c.json(http.MethodGet, "/api/v1/export.json", nil, http.StatusOK)
	if exp["format"] != "cato-export" || len(exp["library"].([]any)) != 3 {
		t.Errorf("json export %v", exp)
	}
}

func TestTypeaheadPutsLibraryFirst(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	c.json(http.MethodPut, "/api/v1/library/1", map[string]any{"status": "playing"}, http.StatusCreated)
	res := c.json(http.MethodGet, "/api/v1/typeahead?q=hades", nil, http.StatusOK)
	lib := res["library"].([]any)
	cat := res["catalog"].([]any)
	if len(lib) != 1 || len(cat) == 0 {
		t.Fatalf("typeahead %v", res)
	}
	for _, x := range cat {
		g := x.(map[string]any)
		if g["id"].(float64) == 1 && g["status"] != "playing" {
			t.Errorf("catalog hit lacks my status: %v", g)
		}
	}
}

func TestWriteRateLimit(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	f.api.writes = auth.NewRateLimiter(2, time.Minute)
	c.json(http.MethodPut, "/api/v1/library/1", map[string]any{}, http.StatusCreated)
	c.json(http.MethodPut, "/api/v1/library/2", map[string]any{}, http.StatusCreated)
	c.json(http.MethodPut, "/api/v1/library/3", map[string]any{}, http.StatusTooManyRequests)
	c.json(http.MethodGet, "/api/v1/library", nil, http.StatusOK)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestDevicesAndDiscover(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	other, err := auth.CreateSession(f.db, "u1")
	if err != nil {
		t.Fatal(err)
	}
	auth.NoteSessionClient(f.db, other.ID, "Mozilla/5.0 (iPhone)")
	list := c.json(http.MethodGet, "/api/v1/devices", nil, http.StatusOK)["items"].([]any)
	if len(list) != 2 {
		t.Fatalf("devices %v", list)
	}
	var otherID float64
	currents := 0
	for _, d := range list {
		m := d.(map[string]any)
		if m["current"] == true {
			currents++
		} else {
			otherID = m["id"].(float64)
			if m["user_agent"] != "Mozilla/5.0 (iPhone)" {
				t.Errorf("user agent %v", m)
			}
		}
	}
	if currents != 1 {
		t.Errorf("current device count %d", currents)
	}
	intruder := f.user(t, "u2")
	intruder.json(http.MethodDelete, "/api/v1/devices/"+itoa(int64(otherID)), nil, http.StatusNotFound)
	c.json(http.MethodDelete, "/api/v1/devices/"+itoa(int64(otherID)), nil, http.StatusOK)
	if s, _ := auth.GetSession(f.db, other.ID); s != nil {
		t.Error("revoked session still valid")
	}
	third, _ := auth.CreateSession(f.db, "u1")
	res := c.json(http.MethodPost, "/api/v1/devices/revoke-others", nil, http.StatusOK)
	if res["revoked"].(float64) != 1 {
		t.Errorf("revoke others %v", res)
	}
	if s, _ := auth.GetSession(f.db, third.ID); s != nil {
		t.Error("other session survived")
	}
	c.json(http.MethodGet, "/api/v1/me", nil, http.StatusOK) // the current one still works

	f.db.Exec(`UPDATE games SET local_cover_path = '/covers/' || id || '.jpg'`)
	c.json(http.MethodPut, "/api/v1/library/1", map[string]any{}, http.StatusCreated)
	shelves := c.json(http.MethodGet, "/api/v1/discover", nil, http.StatusOK)["shelves"].([]any)
	popular := shelves[0].(map[string]any)["items"].([]any)
	for _, g := range popular {
		if g.(map[string]any)["id"].(float64) == 1 {
			t.Error("discover suggests a game already in the library")
		}
	}
	if len(popular) != 2 {
		t.Errorf("popular %v", popular)
	}

	views := c.json(http.MethodPatch, "/api/v1/me/prefs", map[string]any{"saved_views": []map[string]string{{"name": "Couch co-op", "query": "shelf=backlog&q=$co-op"}}}, http.StatusOK)
	if len(views["prefs"].(map[string]any)["saved_views"].([]any)) != 1 {
		t.Errorf("saved views %v", views)
	}
	c.json(http.MethodPatch, "/api/v1/me/prefs", map[string]any{"saved_views": []map[string]string{{"name": "", "query": "x"}}}, http.StatusBadRequest)
}

// S2: catalog search needs an account, caps the query and is rate limited
// per IP; typeahead shares the limiter.
func TestSearchGuards(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=hades", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous search: %d", rec.Code)
	}
	long := strings.Repeat("a", 150)
	if q := searchQuery(httptest.NewRequest(http.MethodGet, "/api/v1/search?q="+long, nil)); len(q) != maxQueryChars {
		t.Errorf("query not capped: %d", len(q))
	}
	f.api.SearchLimiter = auth.NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		c.json(http.MethodGet, "/api/v1/typeahead?q=hades", nil, http.StatusOK)
	}
	c.json(http.MethodGet, "/api/v1/search?q=hades", nil, http.StatusTooManyRequests)
	// A different client IP has its own bucket.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=hades", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	req.AddCookie(&http.Cookie{Name: "cato_session", Value: c.session.ID})
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("other IP: %d", rec.Code)
	}
}

func TestDiscoverSkipsCoverlessAndLimits(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	f.db.Exec(`UPDATE games SET local_cover_path = '/covers/1.jpg', popularity_score = 5 WHERE id = 1`)
	f.db.Exec(`UPDATE games SET local_cover_path = '/covers/3.jpg', popularity_score = 9 WHERE id = 3`)
	shelves := c.json(http.MethodGet, "/api/v1/discover", nil, http.StatusOK)["shelves"].([]any)
	items := shelves[0].(map[string]any)["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"].(float64) != 3 {
		t.Errorf("popular %v: games with covers, most popular first", items)
	}
}

func TestGamePageExtrasCustomGamesAndEditions(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "u1")
	f.db.Exec(`UPDATE games SET ttb_normally = 600 WHERE id = 1`)
	f.db.Exec(`INSERT INTO games (id, name, slug, normalized_name, version_parent) VALUES (4, 'Hades Deluxe', 'hd', 'hades deluxe', 1)`)
	page := c.json(http.MethodGet, "/api/v1/games/1", nil, http.StatusOK)
	x := page["extras"].(map[string]any)
	if x["ttb"].(map[string]any)["normally"].(float64) != 600 || len(x["editions"].([]any)) != 1 {
		t.Errorf("extras %v", x)
	}
	if page["estimate"].(map[string]any)["minutes"].(float64) != 600 {
		t.Errorf("estimate at a neutral pace %v", page["estimate"])
	}

	// Adding an edition puts the base game in the library and records the
	// edition on the playthrough.
	res := c.json(http.MethodPut, "/api/v1/library/4", map[string]any{"status": "playing"}, http.StatusCreated)
	if res["item"].(map[string]any)["game_id"].(float64) != 1 {
		t.Fatalf("edition not rolled up: %v", res["item"])
	}
	var edition int64
	f.db.QueryRow(`SELECT edition_game_id FROM playthroughs WHERE game_id = 1`).Scan(&edition)
	if edition != 4 {
		t.Errorf("edition on playthrough = %d", edition)
	}

	// Custom entries.
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	made := c.json(http.MethodPost, "/api/v1/games", map[string]any{"name": "Local Demo", "platform": "Nintendo Switch", "year": 2025, "cover": png}, http.StatusCreated)
	g := made["game"].(map[string]any)
	id := int64(g["id"].(float64))
	if id >= 0 || !strings.HasPrefix(g["cover"].(string), "/covers/"+strconv.FormatInt(id, 10)+".jpg?u=") {
		t.Fatalf("custom game %v", g)
	}
	c.json(http.MethodPut, "/api/v1/library/"+strconv.FormatInt(id, 10), map[string]any{"status": "backlog"}, http.StatusCreated)
	other := f.user(t, "u2")
	other.json(http.MethodPut, "/api/v1/games/"+strconv.FormatInt(id, 10)+"/cover", map[string]any{"cover": png}, http.StatusForbidden)
	c.json(http.MethodPut, "/api/v1/games/"+strconv.FormatInt(id, 10)+"/cover", map[string]any{"cover": png}, http.StatusOK)
	c.json(http.MethodPost, "/api/v1/games", map[string]any{"name": ""}, http.StatusBadRequest)
	c.json(http.MethodPost, "/api/v1/games", map[string]any{"name": "X", "cover": "data:text/html;base64,PHNjcmlwdD4="}, http.StatusBadRequest)

	// Up Next with a fit, and its validation.
	c.json(http.MethodGet, "/api/v1/up-next?fit=tonight", nil, http.StatusOK)
	c.json(http.MethodGet, "/api/v1/up-next?fit=forever", nil, http.StatusBadRequest)
	home := c.json(http.MethodGet, "/api/v1/home", nil, http.StatusOK)
	if _, ok := home["coming_soon"]; !ok {
		t.Error("home lacks coming_soon")
	}
}
