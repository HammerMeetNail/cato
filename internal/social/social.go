// Package social holds the people features (plan §7): profiles, follows
// and blocks, reviews, lists, the activity feed, notifications, invites and
// the admin tools. Everything is limited to this server and private by
// default.
//
// Who may see what is decided in one place, CanSee and the SQL predicate
// visibleSQL, so every query applies the same rules:
//
//   - you always see your own things;
//   - a block (either way) hides everything, and a disabled account is hidden;
//   - "private" is you only; "followers" adds people whose follow you
//     accepted; "instance" is everyone with an account on this server;
//   - reviews and lists can override the owner's level ("inherit" uses it).
package social

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"cato/internal/db"
)

// Visibility levels.
const (
	Private   = "private"
	Followers = "followers"
	Instance  = "instance"
	Inherit   = "inherit"
)

// Size limits for user text (plan §7 security work).
const (
	MaxBio         = 500
	MaxReview      = 10000
	MaxListTitle   = 100
	MaxListDesc    = 2000
	MaxListNote    = 500
	MaxListItems   = 500
	MaxReportText  = 500
	MaxFavourites  = 4
	MaxOpenInvites = 10
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrBlocked   = errors.New("blocked")
)

// Error is a validation failure naming the offending field.
type Error struct {
	Code    string
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Message }

func invalid(code, field, msg string) error { return &Error{Code: code, Field: field, Message: msg} }

// Service is the social store.
type Service struct {
	db *db.DB
	// AvatarDir holds uploaded avatars (data/avatars).
	AvatarDir string
}

func New(database *db.DB, avatarDir string) *Service {
	return &Service{db: database, AvatarDir: avatarDir}
}

func validLevel(v string, allowInherit bool) bool {
	switch v {
	case Private, Followers, Instance:
		return true
	case Inherit:
		return allowInherit
	}
	return false
}

// visibleSQL is a WHERE fragment true when viewer (the first argument it
// takes) may see something owned by ownerCol at levelExpr. It takes the
// viewer ID four times: use visibleArgs.
func visibleSQL(ownerCol, levelExpr string) string {
	return `(` + ownerCol + ` = ? OR (
		NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = ` + ownerCol + ` AND b.blocked_id = ?) OR (b.blocker_id = ? AND b.blocked_id = ` + ownerCol + `))
		AND (SELECT disabled FROM users du WHERE du.id = ` + ownerCol + `) = 0
		AND (` + levelExpr + ` = 'instance' OR (` + levelExpr + ` = 'followers' AND EXISTS (
			SELECT 1 FROM follows vf WHERE vf.follower_id = ? AND vf.followee_id = ` + ownerCol + ` AND vf.status = 'accepted')))))`
}

func visibleArgs(viewer string) []any { return []any{viewer, viewer, viewer, viewer} }

// effective resolves an item's level against its owner's.
func effective(item, owner string) string {
	if item == "" || item == Inherit {
		return owner
	}
	return item
}

// CanSee reports whether viewer may see what owner shares at level
// ("inherit" means the owner's profile level).
func (s *Service) CanSee(ctx context.Context, viewer, owner, level string) (bool, error) {
	if viewer == owner {
		return true, nil
	}
	var ownerLevel string
	var disabled int
	err := s.db.QueryRowContext(ctx, `SELECT visibility, disabled FROM users WHERE id = ?`, owner).Scan(&ownerLevel, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if disabled != 0 {
		return false, nil
	}
	if blocked, err := s.blockedEitherWay(ctx, viewer, owner); err != nil || blocked {
		return false, err
	}
	switch effective(level, ownerLevel) {
	case Instance:
		return true, nil
	case Followers:
		return s.follows(ctx, viewer, owner)
	}
	return false, nil
}

func (s *Service) blockedEitherWay(ctx context.Context, a, b string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocks WHERE (blocker_id = ? AND blocked_id = ?) OR (blocker_id = ? AND blocked_id = ?)`,
		a, b, b, a).Scan(&n)
	return n > 0, err
}

// follows reports an accepted follow from a to b.
func (s *Service) follows(ctx context.Context, a, b string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM follows WHERE follower_id = ? AND followee_id = ? AND status = 'accepted'`, a, b).Scan(&n)
	return n > 0, err
}

// IsAdmin reports whether the user is an enabled admin.
func (s *Service) IsAdmin(ctx context.Context, userID string) bool {
	var role string
	var disabled int
	if err := s.db.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE id = ?`, userID).Scan(&role, &disabled); err != nil {
		return false
	}
	return role == "admin" && disabled == 0
}

var usernameRe = regexp.MustCompile(`^[a-z0-9_.-]{1,32}$`)

// NormalizeUsername lower-cases and checks a username.
func NormalizeUsername(u string) (string, error) {
	u = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(u), "@")))
	if !usernameRe.MatchString(u) {
		return "", invalid("invalid_username", "username", "Use 1–32 letters, digits, dots, dashes or underscores")
	}
	if reservedUsernames[u] {
		return "", invalid("username_taken", "username", "That username is reserved")
	}
	return u, nil
}

var reservedUsernames = map[string]bool{"me": true, "you": true, "api": true, "settings": true, "system": true, "cato": true}

// SuggestUsername derives a free username from an email address. q must
// be inside the transaction creating the user.
func SuggestUsername(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, email string) (string, error) {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	var b strings.Builder
	for _, r := range local {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == '.' || r == '+':
			b.WriteByte('_')
		}
	}
	base := b.String()
	if len(base) > 24 {
		base = base[:24]
	}
	if base == "" || reservedUsernames[base] {
		base = "player"
	}
	cand := base
	for i := 2; i < 10000; i++ {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE`, cand).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return cand, nil
		}
		cand = base + "_" + strconv.Itoa(i)
	}
	return "", errors.New("no free username")
}

// Audit records an admin action.
func (s *Service) Audit(ctx context.Context, actor, action, target, dataJSON string) {
	if dataJSON == "" {
		dataJSON = "{}"
	}
	s.db.ExecContext(ctx, `INSERT INTO audit_log (actor_id, action, target, data_json) VALUES (?, ?, ?, ?)`, actor, action, target, dataJSON)
}
