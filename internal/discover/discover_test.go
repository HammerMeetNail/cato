package discover

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
	"cato/internal/library"
)

type fixture struct {
	t   *testing.T
	db  *db.DB
	lib *library.Store
	svc *Service
	ctx context.Context
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: database, ctx: context.Background(), now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	f.lib = library.NewStore(database, catalog.NewStore(database))
	f.svc = New(database, f.lib)
	f.svc.now = func() time.Time { return f.now }
	f.exec(`INSERT INTO users (id, email) VALUES ('me', 'me@example.com'), ('friend', 'f@example.com')`)
	return f
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("%v: %s", err, q)
	}
}

// game inserts a catalog row; opts are column=value SQL fragments.
func (f *fixture) game(id int64, name string, set string) {
	f.t.Helper()
	f.exec(`INSERT INTO games (id, name, slug, normalized_name, cover_url) VALUES (?, ?, ?, ?, 'https://images.igdb.com/x.jpg')`,
		id, name, strings.ToLower(name), strings.ToLower(name))
	if set != "" {
		f.exec(`UPDATE games SET `+set+` WHERE id = ?`, id)
	}
}

func (f *fixture) add(user string, id int64, status string, extra string) {
	f.t.Helper()
	f.exec(`INSERT INTO library_items (user_id, game_id, status, created_at) VALUES (?, ?, ?, '2026-01-01T00:00:00Z')`, user, id, status)
	if extra != "" {
		f.exec(`UPDATE library_items SET `+extra+` WHERE user_id = ? AND game_id = ?`, user, id)
	}
}

func titles(items []Game) string {
	var out []string
	for _, g := range items {
		out = append(out, g.Name)
	}
	return strings.Join(out, ",")
}

func TestShelves(t *testing.T) {
	f := newFixture(t)
	now := f.now.Unix()
	f.game(1, "Trending Hit", "")
	f.game(2, "Trending Owned", "")
	f.game(3, "Coming Switch", "")
	f.game(4, "Coming PS5", "")
	f.game(5, "Fresh", "first_release_date = "+itoa(now-10*86400))
	f.game(6, "Short Great", "ttb_normally = 300, total_rating = 90, total_rating_count = 500")
	f.game(7, "Long Great", "ttb_normally = 3000, total_rating = 95, total_rating_count = 900")
	f.game(8, "Gem", "total_rating = 91, total_rating_count = 12")
	f.game(9, "Classic", "first_release_date = 820454400, total_rating = 93, total_rating_count = 400")
	f.game(10, "Loved", "")
	f.game(11, "Like Loved", "total_rating = 80")
	f.game(12, "Friend Pick", "")
	f.game(13, "Edition", "version_parent = 1")
	f.exec(`INSERT INTO platforms (id, name, abbreviation) VALUES (130, 'Nintendo Switch', 'Switch') ON CONFLICT(id) DO NOTHING`)
	f.exec(`INSERT INTO game_popularity (game_id, type, value, updated_at) VALUES (1, 1, 0.9, 0), (2, 1, 0.8, 0), (13, 1, 0.95, 0)`)
	f.exec(`INSERT INTO game_release_dates (game_id, platform_id, date) VALUES (3, 130, ?), (4, 167, ?)`, now+30*86400, now+20*86400)
	f.exec(`INSERT INTO game_relations (game_id, related_id, kind) VALUES (10, 11, 'similar')`)
	f.add("me", 2, "backlog", "")
	f.add("me", 10, "completed", "rating = 90, owned_platforms_json = '[\"Nintendo Switch\"]'")
	f.add("friend", 12, "playing", "")

	shelves, err := f.svc.Shelves(f.ctx, "me")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range shelves {
		got[s.ID] = titles(s.Items)
	}
	want := map[string]string{
		"trending":    "Trending Hit",
		"coming-soon": "Coming Switch",
		"new":         "Fresh",
		"instance":    "Friend Pick",
		"because":     "Like Loved",
		"short":       "Short Great",
		"gems":        "Gem",
		"classics":    "Classic",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("shelf %s = %q, want %q", id, got[id], w)
		}
	}
	for _, s := range shelves {
		if s.ID == "because" && s.Title != "Because you liked Loved" {
			t.Errorf("because title %q", s.Title)
		}
	}
}

