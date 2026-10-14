package library

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Behaviours the legacy /api/library tests guarded, now owned by the store.

func TestTimestampSemantics(t *testing.T) {
	f := newFixture(t)
	// Moving into Playing auto-sets started_at; into Finished, completed_at.
	res := f.patch(1, Change{Status: str("playing")})
	if res.Item.StartedAt == nil || res.Item.CompletedAt != nil {
		t.Fatalf("playing: %v %v", res.Item.StartedAt, res.Item.CompletedAt)
	}
	started := *res.Item.StartedAt
	f.clock = f.clock.Add(time.Hour)
	res = f.patch(1, Change{Status: str("completed")})
	if res.Item.CompletedAt == nil || *res.Item.StartedAt != started {
		t.Fatalf("completed: %v %v", res.Item.StartedAt, res.Item.CompletedAt)
	}
	completed := *res.Item.CompletedAt
	// Unrelated edits keep both dates.
	res = f.patch(1, Change{Notes: str("great"), Rating: num(80)})
	if *res.Item.StartedAt != started || *res.Item.CompletedAt != completed {
		t.Errorf("dates changed by an unrelated edit")
	}
	// Explicit values are normalized to UTC RFC 3339.
	res = f.patch(1, Change{StartedAt: str("2024-01-02T03:04:05+02:00")})
	if *res.Item.StartedAt != "2024-01-02T01:04:05Z" {
		t.Errorf("normalized %q", *res.Item.StartedAt)
	}
	res = f.patch(1, Change{CompletedAt: str("2024-02-03")})
	if *res.Item.CompletedAt != "2024-02-03T00:00:00Z" {
		t.Errorf("day %q", *res.Item.CompletedAt)
	}
	// "" clears; a later status change doesn't resurrect a cleared date.
	res = f.patch(1, Change{CompletedAt: str("")})
	if res.Item.CompletedAt != nil {
		t.Errorf("clear failed")
	}
	res = f.patch(1, Change{Rating: num(90)})
	if res.Item.CompletedAt != nil {
		t.Errorf("cleared completed_at came back")
	}
	if _, err := f.store.Patch(f.ctx, "u1", 1, Change{StartedAt: str("next tuesday")}); err == nil {
		t.Error("invalid timestamp accepted")
	}
}

func TestPatchSemantics(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("backlog"), Rating: num(70), Notes: str("n"), Tags: &[]string{"rpg"}, Platforms: &[]string{"Switch"}, Medium: str("physical")})
	// Only provided fields change.
	res := f.patch(1, Change{Status: str("playing")})
	it := res.Item
	if it.Rating != 70 || it.Notes != "n" || strings.Join(it.Tags, ",") != "rpg" || strings.Join(it.Platforms, ",") != "Switch" || it.Medium != "physical" {
		t.Fatalf("patch touched other fields: %+v", it)
	}
	// Rating and notes can be cleared explicitly.
	it = f.patch(1, Change{Rating: num(0), Notes: str("")}).Item
	if it.Rating != 0 || it.Notes != "" {
		t.Errorf("clear: %+v", it)
	}
	// Ownership: several platforms, the legacy single setter, clearing.
	it = f.patch(1, Change{Platforms: &[]string{"PS5", " ps5 ", "PC"}}).Item
	if strings.Join(it.Platforms, ",") != "PS5,PC" {
		t.Errorf("platforms dedupe: %v", it.Platforms)
	}
	it = f.patch(1, Change{Platform: str("Switch 2")}).Item
	if strings.Join(it.Platforms, ",") != "Switch 2" {
		t.Errorf("legacy setter: %v", it.Platforms)
	}
	it = f.patch(1, Change{Platforms: &[]string{}, Medium: str("")}).Item
	if len(it.Platforms) != 0 || it.Medium != "" {
		t.Errorf("clear ownership: %+v", it)
	}
	for name, ch := range map[string]Change{
		"medium":        {Medium: str("cartridge")},
		"long platform": {Platforms: &[]string{strings.Repeat("x", 65)}},
		"status":        {Status: str("finished")},
	} {
		var ve *ValidationError
		if _, err := f.store.Patch(f.ctx, "u1", 1, ch); !errors.As(err, &ve) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestFieldLimits(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{})
	many := make([]string, 33)
	for i := range many {
		many[i] = "t" + itoa(int64(i))
	}
	plats := make([]string, 33)
	for i := range plats {
		plats[i] = "P" + itoa(int64(i))
	}
	for name, ch := range map[string]Change{
		"notes":     {Notes: str(strings.Repeat("é", MaxNotesChars+1))},
		"tags":      {Tags: &many},
		"tag chars": {Tags: &[]string{strings.Repeat("a", 41)}},
		"platforms": {Platforms: &plats},
	} {
		var ve *ValidationError
		if _, err := f.store.Patch(f.ctx, "u1", 1, ch); !errors.As(err, &ve) {
			t.Errorf("%s over the limit accepted: %v", name, err)
		}
	}
	// At the limits is fine (characters, not bytes).
	if _, err := f.store.Patch(f.ctx, "u1", 1, Change{Notes: str(strings.Repeat("é", MaxNotesChars)), Tags: &[]string{strings.Repeat("a", 40)}}); err != nil {
		t.Errorf("at the limits: %v", err)
	}
}

