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
	if n != 2 {
		t.Fatalf("platform count after sync = %d, want 2", n)
	}

	// Second sync is a no-op — the reference list is fetched only once.
	if err := svc.SyncPlatforms(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if fake.platformCalls != 1 {
		t.Errorf("GetPlatforms called %d times, want 1 (table already populated)", fake.platformCalls)
	}

	// Search results carry resolved platform names.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, platforms_json)
		VALUES (1, 'Hollow Knight', 'hollow-knight', 'hollow knight', '[6,130]')`)
	results, err := store.SearchLocal(ctx, "hollow", 10)
	if err != nil || len(results) != 1 {
		t.Fatalf("search: %d results, %v", len(results), err)
	}
	want := []string{"PC (Microsoft Windows)", "Nintendo Switch"}
	if !reflect.DeepEqual(results[0].Platforms, want) {
		t.Errorf("search platforms = %v, want %v", results[0].Platforms, want)
	}
}
