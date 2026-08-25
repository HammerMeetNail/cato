package games

import (
	"context"
	"testing"
	"time"

	"cato/internal/db"
)

// yearUnix converts a year to the unix bounds used by searchOptions
// yearFrom/yearTo (mirrors parseYearParam in internal/http).
func yearUnix(t *testing.T, y int, end bool) int64 {
	t.Helper()
	if !end {
		return time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
	return time.Date(y+1, time.January, 1, 0, 0, 0, 0, time.UTC).Unix() - 1
}

// seedZelda inserts the canonical alias-test fixture: a hugely popular main
// game plus a couple of competitors.
func seedZelda(t *testing.T, database *db.DB) {
	t.Helper()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date, aggregated_rating, aggregated_rating_count, category, popularity_score) VALUES
		(1, 'The Legend of Zelda: Breath of the Wild', 'botw', 'the legend of zelda breath of the wild', 1490726400, 97, 2895, 0, 9000),
		(2, 'The Legend of Zelda: Tears of the Kingdom', 'totk', 'the legend of zelda tears of the kingdom', 1683936000, 96, 2200, 0, 8500),
		(3, 'Zelda II: The Adventure of Link', 'zelda-ii', 'zelda ii the adventure of link', 567993600, 75, 200, 0, 300)`)
}

// TestAliasSearchFindsMainGame covers SEARCH_IMPROVEMENTS.md §4.1: a game
// upserted with IGDB alternative_names is findable by abbreviation and by
// localized title, ranked below direct name matches.
func TestAliasSearchFindsMainGame(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	err := store.UpsertIGDBGame(context.Background(), Game{
		ID:             1,
		Name:           "The Legend of Zelda: Breath of the Wild",
		Slug:           "botw",
		SafeName:       "x",
		NormalizedName: NormalizeName("The Legend of Zelda: Breath of the Wild"),
		Aliases:        []string{"BotW", "Breath of the Wild", "ゼルダの伝説 ブレス オブ ザ ワイルド"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedZelda(t, database)
	database.Exec(`DELETE FROM games_fts; INSERT INTO games_fts(rowid, normalized_name) SELECT rowid, normalized_name FROM games`)

	for _, q := range []string{"botw", "breath of the wild"} {
		results, err := store.SearchLocal(context.Background(), q, 10)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		found := false
		for _, r := range results {
			if r.ID == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("query %q should find game 1 via alias; got %v", q, results)
		}
	}

	// Direct name matches must always outrank an alias-only match.
	results, err := store.SearchLocal(context.Background(), "breath of the wild zelda", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) > 1 && results[0].ID != 1 {
		t.Errorf("name match should rank above alias matches; got %v", results)
	}
}

// TestAliasReplaceOnUpsert verifies the delete-all/reinsert semantics: a
// refreshed game's stale aliases disappear (e.g. upstream renamed them).
func TestAliasReplaceOnUpsert(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	mk := func(aliases []string) Game {
		return Game{
			ID:             7,
			Name:           "Super Cool Game",
			Slug:           "scg",
			SafeName:       "x",
			NormalizedName: NormalizeName("Super Cool Game"),
			Aliases:        aliases,
		}
	}

	if err := store.UpsertIGDBGame(ctx, mk([]string{"SCG", "Old Name"})); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 7`).Scan(&n)
	if n != 2 {
		t.Fatalf("expected 2 aliases, got %d", n)
	}

	if err := store.UpsertIGDBGame(ctx, mk([]string{"New Nickname"})); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 7`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 alias after replace, got %d", n)
	}

	results, err := store.SearchLocal(ctx, "old name", 10)
	if err != nil || len(results) != 0 {
		t.Errorf("stale alias should no longer match; got %v (%v)", results, err)
	}
	results, _ = store.SearchLocal(ctx, "new nickname", 10)
	if len(results) != 1 || results[0].ID != 7 {
		t.Errorf("new alias should match game 7; got %v", results)
	}
}

// TestAliasOwnNameNotStored: the game's own name is skipped as an alias.
func TestAliasOwnNameNotStored(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	err := store.UpsertIGDBGame(context.Background(), Game{
		ID:             9,
		Name:           "Doom",
		Slug:           "doom",
		SafeName:       "x",
		NormalizedName: NormalizeName("DOOM"),
		Aliases:        []string{"DOOM"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 9`).Scan(&n)
	if n != 0 {
		t.Errorf("own name should not be stored as alias; got %d", n)
	}
}

