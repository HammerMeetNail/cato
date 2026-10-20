package catalog

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"cato/internal/db"
)

// productionStats are the NAS's planner statistics for games and
// game_series (October 2026). ANALYZE samples at most analysis_limit rows
// per index, so it records version_parent = 0 as matching 4 of 312k games
// when it matches 99% of them.
var productionStats = [][2]string{
	{"game_series", "idx_game_series_series|2133 401 5"},
	{"game_series", "sqlite_autoindex_game_series_1|2133 3 2 1"},
	{"games", "idx_games_category|311959 401"},
	{"games", "idx_games_details_fetched|311959 401"},
	{"games", "idx_games_normalized_name|311959 1"},
	{"games", "idx_games_parent_game|311959 11"},
	{"games", "idx_games_pop_fetch|245894 401"},
	{"games", "idx_games_popularity|311959 2"},
	{"games", "idx_games_rating_release|311959 401 28 2"},
	{"games", "idx_games_release|311959 19"},
	{"games", "idx_games_slug|311959 1"},
	{"games", "idx_games_source_updated|311959 1"},
	{"games", "idx_games_suggest|76121 2 1 1"},
	{"games", "idx_games_version_parent|311959 4"},
}

// withProductionStats returns a migrated database whose every connection
// plans with productionStats.
func withProductionStats(t *testing.T) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`ANALYZE; DELETE FROM sqlite_stat1`); err != nil {
		t.Fatal(err)
	}
	for _, s := range productionStats {
		idx, stat, _ := strings.Cut(s[1], "|")
		if _, err := database.Exec(`INSERT INTO sqlite_stat1 (tbl, idx, stat) SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM sqlite_master WHERE name = ?)`, s[0], idx, stat, idx); err != nil {
			t.Fatal(err)
		}
	}
	database.Close()
	// Connections load the statistics when they open.
	if database, err = db.Open(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func explain(t *testing.T, database *db.DB, query string, args ...any) string {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		rows.Scan(&id, &parent, &notused, &detail)
		b.WriteString(detail + "\n")
	}
	return b.String()
}

// On the NAS the game page's series lookup took ~470 ms of a 475 ms
// request: SQLite walked every base game through idx_games_version_parent
// and probed game_series for each, so the page's details arrived half a
// second after its title. It starts from the game's own series rows.
func TestGamePageQueriesStayIndexedWithProductionStats(t *testing.T) {
	database := withProductionStats(t)

	plan := explain(t, database, seriesSQL, 228525)
	if !strings.HasPrefix(plan, "SEARCH mine ") || strings.Contains(plan, "idx_games_version_parent") {
		t.Errorf("series lookup doesn't start from the game's series:\n%s", plan)
	}
	// Editions still find their rows through the index.
	if plan := explain(t, database, editionsSQL, 228525); !strings.Contains(plan, "idx_games_version_parent") {
		t.Errorf("editions lookup doesn't use idx_games_version_parent:\n%s", plan)
	}
}

func TestGameExtrasSeriesAndEditions(t *testing.T) {
	database, store := setupGameDB(t)
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, first_release_date, version_parent) VALUES
		(1, 'Hades', 'hades', 'hades', 1600000000, 0),
		(2, 'Hades II', 'hades-ii', 'hades ii', 1750000000, 0),
		(3, 'Hades II: Deluxe', 'hades-ii-deluxe', 'hades ii deluxe', 1750000000, 2),
		(4, 'Bastion', 'bastion', 'bastion', 1300000000, 0);
		INSERT INTO game_series (game_id, series_id, kind, name) VALUES
		(1, 7, 'collection', 'Hades'), (2, 7, 'collection', 'Hades'), (3, 7, 'collection', 'Hades'), (4, 8, 'collection', 'Bastion')`); err != nil {
		t.Fatal(err)
	}
	x, err := store.GameExtras(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Series) != 1 || len(x.Series[0].Games) != 2 || x.Series[0].Games[0].ID != 1 || x.Series[0].Games[1].ID != 2 {
		t.Fatalf("series = %+v, want Hades then Hades II (no editions)", x.Series)
	}
	if len(x.Editions) != 1 || x.Editions[0].ID != 3 {
		t.Fatalf("editions = %+v, want the Deluxe edition", x.Editions)
	}
	// A base game is no one's edition.
	if x, err = store.GameExtras(context.Background(), 4); err != nil || len(x.Editions) != 0 {
		t.Fatalf("editions of a game without any = %+v, %v", x.Editions, err)
	}
}