func TestComingSoonForYou(t *testing.T) {
	f := newFixture(t)
	now := f.now.Unix()
	f.game(1, "Wanted Soon", "first_release_date = "+itoa(now+5*86400))
	f.game(2, "Wanted Out", "first_release_date = "+itoa(now-5*86400))
	f.game(3, "Backlog Soon", "first_release_date = "+itoa(now+5*86400))
	f.add("me", 1, "wishlist", "")
	f.add("me", 2, "wishlist", "")
	f.add("me", 3, "backlog", "")
	soon, err := f.svc.ComingSoonForYou(f.ctx, "me", 5)
	if err != nil || titles(soon) != "Wanted Soon" {
		t.Fatalf("coming soon %v %q", err, titles(soon))
	}
}

func TestPaceAndEstimate(t *testing.T) {
	f := newFixture(t)
	f.game(1, "A", "ttb_normally = 600")
	f.game(2, "B", "ttb_normally = 1200")
	f.game(3, "Next", "ttb_normally = 1000")
	f.add("me", 1, "completed", "playtime_minutes = 900")
	f.add("me", 2, "completed", "playtime_minutes = 1800")
	f.add("me", 3, "backlog", "")
	f.exec(`INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('me', 3, '2026-10-01', 480), ('me', 3, '2026-09-20', 480)`)
	p, err := f.svc.PaceFor(f.ctx, "me")
	if err != nil {
		t.Fatal(err)
	}
	if p.Multiplier != 1.5 || p.WeeklyMinutes != 120 || p.Samples != 2 {
		t.Errorf("pace %+v", p)
	}
	e, err := f.svc.EstimateFor(f.ctx, "me", 3)
	if err != nil || e == nil || e.Minutes != 1500 || e.Weeks != 12.5 {
		t.Errorf("estimate %+v %v", e, err)
	}
	// One finished game isn't enough history.
	f.exec(`DELETE FROM library_items WHERE game_id = 2`)
	p, _ = f.svc.PaceFor(f.ctx, "me")
	if p.Multiplier != 1 {
		t.Errorf("multiplier with one sample %v", p.Multiplier)
	}
}

func TestUpNextScoring(t *testing.T) {
	f := newFixture(t)
	f.game(1, "Zelda 1", "first_release_date = 100, genres_json = '[12]'")
	f.game(2, "Zelda 2", "first_release_date = 200, ttb_normally = 2400")
	f.game(3, "Quick RPG", "ttb_normally = 120, genres_json = '[12]'")
	f.game(4, "Old Backlog", "ttb_normally = 6000")
	f.game(5, "Paused One", "")
	f.exec(`INSERT INTO game_series (game_id, series_id, kind, name) VALUES (1, 106, 'collection', 'Zelda'), (2, 106, 'collection', 'Zelda')`)
	f.add("me", 1, "completed", "rating = 95, owned_platforms_json = '[\"Nintendo Switch\"]'")
	f.add("me", 2, "backlog", "owned_platforms_json = '[\"Nintendo Switch\"]'")
	f.add("me", 3, "backlog", "")
	f.add("me", 4, "backlog", "created_at = '2022-01-01T00:00:00Z'")
	f.add("me", 5, "paused", "")
	f.exec(`INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('me', 1, '2026-10-10', 60)`)

	picks, err := f.svc.UpNext(f.ctx, "me", 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if picks[0].Item.Game.Name != "Zelda 2" || !strings.Contains(picks[0].Reason, "next in the Zelda series") || !strings.Contains(picks[0].Reason, "on Switch") {
		t.Fatalf("top pick %q: %q", picks[0].Item.Game.Name, picks[0].Reason)
	}
	// An evening favours the short game.
	tonight, _ := f.svc.UpNext(f.ctx, "me", 1, "tonight")
	if tonight[0].Item.Game.Name != "Quick RPG" || !strings.Contains(tonight[0].Reason, "~2h") || !strings.Contains(tonight[0].Reason, "you loved Zelda 1") {
		t.Errorf("tonight %q: %q", tonight[0].Item.Game.Name, tonight[0].Reason)
	}
	long, _ := f.svc.UpNext(f.ctx, "me", 5, "long")
	for _, p := range long {
		if p.Item.Game.Name == "Old Backlog" && !strings.Contains(p.Reason, "waiting 4 years") {
			t.Errorf("backlog age reason %q", p.Reason)
		}
	}
	// The pinned queue still comes first through library.UpNext.
	f.lib.Scorer = func(ctx context.Context, userID string, n int) ([]library.UpNextPick, error) {
		return f.svc.UpNext(ctx, userID, n, "")
	}
	queued, _ := f.lib.UpNext(f.ctx, "me", []int64{4}, 3)
	if queued[0].Item.GameID != 4 || !queued[0].Pinned || queued[1].Item.GameID != 2 {
		t.Errorf("queue then scored: %+v", queued)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
