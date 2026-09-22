package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Querier is satisfied by both *sql.DB and *db.DB.
// This allows auth functions to work with either type.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

type Session struct {
	ID        string
	UserID    string
	CSRFToken string
	ExpiresAt time.Time
}

var ErrSessionRejected = errors.New("account or credentials no longer valid")

func CreateSession(db Querier, userID string) (*Session, error) {
	return createSession(db, userID, nil)
}

// CreatePasswordSession binds issuance to the hash that was actually verified.
// SQLite serializes this insert with password changes on the writer.
func CreatePasswordSession(db Querier, userID, verifiedHash string) (*Session, error) {
	return createSession(db, userID, &verifiedHash)
}

func createSession(db Querier, userID string, verifiedHash *string) (*Session, error) {
	id := RandomToken(32)
	csrf := RandomToken(32)
	hashedID := hashToken(id)

	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	query := "INSERT INTO sessions (id, user_id, csrf_token, expires_at) SELECT ?, id, ?, ? FROM users WHERE id = ? AND disabled = 0"
	args := []any{hashedID, csrf, expiresAt.Format(time.RFC3339), userID}
	if verifiedHash != nil {
		query += " AND password_hash = ?"
		args = append(args, *verifiedHash)
	}
	result, err := db.Exec(query, args...)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrSessionRejected
	}

	return &Session{
		ID:        id,
		UserID:    userID,
		CSRFToken: csrf,
		ExpiresAt: expiresAt,
	}, nil
}

func GetSession(db Querier, sessionID string) (*Session, error) {
	hashedID := hashToken(sessionID)

	var s Session
	var expiresAtStr string
	err := db.QueryRow(
		"SELECT s.id, s.user_id, s.csrf_token, s.expires_at FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = ? AND u.disabled = 0",
		hashedID,
	).Scan(&s.ID, &s.UserID, &s.CSRFToken, &expiresAtStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	s.ExpiresAt, err = time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse expires_at: %w", err)
	}

	if time.Now().After(s.ExpiresAt) {
		db.Exec("DELETE FROM sessions WHERE id = ?", hashedID)
		return nil, nil
	}

	// Restore the unhashed session ID
	s.ID = sessionID
	return &s, nil
}

func DeleteSession(db Querier, sessionID string) error {
	hashedID := hashToken(sessionID)
	_, err := db.Exec("DELETE FROM sessions WHERE id = ?", hashedID)
	return err
}

func DeleteUserSessions(db Querier, userID string) error {
	_, err := db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

// CleanupExpiredSessions deletes sessions whose expiry has passed. GetSession
// only removes a session lazily when that exact cookie is presented, so
// abandoned sessions would otherwise accumulate forever. Call this
// periodically (e.g. from a background ticker).
//
// expires_at is written by CreateSession as RFC3339 in server-local time
// (matching time.Now().Format(time.RFC3339)), so the cutoff uses the same
// format — RFC3339 strings with a consistent offset compare correctly
// lexicographically.
func CleanupExpiredSessions(db Querier) (int64, error) {
	res, err := db.Exec("DELETE FROM sessions WHERE expires_at < ?",
		time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func RandomToken(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ChangePassword atomically replaces verified credentials and revokes every
// other session. The retained session must still belong to an enabled user.
func ChangePassword(db interface{ Begin() (*sql.Tx, error) }, userID, currentSession, verifiedHash, newHash string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP
 WHERE id = ? AND password_hash = ? AND disabled = 0
 AND EXISTS (SELECT 1 FROM sessions WHERE id = ? AND user_id = users.id AND julianday(expires_at) > julianday('now'))`,
		newHash, userID, verifiedHash, hashToken(currentSession))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSessionRejected
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE user_id = ? AND id != ?", userID, hashToken(currentSession)); err != nil {
		return err
	}
	return tx.Commit()
}
