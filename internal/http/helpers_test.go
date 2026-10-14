package http

import (
	"path/filepath"
	"testing"

	"cato/internal/auth"
	"cato/internal/db"
)

func setupGamesTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

// setupLibraryTestDB adds a user (user-1) and two games.
func setupLibraryTestDB(t *testing.T) *db.DB {
	t.Helper()
	database := setupGamesTestDB(t)
	database.Exec("INSERT INTO users (id, email) VALUES ('user-1', 'test@test.com')")
	database.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (1, 'Test Game', 'test-game', 'test game')")
	database.Exec("INSERT INTO games (id, name, slug, normalized_name) VALUES (2, 'Game Two', 'game-two', 'game two')")
	return database
}

func createLibrarySession(t *testing.T, database *db.DB, userID string) string {
	t.Helper()
	s, err := auth.CreateSession(database, userID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s.ID
}
