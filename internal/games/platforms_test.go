package games

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestResolvePlatformNames(t *testing.T) {
	names := map[int64]string{
		6:   "PC (Microsoft Windows)",
		130: "Nintendo Switch",
	}

	t.Run("numeric IDs resolve via lookup", func(t *testing.T) {
		got := ResolvePlatformNames("[6,130]", names)
		want := []string{"PC (Microsoft Windows)", "Nintendo Switch"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("unknown IDs dropped, known kept", func(t *testing.T) {
		got := ResolvePlatformNames("[6,99999]", names)
		if !reflect.DeepEqual(got, []string{"PC (Microsoft Windows)"}) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("legacy name strings pass through", func(t *testing.T) {
		got := ResolvePlatformNames(`["PC (Microsoft Windows)","Nintendo Switch"]`, names)
		want := []string{"PC (Microsoft Windows)", "Nintendo Switch"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("mixed shapes", func(t *testing.T) {
		got := ResolvePlatformNames(`[130,"Steam Deck"]`, names)
		if !reflect.DeepEqual(got, []string{"Nintendo Switch", "Steam Deck"}) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("empty and invalid inputs yield nil", func(t *testing.T) {
		for _, raw := range []string{"", "[]", "null", "not json"} {
			if got := ResolvePlatformNames(raw, names); got != nil {
				t.Errorf("ResolvePlatformNames(%q) = %v, want nil", raw, got)
			}
		}
	})
}

func TestUpsertPlatformsAndNames(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	ctx := context.Background()
	err := store.UpsertPlatforms(ctx, []Platform{
		{ID: 6, Name: "PC (Microsoft Windows)", Abbreviation: "win"},
		{ID: 130, Name: "Nintendo Switch", Abbreviation: "swi"},
	})
	if err != nil {
		t.Fatalf("upsert platforms: %v", err)
	}

	// Re-upsert with a changed name — must replace, not duplicate or error.
	err = store.UpsertPlatforms(ctx, []Platform{{ID: 130, Name: "Nintendo Switch 2", Abbreviation: "s2"}})
	if err != nil {
		t.Fatalf("re-upsert platforms: %v", err)
	}

	n, err := store.CountPlatforms(ctx)
	if err != nil || n != 2 {
		t.Fatalf("count platforms = %d, %v; want 2, nil", n, err)
	}

	got, err := store.PlatformNames(ctx)
	if err != nil {
		t.Fatalf("platform names: %v", err)
	}
	want := map[int64]string{6: "PC (Microsoft Windows)", 130: "Nintendo Switch 2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}

func TestSyncPlatforms(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	fake := &fakeIGDB{}
	svc := NewService(store, fake, database)
	ctx := context.Background()

	if err := svc.SyncPlatforms(ctx); err != nil {
		t.Fatalf("sync platforms: %v", err)
	}
	n, _ := store.CountPlatforms(ctx)
	if n != 3 {
		t.Fatalf("platform count after sync = %d, want 3", n)
	}

	// Second sync is a no-op — the reference list is fetched only once.
	if err := svc.SyncPlatforms(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if fake.platformCalls != 1 {
		t.Errorf("GetPlatforms called %d times, want 1 (table already populated)", fake.platformCalls)
	}

	// Search results carry resolved platform names.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score, platforms_json)
		VALUES (1, 'Hollow Knight', 'hollow-knight', 'hollow knight', 50, '[6,130]'),
		       (9, 'Switch 2 Launch Title', 'switch-2-launch', 'switch 2 launch title', 50, '[508]')`)
	results, err := store.SearchLocal(ctx, "hollow", 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("search: %d results, %v", len(results), err)
	}
	want := []string{"PC (Microsoft Windows)", "Nintendo Switch"}
	if !reflect.DeepEqual(results[0].Platforms, want) {
		t.Errorf("search platforms = %v, want %v", results[0].Platforms, want)
	}

	// Curated shortnames are applied by SyncPlatforms: the Switch 2 game is
	// findable by "sw2" even though IGDB's abbreviation is "Switch 2".
	_, total, err := svc.SearchPagedFull(ctx, "title", 10, 0, "", 0, 0, 0, "sw2", false)
	if err != nil || total != 1 {
		t.Errorf("shortname filter sw2: total=%d err=%v, want 1", total, err)
	}
}

func TestMigratePlatformTags(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'u1@test.com')`)
	for id := 1; id <= 5; id++ {
		database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (?, ?, ?, ?)`,
			id, fmt.Sprintf("Game %d", id), fmt.Sprintf("game-%d", id), fmt.Sprintf("game %d", id))
	}
	database.Exec(`INSERT INTO library_items (user_id, game_id, status, tags_json) VALUES
		('u1', 1, 'backlog', '["Switch","rpg"]'),
		('u1', 2, 'playing', '["Steam","PS5"]'),
		('u1', 3, 'backlog', '["PS4","X1"]'),
		('u1', 4, 'completed', '["GOTY","Nostalgia"]'),
		('u1', 5, 'wishlist', '[]')`)

	migrated, err := MigratePlatformTags(database)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated != 3 {
		t.Fatalf("migrated = %d, want 3", migrated)
	}

	var platform, tagsJSON, ownedJSON string
	database.QueryRow(`SELECT platform, tags_json, owned_platforms_json FROM library_items WHERE game_id = 1`).
		Scan(&platform, &tagsJSON, &ownedJSON)
	if platform != "Nintendo Switch" || tagsJSON != `["rpg"]` || ownedJSON != `["Nintendo Switch"]` {
		t.Errorf("item 1: platform=%q tags=%s owned=%s", platform, tagsJSON, ownedJSON)
	}

	database.QueryRow(`SELECT platform, tags_json, owned_platforms_json FROM library_items WHERE game_id = 2`).
		Scan(&platform, &tagsJSON, &ownedJSON)
	if platform != "PlayStation 5" || tagsJSON != `["Steam"]` || ownedJSON != `["PlayStation 5"]` {
		t.Errorf("item 2: platform=%q tags=%s owned=%s (storefront tag must survive)", platform, tagsJSON, ownedJSON)
	}

	// Multi-ownership: PS4+X1 both migrate into the owned list; the legacy
	// singular column keeps the first entry.
	database.QueryRow(`SELECT platform, tags_json, owned_platforms_json FROM library_items WHERE game_id = 3`).
		Scan(&platform, &tagsJSON, &ownedJSON)
	if platform != "PlayStation 4" || tagsJSON != `[]` || ownedJSON != `["PlayStation 4","Xbox One"]` {
		t.Errorf("item 3: platform=%q tags=%s owned=%s", platform, tagsJSON, ownedJSON)
	}
	// Unmapped-only items (GOTY/Nostalgia) stay untouched.
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM library_items WHERE platform != ''`).Scan(&n)
	if n != 3 {
		t.Errorf("%d items have platform set, want exactly the 3 migrated ones", n)
	}

	// Idempotent: second run finds nothing left to do.
	again, err := MigratePlatformTags(database)
	if err != nil || again != 0 {
		t.Errorf("second run migrated %d items (err=%v), want 0", again, err)
	}
}

func TestSearchPlatformFilter(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, popularity_score, platforms_json) VALUES
		(1, 'Hollow Knight', 'hollow-knight', 'hollow knight', 50, '[6,130]'),
		(2, 'Celeste', 'celeste', 'celeste', 50, '[6,508,99999]'),
		(3, 'Super Mario Odyssey', 'super-mario-odyssey', 'super mario odyssey', 50, '[130]'),
		(4, 'Legacy Entry', 'legacy-entry', 'legacy entry', 50, '["PC (Microsoft Windows)"]'),
		(5, 'Xbox Exclusive', 'xbox-exclusive', 'xbox exclusive', 50, '[169]')`)
	database.Exec(`INSERT INTO platforms (id, name, abbreviation) VALUES
		(6, 'PC (Microsoft Windows)', 'PC'),
		(508, 'Nintendo Switch 2', 'Switch 2'),
		(130, 'Nintendo Switch', 'Switch'),
		(169, 'Xbox Series X|S', 'Series X|S')`)

	svc := NewService(store, &fakeIGDB{}, database)
	ctx := context.Background()
	if err := store.ApplyPlatformShortnames(ctx); err != nil {
		t.Fatalf("apply shortnames: %v", err)
	}

	// Substring match against resolved names ("switch" hits both Switches).
	_, total, err := svc.SearchPagedFull(ctx, "mario", 10, 0, "", 0, 0, 0, "switch", false)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (Super Mario Odyssey)", total)
	}

	// Curated shortname match ("win" → PC even though IGDB's abbr is "PC").
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "win", false)
	if err != nil || total != 1 {
		t.Errorf("abbrev filter: total=%d err=%v, want 1", total, err)
	}

	// Gamer-style codes: sw2/switch2 → Nintendo Switch 2 (Celeste), xsx →
	// Xbox Series X|S.
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "sw2", false)
	if err != nil || total != 1 {
		t.Errorf("shortname sw2: total=%d err=%v, want 1", total, err)
	}
	_, total, err = svc.SearchPagedFull(ctx, "xbox exclusive", 10, 0, "", 0, 0, 0, "xsx", false)
	if err != nil || total != 1 {
		t.Errorf("shortname xsx: total=%d err=%v, want 1", total, err)
	}

	// Legacy string rows (names instead of IDs) match by text.
	_, total, err = svc.SearchPagedFull(ctx, "legacy", 10, 0, "", 0, 0, 0, "pc", false)
	if err != nil || total != 1 {
		t.Errorf("legacy row filter: total=%d err=%v, want 1", total, err)
	}

	// Unknown IDs match nothing even when the raw text would ("99999").
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "99999", false)
	if err != nil || total != 0 {
		t.Errorf("unknown id filter: total=%d err=%v, want 0", total, err)
	}

	// Empty platform = no filtering.
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "", false)
	if err != nil || total != 1 {
		t.Errorf("no filter: total=%d err=%v, want 1", total, err)
	}
}
