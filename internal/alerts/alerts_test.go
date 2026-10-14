package alerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
	"cato/internal/library"
	"cato/internal/social"
)

func setup(t *testing.T) (*db.DB, *Service, *library.Store) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	db.Migrate(database)
	for _, q := range []string{
		`INSERT INTO users (id, email, username) VALUES ('u', 'u@x', 'u'), ('w', 'w@x', 'w')`,
		`INSERT INTO platforms (id, name, abbreviation) VALUES (130, 'Nintendo Switch', 'Switch'), (167, 'PlayStation 5', 'PS5') ON CONFLICT(id) DO NOTHING`,
		`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Silksong', 'silksong', 'silksong'), (2, 'Later', 'later', 'later'), (3, 'Owned', 'owned', 'owned')`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	lib := library.NewStore(database, catalog.NewStore(database))
	s := &Service{DB: database, Library: lib, Social: social.New(database, t.TempDir()),
		Now: func() time.Time { return time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC) }}
	return database, s, lib
}

func count(t *testing.T, d *db.DB, q string, args ...any) int {
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReleaseAlerts(t *testing.T) {
	d, s, lib := setup(t)
	ctx := context.Background()
	today := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC).Unix()
	week := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC).Unix()
	d.Exec(`INSERT INTO game_release_dates (game_id, platform_id, date) VALUES (1, 130, ?), (1, 167, ?), (2, 130, ?)`, today, today, week)
	st := "wishlist"
	lib.Upsert(ctx, "u", 1, library.Change{Status: &st})
	lib.Upsert(ctx, "u", 2, library.Change{Status: &st})
	lib.Upsert(ctx, "w", 1, library.Change{Status: &st})
	// u plays on PS5 only and wants the week-ahead heads-up; w has no platforms.
	lib.UpdatePrefs(ctx, "u", json.RawMessage(`{"default_platforms":["PlayStation 5"],"release_week":true}`))
	if err := s.Releases(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE user_id = 'u' AND kind = 'release' AND game_id = 1 AND json_extract(data_json, '$.platform') = 'PlayStation 5'`); n != 1 {
		t.Fatalf("u release alerts %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE user_id = 'u' AND kind = 'release_soon'`); n != 0 {
		t.Fatalf("week-ahead is only for u's platforms (game 2 is Switch only): %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE user_id = 'w' AND kind = 'release'`); n != 1 {
		t.Fatalf("w with no platforms hears once: %d", n)
	}
	s.Releases(ctx)
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE kind = 'release'`); n != 2 {
		t.Fatalf("alerts are sent once: %d", n)
	}
	lib.UpdatePrefs(ctx, "u", json.RawMessage(`{"default_platforms":["Nintendo Switch"]}`))
	s.Releases(ctx)
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE user_id = 'u' AND kind = 'release_soon' AND game_id = 2`); n != 1 {
		t.Fatalf("week-ahead heads-up: %d", n)
	}
}

func TestPriceAlerts(t *testing.T) {
	d, s, lib := setup(t)
	ctx := context.Background()
	d.Exec(`INSERT INTO game_external_ids (game_id, source, uid) VALUES (1, 1, '1030300'), (2, 1, '42')`)
	st := "wishlist"
	lib.Upsert(ctx, "u", 1, library.Change{Status: &st})
	lib.Upsert(ctx, "u", 2, library.Change{Status: &st})
	lib.Upsert(ctx, "w", 1, library.Change{Status: &st})
	lib.UpdatePrefs(ctx, "u", json.RawMessage(`{"price_alerts":{"enabled":true,"min_cut":40,"country":"gb"}}`))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("key") != "k" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/lookup/id/shop/61/v1":
			w.Write([]byte(`{"app/1030300":"silk","app/42":"later"}`))
		case "/games/overview/v2":
			if r.URL.Query().Get("country") != "GB" {
				t.Errorf("country %q", r.URL.Query().Get("country"))
			}
			w.Write([]byte(`{"prices":[
				{"id":"silk","current":{"shop":{"id":61,"name":"Steam"},"price":{"amount":9.99,"currency":"GBP"},"regular":{"amount":19.99},"cut":50,"url":"https://itad.link/x"},"lowest":{"price":{"amount":7.49}}},
				{"id":"later","current":{"shop":{"id":35,"name":"GOG"},"price":{"amount":18,"currency":"GBP"},"cut":10,"url":"https://itad.link/y"},"lowest":{"price":{"amount":18}}}]}`))
		}
	}))
	defer srv.Close()
	s.ITAD = &ITAD{Key: "k", Endpoint: srv.URL}
	if err := s.Prices(ctx); err != nil {
		t.Fatal(err)
	}
	var data string
	d.QueryRow(`SELECT data_json FROM notifications WHERE user_id = 'u' AND kind = 'price' AND game_id = 1`).Scan(&data)
	var got map[string]any
	json.Unmarshal([]byte(data), &got)
	if got["cut"] != float64(50) || got["shop"] != "Steam" || got["price"] != "£9.99" || got["low"] != false || got["url"] != "https://itad.link/x" {
		t.Fatalf("deal %v", got)
	}
	// A 10% cut is below 40%, but it's the historical low.
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE user_id = 'u' AND kind = 'price' AND game_id = 2 AND json_extract(data_json, '$.low') = 1`); n != 1 {
		t.Fatalf("historical low alert: %d", n)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE user_id = 'w'`); n != 0 {
		t.Fatal("w didn't turn price alerts on")
	}
	before := calls
	s.Prices(ctx)
	if n := count(t, d, `SELECT COUNT(*) FROM notifications WHERE kind = 'price'`); n != 2 {
		t.Fatalf("a deal is announced once: %d", n)
	}
	if calls-before != 1 {
		t.Fatalf("ITAD IDs are cached: %d calls", calls-before)
	}
}
