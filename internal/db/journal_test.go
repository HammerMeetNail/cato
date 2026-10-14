package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openAt(t *testing.T, version int) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrateTo(database, version); err != nil {
		t.Fatalf("migrate to %d: %v", version, err)
	}
	return database
}

func mustExec(t *testing.T, database *DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func scalar(t *testing.T, database *DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := database.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// v18 turns each legacy item into journal rows without changing any total.
func TestJournalBackfillKeepsTotals(t *testing.T) {
	database := openAt(t, 17)
	mustExec(t, database, `INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com'), ('u2', 'u2@example.com');
		INSERT INTO games (id, name, slug, normalized_name) VALUES
		  (1, 'Done', 'done', 'done'), (2, 'Dropped', 'dropped', 'dropped'),
		  (3, 'Wish', 'wish', 'wish'), (4, 'Playing', 'playing', 'playing');
		INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, started_at, completed_at, updated_at, tags_json) VALUES
		  ('u1', 1, 'completed', 90, 2400, '2025-01-02T10:00:00Z', '2025-02-03T10:00:00Z', '2025-02-03T10:00:00Z', '["rpg"]'),
		  ('u1', 2, 'abandoned', 0, 60, '2025-03-01T10:00:00Z', '2025-03-05T10:00:00Z', '2025-03-05T10:00:00Z', '[]'),
		  ('u1', 3, 'wishlist', 0, 0, NULL, NULL, '2025-04-01T10:00:00Z', '[]'),
		  ('u2', 4, 'playing', 0, 30, NULL, NULL, '2025-05-01 09:00:00', '[]');`)

	if err := Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if got := scalar(t, database, `SELECT COUNT(*) FROM playthroughs WHERE user_id = 'u1'`); got != 2 {
		t.Fatalf("u1 playthroughs = %d, want 2 (only dated items)", got)
	}
	var started, finished, completion string
	var rating, minutes int64
	if err := database.QueryRow(`SELECT started_on, finished_on, COALESCE(completion, ''), rating, minutes
		FROM playthroughs WHERE user_id = 'u1' AND game_id = 1`).Scan(&started, &finished, &completion, &rating, &minutes); err != nil {
		t.Fatalf("playthrough: %v", err)
	}
	if started != "2025-01-02" || finished != "2025-02-03" || completion != "" || rating != 90 || minutes != 2400 {
		t.Errorf("finished playthrough = %s %s %q %d %d", started, finished, completion, rating, minutes)
	}
	if got := scalar(t, database, `SELECT COUNT(*) FROM playthroughs WHERE game_id = 2 AND completion = 'dropped'`); got != 1 {
		t.Errorf("abandoned item should backfill a dropped playthrough")
	}
	// Totals are unchanged and equal the session sums.
	for _, c := range []struct {
		user string
		game int64
		want int64
	}{{"u1", 1, 2400}, {"u1", 2, 60}, {"u1", 3, 0}, {"u2", 4, 30}} {
		if got := scalar(t, database, `SELECT playtime_minutes FROM library_items WHERE user_id = ? AND game_id = ?`, c.user, c.game); got != c.want {
			t.Errorf("%s/%d playtime = %d, want %d", c.user, c.game, got, c.want)
		}
		if got := scalar(t, database, `SELECT COALESCE(SUM(minutes), 0) FROM play_sessions WHERE user_id = ? AND game_id = ?`, c.user, c.game); got != c.want {
			t.Errorf("%s/%d session sum = %d, want %d", c.user, c.game, got, c.want)
		}
	}
	if got := scalar(t, database, `SELECT COUNT(*) FROM play_sessions WHERE source != 'legacy'`); got != 0 {
		t.Errorf("backfilled sessions must be source=legacy")
	}
	var playedOn string
	database.QueryRow(`SELECT played_on FROM play_sessions WHERE game_id = 4`).Scan(&playedOn)
	if playedOn != "2025-05-01" {
		t.Errorf("legacy session date = %q, want the updated_at day", playedOn)
	}
	if got := scalar(t, database, `SELECT COUNT(*) FROM library_items WHERE game_id = 1 AND current_playthrough_id IS NOT NULL`); got != 1 {
		t.Errorf("current_playthrough_id not set")
	}
	// Tags survived the table rebuild and the tag triggers are back.
	if got := scalar(t, database, `SELECT COUNT(*) FROM library_tags WHERE user_id = 'u1' AND game_id = 1`); got != 1 {
		t.Errorf("library_tags lost in rebuild")
	}
	mustExec(t, database, `UPDATE library_items SET tags_json = '["a","b"]' WHERE user_id = 'u1' AND game_id = 1`)
	if got := scalar(t, database, `SELECT COUNT(*) FROM library_tags WHERE user_id = 'u1' AND game_id = 1`); got != 2 {
		t.Errorf("library_tags_au not recreated")
	}
	if got := scalar(t, database, `SELECT COUNT(*) FROM user_state`); got != 2 {
		t.Errorf("user_state rows = %d, want one per user", got)
	}
	if err := database.QueryRow(`PRAGMA foreign_key_check`).Scan(); err != sql.ErrNoRows {
		t.Errorf("foreign_key_check reported violations: %v", err)
	}
	var integrity string
	database.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)
	if integrity != "ok" {
		t.Errorf("integrity_check = %q", integrity)
	}
}

func TestPausedStatusIsValid(t *testing.T) {
	database := openAt(t, LatestVersion())
	mustExec(t, database, `INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com');
		INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'G', 'g', 'g');
		INSERT INTO library_items (user_id, game_id, status) VALUES ('u1', 1, 'paused')`)
	if _, err := database.Exec(`UPDATE library_items SET status = 'shelved'`); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestLibraryRevTracksEveryChange(t *testing.T) {
	database := openAt(t, LatestVersion())
	mustExec(t, database, `INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com'), ('u2', 'u2@example.com');
		INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'One', 'one', 'one'), (2, 'Two', 'two', 'two')`)
	rev := func(user string) int64 {
		return scalar(t, database, `SELECT COALESCE((SELECT library_rev FROM user_state WHERE user_id = ?), 0)`, user)
	}
	start := rev("u1")
	mustExec(t, database, `INSERT INTO library_items (user_id, game_id, status) VALUES ('u1', 1, 'backlog')`)
	if rev("u1") != start+1 {
		t.Fatalf("insert: rev %d, want %d", rev("u1"), start+1)
	}
	if got := scalar(t, database, `SELECT rev FROM library_items WHERE user_id = 'u1' AND game_id = 1`); got != rev("u1") {
		t.Errorf("row rev %d != library_rev %d", got, rev("u1"))
	}
	mustExec(t, database, `UPDATE library_items SET rating = 80 WHERE user_id = 'u1' AND game_id = 1`)
	if rev("u1") != start+2 {
		t.Errorf("update: rev %d, want %d", rev("u1"), start+2)
	}
	// A session changes the aggregate, so it changes the row.
	mustExec(t, database, `INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('u1', 1, '2026-10-01', 45)`)
	if rev("u1") != start+3 {
		t.Errorf("session: rev %d, want %d", rev("u1"), start+3)
	}
	// Another user's library and an unrelated game column don't count.
	mustExec(t, database, `INSERT INTO library_items (user_id, game_id, status) VALUES ('u2', 2, 'backlog')`)
	mustExec(t, database, `UPDATE games SET popularity_score = 5 WHERE id = 1`)
	if rev("u1") != start+3 {
		t.Errorf("unrelated writes moved u1's rev to %d", rev("u1"))
	}
	// The game's cover is part of each index entry.
	mustExec(t, database, `UPDATE games SET local_cover_path = '/covers/1.jpg' WHERE id = 1`)
	if rev("u1") != start+4 {
		t.Errorf("cover change: rev %d, want %d", rev("u1"), start+4)
	}
	mustExec(t, database, `DELETE FROM library_items WHERE user_id = 'u1' AND game_id = 1`)
	if got := scalar(t, database, `SELECT rev FROM library_tombstones WHERE user_id = 'u1' AND game_id = 1`); got != rev("u1") || got != start+5 {
		t.Errorf("tombstone rev %d, library_rev %d", got, rev("u1"))
	}
	if got := scalar(t, database, `SELECT COUNT(*) FROM play_sessions WHERE user_id = 'u1'`); got != 0 {
		t.Errorf("sessions should cascade with the item")
	}
	mustExec(t, database, `INSERT INTO library_items (user_id, game_id, status) VALUES ('u1', 1, 'wishlist')`)
	if got := scalar(t, database, `SELECT COUNT(*) FROM library_tombstones WHERE user_id = 'u1'`); got != 0 {
		t.Errorf("re-adding should clear the tombstone")
	}
	// Account deletion cascades through every journal table.
	mustExec(t, database, `INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('u1', 1, '2026-10-02', 30)`)
	mustExec(t, database, `DELETE FROM library_items WHERE user_id = 'u1'`)
	mustExec(t, database, `DELETE FROM users WHERE id = 'u1'`)
	for _, table := range []string{"user_state", "library_tombstones", "play_sessions", "playthroughs", "library_items"} {
		if got := scalar(t, database, `SELECT COUNT(*) FROM `+table+` WHERE user_id = 'u1'`); got != 0 {
			t.Errorf("%s keeps %d rows after account deletion", table, got)
		}
	}
}

func TestSessionsKeepPlaytimeInStep(t *testing.T) {
	database := openAt(t, LatestVersion())
	mustExec(t, database, `INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com');
		INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'G', 'g', 'g');
		INSERT INTO library_items (user_id, game_id, status) VALUES ('u1', 1, 'playing');
		INSERT INTO playthroughs (id, user_id, game_id, started_on) VALUES (7, 'u1', 1, '2026-10-01')`)
	total := func() int64 {
		return scalar(t, database, `SELECT playtime_minutes FROM library_items WHERE user_id = 'u1' AND game_id = 1`)
	}
	ptMinutes := func() int64 { return scalar(t, database, `SELECT minutes FROM playthroughs WHERE id = 7`) }
	mustExec(t, database, `INSERT INTO play_sessions (id, user_id, game_id, playthrough_id, played_on, minutes) VALUES (1, 'u1', 1, 7, '2026-10-01', 90)`)
	mustExec(t, database, `INSERT INTO play_sessions (id, user_id, game_id, played_on, minutes) VALUES (2, 'u1', 1, '2026-10-02', 30)`)
	if total() != 120 || ptMinutes() != 90 {
		t.Fatalf("after inserts total=%d pt=%d", total(), ptMinutes())
	}
	mustExec(t, database, `UPDATE play_sessions SET minutes = 60, playthrough_id = 7 WHERE id = 2`)
	if total() != 150 || ptMinutes() != 150 {
		t.Errorf("after update total=%d pt=%d", total(), ptMinutes())
	}
	mustExec(t, database, `INSERT INTO play_sessions (user_id, game_id, played_on, minutes, source) VALUES ('u1', 1, '2026-10-03', -20, 'adjust')`)
	if total() != 130 {
		t.Errorf("negative adjustment total=%d", total())
	}
	mustExec(t, database, `DELETE FROM play_sessions WHERE id = 1`)
	if total() != 40 || ptMinutes() != 60 {
		t.Errorf("after delete total=%d pt=%d", total(), ptMinutes())
	}
	for _, bad := range []string{
		`INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('u1', 1, '2026-10-04', 0)`,
		`INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('u1', 1, '2026-10-04', -5)`,
		`INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('u1', 1, '2026-10-04', 1441)`,
		`INSERT INTO play_sessions (user_id, game_id, played_on, minutes) VALUES ('u1', 2, '2026-10-04', 10)`,
	} {
		if _, err := database.Exec(bad); err == nil {
			t.Errorf("accepted invalid session: %s", bad)
		}
	}
}
