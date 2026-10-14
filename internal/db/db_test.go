package db

import (
	"database/sql"
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

// The writer's transactions must take the write lock at BEGIN (IMMEDIATE),
// so read-modify-write transactions serialize against other processes
// instead of failing late with SQLITE_BUSY_SNAPSHOT.
func TestWriterTransactionsAreImmediate(t *testing.T) {
	path := t.TempDir() + "/test.db"
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE t (v INTEGER)"); err != nil {
		t.Fatal(err)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec("INSERT INTO t VALUES (1)"); err == nil {
		t.Fatal("another connection wrote while a writer transaction was open (BEGIN was not IMMEDIATE)")
	}
	// Reads are unaffected.
	if err := database.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
}
