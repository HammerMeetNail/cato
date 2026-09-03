package games

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cato/internal/db"

	_ "modernc.org/sqlite"
)

func seedUserLibrary(t *testing.T, database *db.DB, userID string, gameID int64, tags []string, tagJSON string) {
	t.Helper()
	database.Exec(`INSERT OR IGNORE INTO users (id, email) VALUES (?, ?)`, userID, userID+"@example.com")
	database.Exec(`INSERT OR IGNORE INTO games (id, name, slug, normalized_name) VALUES (?, ?, ?, ?)`,
		gameID, fmt.Sprintf("Game %d", gameID), fmt.Sprintf("game-%d", gameID), fmt.Sprintf("game %d", gameID))
	if _, err := database.Exec(`INSERT INTO library_items (user_id, game_id, status, tags_json) VALUES (?, ?, 'playing', ?)`,
		userID, gameID, tagJSON); err != nil {
		t.Fatalf("seed library item: %v", err)
	}
	_ = tags
}

func TestRepairTagCaseDuplicates(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()

	seedUserLibrary(t, database, "u1", 1, nil, `["RPG","rpg","JRPG","  ","rpg"]`)
	seedUserLibrary(t, database, "u1", 2, nil, `["clean"]`)
	seedUserLibrary(t, database, "u1", 3, nil, `[" padded "]`)

	n, err := NewStore(database).RepairTagCaseDuplicates(context.Background())
	if err != nil {
		t.Fatalf("RepairTagCaseDuplicates: %v", err)
	}
	// NOTE: trim-only items ([" padded "]) are intentionally left alone —
	// the dedupe pass rewrites only when case-duplicates are dropped.
	if n != 1 {
		t.Errorf("fixed %d items, want 1", n)
	}
	var tags1, tags2, tags3 string
	database.QueryRow(`SELECT tags_json FROM library_items WHERE game_id = 1`).Scan(&tags1)
	database.QueryRow(`SELECT tags_json FROM library_items WHERE game_id = 2`).Scan(&tags2)
	database.QueryRow(`SELECT tags_json FROM library_items WHERE game_id = 3`).Scan(&tags3)
	if tags1 != `["RPG","JRPG"]` {
		t.Errorf("item 1 tags = %s", tags1)
	}
	if tags2 != `["clean"]` {
		t.Errorf("item 2 untouched, got %s", tags2)
	}
	if tags3 != `[" padded "]` {
		t.Errorf("item 3 tags = %s", tags3)
	}

	// Idempotent.
	if n, _ := NewStore(database).RepairTagCaseDuplicates(context.Background()); n != 0 {
		t.Errorf("second pass fixed %d", n)
	}
}

func TestRepairNormalizedAliases(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'G', 'g', 'g')`)
	// Accent-bearing alias (legacy) plus a clean one.
	database.Exec(`INSERT INTO game_aliases (game_id, normalized_alias) VALUES (1, 'pokémon')`)
	database.Exec(`INSERT INTO game_aliases (game_id, normalized_alias) VALUES (1, 'clean')`)

	n, err := NewStore(database).RepairNormalizedAliases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("fixed %d aliases, want 1", n)
	}
	var clean int
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 1 AND normalized_alias = 'pokemon'`).Scan(&clean)
	if clean != 1 {
		t.Error("accent-stripped alias missing")
	}
	// Idempotent.
	if n, _ := NewStore(database).RepairNormalizedAliases(context.Background()); n != 0 {
		t.Errorf("second pass fixed %d", n)
	}
}

