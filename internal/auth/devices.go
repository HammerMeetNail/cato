package auth

import (
	"errors"
	"sync"
	"time"
)

// Device is one signed-in session as listed in Settings.
type Device struct {
	ID         int64  `json:"id"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	Current    bool   `json:"current"`
}

const maxUserAgent = 300

// NoteSessionClient records the browser a new session signed in from.
func NoteSessionClient(db Querier, sessionID, userAgent string) {
	if len(userAgent) > maxUserAgent {
		userAgent = userAgent[:maxUserAgent]
	}
	db.Exec(`UPDATE sessions SET user_agent = ?, last_seen_at = ? WHERE id = ?`,
		userAgent, time.Now().UTC().Format(time.RFC3339), hashToken(sessionID))
}

// touchInterval bounds last_seen_at writes to one per session per hour, so
// reads don't become writes.
const touchInterval = time.Hour

var touched sync.Map // hashed session ID → time.Time

// TouchSession updates the session's last use, at most once per hour.
func TouchSession(db Querier, sessionID string) {
	h := hashToken(sessionID)
	now := time.Now()
	if last, ok := touched.Load(h); ok && now.Sub(last.(time.Time)) < touchInterval {
		return
	}
	touched.Store(h, now)
	db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now.UTC().Format(time.RFC3339), h)
}

// ListDevices returns the user's unexpired sessions, newest use first.
func ListDevices(db Querier, userID, currentSessionID string) ([]Device, error) {
	rows, err := db.Query(`SELECT rowid, id, user_agent, created_at, COALESCE(last_seen_at, created_at)
		FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY COALESCE(last_seen_at, created_at) DESC`,
		userID, time.Now().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cur := hashToken(currentSessionID)
	out := []Device{}
	for rows.Next() {
		var d Device
		var hashed string
		if err := rows.Scan(&d.ID, &hashed, &d.UserAgent, &d.CreatedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		d.Current = hashed == cur
		out = append(out, d)
	}
	return out, rows.Err()
}

var ErrNoSuchDevice = errors.New("no such device")

// RevokeDevice signs one of the user's sessions out.
func RevokeDevice(db Querier, userID string, deviceID int64) error {
	res, err := db.Exec(`DELETE FROM sessions WHERE rowid = ? AND user_id = ?`, deviceID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchDevice
	}
	return nil
}

// RevokeOtherDevices signs out every session but the current one.
func RevokeOtherDevices(db Querier, userID, currentSessionID string) (int64, error) {
	res, err := db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, hashToken(currentSessionID))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