func TestConcurrentEditsKeepLoggedTime(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Status: str("playing")})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			f.store.LogSession(f.ctx, "u1", 1, 15, "", "")
		}()
		go func(i int) {
			defer wg.Done()
			f.store.Patch(f.ctx, "u1", 1, Change{Notes: str("edit " + itoa(int64(i))), Rating: num(int64(i % 10 * 10))})
		}(i)
	}
	wg.Wait()
	if got := f.scalar(`SELECT playtime_minutes FROM library_items WHERE game_id = 1`); got != 300 {
		t.Fatalf("total %d, want 300: a field edit overwrote logged time", got)
	}
	f.invariant(1)
}

func TestSortsAndFormatFilter(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{Rating: num(90), Medium: str("digital"), Platforms: &[]string{"Switch"}})
	f.clock = f.clock.Add(time.Minute)
	f.patch(2, Change{Rating: num(0), Medium: str("physical"), Platforms: &[]string{"PC"}})
	f.clock = f.clock.Add(time.Minute)
	f.patch(3, Change{Rating: num(40)})
	ids := func(q ListQuery) string {
		p, err := f.store.List(f.ctx, "u1", q)
		if err != nil {
			t.Fatal(err)
		}
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
		"rating":           {ListQuery{Sort: "rating"}, "1,3,2"},
		"rating low":       {ListQuery{Sort: "rating_low"}, "3,1,2"},
		"format digital":   {ListQuery{Filter: Filter{Format: "digital"}}, "1"},
		"format both":      {ListQuery{Filter: Filter{Format: "both"}}, "2,1"},
		"format none":      {ListQuery{Filter: Filter{Format: "none"}}, "3"},
		"owned platform":   {ListQuery{Sort: "platform"}, "2,1,3"},
		"available on":     {ListQuery{Filter: Filter{Platform: "switch"}}, "2,1"},
		"available sort":   {ListQuery{Sort: "available_platform"}, "2,1,3"},
		"legacy sort name": {ListQuery{Sort: "my_rating"}, "1,3,2"},
	}
	for name, c := range cases {
		if got := ids(c.q); got != c.want {
			t.Errorf("%s: got %s want %s", name, got, c.want)
		}
	}
	tags, _ := f.store.Tags(f.ctx, "u1", "", 10)
	if len(tags) != 0 {
		t.Errorf("tags %v", tags)
	}
	f.patch(1, Change{Tags: &[]string{"RPG", "co-op"}})
	f.patch(2, Change{Tags: &[]string{"rpg"}})
	tags, _ = f.store.Tags(f.ctx, "u1", "r", 10)
	if len(tags) != 1 || !strings.EqualFold(tags[0], "rpg") {
		t.Errorf("tag autocomplete dedupes case: %v", tags)
	}
}

// The available-platform sort aggregates only the caller's library, not
// the whole catalog (F5).
func TestAvailablePlatformSortScansOnlyTheLibrary(t *testing.T) {
	f := newFixture(t)
	f.patch(1, Change{})
	rows, err := f.db.Query(`EXPLAIN QUERY PLAN SELECT 1 FROM library_items li JOIN games g ON g.id = li.game_id`+availablePlatformJoin+` WHERE li.user_id = ?`, "u1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var a, b, c int
		var d string
		rows.Scan(&a, &b, &c, &d)
		plan.WriteString(d + "\n")
	}
	if strings.Contains(plan.String(), "SCAN gp") || strings.Contains(plan.String(), "SCAN game_platforms") {
		t.Errorf("available platform join scans the catalog:\n%s", plan.String())
	}
}

func TestOnWriteWakesCoverWorker(t *testing.T) {
	f := newFixture(t)
	woke := 0
	f.store.OnWrite = func() { woke++ }
	f.patch(1, Change{})
	if woke != 1 {
		t.Errorf("OnWrite ran %d times", woke)
	}
}