func TestRepairNormalizedNames(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, safe_name, normalized_name) VALUES (5, 'Pokémon', 'p', 'Pokémon', 'pokémon')`)
	database.Exec(`INSERT INTO games (id, name, slug, safe_name, normalized_name) VALUES (6, 'Fine', 'f', 'Fine', 'fine')`)

	n, err := NewStore(database).RepairNormalizedNames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("fixed %d, want 1", n)
	}
	var norm string
	database.QueryRow(`SELECT normalized_name FROM games WHERE id = 5`).Scan(&norm)
	if norm != "pokemon" {
		t.Errorf("normalized_name = %q", norm)
	}
}

func TestRefreshStaleGames(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	// Stale game (source_updated_at 100 days old) and a fresh one.
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, source_updated_at) VALUES (1, 'Old', 'old', 'old', ?)`,
		timeNowUnix()-100*86400)
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, source_updated_at) VALUES (2, 'New', 'new', 'new', ?)`,
		timeNowUnix())

	// fakeIGDB.GetGame returns nil (game gone upstream) → MarkRefreshed.
	svc := NewService(NewStore(database), &fakeIGDB{}, database)
	svc.refreshStaleGames(10)

	var stale int64
	database.QueryRow(`SELECT COUNT(*) FROM games WHERE source_updated_at < ? AND source_updated_at > 0`,
		timeNowUnix()-90*86400).Scan(&stale)
	if stale != 0 {
		t.Errorf("stale game not marked refreshed")
	}
}

func TestRefreshStaleQueries(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	// A stale cache entry for query "zelda".
	database.Exec(`INSERT INTO igdb_query_cache (normalized_query, response_json, expires_at) VALUES ('search:zelda', '{}', '2020-01-01T00:00:00Z')`)

	svc := NewService(NewStore(database), &fakeIGDB{}, database)
	svc.refreshStaleQueries(10)
	// refreshFromIGDB with the fake returns no remote games but caches the
	// (empty) result, pushing expiry forward.
	var expires string
	err := database.QueryRow(`SELECT expires_at FROM igdb_query_cache WHERE normalized_query = 'search:zelda'`).Scan(&expires)
	if err != nil {
		t.Fatalf("cache entry vanished: %v", err)
	}
	if !strings.Contains(expires, "20") || strings.HasPrefix(expires, "2020") {
		t.Errorf("expiry not refreshed: %q", expires)
	}
}

func TestRepairGuessedCoverBatch(t *testing.T) {
	database, _ := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, cover_url, cover_id) VALUES (1, 'G', 'g', 'g', 'https://images.igdb.com/igdb/image/upload/t_cover_big/co542412.jpg', 542412)`)

	// The fake returns cover image_id co1 (URL co1.jpg) → differs → fixed.
	igdb := &backfillIGDB{}
	svc := NewService(NewStore(database), igdb, database)
	if err := svc.repairGuessedCoverBatch(context.Background(), 10); err != nil {
		t.Fatalf("repairGuessedCoverBatch: %v", err)
	}
	var url string
	database.QueryRow(`SELECT cover_url FROM games WHERE id = 1`).Scan(&url)
	if url != "https://images.igdb.com/igdb/image/upload/t_cover_big/co1.jpg" {
		t.Errorf("cover_url not corrected: %q", url)
	}
}

func TestSearchPagedWrapper(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Zelda', 'z', 'zelda')`)
	svc := NewService(store, &fakeIGDB{}, database)

	results, err := svc.SearchPaged(context.Background(), "zelda", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "Zelda" {
		t.Errorf("results = %+v", results)
	}
	// Page beyond the end → empty, no error.
	results, err = svc.SearchPaged(context.Background(), "zelda", 10, 100)
	if err != nil || len(results) != 0 {
		t.Errorf("deep page = (%v, %v)", results, err)
	}
	// Short query → nothing.
	if results, _ := svc.SearchPaged(context.Background(), "z", 10, 0); results != nil {
		t.Errorf("short query results = %v", results)
	}
}

func TestGetCoverRepairCandidatesAfter(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	for _, id := range []int64{1, 2, 3} {
		database.Exec(`INSERT INTO games (id, name, slug, normalized_name, cover_url, cover_id)
			VALUES (?, 'N', 'n', 'n', 'https://images.igdb.com/igdb/image/upload/t_cover_big/co100.jpg', 100)`, id)
	}
	ids, err := store.GetCoverRepairCandidatesAfter(context.Background(), 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 3 {
		t.Errorf("ids after 1 = %v", ids)
	}
	n, err := store.CountPendingCoverRepair(context.Background())
	if err != nil || n != 3 {
		t.Errorf("CountPendingCoverRepair = %d, %v", n, err)
	}
}

func TestGetBackfillCandidatesFilters(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	// Rated old game (candidate), recent unrated (candidate), old unrated (skipped), already fetched (skipped).
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, aggregated_rating_count, first_release_date) VALUES (1, 'A', 'a', 'a', 10, 1000000000)`)
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, aggregated_rating_count, first_release_date) VALUES (2, 'B', 'b', 'b', 0, ?)`, timeNowUnix())
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, aggregated_rating_count, first_release_date) VALUES (3, 'C', 'c', 'c', 0, 1000000000)`)
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name, aggregated_rating_count, first_release_date, popularity_fetched_at) VALUES (4, 'D', 'd', 'd', 10, 1000000000, 1)`)

	ids, err := store.GetBackfillCandidates(context.Background(), 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("candidates = %v, want [1 2]", ids)
	}
	n, err := store.CountPendingBackfill(context.Background(), 2)
	if err != nil || n != 2 {
		t.Errorf("CountPendingBackfill = %d, %v", n, err)
	}
}

