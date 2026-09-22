package auth

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupSessionDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
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

func TestDisabledSessionOwner(t *testing.T) {
	db := setupSessionDB(t)
	defer db.Close()
	session, err := CreateSession(db, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE users SET disabled = 1 WHERE id = 'user-1'"); err != nil {
		t.Fatal(err)
	}
	if got, err := GetSession(db, session.ID); err != nil || got != nil {
		t.Fatalf("disabled lookup = %v, %v", got, err)
	}
	if _, err := CreateSession(db, "user-1"); !errors.Is(err, ErrSessionRejected) {
		t.Fatalf("disabled issuance = %v", err)
	}
}

func TestPasswordChangeConcurrentLoginBoundary(t *testing.T) {
	for _, issueBeforeChange := range []bool{true, false} {
		name := "insert_after_change"
		if issueBeforeChange {
			name = "insert_before_change"
		}
		t.Run(name, func(t *testing.T) {
			db := setupSessionDB(t)
			defer db.Close()
			oldHash, err := HashPassword("oldpassword")
			if err != nil {
				t.Fatal(err)
			}
			newHash, err := HashPassword("newpassword")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE users SET password_hash = ? WHERE id = 'user-1'", oldHash); err != nil {
				t.Fatal(err)
			}
			current, err := CreateSession(db, "user-1")
			if err != nil {
				t.Fatal(err)
			}
			other, err := CreateSession(db, "user-1")
			if err != nil {
				t.Fatal(err)
			}
			verified := make(chan error, 1)
			release := make(chan struct{})
			type result struct {
				session *Session
				err     error
			}
			issued := make(chan result, 1)
			// Model the production read/check/insert path with an explicit barrier
			// after bcrypt verification. No sleep or scheduler assumption orders it.
			go func() {
				var hash string
				err := db.QueryRow("SELECT password_hash FROM users WHERE id = 'user-1'").Scan(&hash)
				if err == nil && !CheckPassword("oldpassword", hash) {
					err = errors.New("password check failed")
				}
				verified <- err
				<-release
				if err != nil {
					issued <- result{err: err}
					return
				}
				session, err := CreatePasswordSession(db, "user-1", hash)
				issued <- result{session, err}
			}()
			verificationErr := <-verified
			if verificationErr != nil {
				close(release)
				<-issued
				t.Fatal(verificationErr)
			}
			var login result
			if issueBeforeChange {
				close(release)
				login = <-issued
			}
			changeErr := ChangePassword(db, "user-1", current.ID, oldHash, newHash)
			if !issueBeforeChange {
				close(release)
				login = <-issued
			}
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			if issueBeforeChange {
				if login.err != nil {
					t.Fatal(login.err)
				}
				if got, err := GetSession(db, login.session.ID); err != nil || got != nil {
					t.Fatalf("old login survived: %v, %v", got, err)
				}
			} else if !errors.Is(login.err, ErrSessionRejected) {
				t.Fatalf("stale login issuance: %v", login.err)
			}
			if got, err := GetSession(db, current.ID); err != nil || got == nil {
				t.Fatalf("current session lost: %v", err)
			}
			if got, err := GetSession(db, other.ID); err != nil || got != nil {
				t.Fatalf("other session survived: %v", err)
			}
			if _, err := CreatePasswordSession(db, "user-1", newHash); err != nil {
				t.Fatalf("new login rejected: %v", err)
			}
		})
	}
}

func TestPasswordChangeRollsBackWhenRevocationFails(t *testing.T) {
	db := setupSessionDB(t)
	defer db.Close()
	if _, err := db.Exec("UPDATE users SET password_hash = 'old-hash' WHERE id = 'user-1'"); err != nil {
		t.Fatal(err)
	}
	current, err := CreateSession(db, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := CreateSession(db, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_revocation BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := ChangePassword(db, "user-1", current.ID, "old-hash", "new-hash"); err == nil {
		t.Fatal("expected revocation failure")
	}
	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id = 'user-1'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "old-hash" {
		t.Fatal("password update escaped rollback")
	}
	if session, err := GetSession(db, other.ID); err != nil || session == nil {
		t.Fatalf("session lost on rollback: %v", err)
	}
}
