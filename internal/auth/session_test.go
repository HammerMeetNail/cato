package auth

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupSessionDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT,
		display_name TEXT NOT NULL DEFAULT '',
		avatar_url TEXT NOT NULL DEFAULT '',
		google_subject TEXT UNIQUE,
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create users table: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf_token TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create sessions table: %v", err)
	}

	_, err = db.Exec("INSERT INTO users (id, email) VALUES ('user-1', 'test@example.com')")
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}

	return db
}

func TestCreateAndGetSession(t *testing.T) {
	db := setupSessionDB(t)
	defer db.Close()

	session, err := CreateSession(db, "user-1")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	if session.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if session.UserID != "user-1" {
		t.Errorf("expected user-1, got %s", session.UserID)
	}
	if session.CSRFToken == "" {
		t.Error("expected non-empty CSRF token")
	}

	// Get the session
	retrieved, err := GetSession(db, session.ID)
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected session to exist")
	}
	if retrieved.UserID != "user-1" {
		t.Errorf("expected user-1, got %s", retrieved.UserID)
	}
	if retrieved.CSRFToken != session.CSRFToken {
		t.Error("expected matching CSRF tokens")
	}
}

func TestGetSessionInvalidID(t *testing.T) {
	db := setupSessionDB(t)
	defer db.Close()

	session, err := GetSession(db, "nonexistent-session-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session != nil {
		t.Error("expected nil session for invalid ID")
	}
}

func TestDeleteSession(t *testing.T) {
	db := setupSessionDB(t)
	defer db.Close()

	session, err := CreateSession(db, "user-1")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	if err := DeleteSession(db, session.ID); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	retrieved, err := GetSession(db, session.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retrieved != nil {
		t.Error("expected session to be deleted")
	}
}

func TestDeleteUserSessions(t *testing.T) {
	db := setupSessionDB(t)
	defer db.Close()

	// Add another user
	db.Exec("INSERT INTO users (id, email) VALUES ('user-2', 'test2@example.com')")

	_, err := CreateSession(db, "user-1")
	if err != nil {
		t.Fatalf("failed to create session 1: %v", err)
	}
	_, err = CreateSession(db, "user-1")
	if err != nil {
		t.Fatalf("failed to create session 2: %v", err)
	}
	_, err = CreateSession(db, "user-2")
	if err != nil {
		t.Fatalf("failed to create session 3: %v", err)
	}

	if err := DeleteUserSessions(db, "user-1"); err != nil {
		t.Fatalf("failed to delete user sessions: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id = 'user-1'").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 sessions for user-1, got %d", count)
	}

	db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id = 'user-2'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 session for user-2, got %d", count)
	}
}

func TestExpiredSession(t *testing.T) {
	db := setupSessionDB(t)
	defer db.Close()

	session, err := CreateSession(db, "user-1")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Manually expire the session
	hashedID := hashToken(session.ID)
	_, err = db.Exec("UPDATE sessions SET expires_at = ? WHERE id = ?",
		time.Now().Add(-1*time.Hour).Format(time.RFC3339), hashedID)
	if err != nil {
		t.Fatalf("failed to expire session: %v", err)
	}

	retrieved, err := GetSession(db, session.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil for expired session")
	}
}
