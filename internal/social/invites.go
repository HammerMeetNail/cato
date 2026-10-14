package social

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"cato/internal/auth"
)

// InviteTTL is how long an invite link works.
const InviteTTL = 7 * 24 * time.Hour

// ResetTTL is how long an admin-made password reset link works.
const ResetTTL = 24 * time.Hour

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// Invite is a link someone made. Token is only known when it's created.
type Invite struct {
	ID        string  `json:"id"`
	Token     string  `json:"token,omitempty"`
	Note      string  `json:"note"`
	CreatedBy *Person `json:"created_by,omitempty"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt string  `json:"expires_at"`
	UsedBy    *Person `json:"used_by,omitempty"`
	UsedAt    string  `json:"used_at,omitempty"`
	State     string  `json:"state"` // open, used, expired, revoked
}

// EffectiveSignup is the signup mode in force: the admin's choice in
// instance settings wins over CATO_SIGNUP.
func (s *Service) EffectiveSignup(ctx context.Context, envMode string) string {
	var v string
	s.db.QueryRowContext(ctx, `SELECT value FROM instance_settings WHERE key = 'signup'`).Scan(&v)
	switch v {
	case "open", "invite", "closed":
		return v
	}
	switch envMode {
	case "open", "invite":
		return envMode
	}
	return "closed"
}

// CreateInvite makes a 7-day, single-use link. In closed mode only admins
// can invite.
func (s *Service) CreateInvite(ctx context.Context, userID, envMode, note string) (*Invite, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 100 {
		return nil, invalid("too_long", "note", "Keep the note short")
	}
	admin := s.IsAdmin(ctx, userID)
	if s.EffectiveSignup(ctx, envMode) == "closed" && !admin {
		return nil, invalid("signup_closed", "", "Sign-up is closed on this server; ask an admin")
	}
	var open int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM invites WHERE created_by = ? AND used_by IS NULL AND revoked_at IS NULL AND expires_at > ?`,
		userID, time.Now().UTC().Format(time.RFC3339)).Scan(&open)
	if open >= MaxOpenInvites && !admin {
		return nil, invalid("too_many", "", "You have 10 unused invites; revoke one first")
	}
	token := auth.RandomToken(24)
	exp := time.Now().Add(InviteTTL).UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO invites (token_hash, created_by, note, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, note, exp); err != nil {
		return nil, err
	}
	inv, err := s.invite(ctx, hashToken(token))
	if err != nil {
		return nil, err
	}
	inv.Token = token
	return inv, nil
}

func (s *Service) invites(ctx context.Context, where string, args ...any) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.token_hash, i.note, i.created_at, i.expires_at, COALESCE(i.used_at, ''), i.revoked_at IS NOT NULL,
			COALESCE(i.used_by, ''), i.created_by
		FROM invites i WHERE `+where+` ORDER BY i.created_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	type row struct {
		inv     Invite
		usedBy  string
		creator string
	}
	var rs []row
	now := time.Now().UTC().Format(time.RFC3339)
	for rows.Next() {
		var r row
		var hash string
		var revoked bool
		if err := rows.Scan(&hash, &r.inv.Note, &r.inv.CreatedAt, &r.inv.ExpiresAt, &r.inv.UsedAt, &revoked, &r.usedBy, &r.creator); err != nil {
			rows.Close()
			return nil, err
		}
		r.inv.ID = hash[:16]
		switch {
		case r.inv.UsedAt != "":
			r.inv.State = "used"
		case revoked:
			r.inv.State = "revoked"
		case r.inv.ExpiresAt <= now:
			r.inv.State = "expired"
		default:
			r.inv.State = "open"
		}
		rs = append(rs, r)
	}
	rows.Close()
	out := []Invite{}
	for _, r := range rs {
		if r.usedBy != "" {
			if p, err := s.Person(ctx, r.usedBy); err == nil {
				r.inv.UsedBy = &p
			}
		}
		if p, err := s.Person(ctx, r.creator); err == nil {
			r.inv.CreatedBy = &p
		}
		out = append(out, r.inv)
	}
	return out, rows.Err()
}

func (s *Service) invite(ctx context.Context, hash string) (*Invite, error) {
	is, err := s.invites(ctx, `i.token_hash = ?`, hash)
	if err != nil {
		return nil, err
	}
	if len(is) == 0 {
		return nil, ErrNotFound
	}
	return &is[0], nil
}

// MyInvites lists invites the user made.
func (s *Service) MyInvites(ctx context.Context, userID string) ([]Invite, error) {
	return s.invites(ctx, `i.created_by = ?`, userID)
}

// AllInvites is the admin view.
func (s *Service) AllInvites(ctx context.Context) ([]Invite, error) {
	return s.invites(ctx, `1`)
}

