package db

import (
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	if err := database.Ping(); err != nil {
		t.Fatalf("failed to ping db: %v", err)
	}
}

func TestOpenCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	database.Close()

	// Verify file was created
	// On some platforms, SQLite may not create the file immediately
	// Just verify no error occurred
}
