package db

import (
	"path/filepath"
	"testing"
)

func TestMigrateCreatesTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	tables := []string{"users", "sessions", "games", "library_items", "igdb_query_cache", "igdb_sync_state", "cover_jobs", "game_platforms", "library_tags", "schema_migrations"}
	for _, table := range tables {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			t.Fatalf("failed to check table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("expected table %s to exist", table)
		}
	}
}

func TestNormalizedSearchTablesFollowRawWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()
	if err := Migrate(database); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO games (id, name, slug, normalized_name, platforms_json)
		VALUES (1, 'Game One', 'game-one', 'game one', '[6,"6","Nintendo Switch",6,null]')`); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	assertRows := func(query string, want int) {
		t.Helper()
		var got int
		if err := database.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("count normalized rows: %v", err)
		}
		if got != want {
			t.Errorf("normalized row count = %d, want %d", got, want)
		}
	}
	assertRows(`SELECT COUNT(*) FROM game_platforms WHERE game_id = 1`, 2)

	if _, err := database.Exec(`UPDATE games SET platforms_json = '[130,"Steam Deck"]' WHERE id = 1`); err != nil {
		t.Fatalf("update platforms: %v", err)
	}
	assertRows(`SELECT COUNT(*) FROM game_platforms WHERE game_id = 1`, 2)
	var platformID, platformValue int64
	if err := database.QueryRow(`SELECT platform_id, LENGTH(platform_value)
		FROM game_platforms WHERE game_id = 1 AND platform_id = 130`).Scan(&platformID, &platformValue); err != nil {
		t.Fatalf("scan platform row: %v", err)
	}
	if platformID != 130 || platformValue != 0 {
		t.Errorf("numeric platform row = (%d, %d), want (130, 0)", platformID, platformValue)
	}
	assertRows(`SELECT COUNT(*) FROM game_platforms WHERE game_id = 1 AND platform_value = 'Steam Deck'`, 1)

	if _, err := database.Exec(`INSERT INTO library_items (user_id, game_id, status, tags_json)
		VALUES ('u1', 1, 'backlog', '["rpg","favorite","rpg",null]')`); err != nil {
		t.Fatalf("insert library item: %v", err)
	}
	assertRows(`SELECT COUNT(*) FROM library_tags WHERE user_id = 'u1' AND game_id = 1`, 2)

	if _, err := database.Exec(`UPDATE library_items SET tags_json = '["finished"]' WHERE user_id = 'u1' AND game_id = 1`); err != nil {
		t.Fatalf("update tags: %v", err)
	}
	assertRows(`SELECT COUNT(*) FROM library_tags WHERE user_id = 'u1' AND game_id = 1`, 1)
	assertRows(`SELECT COUNT(*) FROM library_tags WHERE user_id = 'u1' AND game_id = 1 AND tag = 'finished'`, 1)

	if _, err := database.Exec(`INSERT INTO users (id, email) VALUES ('u2', 'u2@example.com');
		INSERT INTO games (id, name, slug, normalized_name) VALUES (2, 'Game Two', 'game-two', 'game two');
		UPDATE library_items SET user_id = 'u2', game_id = 2, tags_json = '["moved"]'
			WHERE user_id = 'u1' AND game_id = 1`); err != nil {
		t.Fatalf("move library item: %v", err)
	}
	assertRows(`SELECT COUNT(*) FROM library_tags WHERE user_id = 'u1' AND game_id = 1`, 0)
	assertRows(`SELECT COUNT(*) FROM library_tags WHERE user_id = 'u2' AND game_id = 2 AND tag = 'moved'`, 1)

	if _, err := database.Exec(`DELETE FROM library_items WHERE user_id = 'u2' AND game_id = 2`); err != nil {
		t.Fatalf("delete library item: %v", err)
	}
	assertRows(`SELECT COUNT(*) FROM library_tags WHERE user_id = 'u2' AND game_id = 2`, 0)

	if _, err := database.Exec(`DELETE FROM games WHERE id = 1`); err != nil {
		t.Fatalf("delete game: %v", err)
	}
	assertRows(`SELECT COUNT(*) FROM game_platforms WHERE game_id = 1`, 0)
}

func TestNormalizedTablesBackfillLegacyJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	// Start from the current schema, then remove only v15's objects and marker.
	// This gives the test a complete v14-compatible schema while exercising the
	// actual migration backfill rather than only its live triggers.
	if err := Migrate(database); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com');
		INSERT INTO games (id, name, slug, normalized_name, platforms_json)
			VALUES (1, 'Legacy', 'legacy', 'legacy', '[6,"PC (Microsoft Windows)"]');
		INSERT INTO library_items (user_id, game_id, status, tags_json)
			VALUES ('u1', 1, 'backlog', '["rpg","rpg"]');
		DROP TRIGGER game_platforms_ai;
		DROP TRIGGER game_platforms_au;
		DROP TRIGGER library_tags_ai;
		DROP TRIGGER library_tags_au;
		DROP TRIGGER library_tags_ad;
		DROP TABLE game_platforms;
		DROP TABLE library_tags;
		DELETE FROM schema_migrations WHERE version = 15;`); err != nil {
		t.Fatalf("prepare legacy schema: %v", err)
	}
	if err := Migrate(database); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}

	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM game_platforms WHERE game_id = 1`).Scan(&n); err != nil {
		t.Fatalf("count backfilled platforms: %v", err)
	}
	if n != 2 {
		t.Errorf("backfilled platform rows = %d, want 2", n)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM library_tags WHERE user_id = 'u1' AND game_id = 1`).Scan(&n); err != nil {
		t.Fatalf("count backfilled tags: %v", err)
	}
	if n != 1 {
		t.Errorf("backfilled tag rows = %d, want 1", n)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("first migrate failed: %v", err)
	}

	if err := Migrate(database); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}

	var version int
	if err := database.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("failed to get version: %v", err)
	}
	// Track len(migrations) rather than a hardcoded number so adding
	// migration N+1 can't silently leave this assertion stale.
	if version != len(migrations) {
		t.Errorf("expected version %d, got %d", len(migrations), version)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	// Insert a user
	_, err = database.Exec("INSERT INTO users (id, email) VALUES ('u1', 'test@test.com')")
	if err != nil {
		t.Fatalf("failed to insert user: %v", err)
	}

	// Try inserting a library_item for non-existent game - should fail due to FK
	_, err = database.Exec("INSERT INTO library_items (user_id, game_id, status) VALUES ('u1', 99999, 'backlog')")
	if err == nil {
		t.Error("expected foreign key error, got nil")
	}
}

func TestLibraryStatusCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	// Insert user and game
	database.Exec("INSERT INTO users (id, email) VALUES ('u1', 'test@test.com')")
	database.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Test Game', 'test-game', 'test game')")

	// Valid status
	_, err = database.Exec("INSERT INTO library_items (user_id, game_id, status) VALUES ('u1', 1, 'backlog')")
	if err != nil {
		t.Fatalf("expected valid status 'backlog' to succeed: %v", err)
	}

	// Invalid status
	_, err = database.Exec("INSERT INTO library_items (user_id, game_id, status) VALUES ('u1', 1, 'invalid_status')")
	if err == nil {
		t.Error("expected CHECK constraint error for invalid status")
	}
}
