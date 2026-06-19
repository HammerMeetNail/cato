package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

type Session struct {
	ID        string
	UserID    string
	CSRFToken string
	ExpiresAt time.Time
}

func CreateSession(db *sql.DB, userID string) (*Session, error) {
	id := RandomToken(32)
	csrf := RandomToken(32)
	hashedID := hashToken(id)

	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	_, err := db.Exec(
		"INSERT INTO sessions (id, user_id, csrf_token, expires_at) VALUES (?, ?, ?, ?)",
		hashedID, userID, csrf, expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &Session{
		ID:        id,
		UserID:    userID,
		CSRFToken: csrf,
		ExpiresAt: expiresAt,
	}, nil
}

func GetSession(db *sql.DB, sessionID string) (*Session, error) {
	hashedID := hashToken(sessionID)

	var s Session
	var expiresAtStr string
	err := db.QueryRow(
		"SELECT id, user_id, csrf_token, expires_at FROM sessions WHERE id = ?",
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

func DeleteSession(db *sql.DB, sessionID string) error {
	hashedID := hashToken(sessionID)
	_, err := db.Exec("DELETE FROM sessions WHERE id = ?", hashedID)
	return err
}

func DeleteUserSessions(db *sql.DB, userID string) error {
	_, err := db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	return err
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
