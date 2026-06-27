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

	tables := []string{"users", "sessions", "games", "library_items", "igdb_query_cache", "igdb_sync_state", "cover_jobs", "schema_migrations"}
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
	if version != 5 {
		t.Errorf("expected version 5, got %d", version)
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
