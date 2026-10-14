package catalog

import (
	"context"
	"testing"
)

func TestCustomGamesAndMerge(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()
	database.Exec(`INSERT INTO users (id, email) VALUES ('a', 'a@x'), ('b', 'b@x')`)
	id, err := store.CreateCustom(ctx, "a", CustomGame{Name: "Homebrew Quest", Platform: "Nintendo Switch", Year: 2024})
	if err != nil || id >= 0 {
		t.Fatalf("create: %d %v", id, err)
	}
	id2, _ := store.CreateCustom(ctx, "a", CustomGame{Name: "Demo"})
	if id2 != id-1 {
		t.Errorf("IDs count down from -1: %d then %d", id, id2)
	}
	if owner, _ := store.CustomOwner(ctx, id); owner != "a" {
		t.Errorf("owner %q", owner)
	}
	if _, err := store.CreateCustom(ctx, "a", CustomGame{Name: "  "}); err == nil {
		t.Error("empty name accepted")
	}
	g, _ := store.GetByID(ctx, id)
	if g == nil || g.Name != "Homebrew Quest" || g.FirstReleaseDate == 0 {
		t.Fatalf("custom row %+v", g)
	}
	// Searchable like any game.
	res, _ := store.SearchLocal(ctx, "homebrew", 5)
	if len(res) != 1 || res[0].ID != id {
		t.Errorf("search %+v", res)
	}

	// Merge into a catalog game: user a's item and sessions move; user b
	// already has the target and keeps it, gaining b's custom time.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (500, 'Real Quest', 'real-quest', 'real quest')`)
	database.Exec(`INSERT INTO library_items (user_id, game_id, status, rating) VALUES ('a', ?, 'playing', 80), ('b', ?, 'backlog', 0), ('b', 500, 'completed', 90)`, id, id)
	database.Exec(`INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('a', ?, '2026-10-01', 60), ('b', ?, '2026-10-02', 30), ('b', 500, '2026-09-01', 100)`, id, id)
	if err := store.MergeCustom(ctx, id, 500); err != nil {
		t.Fatal(err)
	}
	var status string
	var rating, minutes int64
	database.QueryRow(`SELECT status, rating, playtime_minutes FROM library_items WHERE user_id = 'a' AND game_id = 500`).Scan(&status, &rating, &minutes)
	if status != "playing" || rating != 80 || minutes != 60 {
		t.Errorf("a's merged item %s %d %d", status, rating, minutes)
	}
	database.QueryRow(`SELECT status, playtime_minutes FROM library_items WHERE user_id = 'b' AND game_id = 500`).Scan(&status, &minutes)
	if status != "completed" || minutes != 130 {
		t.Errorf("b keeps the target item with both times: %s %d", status, minutes)
	}
	var left int
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE id = ?`, id).Scan(&left)
	if left != 0 {
		t.Error("custom game not removed")
	}

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, version_parent) VALUES (501, 'Real Quest Deluxe', 'rqd', 'real quest deluxe', 500), (502, 'Orphan Edition', 'oe', 'orphan edition', 9999)`)
	if base, _ := store.BaseGame(ctx, 501); base != 500 {
		t.Errorf("edition base %d", base)
	}
	if base, _ := store.BaseGame(ctx, 502); base != 502 {
		t.Errorf("edition with an unknown parent stays itself: %d", base)
	}
	if base, _ := store.BaseGame(ctx, 500); base != 500 {
		t.Errorf("base game %d", base)
	}
}