func TestCountTotalGamesAndIDsAfter(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	for i := int64(1); i <= 5; i++ {
		database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (?, 'N', 'n', 'n')`, i)
	}
	n, err := store.CountTotalGames(context.Background())
	if err != nil || n != 5 {
		t.Fatalf("CountTotalGames = %d, %v", n, err)
	}
	ids, err := store.GetGameIDsAfter(context.Background(), 3, 10)
	if err != nil || len(ids) != 2 || ids[0] != 4 {
		t.Errorf("GetGameIDsAfter(3) = %v, %v", ids, err)
	}
}

func TestMarkHelpers(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'N', 'n', 'n')`)

	if err := store.MarkPopularityFetched(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var pf int64
	database.QueryRow(`SELECT popularity_fetched_at FROM games WHERE id = 1`).Scan(&pf)
	if pf == 0 {
		t.Error("popularity_fetched_at not stamped")
	}

	if err := store.MarkEditionFetched(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVersionParentAndMarkFetched(context.Background(), 1, 7); err != nil {
		t.Fatal(err)
	}
	var vp int64
	database.QueryRow(`SELECT version_parent FROM games WHERE id = 1`).Scan(&vp)
	if vp != 7 {
		t.Errorf("version_parent = %d", vp)
	}
	if err := store.SetCoverAndMarkFetched(context.Background(), 1, 9, "https://x/co9.jpg"); err != nil {
		t.Fatal(err)
	}
	var cu string
	database.QueryRow(`SELECT cover_url FROM games WHERE id = 1`).Scan(&cu)
	if cu != "https://x/co9.jpg" {
		t.Errorf("cover_url = %q", cu)
	}
	if err := store.SetEditionInfoAndMarkFetched(context.Background(), 1, 2, 13, 1); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT version_parent, category, parent_game FROM games WHERE id = 1`).Scan(&vp, &pf, &cu)
	if vp != 2 || pf != 13 || cu != "1" {
		t.Errorf("edition info = (%d, %d, %q)", vp, pf, cu)
	}
	if err := store.UpdateCategoryAndParentGame(context.Background(), 1, 9, 8); err != nil {
		t.Fatal(err)
	}
	database.QueryRow(`SELECT category, parent_game FROM games WHERE id = 1`).Scan(&pf, &cu)
	if pf != 9 || cu != "8" {
		t.Errorf("category/parent = (%d, %q)", pf, cu)
	}
}

func TestSetAliasesAndMarkFetched(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Zelda', 'z', 'zelda')`)

	if err := store.SetAliasesAndMarkFetched(context.Background(), 1, "Zelda", []string{"BotW", "ZELDA"}); err != nil {
		t.Fatal(err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM game_aliases WHERE game_id = 1`).Scan(&n)
	if n != 1 { // "ZELDA" dedupes to the game's own name
		t.Errorf("aliases = %d, want 1", n)
	}
	var fetched int64
	database.QueryRow(`SELECT aliases_fetched_at FROM games WHERE id = 1`).Scan(&fetched)
	if fetched == 0 {
		t.Error("aliases_fetched_at not stamped")
	}
}

func TestIsBenignRepairErr(t *testing.T) {
	if isBenignRepairErr(nil) {
		t.Error("nil error is not benign")
	}
	cases := map[string]bool{
		"no such table: games": true,
		"database is closed":   true,
		"bad connection":       true,
		"real failure":         false,
	}
	for msg, want := range cases {
		if got := isBenignRepairErr(fmt.Errorf("%s", msg)); got != want {
			t.Errorf("isBenignRepairErr(%q) = %v", msg, got)
		}
	}
}

// RepairNormalizedNames on a DB where the games table is missing must be a
// silent no-op (fresh test DBs may predate migrations).
func TestRepairNormalizationMissingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	if n, err := store.RepairNormalizedNames(context.Background()); err != nil || n != 0 {
		t.Errorf("RepairNormalizedNames on empty schema = (%d, %v)", n, err)
	}
	if n, err := store.RepairNormalizedAliases(context.Background()); err != nil || n != 0 {
		t.Errorf("RepairNormalizedAliases on empty schema = (%d, %v)", n, err)
	}
}

func TestCountPendingAliasAndEditionBackfill(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	for i := int64(1); i <= 3; i++ {
		database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (?, 'N', 'n', 'n')`, i)
	}
	database.Exec(`UPDATE games SET aliases_fetched_at = 1 WHERE id = 3`)
	database.Exec(`UPDATE games SET version_parent_fetched_at = 1 WHERE id = 2`)

	a, err := store.CountPendingAliasBackfill(context.Background())
	if err != nil || a != 2 {
		t.Errorf("alias pending = %d, %v", a, err)
	}
	e, err := store.CountPendingEditionBackfill(context.Background())
	if err != nil || e != 2 {
		t.Errorf("edition pending = %d, %v", e, err)
	}
}

func timeNowUnix() int64 { return time.Now().Unix() }

func timeNow() time.Time { return time.Now() }
