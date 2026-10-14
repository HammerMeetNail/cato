package library

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
)

type fixture struct {
	t     *testing.T
	db    *db.DB
	store *Store
	ctx   context.Context
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: database, ctx: context.Background(), clock: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	f.store = NewStore(database, catalog.NewStore(database))
	f.store.now = func() time.Time { return f.clock }
	f.exec(`INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com'), ('u2', 'u2@example.com');
		INSERT INTO platforms (id, name, abbreviation) VALUES (130, 'Nintendo Switch', 'Switch') ON CONFLICT(id) DO NOTHING;
		INSERT INTO games (id, name, slug, normalized_name, cover_url, platforms_json, first_release_date) VALUES
		  (1, 'Hades', 'hades', 'hades', 'https://images.igdb.com/igdb/image/upload/t_cover_big/a.jpg', '[130,6]', 1600000000),
		  (2, 'Celeste', 'celeste', 'celeste', '', '[130]', 1516000000),
		  (3, 'Balatro', 'balatro', 'balatro', '', '[]', 1708000000),
		  (4, 'Outer Wilds', 'outer-wilds', 'outer wilds', '', '[]', 1559000000);`)
	return f
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("exec: %v", err)
	}
}

func (f *fixture) scalar(q string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatalf("scalar %q: %v", q, err)
	}
	return n
}

func str(v string) *string { return &v }
func num(v int64) *int64    { return &v }

func (f *fixture) patch(game int64, ch Change) *WriteResult {
	f.t.Helper()
	res, err := f.store.Upsert(f.ctx, "u1", game, ch)
	if err != nil {
		f.t.Fatalf("upsert %d: %v", game, err)
	}
	return res
}

func (f *fixture) invariant(game int64) {
	f.t.Helper()
	total := f.scalar(`SELECT playtime_minutes FROM library_items WHERE user_id = 'u1' AND game_id = ?`, game)
	sum := f.scalar(`SELECT COALESCE(SUM(minutes), 0) FROM play_sessions WHERE user_id = 'u1' AND game_id = ?`, game)
	if total != sum {
		f.t.Fatalf("game %d: total %d != session sum %d", game, total, sum)
	}
}

func TestUpsertCreatesItemAndQueuesCover(t *testing.T) {
	f := newFixture(t)
	res := f.patch(1, Change{})
	if !res.Created || res.Item.Status != StatusBacklog {
		t.Fatalf("created=%v status=%q", res.Created, res.Item.Status)
	}
	if res.Item.Game.Name != "Hades" || res.Item.Game.Cover == "" {
		t.Errorf("game summary %+v", res.Item.Game)
	}
	if got := strings.Join(res.Item.Game.Platforms, ","); !strings.Contains(got, "Nintendo Switch") {
		t.Errorf("available platforms %q", got)
	}
	if f.scalar(`SELECT COUNT(*) FROM cover_jobs WHERE game_id = 1`) != 1 {
		t.Errorf("no cover job queued for a library game")
	}
	if res.UndoID == "" || res.Rev == 0 {
		t.Errorf("undo=%q rev=%d", res.UndoID, res.Rev)
	}
	if _, err := f.store.Patch(f.ctx, "u1", 2, Change{Status: str("playing")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("patch of missing item: %v", err)
	}
	if _, err := f.store.Upsert(f.ctx, "u1", 999, Change{}); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("unknown game: %v", err)
	}
}

func TestValidation(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 41)
	for name, ch := range map[string]Change{
		"status":     {Status: str("shelved")},
		"rating":     {Rating: num(101)},
		"tag length": {Tags: &[]string{long}},
		"medium":     {Medium: str("cartridge")},
		"both times": {Minutes: num(10), MinutesDelta: num(5)},
		"date":       {StartedAt: str("yesterday")},
		"played_on":  {PlayedOn: "14/10/2026", MinutesDelta: num(5)},
		"completion": {Status: str("completed"), Completion: str("most")},
	} {
		_, err := f.store.Upsert(f.ctx, "u1", 1, ch)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
}

