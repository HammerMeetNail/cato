package db

import (
	"context"
	"strings"
	"testing"
)

func migrated(t *testing.T) *DB {
	t.Helper()
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func queryPlan(t *testing.T, database *DB, query string, args ...any) string {
	t.Helper()
	rows, err := database.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		b.WriteString(detail + "\n")
	}
	return b.String()
}

// F2: lookups of library rows by game (cover worker join, ON DELETE CASCADE
// from games) use an index instead of scanning every user's library.
func TestLibraryGameIndex(t *testing.T) {
	database := migrated(t)
	plan := queryPlan(t, database, "SELECT user_id FROM library_items WHERE game_id = ?", 1)
	if !strings.Contains(plan, "idx_library_game") {
		t.Fatalf("plan does not use idx_library_game:\n%s", plan)
	}
}

// F6: the default library order (updated, newest first) is served by an index:
// no temp B-tree sort.
func TestLibraryDefaultOrderUsesIndex(t *testing.T) {
	database := migrated(t)
	plan := queryPlan(t, database, `SELECT li.game_id FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? ORDER BY li.updated_at DESC, li.game_id DESC LIMIT 61`, "u")
	if strings.Contains(plan, "TEMP B-TREE") || !strings.Contains(plan, "idx_library_user_updated") {
		t.Fatalf("default library order is not index-backed:\n%s", plan)
	}
}

// totalChanges counts row changes including those made by triggers, on the
// single writer connection.
func changesFor(t *testing.T, database *DB, stmt string) int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := database.Write.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var before, after int64
	if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	return after - before
}

// F3: only a name change rewrites the FTS row, and rewriting platforms_json
// with the same value doesn't rebuild game_platforms.
func TestGameUpdatesOnlyReindexWhatChanged(t *testing.T) {
	database := migrated(t)
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, platforms_json) VALUES (1, 'Hades', 'hades', 'hades', '[6,130]')`); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"UPDATE games SET cover_url = 'x', local_cover_path = '/covers/1.jpg' WHERE id = 1",
		"UPDATE games SET popularity_score = 99, source_updated_at = 1 WHERE id = 1",
		"UPDATE games SET platforms_json = '[6,130]' WHERE id = 1",
	} {
		if n := changesFor(t, database, stmt); n != 1 {
			t.Errorf("%s: %d row changes (triggers rewrote derived rows)", stmt, n)
		}
	}
	// Real changes still propagate.
	if _, err := database.Exec("UPDATE games SET normalized_name = 'hades ii', platforms_json = '[167]' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	var hits int
	database.QueryRow(`SELECT COUNT(*) FROM games_fts WHERE games_fts MATCH '"hades ii"'`).Scan(&hits)
	if hits != 1 {
		t.Fatal("renamed game not re-indexed")
	}
	var plats string
	database.QueryRow(`SELECT group_concat(platform_id) FROM game_platforms WHERE game_id = 1`).Scan(&plats)
	if plats != "167" {
		t.Fatalf("game_platforms = %q", plats)
	}
}

// F7: the read pool is query-only and both pools carry the tuned pragmas.
func TestPoolPragmas(t *testing.T) {
	database := migrated(t)
	read := func(name string) (v int64) { database.Read.QueryRow("PRAGMA " + name).Scan(&v); return }
	write := func(name string) (v int64) { database.Write.QueryRow("PRAGMA " + name).Scan(&v); return }
	if read("query_only") != 1 || write("query_only") != 0 {
		t.Fatalf("query_only read=%d write=%d", read("query_only"), write("query_only"))
	}
	for _, get := range []func(string) int64{read, write} {
		if get("temp_store") != 2 || get("cache_size") != -16000 || get("mmap_size") != 134217728 || get("busy_timeout") != 5000 || get("foreign_keys") != 1 {
			t.Fatalf("pragmas: temp_store=%d cache_size=%d mmap_size=%d busy=%d fk=%d",
				get("temp_store"), get("cache_size"), get("mmap_size"), get("busy_timeout"), get("foreign_keys"))
		}
	}
	if _, err := database.Read.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (9, 'x', 'x', 'x')"); err == nil {
		t.Fatal("read pool accepted a write")
	}
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'a', 'a', 'a'), (2, 'b', 'b', 'b')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // first run analyzes, the second optimizes
		if err := database.Optimize(context.Background()); err != nil {
			t.Fatalf("Optimize: %v", err)
		}
	}
	var stats int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_stat1 WHERE tbl = 'games'`).Scan(&stats); err != nil || stats == 0 {
		t.Fatalf("planner statistics missing after Optimize: %d %v", stats, err)
	}
}
