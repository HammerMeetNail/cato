package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// AdminUser is a row on the admin Users page.
type AdminUser struct {
	Person
	Email      string `json:"email"`
	Role       string `json:"role"`
	Disabled   bool   `json:"disabled"`
	Visibility string `json:"visibility"`
	CreatedAt  string `json:"created_at"`
	LastSeen   string `json:"last_seen_at"`
	Games      int    `json:"games"`
	Google     bool   `json:"google"`
	Password   bool   `json:"password"`
}

func (s *Service) AdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, COALESCE(u.username, ''), u.display_name, u.avatar_path, u.use_gravatar, u.email, u.avatar_url,
			u.email, u.role, u.disabled, u.visibility, u.created_at,
			COALESCE((SELECT MAX(COALESCE(se.last_seen_at, se.created_at)) FROM sessions se WHERE se.user_id = u.id), ''),
			(SELECT COUNT(*) FROM library_items li WHERE li.user_id = u.id),
			u.google_subject IS NOT NULL, COALESCE(u.password_hash, '') != ''
		FROM users u ORDER BY u.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminUser{}
	for rows.Next() {
		var a AdminUser
		a.Person, err = scanPerson(rows, &a.Email, &a.Role, &a.Disabled, &a.Visibility, &a.CreatedAt, &a.LastSeen, &a.Games, &a.Google, &a.Password)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AdminUserEdit changes someone's role or disables the account.
type AdminUserEdit struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

// ErrLastAdmin protects the server from losing its only admin.
var ErrLastAdmin = errors.New("last admin")

func (s *Service) AdminEditUser(ctx context.Context, adminID, userID string, e AdminUserEdit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var role string
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE id = ?`, userID).Scan(&role, &disabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	losingAdmin := role == "admin" && !disabled &&
		((e.Role != nil && *e.Role != "admin") || (e.Disabled != nil && *e.Disabled))
	if losingAdmin {
		var admins int
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&admins)
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	if e.Role != nil {
		if *e.Role != "admin" && *e.Role != "user" {
			return invalid("invalid_role", "role", "Role is user or admin")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, *e.Role, userID); err != nil {
			return err
		}
	}
	if e.Disabled != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, *e.Disabled, userID); err != nil {
			return err
		}
		if *e.Disabled {
			// Disabling signs the account out everywhere.
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
				return err
			}
		}
	}
	b, _ := json.Marshal(e)
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (actor_id, action, target, data_json) VALUES (?, 'user.edit', ?, ?)`, adminID, userID, string(b)); err != nil {
		return err
	}
	return tx.Commit()
}