func TestPlaythroughLifecycle(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("backlog")})
	if n := f.scalar(`SELECT COUNT(*) FROM playthroughs`); n != 0 {
		t.Fatalf("backlog created %d playthroughs", n)
	}
	f.patch(1, Change{Status: str("playing"), PlayedOn: "2026-10-01"})
	pts, _ := f.store.Playthroughs(f.ctx, "u1", 1)
	if len(pts) != 1 || pts[0].StartedOn == nil || *pts[0].StartedOn != "2026-10-01" || !pts[0].Current {
		t.Fatalf("start: %+v", pts)
	}
	first := pts[0].ID
	// Pause and resume keep the same run.
	f.patch(1, Change{Status: str("paused")})
	f.patch(1, Change{Status: str("playing")})
	if n := f.scalar(`SELECT COUNT(*) FROM playthroughs`); n != 1 {
		t.Errorf("resume created a new playthrough")
	}
	res := f.patch(1, Change{Status: str("completed"), Rating: num(90), Completion: str("main_extras"), PlayedOn: "2026-10-12"})
	if res.Item.CompletedAt == nil {
		t.Errorf("completed_at not set")
	}
	pts, _ = f.store.Playthroughs(f.ctx, "u1", 1)
	if pts[0].FinishedOn == nil || pts[0].Completion == nil || *pts[0].Completion != "main_extras" || pts[0].Rating == nil || *pts[0].Rating != 90 {
		t.Fatalf("finish: %+v", pts[0])
	}
	// Playing again is a replay: a second playthrough, the first untouched.
	f.clock = f.clock.Add(48 * time.Hour)
	f.patch(1, Change{Status: str("playing")})
	pts, _ = f.store.Playthroughs(f.ctx, "u1", 1)
	if len(pts) != 2 || pts[0].ID == first || !pts[0].Current || pts[0].FinishedOn != nil {
		t.Fatalf("replay: %+v", pts)
	}
	f.patch(1, Change{Status: str("abandoned")})
	pts, _ = f.store.Playthroughs(f.ctx, "u1", 1)
	if pts[0].Completion == nil || *pts[0].Completion != "dropped" || pts[0].FinishedOn == nil {
		t.Errorf("drop: %+v", pts[0])
	}
	if pts[1].Completion == nil || *pts[1].Completion != "main_extras" {
		t.Errorf("first run changed: %+v", pts[1])
	}
}

func TestPlaytimeIsTheSumOfSessions(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing")})
	res := f.patch(1, Change{MinutesDelta: num(45), PlayedOn: "2026-10-13", SessionNote: "boss"})
	if res.Session == nil || res.Session.Source != "manual" || res.Session.PlayedOn != "2026-10-13" || res.Session.Note != "boss" {
		t.Fatalf("session %+v", res.Session)
	}
	if res.Session.PlaythroughID == nil {
		t.Errorf("session not attached to the current playthrough")
	}
	if res.Item.Minutes != 45 {
		t.Errorf("minutes %d", res.Item.Minutes)
	}
	f.invariant(1)
	// Setting the total records the difference as an adjustment.
	res = f.patch(1, Change{Minutes: num(600)})
	if res.Session == nil || res.Session.Source != "adjust" || res.Session.Minutes != 555 || res.Item.Minutes != 600 {
		t.Fatalf("adjust: %+v item=%d", res.Session, res.Item.Minutes)
	}
	f.invariant(1)
	res = f.patch(1, Change{Minutes: num(100)})
	if res.Item.Minutes != 100 {
		t.Errorf("lower total: %d", res.Item.Minutes)
	}
	f.invariant(1)
	// A negative delta can't take the total below zero.
	res = f.patch(1, Change{MinutesDelta: num(-500)})
	if res.Item.Minutes != 0 {
		t.Errorf("clamped total %d", res.Item.Minutes)
	}
	f.invariant(1)
	// Over a day at once is an adjustment, not a session.
	res = f.patch(1, Change{MinutesDelta: num(3000)})
	if res.Session.Source != "adjust" {
		t.Errorf("source %q", res.Session.Source)
	}
	diary, _ := f.store.Sessions(f.ctx, "u1", SessionRange{})
	if len(diary) != 1 || diary[0].Minutes != 45 || diary[0].GameName != "Hades" {
		t.Errorf("diary should hide adjustments: %+v", diary)
	}
}

func TestConcurrentDeltasAreNotLost(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing")})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.store.LogSession(f.ctx, "u1", 1, 30, "", ""); err != nil {
				t.Errorf("log: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := f.scalar(`SELECT playtime_minutes FROM library_items WHERE game_id = 1`); got != 720 {
		t.Fatalf("total %d, want 720", got)
	}
	f.invariant(1)
}

