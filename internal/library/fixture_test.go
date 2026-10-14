package library

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"cato/internal/catalog"
	"cato/internal/db"
)

// The production-shaped fixture (docs/cato_smooth.md §11): ~300k catalog
// games and a 5k-item library, built at schema v17 and upgraded, then timed.
// It takes a few seconds, so -short skips it; make test runs it.

const (
	fixtureGames   = 300_000
	fixtureLibrary = 5_000
)

func buildFixture(t testing.TB) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.MigrateTo(database, 17); err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`INSERT INTO users (id, email) VALUES ('owner', 'owner@example.com'), ('guest', 'guest@example.com')`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ` + itoa(fixtureLibrary) + `)
		 INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes, started_at, completed_at, created_at, updated_at, owned_platforms_json)
		 SELECT 'owner', i * 37, CASE i % 5 WHEN 0 THEN 'wishlist' WHEN 1 THEN 'backlog' WHEN 2 THEN 'playing' WHEN 3 THEN 'completed' ELSE 'abandoned' END,
		        (i % 11) * 10, (i % 4) * 600, CASE WHEN i % 3 = 0 THEN '["rpg","co-op"]' ELSE '["indie"]' END, 'note ' || i,
		        CASE WHEN i % 5 IN (2, 3, 4) THEN '2024-01-01T00:00:00Z' END, CASE WHEN i % 5 = 3 THEN '2024-06-01T00:00:00Z' END,
		        '2023-01-01T00:00:00Z', strftime('%Y-%m-%dT%H:%M:%SZ', 1700000000 + i * 60, 'unixepoch'), '["Nintendo Switch"]'
		 FROM n`,
		`INSERT INTO library_items (user_id, game_id, status) SELECT 'guest', id, 'backlog' FROM games WHERE id <= 200`,
	}
	if _, err := database.Exec(stmts[0]); err != nil {
		t.Fatal(err)
	}
	insertCatalog(t, database)
	for _, q := range stmts[1:] {
		if _, err := database.Exec(q); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	return database
}

// fixtureWords is a game-title vocabulary; titles draw 1–4 words with a
// skewed distribution (early words are common, as "the", "of", "war" are),
// so trigram and prefix match counts resemble a real catalog.
var fixtureWords = strings.Fields(`the of legend war dark star world space dragon king super shadow
	quest final night city lost fire blood fantasy tales age racing soccer simulator battle hero heroes
	empire kingdom chronicles island ghost knight dead zombie wars galaxy ultimate land lord legends
	street fighter mario zelda sonic metal gear crash spyro halo forza souls elden ring hollow celeste
	hades balatro witcher cyber punk portal half life doom quake fallout skyrim oblivion morrowind mass
	effect dragon age baldur gate persona yakuza tekken mortal kombat street pokemon kirby metroid
	donkey kong pikmin animal crossing splatoon xenoblade fire emblem octopath bravely trials mana
	secret ocean river mountain forest desert winter summer spring autumn moon sun sky storm thunder
	crystal iron steel gold silver diamond ruby emerald sapphire red blue green black white orange
	tactics arena rivals masters champions league cup grand prix rally drift tokyo paris london
	neon retro pixel tiny little big giant great small brave wild free last first new old ancient
	eternal infinite endless broken hidden forgotten silent burning frozen golden crimson azure
	ninja samurai pirate cowboy robot alien wizard witch vampire monster beast hunter hunters
	escape survival horror mystery puzzle adventure odyssey journey saga story tale song dream`)

func insertCatalog(t testing.TB, database *db.DB) {
	t.Helper()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO games (id, name, slug, normalized_name, platforms_json, first_release_date, cover_url, popularity_score, aggregated_rating, aggregated_rating_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	seed := uint64(42)
	next := func() uint64 { // xorshift: deterministic, fast
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	pick := func() string {
		// Skewed: squaring a uniform value favours the first words.
		u := float64(next()%1_000_000) / 1_000_000
		return fixtureWords[int(u*u*float64(len(fixtureWords)))]
	}
	for i := 1; i <= fixtureGames; i++ {
		n := 1 + int(next()%4)
		words := make([]string, n)
		for w := range words {
			words[w] = pick()
		}
		name := strings.Join(words, " ")
		if next()%5 == 0 {
			name += " " + itoa(int64(2+next()%5))
		}
		title := strings.ToUpper(name[:1]) + name[1:]
		if _, err := stmt.Exec(i, title, "g-"+itoa(int64(i)), name, "["+itoa(int64(6+i%5))+","+itoa(int64(130+i%3))+"]",
			946684800+int64(i%9000)*86400, "https://images.igdb.com/igdb/image/upload/t_cover_big/co"+itoa(int64(i))+".jpg",
			int64(next()%5000), int64(i%100), int64(i%50)); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func p95(samples []time.Duration) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[len(samples)*95/100]
}

// budget scales timing gates on slow CI machines (CATO_PERF_SLACK=2).
func budget(d time.Duration) time.Duration {
	if os.Getenv("CATO_PERF_SLACK") == "2" {
		return d * 2
	}
	return d
}

func TestProductionShapedMigrationAndPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("production-shaped fixture is slow; run without -short")
	}
	if raceEnabled {
		t.Skip("timing gates are meaningless under the race detector")
	}
	database := buildFixture(t)
	ctx := context.Background()

	var before, beforeMinutes int64
	database.QueryRow(`SELECT COUNT(*), SUM(playtime_minutes) FROM library_items`).Scan(&before, &beforeMinutes)

	start := time.Now()
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	migration := time.Since(start)
	t.Logf("v17→latest on %d games / %d items: %v", fixtureGames, before, migration)
	if migration > budget(60*time.Second) {
		t.Errorf("migration took %v", migration)
	}

	var after, afterMinutes, sessionMinutes int64
	database.QueryRow(`SELECT COUNT(*), SUM(playtime_minutes) FROM library_items`).Scan(&after, &afterMinutes)
	database.QueryRow(`SELECT SUM(minutes) FROM play_sessions`).Scan(&sessionMinutes)
	if after != before || afterMinutes != beforeMinutes || sessionMinutes != beforeMinutes {
		t.Fatalf("items %d→%d, minutes %d→%d, sessions %d", before, after, beforeMinutes, afterMinutes, sessionMinutes)
	}
	var integrity string
	database.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)
	if integrity != "ok" {
		t.Fatalf("integrity_check: %s", integrity)
	}
	if rows, err := database.Query(`PRAGMA foreign_key_check`); err != nil {
		t.Fatal(err)
	} else {
		if rows.Next() {
			t.Error("foreign key violations after migration")
		}
		rows.Close()
	}
	if err := database.Optimize(ctx); err != nil {
		t.Fatal(err)
	}

	games := catalog.NewStore(database)
	store := NewStore(database, games)

	// Library index: full sync < 150 ms, delta < 20 ms (5k items).
	var fullTimes, deltaTimes []time.Duration
	for i := 0; i < 20; i++ {
		s := time.Now()
		idx, err := store.Index(ctx, "owner", 0)
		if err != nil || len(idx.Items) != fixtureLibrary {
			t.Fatalf("index: %v %d", err, len(idx.Items))
		}
		fullTimes = append(fullTimes, time.Since(s))
		rev := idx.Rev
		store.Patch(ctx, "owner", 37*int64(i+1), Change{Notes: ptr("touched")})
		s = time.Now()
		delta, err := store.Index(ctx, "owner", rev)
		if err != nil || len(delta.Items) != 1 {
			t.Fatalf("delta: %v %d", err, len(delta.Items))
		}
		deltaTimes = append(deltaTimes, time.Since(s))
	}
	t.Logf("index full p95 %v, delta p95 %v", p95(fullTimes), p95(deltaTimes))
	if d := p95(fullTimes); d > budget(150*time.Millisecond) {
		t.Errorf("full index sync p95 %v > 150ms", d)
	}
	if d := p95(deltaTimes); d > budget(20*time.Millisecond) {
		t.Errorf("delta index sync p95 %v > 20ms", d)
	}

	// Library page p95 < 25 ms across the common views.
	queries := []ListQuery{{}, {Filter: Filter{Statuses: []string{"backlog"}}}, {Sort: "name"}, {Filter: Filter{Tags: []string{"rpg"}}},
		{Filter: Filter{Q: "dragon"}}, {Sort: "rating"}}
	var pageTimes []time.Duration
	for i := 0; i < 10; i++ {
		for _, q := range queries {
			s := time.Now()
			if _, err := store.List(ctx, "owner", q); err != nil {
				t.Fatal(err)
			}
			pageTimes = append(pageTimes, time.Since(s))
		}
	}
	t.Logf("library page p95 %v", p95(pageTimes))
	if d := p95(pageTimes); d > budget(25*time.Millisecond) {
		t.Errorf("library page p95 %v > 25ms", d)
	}

	// Game page (catalog row + your item + journal) < 40 ms.
	var gameTimes []time.Duration
	for i := 1; i <= 50; i++ {
		id := int64(i * 37)
		s := time.Now()
		if _, err := games.GetByID(ctx, id); err != nil {
			t.Fatal(err)
		}
		store.Get(ctx, "owner", id)
		store.Playthroughs(ctx, "owner", id)
		store.Sessions(ctx, "owner", SessionRange{GameID: id, Limit: 50})
		gameTimes = append(gameTimes, time.Since(s))
	}
	t.Logf("game page p95 %v", p95(gameTimes))
	if d := p95(gameTimes); d > budget(40*time.Millisecond) {
		t.Errorf("game page p95 %v > 40ms", d)
	}

	// Typeahead p95 < 30 ms over the 300k catalog.
	var typeTimes []time.Duration
	for _, q := range []string{"th", "the", "the legend", "war", "dragon quest", "zelda", "mario kart", "ze", "final fantasy 7", "dark souls", "qq", "stor"} {
		for i := 0; i < 5; i++ {
			s := time.Now()
			if _, err := games.SearchLocal(ctx, q, 10); err != nil {
				t.Fatal(err)
			}
			typeTimes = append(typeTimes, time.Since(s))
		}
		t.Logf("typeahead %q: %v", q, typeTimes[len(typeTimes)-1])
	}
	t.Logf("typeahead p95 %v", p95(typeTimes))
	if d := p95(typeTimes); d > budget(30*time.Millisecond) {
		t.Errorf("typeahead p95 %v > 30ms", d)
	}
}

func ptr[T any](v T) *T { return &v }
