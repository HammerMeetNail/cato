package social

import (
	"context"
	"strings"
)

// Review is a written review of a game.
type Review struct {
	ID            int64   `json:"id"`
	Author        Person  `json:"author"`
	Game          GameRef `json:"game"`
	PlaythroughID int64   `json:"playthrough_id,omitempty"`
	Body          string  `json:"body"`
	Spoiler       bool    `json:"spoiler"`
	Visibility    string  `json:"visibility"`
	Rating        int64   `json:"rating"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	Likes         int     `json:"likes"`
	LikedByMe     bool    `json:"liked_by_me"`
	Friend        bool    `json:"friend"`
	Hidden        bool    `json:"hidden,omitempty"`
	Mine          bool    `json:"mine"`
}

// ReviewInput writes a review. Visibility "" keeps (or defaults to) inherit.
type ReviewInput struct {
	GameID        int64  `json:"game_id"`
	PlaythroughID int64  `json:"playthrough_id"`
	Body          string `json:"body"`
	Spoiler       bool   `json:"spoiler"`
	Visibility    string `json:"visibility"`
}

func (in *ReviewInput) check() error {
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" {
		return invalid("required", "body", "Write something first")
	}
	if len([]rune(in.Body)) > MaxReview {
		return invalid("too_long", "body", "Keep reviews under 10,000 characters")
	}
	if in.Visibility == "" {
		in.Visibility = Inherit
	}
	if !validLevel(in.Visibility, true) {
		return invalid("invalid_visibility", "visibility", "Choose inherit, private, followers or instance")
	}
	return nil
}

const reviewSelect = `SELECT r.id, COALESCE(r.playthrough_id, 0), r.body, r.spoiler, r.visibility, r.rating, r.created_at, r.updated_at,
		r.hidden_at IS NOT NULL,
		(SELECT COUNT(*) FROM review_likes l WHERE l.review_id = r.id),
		EXISTS (SELECT 1 FROM review_likes l WHERE l.review_id = r.id AND l.user_id = ?),
		EXISTS (SELECT 1 FROM follows ff WHERE ff.follower_id = ? AND ff.followee_id = r.user_id AND ff.status = 'accepted'),
		g.id, g.name, COALESCE(g.local_cover_path, ''), COALESCE(g.cover_url, ''),
		` + personCols + `
	FROM reviews r JOIN users u ON u.id = r.user_id JOIN games g ON g.id = r.game_id`

// reviewLevel is a review's effective visibility in SQL.
const reviewLevel = `(CASE r.visibility WHEN 'inherit' THEN u.visibility ELSE r.visibility END)`

// reviews lists the reviews viewer may see matching where. Hidden reviews
// show only to their author.
func (s *Service) reviews(ctx context.Context, viewer, where string, args []any, order string, limit int) ([]Review, error) {
	q := reviewSelect + ` WHERE ` + where + ` AND ` + visibleSQL("r.user_id", reviewLevel) +
		` AND (r.hidden_at IS NULL OR r.user_id = ?) ORDER BY ` + order + ` LIMIT ?`
	all := append([]any{viewer, viewer}, args...)
	all = append(all, visibleArgs(viewer)...)
	all = append(all, viewer, limit)
	rows, err := s.db.QueryContext(ctx, q, all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		var r Review
		var local, remote string
		r.Author, err = scanPerson(rowPrefix{rows, []any{&r.ID, &r.PlaythroughID, &r.Body, &r.Spoiler, &r.Visibility, &r.Rating, &r.CreatedAt, &r.UpdatedAt,
			&r.Hidden, &r.Likes, &r.LikedByMe, &r.Friend, &r.Game.ID, &r.Game.Name, &local, &remote}})
		if err != nil {
			return nil, err
		}
		r.Game.Cover = coverURL(local, remote)
		r.Mine = r.Author.ID == viewer
		out = append(out, r)
	}
	return out, rows.Err()
}

// rowPrefix scans leading columns before the person columns.
type rowPrefix struct {
	sc     interface{ Scan(...any) error }
	prefix []any
}

func (p rowPrefix) Scan(dest ...any) error { return p.sc.Scan(append(p.prefix, dest...)...) }

// GameReviews lists a game's reviews: yours, then friends', then the most
// liked.
func (s *Service) GameReviews(ctx context.Context, viewer string, gameID int64, limit int) ([]Review, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	return s.reviews(ctx, viewer, `r.game_id = ?`, []any{gameID},
		`(r.user_id = `+quote(viewer)+`) DESC, 12 DESC, 10 DESC, r.created_at DESC`, limit)
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Review loads one review if viewer may see it.
func (s *Service) Review(ctx context.Context, viewer string, id int64) (*Review, error) {
	rs, err := s.reviews(ctx, viewer, `r.id = ?`, []any{id}, `r.id`, 1)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, ErrNotFound
	}
	return &rs[0], nil
}

// WriteReview creates a review (rated with your current rating of the game).
func (s *Service) WriteReview(ctx context.Context, userID string, in ReviewInput) (*Review, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var n int
	tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM games WHERE id = ?`, in.GameID).Scan(&n)
	if n == 0 {
		return nil, invalid("game_not_found", "game_id", "Game not found")
	}
	var pt any
	if in.PlaythroughID != 0 {
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM playthroughs WHERE id = ? AND user_id = ? AND game_id = ?`, in.PlaythroughID, userID, in.GameID).Scan(&n)
		if n == 0 {
			return nil, invalid("invalid_playthrough", "playthrough_id", "That playthrough isn't yours")
		}
		pt = in.PlaythroughID
	}
	var rating int64
	tx.QueryRowContext(ctx, `SELECT rating FROM library_items WHERE user_id = ? AND game_id = ?`, userID, in.GameID).Scan(&rating)
	res, err := tx.ExecContext(ctx, `INSERT INTO reviews (user_id, game_id, playthrough_id, body, spoiler, visibility, rating) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, in.GameID, pt, in.Body, in.Spoiler, in.Visibility, rating)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO activity (user_id, kind, game_id, ref_id, data_json) VALUES (?, 'reviewed', ?, ?, json_object('rating', ?, 'spoiler', ?))`,
		userID, in.GameID, id, rating, in.Spoiler); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Review(ctx, userID, id)
}

// EditReview changes your review's text, spoiler flag or visibility.
func (s *Service) EditReview(ctx context.Context, userID string, id int64, in ReviewInput) (*Review, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE reviews SET body = ?, spoiler = ?, visibility = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ? AND user_id = ?`, in.Body, in.Spoiler, in.Visibility, id, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.Review(ctx, userID, id)
}