func TestUndoRestoresEverything(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing"), Tags: &[]string{"rpg"}, Notes: str("good")})
	f.patch(1, Change{MinutesDelta: num(90)})
	before, _ := f.store.Get(f.ctx, "u1", 1)

	// Finish then undo.
	f.clock = f.clock.Add(time.Hour)
	res := f.patch(1, Change{Status: str("completed"), Rating: num(80), Completion: str("main")})
	if _, err := f.store.Undo(f.ctx, "u1", res.UndoID); err != nil {
		t.Fatal(err)
	}
	after, _ := f.store.Get(f.ctx, "u1", 1)
	if after.Status != "playing" || after.Rating != 0 || after.CompletedAt != nil || after.Minutes != 90 || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("after undo: %+v", after)
	}
	pts, _ := f.store.Playthroughs(f.ctx, "u1", 1)
	if len(pts) != 1 || pts[0].FinishedOn != nil || pts[0].Minutes != 90 || !pts[0].Current {
		t.Fatalf("playthrough after undo: %+v", pts)
	}
	f.invariant(1)
	if _, err := f.store.Undo(f.ctx, "u1", res.UndoID); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("second undo: %v", err)
	}

	// Removing the game then undoing brings back the journal.
	undo, err := f.store.Delete(f.ctx, "u1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if f.scalar(`SELECT COUNT(*) FROM play_sessions`) != 0 {
		t.Fatal("sessions survived delete")
	}
	if _, err := f.store.Undo(f.ctx, "u2", undo); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("another user undid my delete: %v", err)
	}
	if _, err := f.store.Undo(f.ctx, "u1", undo); err != nil {
		t.Fatal(err)
	}
	restored, _ := f.store.Get(f.ctx, "u1", 1)
	if restored.Minutes != 90 || restored.Notes != "good" || len(restored.Tags) != 1 || restored.CurrentPlaythroughID == nil {
		t.Errorf("restored %+v", restored)
	}
	f.invariant(1)

	// Undoing an add removes the game again.
	res = f.patch(2, Change{Status: str("wishlist")})
	f.store.Undo(f.ctx, "u1", res.UndoID)
	if _, err := f.store.Get(f.ctx, "u1", 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("undo of add left the game: %v", err)
	}

	// Undo expires.
	res = f.patch(3, Change{})
	f.exec(`UPDATE undo_log SET created_at = '2000-01-01T00:00:00Z'`)
	if _, err := f.store.Undo(f.ctx, "u1", res.UndoID); !errors.Is(err, ErrUndoExpired) {
		t.Errorf("expired undo: %v", err)
	}
}

func TestIndexFullAndDelta(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing"), Tags: &[]string{"roguelike"}, Platforms: &[]string{"Switch"}})
	f.patch(2, Change{Status: str("wishlist")})
	full, err := f.store.Index(f.ctx, "u1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !full.Full || len(full.Items) != 2 || full.Rev == 0 {
		t.Fatalf("full index %+v", full)
	}
	row := full.Items[1]
	field := func(name string) any {
		for i, f := range full.Fields {
			if f == name {
				return row[i]
			}
		}
		t.Fatalf("no field %s", name)
		return nil
	}
	if field("name") != "Hades" || field("status") != "playing" || full.PlatformNames[130] != "Nintendo Switch" {
		t.Errorf("entry %v names %v", row, full.PlatformNames)
	}
	rev := full.Rev
	f.patch(2, Change{Rating: num(70)})
	f.store.Delete(f.ctx, "u1", 1)
	delta, _ := f.store.Index(f.ctx, "u1", rev)
	if delta.Full || len(delta.Items) != 1 || delta.Items[0][0] != int64(2) || len(delta.Deleted) != 1 || delta.Deleted[0] != 1 {
		t.Fatalf("delta %+v", delta)
	}
	none, _ := f.store.Index(f.ctx, "u1", delta.Rev)
	if len(none.Items) != 0 || len(none.Deleted) != 0 {
		t.Errorf("no-change delta %+v", none)
	}
	if ahead, _ := f.store.Index(f.ctx, "u1", delta.Rev+100); !ahead.Full {
		t.Errorf("a client ahead of the server must resync fully")
	}
}