// RevokeInvite cancels an unused invite (its maker or an admin).
func (s *Service) RevokeInvite(ctx context.Context, userID, id string) error {
	if len(id) != 16 {
		return ErrNotFound
	}
	q := `UPDATE invites SET revoked_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE substr(token_hash, 1, 16) = ? AND used_by IS NULL`
	args := []any{id}
	if !s.IsAdmin(ctx, userID) {
		q += ` AND created_by = ?`
		args = append(args, userID)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// InviteInfo is what the sign-up page learns about an invite token.
type InviteInfo struct {
	Valid     bool   `json:"invite_valid"`
	InvitedBy string `json:"invited_by,omitempty"`
}

// CheckInvite reports whether token can create an account now.
func (s *Service) CheckInvite(ctx context.Context, token, envMode string) InviteInfo {
	if token == "" {
		return InviteInfo{}
	}
	var name, user, role string
	err := s.db.QueryRowContext(ctx, `SELECT u.display_name, COALESCE(u.username, ''), u.role FROM invites i JOIN users u ON u.id = i.created_by
		WHERE i.token_hash = ? AND i.used_by IS NULL AND i.revoked_at IS NULL AND i.expires_at > ? AND u.disabled = 0`,
		hashToken(token), time.Now().UTC().Format(time.RFC3339)).Scan(&name, &user, &role)
	if err != nil {
		return InviteInfo{}
	}
	if s.EffectiveSignup(ctx, envMode) == "closed" && role != "admin" {
		return InviteInfo{}
	}
	if name == "" {
		name = user
	}
	return InviteInfo{Valid: true, InvitedBy: name}
}

// ErrSignupClosed means no account may be created with what was given.
var ErrSignupClosed = errors.New("signup closed")

// NewUser is an account to create.
type NewUser struct {
	ID            string
	Email         string
	PasswordHash  string
	DisplayName   string
	AvatarURL     string
	GoogleSubject any
	Invite        string
}

// CreateUser creates an account under the effective signup mode, in one
// transaction: the first account on an empty server is always allowed (and
// is the admin); "open" lets anyone in; "invite" and "closed" need a valid
// invite (in closed mode, an admin's). The invite is used up, and the new
// person and the inviter follow each other.
func (s *Service) CreateUser(ctx context.Context, envMode string, u NewUser) error {
	mode := s.EffectiveSignup(ctx, envMode)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var users int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	var inviter, inviteHash string
	if u.Invite != "" {
		var role string
		err := tx.QueryRowContext(ctx, `SELECT i.token_hash, i.created_by, u.role FROM invites i JOIN users u ON u.id = i.created_by
			WHERE i.token_hash = ? AND i.used_by IS NULL AND i.revoked_at IS NULL AND i.expires_at > ? AND u.disabled = 0`,
			hashToken(u.Invite), time.Now().UTC().Format(time.RFC3339)).Scan(&inviteHash, &inviter, &role)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if mode == "closed" && role != "admin" {
			inviter = ""
		}
	}
	if users > 0 && mode != "open" && inviter == "" {
		return ErrSignupClosed
	}
	username, err := SuggestUsername(ctx, tx, u.Email)
	if err != nil {
		return err
	}
	role := "user"
	if users == 0 {
		role = "admin"
	}
	var hash any
	if u.PasswordHash != "" {
		hash = u.PasswordHash
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, email, password_hash, display_name, avatar_url, google_subject, username, role)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, u.ID, u.Email, hash, u.DisplayName, u.AvatarURL, u.GoogleSubject, username, role); err != nil {
		return err
	}
	if inviter != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE invites SET used_by = ?, used_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE token_hash = ?`, u.ID, inviteHash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO follows (follower_id, followee_id, status) VALUES (?1, ?2, 'accepted'), (?2, ?1, 'accepted')`, u.ID, inviter); err != nil {
			return err
		}
		if err := notify(ctx, tx, inviter, "invite_used", u.ID, 0, 0, nil); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CreateResetLink makes a single-use password reset token for a user
// (admin only; audited by the caller).
func (s *Service) CreateResetLink(ctx context.Context, adminID, userID string) (string, error) {
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&n)
	if n == 0 {
		return "", ErrNotFound
	}
	token := auth.RandomToken(24)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM reset_tokens WHERE user_id = ? AND used_at IS NULL`, userID); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO reset_tokens (token_hash, user_id, created_by, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, adminID, time.Now().Add(ResetTTL).UTC().Format(time.RFC3339))
	return token, err
}

// ErrBadToken means a reset token is unknown, used or expired.
var ErrBadToken = errors.New("invalid or expired link")

// UseResetToken sets a new password with a reset token and signs the
// account out everywhere.
func (s *Service) UseResetToken(ctx context.Context, token, newHash string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var userID string
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM reset_tokens WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		hashToken(token), time.Now().UTC().Format(time.RFC3339)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBadToken
	}
	if err != nil {
		return "", err
	}
	for _, q := range []string{
		`UPDATE reset_tokens SET used_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE token_hash = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, hashToken(token)); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, newHash, userID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return "", err
	}
	return userID, tx.Commit()
}