// SetSignupMode overrides CATO_SIGNUP ("" returns to the environment).
func (s *Service) SetSignupMode(ctx context.Context, adminID, mode string) error {
	switch mode {
	case "":
		if _, err := s.db.ExecContext(ctx, `DELETE FROM instance_settings WHERE key = 'signup'`); err != nil {
			return err
		}
	case "open", "invite", "closed":
		if _, err := s.db.ExecContext(ctx, `INSERT INTO instance_settings (key, value) VALUES ('signup', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, mode); err != nil {
			return err
		}
	default:
		return invalid("invalid_mode", "signup", "Choose open, invite or closed")
	}
	b, _ := json.Marshal(map[string]string{"signup": mode})
	s.Audit(ctx, adminID, "settings.signup", "", string(b))
	return nil
}

// SignupOverride is the admin's signup choice ("" when the env decides).
func (s *Service) SignupOverride(ctx context.Context) string {
	var v string
	s.db.QueryRowContext(ctx, `SELECT value FROM instance_settings WHERE key = 'signup'`).Scan(&v)
	return v
}

// ReportRow is an open report with what it points at.
type ReportRow struct {
	ID        int64   `json:"id"`
	Kind      string  `json:"kind"`
	Target    string  `json:"target"`
	Reason    string  `json:"reason"`
	Reporter  Person  `json:"reporter"`
	CreatedAt string  `json:"created_at"`
	Resolved  bool    `json:"resolved"`
	Review    *Review `json:"review,omitempty"`
	Excerpt   string  `json:"excerpt,omitempty"`
}

func (s *Service) Reports(ctx context.Context, adminID string, includeResolved bool) ([]ReportRow, error) {
	where := `rp.resolved_at IS NULL`
	if includeResolved {
		where = `1`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rp.id, rp.kind, rp.target, rp.reason, rp.created_at, rp.resolved_at IS NOT NULL, `+personCols+`
		FROM reports rp JOIN users u ON u.id = rp.reporter_id WHERE `+where+` ORDER BY rp.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	out := []ReportRow{}
	for rows.Next() {
		var r ReportRow
		r.Reporter, err = scanPerson(rowPrefix{rows, []any{&r.ID, &r.Kind, &r.Target, &r.Reason, &r.CreatedAt, &r.Resolved}})
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	for i := range out {
		switch out[i].Kind {
		case "review":
			// Admins see the reported text whatever its visibility.
			var body string
			s.db.QueryRowContext(ctx, `SELECT body FROM reviews WHERE id = ?`, out[i].Target).Scan(&body)
			out[i].Excerpt = truncate(body, 400)
		case "list":
			var title, desc string
			s.db.QueryRowContext(ctx, `SELECT title, description FROM lists WHERE id = ?`, out[i].Target).Scan(&title, &desc)
			out[i].Excerpt = truncate(title+" — "+desc, 400)
		case "user":
			var name, bio string
			s.db.QueryRowContext(ctx, `SELECT COALESCE(username, ''), bio FROM users WHERE id = ?`, out[i].Target).Scan(&name, &bio)
			out[i].Excerpt = truncate("@"+name+" — "+bio, 400)
		}
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ResolveReport closes a report, hiding what it points at when hide is set.
func (s *Service) ResolveReport(ctx context.Context, adminID string, id int64, hide bool) error {
	var kind, target string
	err := s.db.QueryRowContext(ctx, `SELECT kind, target FROM reports WHERE id = ?`, id).Scan(&kind, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if hide {
		switch kind {
		case "review":
			if err := s.SetHidden(ctx, adminID, "review", target, true); err != nil {
				return err
			}
		case "list":
			if err := s.SetHidden(ctx, adminID, "list", target, true); err != nil {
				return err
			}
		}
	}
	// Resolving one resolves every open report about the same thing.
	if _, err := s.db.ExecContext(ctx, `UPDATE reports SET resolved_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), resolved_by = ?
		WHERE kind = ? AND target = ? AND resolved_at IS NULL`, adminID, kind, target); err != nil {
		return err
	}
	s.Audit(ctx, adminID, "report.resolve", kind+":"+target, `{"hide":`+map[bool]string{true: "true", false: "false"}[hide]+`}`)
	return nil
}

// SetHidden hides or restores a review or list (admin moderation).
func (s *Service) SetHidden(ctx context.Context, adminID, kind, id string, hidden bool) error {
	var q string
	switch kind {
	case "review":
		q = `UPDATE reviews SET hidden_at = CASE WHEN ? THEN strftime('%Y-%m-%dT%H:%M:%SZ', 'now') END, hidden_by = CASE WHEN ? THEN ? END WHERE id = ?`
	case "list":
		q = `UPDATE lists SET hidden_at = CASE WHEN ? THEN strftime('%Y-%m-%dT%H:%M:%SZ', 'now') END WHERE id = ?`
	default:
		return invalid("invalid_kind", "kind", "Hide a review or list")
	}
	var res sql.Result
	var err error
	if kind == "review" {
		res, err = s.db.ExecContext(ctx, q, hidden, hidden, adminID, id)
	} else {
		res, err = s.db.ExecContext(ctx, q, hidden, id)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	action := kind + ".hide"
	if !hidden {
		action = kind + ".unhide"
	}
	s.Audit(ctx, adminID, action, id, "")
	return nil
}

// AuditEntry is one admin action.
type AuditEntry struct {
	ID        int64          `json:"id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Target    string         `json:"target"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt string         `json:"created_at"`
}

func (s *Service) AuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, COALESCE(u.username, a.actor_id, ''), a.action, a.target, a.data_json, a.created_at
		FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id ORDER BY a.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var data string
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &data, &e.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(data), &e.Data)
		out = append(out, e)
	}
	return out, rows.Err()
}