func TestListFiltersAndKeysetPaging(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing"), Tags: &[]string{"RPG", "co-op"}, Notes: str("with Sam")})
	f.clock = f.clock.Add(time.Minute)
	f.patch(2, Change{Status: str("backlog"), Tags: &[]string{"platformer"}, Platforms: &[]string{"Switch"}})
	f.clock = f.clock.Add(time.Minute)
	f.patch(3, Change{Status: str("backlog"), Tags: &[]string{"rpg"}})
	f.clock = f.clock.Add(time.Minute)
	f.patch(4, Change{Status: str("paused")})

	ids := func(p Page) string {
		var out []string
		for _, it := range p.Items {
			out = append(out, itoa(it.GameID))
		}
		return strings.Join(out, ",")
	}
	cases := map[string]struct {
		q    ListQuery
		want string
	}{
		"default newest first": {ListQuery{}, "4,3,2,1"},
		"status":               {ListQuery{Filter: Filter{Statuses: []string{"backlog"}}}, "3,2"},
		"paused":               {ListQuery{Filter: Filter{Statuses: []string{"paused"}}}, "4"},
		"tag case-insensitive": {ListQuery{Filter: Filter{Tags: []string{"rpg"}}}, "3,1"},
		"tags and":             {ListQuery{Filter: Filter{Tags: []string{"rpg", "co-op"}}}, "1"},
		"tags or":              {ListQuery{Filter: Filter{Tags: []string{"platformer", "co-op"}, TagOp: "or"}}, "2,1"},
		"q matches notes":      {ListQuery{Filter: Filter{Q: "sam"}}, "1"},
		"q matches name":       {ListQuery{Filter: Filter{Q: "cel"}}, "2"},
		"owned platform":       {ListQuery{Filter: Filter{OwnedPlatform: "switch"}}, "2"},
		"name sort":            {ListQuery{Sort: "name"}, "3,2,1,4"},
		"release old":          {ListQuery{Sort: "release_old"}, "2,4,1,3"},
	}
	for name, c := range cases {
		page, err := f.store.List(f.ctx, "u1", c.q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := ids(page); got != c.want {
			t.Errorf("%s: got %s want %s", name, got, c.want)
		}
	}

	p1, _ := f.store.List(f.ctx, "u1", ListQuery{Limit: 2})
	if p1.Total == nil || *p1.Total != 4 || p1.NextCursor == "" || ids(p1) != "4,3" {
		t.Fatalf("page 1 %+v", p1)
	}
	// An edit that moves an item to the top doesn't shift the next page.
	f.clock = f.clock.Add(time.Minute)
	f.patch(1, Change{Rating: num(50)})
	p2, _ := f.store.List(f.ctx, "u1", ListQuery{Limit: 2, Cursor: p1.NextCursor})
	if ids(p2) != "2" || p2.Total != nil || p2.NextCursor != "" {
		t.Errorf("page 2 %s total=%v next=%q", ids(p2), p2.Total, p2.NextCursor)
	}
	if _, err := f.store.List(f.ctx, "u1", ListQuery{Sort: "name", Cursor: p1.NextCursor}); !errors.Is(err, ErrBadCursor) {
		t.Errorf("cursor with offset sort: %v", err)
	}
}

func TestBulkActionsUndoTogether(t *testing.T) {
	f := newFixture(t)
	for _, id := range []int64{1, 2, 3} {
		f.patch(id, Change{Status: str("backlog"), Tags: &[]string{"old"}})
	}
	res, err := f.store.Bulk(f.ctx, "u1", BulkAction{GameIDs: []int64{1, 2, 99}, Status: str("wishlist"), AddTags: []string{"maybe"}, RemoveTags: []string{"OLD"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 2 {
		t.Errorf("updated %d", res.Updated)
	}
	it, _ := f.store.Get(f.ctx, "u1", 2)
	if it.Status != "wishlist" || strings.Join(it.Tags, ",") != "maybe" {
		t.Errorf("bulk result %+v", it)
	}
	f.store.Undo(f.ctx, "u1", res.UndoID)
	for _, id := range []int64{1, 2} {
		it, _ := f.store.Get(f.ctx, "u1", id)
		if it.Status != "backlog" || strings.Join(it.Tags, ",") != "old" {
			t.Errorf("undo %d: %+v", id, it)
		}
	}
	res, err = f.store.Bulk(f.ctx, "u1", BulkAction{GameIDs: []int64{1, 3}, Remove: true})
	if err != nil || res.Updated != 2 {
		t.Fatalf("bulk remove %v %+v", err, res)
	}
	if n := f.scalar(`SELECT COUNT(*) FROM library_items WHERE user_id = 'u1'`); n != 1 {
		t.Errorf("left %d items", n)
	}
	f.store.Undo(f.ctx, "u1", res.UndoID)
	if n := f.scalar(`SELECT COUNT(*) FROM library_items WHERE user_id = 'u1'`); n != 3 {
		t.Errorf("undo bulk remove left %d items", n)
	}
}

func TestTimerLogsASession(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing")})
	if _, err := f.store.StartTimer(f.ctx, "u1", 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("timer for a game not in the library: %v", err)
	}
	tm, err := f.store.StartTimer(f.ctx, "u1", 1)
	if err != nil || tm.GameID != 1 || tm.GameName != "Hades" {
		t.Fatalf("start %+v %v", tm, err)
	}
	f.clock = f.clock.Add(95 * time.Minute)
	res, err := f.store.StopTimer(f.ctx, "u1", true, 0, "2026-10-14")
	if err != nil {
		t.Fatal(err)
	}
	if res.Session == nil || res.Session.Minutes != 95 || res.Session.Source != "timer" || res.Item.Minutes != 95 {
		t.Errorf("stop %+v", res.Session)
	}
	if tm, _ := f.store.GetTimer(f.ctx, "u1"); tm != nil {
		t.Errorf("timer still running")
	}
	if _, err := f.store.StopTimer(f.ctx, "u1", true, 0, ""); !errors.Is(err, ErrNoTimer) {
		t.Errorf("stop twice: %v", err)
	}
	f.store.StartTimer(f.ctx, "u1", 1)
	f.clock = f.clock.Add(30 * time.Hour)
	res, _ = f.store.StopTimer(f.ctx, "u1", true, 0, "")
	if res.Session.Minutes != MaxSessionMinutes {
		t.Errorf("forgotten timer logged %d minutes", res.Session.Minutes)
	}
	f.invariant(1)
}

func TestStats(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing")})
	for _, d := range []string{"2026-10-10", "2026-10-11", "2026-10-13", "2026-10-14"} {
		f.store.LogSession(f.ctx, "u1", 1, 60, d, "")
	}
	f.patch(1, Change{Status: str("completed"), Rating: num(90), Completion: str("main")})
	f.patch(2, Change{Status: str("backlog")})
	f.exec(`UPDATE library_items SET created_at = '2023-01-01T00:00:00Z' WHERE game_id = 2`)
	f.patch(3, Change{Status: str("wishlist"), Rating: num(45)})
	st, err := f.store.StatsFor(f.ctx, "u1", 2026, "2026-10-14")
	if err != nil {
		t.Fatal(err)
	}
	if st.YearMinutes != 240 || st.YearSessions != 4 || st.YearDaysPlayd != 4 || st.YearFinished != 1 {
		t.Errorf("year totals %+v", st)
	}
	if len(st.Heatmap) != 4 || st.Heatmap[0].Day != "2026-10-10" {
		t.Errorf("heatmap %+v", st.Heatmap)
	}
	if st.StreakCurrent != 2 || st.StreakLongest != 2 {
		t.Errorf("streaks %d/%d", st.StreakCurrent, st.StreakLongest)
	}
	if st.RatingBuckets[8] != 1 || st.RatingBuckets[4] != 1 {
		t.Errorf("rating buckets %v", st.RatingBuckets)
	}
	if len(st.ByYear) == 0 || st.ByYear[0].ByCompletion["main"] != 1 {
		t.Errorf("by year %+v", st.ByYear)
	}
	if st.BacklogOver2y != 1 || len(st.BacklogOldest) != 1 {
		t.Errorf("backlog age %d %+v", st.BacklogOver2y, st.BacklogOldest)
	}
	if st.LongestSession == nil || st.LongestSession.Minutes != 60 {
		t.Errorf("longest %+v", st.LongestSession)
	}
	if len(st.HoursByMonth) != 12 || st.HoursByMonth[11].Month != "2026-10" || st.HoursByMonth[11].Minutes != 240 {
		t.Errorf("months %+v", st.HoursByMonth)
	}
}

func TestExports(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing"), Notes: str("=HYPERLINK(\"x\")"), Tags: &[]string{"+bad"}})
	f.store.LogSession(f.ctx, "u1", 1, 30, "2026-10-01", "")
	var buf bytes.Buffer
	if err := f.store.WriteCSV(f.ctx, "u1", &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "'=HYPERLINK") || !strings.Contains(out, "'+bad") {
		t.Errorf("csv not formula-safe:\n%s", out)
	}
	all, err := f.store.ExportAll(f.ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || len(all[0].Sessions) != 1 || len(all[0].Playthroughs) != 1 {
		t.Errorf("json export %+v", all)
	}
}