func (s *Service) DeleteReview(ctx context.Context, userID string, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM reviews WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity WHERE kind = 'reviewed' AND ref_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// Like likes a review you can see (and tells its author).
func (s *Service) Like(ctx context.Context, userID string, id int64, like bool) (*Review, error) {
	r, err := s.Review(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if r.Hidden {
		return nil, ErrNotFound
	}
	if like {
		res, err := s.db.ExecContext(ctx, `INSERT INTO review_likes (review_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, id, userID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			notify(ctx, s.db, r.Author.ID, "like", userID, r.Game.ID, id, nil)
		}
	} else if _, err := s.db.ExecContext(ctx, `DELETE FROM review_likes WHERE review_id = ? AND user_id = ?`, id, userID); err != nil {
		return nil, err
	}
	return s.Review(ctx, userID, id)
}

// Report flags a review, list or person for the admins.
func (s *Service) Report(ctx context.Context, userID, kind, target, reason string) error {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > MaxReportText {
		return invalid("too_long", "reason", "Keep it under 500 characters")
	}
	switch kind {
	case "review", "list", "user":
	default:
		return invalid("invalid_kind", "kind", "Report a review, list or user")
	}
	var dup int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports WHERE reporter_id = ? AND kind = ? AND target = ? AND resolved_at IS NULL`, userID, kind, target).Scan(&dup)
	if dup > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO reports (reporter_id, kind, target, reason) VALUES (?, ?, ?, ?)`, userID, kind, target, reason)
	return err
}

// GameSocial is the people part of a game page.
type GameSocial struct {
	Friends   []FriendEntry `json:"friends"`
	Community Community     `json:"community"`
	Reviews   []Review      `json:"reviews"`
}

// FriendEntry is someone you follow who has the game.
type FriendEntry struct {
	Person
	Status   string `json:"status"`
	Rating   int64  `json:"rating"`
	Minutes  int64  `json:"minutes"`
	ReviewID int64  `json:"review_id,omitempty"`
}

// Community is the instance's ratings of a game: only people who share
// beyond themselves count, and never people in a block with you.
type Community struct {
	Players   int     `json:"players"`
	Ratings   int     `json:"ratings"`
	Average   float64 `json:"average"`
	Histogram []int   `json:"histogram"` // ten half-star buckets
}

func (s *Service) GameSocial(ctx context.Context, viewer string, gameID int64) (*GameSocial, error) {
	gs := &GameSocial{Friends: []FriendEntry{}, Community: Community{Histogram: make([]int, 10)}}
	rows, err := s.db.QueryContext(ctx, `SELECT `+personCols+`, li.status, li.rating, li.playtime_minutes,
			COALESCE((SELECT MAX(r.id) FROM reviews r WHERE r.user_id = u.id AND r.game_id = li.game_id AND r.hidden_at IS NULL
			   AND (CASE r.visibility WHEN 'inherit' THEN u.visibility ELSE r.visibility END) IN ('followers', 'instance')), 0)
		FROM follows f JOIN users u ON u.id = f.followee_id
		JOIN library_items li ON li.user_id = u.id AND li.game_id = ?
		WHERE f.follower_id = ? AND f.status = 'accepted' AND u.disabled = 0 AND u.visibility IN ('followers', 'instance')
		  AND NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = u.id AND b.blocked_id = ?) OR (b.blocker_id = ? AND b.blocked_id = u.id))
		ORDER BY li.status = 'playing' DESC, li.updated_at DESC LIMIT 24`, gameID, viewer, viewer, viewer)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f FriendEntry
		if f.Person, err = scanPerson(rows, &f.Status, &f.Rating, &f.Minutes, &f.ReviewID); err != nil {
			rows.Close()
			return nil, err
		}
		gs.Friends = append(gs.Friends, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Ratings visible to this viewer, the viewer's own included.
	rows, err = s.db.QueryContext(ctx, `SELECT li.rating FROM library_items li JOIN users u ON u.id = li.user_id
		WHERE li.game_id = ? AND li.status != 'wishlist' AND `+visibleSQL("li.user_id", "u.visibility"),
		append([]any{gameID}, visibleArgs(viewer)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sum int64
	for rows.Next() {
		var rating int64
		if err := rows.Scan(&rating); err != nil {
			return nil, err
		}
		gs.Community.Players++
		if rating > 0 {
			gs.Community.Ratings++
			sum += rating
			b := int((rating - 1) / 10)
			if b > 9 {
				b = 9
			}
			gs.Community.Histogram[b]++
		}
	}
	if gs.Community.Ratings > 0 {
		gs.Community.Average = float64(sum) / float64(gs.Community.Ratings)
	}
	if gs.Reviews, err = s.GameReviews(ctx, viewer, gameID, 30); err != nil {
		return nil, err
	}
	return gs, rows.Err()
}
