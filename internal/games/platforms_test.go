package games

import (
	"context"
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
	_, total, err := svc.SearchPagedFull(ctx, "title", 10, 0, "", 0, 0, 0, "sw2")
	if err != nil || total != 1 {
		t.Errorf("shortname filter sw2: total=%d err=%v, want 1", total, err)
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
	_, total, err := svc.SearchPagedFull(ctx, "mario", 10, 0, "", 0, 0, 0, "switch")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (Super Mario Odyssey)", total)
	}

	// Curated shortname match ("win" → PC even though IGDB's abbr is "PC").
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "win")
	if err != nil || total != 1 {
		t.Errorf("abbrev filter: total=%d err=%v, want 1", total, err)
	}

	// Gamer-style codes: sw2/switch2 → Nintendo Switch 2 (Celeste), xsx →
	// Xbox Series X|S.
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "sw2")
	if err != nil || total != 1 {
		t.Errorf("shortname sw2: total=%d err=%v, want 1", total, err)
	}
	_, total, err = svc.SearchPagedFull(ctx, "xbox exclusive", 10, 0, "", 0, 0, 0, "xsx")
	if err != nil || total != 1 {
		t.Errorf("shortname xsx: total=%d err=%v, want 1", total, err)
	}

	// Legacy string rows (names instead of IDs) match by text.
	_, total, err = svc.SearchPagedFull(ctx, "legacy", 10, 0, "", 0, 0, 0, "pc")
	if err != nil || total != 1 {
		t.Errorf("legacy row filter: total=%d err=%v, want 1", total, err)
	}

	// Unknown IDs match nothing even when the raw text would ("99999").
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "99999")
	if err != nil || total != 0 {
		t.Errorf("unknown id filter: total=%d err=%v, want 0", total, err)
	}

	// Empty platform = no filtering.
	_, total, err = svc.SearchPagedFull(ctx, "celeste", 10, 0, "", 0, 0, 0, "")
	if err != nil || total != 1 {
		t.Errorf("no filter: total=%d err=%v, want 1", total, err)
	}
}
