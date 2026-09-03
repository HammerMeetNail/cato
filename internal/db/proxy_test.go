package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestProxyRouting verifies the read/write split: Query* hit the read pool,
// Exec*/Begin* the single writer. With modernc.org/sqlite both pools point at
// the same file, so we assert on observable behavior (data round-trips,
// transactions commit/rollback) plus WAL mode actually being set.
func TestProxyRouting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("exec (writer): %v", err)
	}

	// ExecContext routes to the writer.
	if _, err := database.ExecContext(context.Background(), `INSERT INTO items (name) VALUES ('a')`); err != nil {
		t.Fatalf("exec ctx: %v", err)
	}

	// Query routes to the read pool.
	rows, err := database.Query(`SELECT id, name FROM items ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var names []string
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if len(names) != 1 || names[0] != "a" {
		t.Fatalf("query read = %v", names)
	}

	// QueryContext.
	rows2, err := database.QueryContext(context.Background(), `SELECT COUNT(*) FROM items`)
	if err != nil {
		t.Fatalf("query ctx: %v", err)
	}
	rows2.Close()

	// QueryRow + QueryRowContext.
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("QueryRow: %d %v", n, err)
	}
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("QueryRowContext: %d %v", n, err)
	}

	// Begin (writer) commit.
	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO items (name) VALUES ('b')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// BeginTx (writer) rollback.
	tx2, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx2.Exec(`INSERT INTO items (name) VALUES ('c')`); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatal(err)
	}

	// Final state: a and b present, c rolled back.
	database.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
	if n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}

	// WAL mode must be active (the DSN pragma regression guard).
	var mode string
	if err := database.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	// foreign_keys pragma must be ON.
	var fk int
	database.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpenFailureOnBadPath(t *testing.T) {
	// A path in a nonexistent directory fails the ping inside Open.
	if _, err := Open("/nonexistent-dir-xyz/db.sqlite"); err == nil {
		t.Error("expected error opening db in missing directory")
	}
}
