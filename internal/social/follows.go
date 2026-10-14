package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Follow asks to follow someone. Accounts visible to the whole instance
// accept at once; others get a request to approve. It returns the follow
// status ("accepted" or "pending").
func (s *Service) Follow(ctx context.Context, viewer, target string) (string, error) {
	if viewer == target {
		return "", invalid("invalid_target", "user", "You can't follow yourself")
	}
	var level string
	var disabled int
	err := s.db.QueryRowContext(ctx, `SELECT visibility, disabled FROM users WHERE id = ?`, target).Scan(&level, &disabled)
	if errors.Is(err, sql.ErrNoRows) || disabled != 0 {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if blocked, err := s.blockedEitherWay(ctx, viewer, target); err != nil {
		return "", err
	} else if blocked {
		return "", ErrBlocked
	}
	status := "pending"
	if level == Instance {
		status = "accepted"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var cur string
	tx.QueryRowContext(ctx, `SELECT status FROM follows WHERE follower_id = ? AND followee_id = ?`, viewer, target).Scan(&cur)
	if cur != "" {
		return cur, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO follows (follower_id, followee_id, status) VALUES (?, ?, ?)`, viewer, target, status); err != nil {
		return "", err
	}
	kind := "follow"
	if status == "pending" {
		kind = "follow_request"
	}
	if err := notify(ctx, tx, target, kind, viewer, 0, 0, nil); err != nil {
		return "", err
	}
	return status, tx.Commit()
}

// Unfollow stops following (or withdraws a request).
func (s *Service) Unfollow(ctx context.Context, viewer, target string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM follows WHERE follower_id = ? AND followee_id = ?`, viewer, target)
	if err == nil {
		s.db.ExecContext(ctx, `DELETE FROM notifications WHERE user_id = ? AND actor_id = ? AND kind = 'follow_request'`, target, viewer)
	}
	return err
}

// Accept approves a follow request from requester.
func (s *Service) Accept(ctx context.Context, viewer, requester string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE follows SET status = 'accepted' WHERE follower_id = ? AND followee_id = ? AND status = 'pending'`, requester, viewer)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE user_id = ? AND actor_id = ? AND kind = 'follow_request'`, viewer, requester); err != nil {
		return err
	}
	if err := notify(ctx, tx, requester, "follow_accepted", viewer, 0, 0, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveFollower declines a request or removes an existing follower.
func (s *Service) RemoveFollower(ctx context.Context, viewer, follower string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM follows WHERE follower_id = ? AND followee_id = ?`, follower, viewer)
	if err == nil {
		s.db.ExecContext(ctx, `DELETE FROM notifications WHERE user_id = ? AND actor_id = ? AND kind = 'follow_request'`, viewer, follower)
	}
	return err
}

// Block hides each of you from the other and ends follows both ways.
func (s *Service) Block(ctx context.Context, viewer, target string) error {
	if viewer == target {
		return invalid("invalid_target", "user", "You can't block yourself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`INSERT INTO blocks (blocker_id, blocked_id) VALUES (?1, ?2) ON CONFLICT DO NOTHING`,
		`DELETE FROM follows WHERE (follower_id = ?1 AND followee_id = ?2) OR (follower_id = ?2 AND followee_id = ?1)`,
		`DELETE FROM notifications WHERE user_id = ?1 AND actor_id = ?2`,
		`DELETE FROM review_likes WHERE user_id = ?2 AND review_id IN (SELECT id FROM reviews WHERE user_id = ?1)`,
	} {
		if _, err := tx.ExecContext(ctx, q, viewer, target); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) Unblock(ctx context.Context, viewer, target string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM blocks WHERE blocker_id = ? AND blocked_id = ?`, viewer, target)
	return err
}

// Connection is a person with when the connection started.
type Connection struct {
	Person
	Status string `json:"status,omitempty"`
	Since  string `json:"since"`
}

// People lists your followers, who you follow, pending requests and blocks.
type People struct {
	Followers []Connection `json:"followers"`
	Following []Connection `json:"following"`
	Requests  []Connection `json:"requests"`
	Blocked   []Connection `json:"blocked"`
}

func (s *Service) People(ctx context.Context, viewer string) (*People, error) {
	p := &People{}
	var err error
	q := func(sqlText string) []Connection {
		if err != nil {
			return nil
		}
		var rows *sql.Rows
		rows, err = s.db.QueryContext(ctx, sqlText, viewer)
		if err != nil {
			return nil
		}
		defer rows.Close()
		out := []Connection{}
		for rows.Next() {
			var c Connection
			c.Person, err = scanPerson(rows, &c.Status, &c.Since)
			if err != nil {
				return nil
			}
			out = append(out, c)
		}
		err = rows.Err()
		return out
	}
	p.Followers = q(`SELECT ` + personCols + `, f.status, f.created_at FROM follows f JOIN users u ON u.id = f.follower_id
		WHERE f.followee_id = ? AND f.status = 'accepted' AND u.disabled = 0 ORDER BY f.created_at DESC`)
	p.Following = q(`SELECT ` + personCols + `, f.status, f.created_at FROM follows f JOIN users u ON u.id = f.followee_id
		WHERE f.follower_id = ? AND u.disabled = 0 ORDER BY f.status, f.created_at DESC`)
	p.Requests = q(`SELECT ` + personCols + `, f.status, f.created_at FROM follows f JOIN users u ON u.id = f.follower_id
		WHERE f.followee_id = ? AND f.status = 'pending' AND u.disabled = 0 ORDER BY f.created_at DESC`)
	p.Blocked = q(`SELECT ` + personCols + `, '', b.created_at FROM blocks b JOIN users u ON u.id = b.blocked_id
		WHERE b.blocker_id = ? ORDER BY b.created_at DESC`)
	return p, err
}

// --- notifications -------------------------------------------------------

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// notify adds an inbox entry (never for your own actions).
func notify(ctx context.Context, q execer, userID, kind, actor string, gameID, refID int64, data map[string]any) error {
	if userID == actor {
		return nil
	}
	dataJSON := "{}"
	if len(data) > 0 {
		b, _ := json.Marshal(data)
		dataJSON = string(b)
	}
	var a, g, r any
	if actor != "" {
		a = actor
	}
	if gameID != 0 {
		g = gameID
	}
	if refID != 0 {
		r = refID
	}
	_, err := q.ExecContext(ctx, `INSERT INTO notifications (user_id, kind, actor_id, game_id, ref_id, data_json) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, kind, a, g, r, dataJSON)
	return err
}

// Notify adds an inbox entry from another package (release alerts etc.).
func (s *Service) Notify(ctx context.Context, userID, kind string, gameID int64, data map[string]any) error {
	return notify(ctx, s.db, userID, kind, "", gameID, 0, data)
}

// Notification is one inbox entry.
type Notification struct {
	ID        int64          `json:"id"`
	Kind      string         `json:"kind"`
	Actor     *Person        `json:"actor,omitempty"`
	Game      *GameRef       `json:"game,omitempty"`
	RefID     int64          `json:"ref_id,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt string         `json:"created_at"`
	Read      bool           `json:"read"`
}

// GameRef is a game named in social content.
type GameRef struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Cover string `json:"cover"`
}

// Inbox lists notifications newest first (before = id cursor).
func (s *Service) Inbox(ctx context.Context, userID string, before int64, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.id, n.kind, COALESCE(n.ref_id, 0), n.data_json, n.created_at, n.read_at IS NOT NULL,
			COALESCE(g.id, 0), COALESCE(g.name, ''), COALESCE(g.local_cover_path, ''), COALESCE(g.cover_url, ''),
			COALESCE(u.id, ''), COALESCE(u.username, ''), COALESCE(u.display_name, ''), COALESCE(u.avatar_path, ''), COALESCE(u.use_gravatar, 0), COALESCE(u.email, '')
		FROM notifications n
		LEFT JOIN games g ON g.id = n.game_id
		LEFT JOIN users u ON u.id = n.actor_id AND u.disabled = 0
		WHERE n.user_id = ? AND n.id < ?
		  AND (n.actor_id IS NULL OR NOT EXISTS (SELECT 1 FROM blocks b WHERE b.blocker_id = n.user_id AND b.blocked_id = n.actor_id))
		ORDER BY n.id DESC LIMIT ?`, userID, before, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var dataJSON, gName, gLocal, gRemote, avatarPath, email string
		var gID int64
		var p Person
		var grav int
		if err := rows.Scan(&n.ID, &n.Kind, &n.RefID, &dataJSON, &n.CreatedAt, &n.Read, &gID, &gName, &gLocal, &gRemote,
			&p.ID, &p.Username, &p.DisplayName, &avatarPath, &grav, &email); err != nil {
			return nil, 0, err
		}
		json.Unmarshal([]byte(dataJSON), &n.Data)
		if gID != 0 {
			n.Game = &GameRef{ID: gID, Name: gName, Cover: coverURL(gLocal, gRemote)}
		}
		if p.ID != "" {
			p.Avatar = avatarFor(avatarPath, grav != 0, email)
			n.Actor = &p
		}
		out = append(out, n)
	}
	unread, err := s.Unread(ctx, userID)
	return out, unread, err
}

func (s *Service) Unread(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead marks notifications up to and including upTo as read (0 = all).
func (s *Service) MarkRead(ctx context.Context, userID string, upTo int64) error {
	if upTo <= 0 {
		upTo = 1 << 62
	}
	_, err := s.db.ExecContext(ctx, `UPDATE notifications SET read_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE user_id = ? AND id <= ? AND read_at IS NULL`, userID, upTo)
	return err
}

func coverURL(local, remote string) string {
	if local != "" {
		return local
	}
	return remote
}