// TestDuplicateAliasesSingleRow: a game with several aliases that all match
// the query (e.g. "re4" matching both "RE4" and "RE4make") must appear once.
func TestDuplicateAliasesSingleRow(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	err := store.UpsertIGDBGame(context.Background(), Game{
		ID:             1,
		Name:           "Resident Evil 4",
		Slug:           "re4",
		SafeName:       "x",
		NormalizedName: NormalizeName("Resident Evil 4"),
		Aliases:        []string{"RE4", "RE4make"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	results, err := store.SearchLocal(context.Background(), "re4", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	n := 0
	for _, r := range results {
		if r.ID == 1 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("game with multiple matching aliases should appear once; got %d rows in %v", n, results)
	}
}

// TestAliasMatchPassesFloor: an unpopular game found only via alias must not
// be filtered out by the relevance floor on the paged path.
func TestAliasMatchPassesFloor(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, 'Obscure Niche Title', 'ont', 'obscure niche title')`)
	database.Exec(`INSERT INTO game_aliases (game_id, normalized_alias) VALUES (1, 'ont')`)

	results, err := store.SearchLocalPaged(context.Background(), "ont", 10, 0, true)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 || results[0].ID != 1 {
		t.Errorf("alias match should survive the floor; got %v", results)
	}
}

// TestWordOrderRetry covers §4.2: the strict trigram phrase can't match
// reordered words, but the token-AND retry recovers them without touching
// the LIKE fallback.
func TestWordOrderRetry(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	seedZelda(t, database)

	results, err := store.SearchLocal(context.Background(), "kingdom tears", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 || results[0].ID != 2 {
		t.Errorf("'kingdom tears' should find Tears of the Kingdom; got %v", results)
	}

	// A genuinely unmatched query still returns nothing.
	results, err = store.SearchLocal(context.Background(), "zzz qqx vv kkk jjj", 10)
	if err != nil || len(results) != 0 {
		t.Errorf("garbage query should match nothing; got %v (%v)", results, err)
	}
}

func TestSearchGamesPagedTotalSortFilters(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	ctx := context.Background()

	// Dates: unix seconds for 1986, 1998, 2017, 2023.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date, aggregated_rating, aggregated_rating_count, popularity_score) VALUES
		(1, 'Zelda', 'zelda', 'zelda', 504921600, 90, 300, 500),
		(2, 'Zelda Ocarina', 'ocarina', 'zelda ocarina', 883612800, 99, 400, 600),
		(3, 'Zelda BotW', 'botw', 'zelda botw', 1490726400, 97, 2895, 9000),
		(4, 'Zelda TotK', 'totk', 'zelda totk', 1683936000, 96, 2200, 8500)`)

	q := "zelda"
	opts := func(sort string) searchOptions {
		return searchOptions{limit: 10, offset: 0, sort: sort, applyFloor: true, withTotal: true}
	}

	results, total, err := store.SearchGamesPaged(ctx, q, opts(""))
	if err != nil {
		t.Fatalf("paged search: %v", err)
	}
	if total != 4 || len(results) != 4 {
		t.Fatalf("expected total=4 len=4, got total=%d len=%d", total, len(results))
	}

	// release_new: newest first, zero-date rows pushed last.
	results, _, _ = store.SearchGamesPaged(ctx, q, opts("release_new"))
	if len(results) < 2 || results[0].ID != 4 {
		t.Errorf("release_new should put TotK first; got %v", results)
	}

	// release_old: oldest first.
	results, _, _ = store.SearchGamesPaged(ctx, q, opts("release_old"))
	if len(results) < 1 || results[0].ID != 1 {
		t.Errorf("release_old should put Zelda 1986 first; got %v", results)
	}

	// rating order.
	results, _, _ = store.SearchGamesPaged(ctx, q, opts("rating"))
	if len(results) < 2 || results[0].ID != 2 {
		t.Errorf("rating sort should put Ocarina (99) first; got %v", results)
	}

	// popularity order.
	results, _, _ = store.SearchGamesPaged(ctx, q, opts("popularity"))
	if len(results) < 2 || results[0].ID != 3 {
		t.Errorf("popularity sort should put BotW first; got %v", results)
	}

	// Year filter: only 2017+2023 releases.
	yf := yearUnix(t, 2017, false)
	results, total, err = store.SearchGamesPaged(ctx, q, searchOptions{
		limit: 10, offset: 0, sort: "", applyFloor: true, withTotal: true, yearFrom: yf,
	})
	if err != nil || total != 2 || len(results) != 2 {
		t.Errorf("year_from=2017 should yield exactly 2; got total=%d len=%d (%v)", total, len(results), err)
	}

	// Min-rating filter: aggregated_rating >= 98 AND rated.
	results, total, err = store.SearchGamesPaged(ctx, q, searchOptions{
		limit: 10, offset: 0, sort: "", applyFloor: true, withTotal: true, minRating: 98,
	})
	if err != nil || total != 1 || len(results) != 1 || results[0].ID != 2 {
		t.Errorf("min_rating=98 should yield only Ocarina; got total=%d len=%d (%v)", total, len(results), err)
	}

	// Pagination with totals: page 2 of size 2.
	page2, total, err := store.SearchGamesPaged(ctx, q, searchOptions{
		limit: 2, offset: 2, sort: "", applyFloor: true, withTotal: true,
	})
	if err != nil || total != 4 || len(page2) != 2 {
		t.Errorf("page 2 expected total=4 len=2; got total=%d len=%d (%v)", total, len(page2), err)
	}
}

// TestShortQueryLikeStillWorks guards the <3-char LIKE path now that the
// alias branch rides along inside every engine.
func TestShortQueryLikeStillWorks(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()

	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES
		(1, 'Gorogoa', 'gorogoa', 'gorogoa'),
		(2, 'Go Mecha Ball', 'gmb', 'go mecha ball')`)

	results, err := store.SearchLocal(context.Background(), "go", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("short prefix query should match both; got %v", results)
	}
}
