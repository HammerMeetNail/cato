package social

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ListSummary is a list without its games.
type ListSummary struct {
	ID          int64     `json:"id"`
	Owner       Person    `json:"owner"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Ranked      bool      `json:"ranked"`
	Visibility  string    `json:"visibility"`
	Count       int       `json:"count"`
	Covers      []GameRef `json:"preview"`
	UpdatedAt   string    `json:"updated_at"`
	Mine        bool      `json:"mine"`
	Hidden      bool      `json:"hidden,omitempty"`
}

// List is a list with its games in order.
type List struct {
	ListSummary
	Items []ListItem `json:"items"`
}

type ListItem struct {
	Game     GameRef `json:"game"`
	Position int     `json:"position"`
	Note     string  `json:"note"`
	Year     int64   `json:"release_date"`
	Status   string  `json:"status,omitempty"` // in the viewer's library
}

// ListInput creates or edits a list; nil fields stay.
type ListInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Ranked      *bool   `json:"ranked"`
	Visibility  *string `json:"visibility"`
}

func (in *ListInput) check(creating bool) error {
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		in.Title = &t
		if t == "" {
			return invalid("required", "title", "Give the list a title")
		}
		if len([]rune(t)) > MaxListTitle {
			return invalid("too_long", "title", "Keep titles under 100 characters")
		}
	} else if creating {
		return invalid("required", "title", "Give the list a title")
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		in.Description = &d
		if len([]rune(d)) > MaxListDesc {
			return invalid("too_long", "description", "Keep descriptions under 2,000 characters")
		}
	}
	if in.Visibility != nil && !validLevel(*in.Visibility, true) {
		return invalid("invalid_visibility", "visibility", "Choose inherit, private, followers or instance")
	}
	return nil
}

const listLevel = `(CASE l.visibility WHEN 'inherit' THEN u.visibility ELSE l.visibility END)`

func (s *Service) listSummaries(ctx context.Context, viewer, where string, args []any, limit int) ([]ListSummary, error) {
	q := `SELECT l.id, l.title, l.description, l.ranked, l.visibility, l.updated_at, l.hidden_at IS NOT NULL,
			(SELECT COUNT(*) FROM list_items li WHERE li.list_id = l.id), ` + personCols + `
		FROM lists l JOIN users u ON u.id = l.user_id
		WHERE ` + where + ` AND ` + visibleSQL("l.user_id", listLevel) + ` AND (l.hidden_at IS NULL OR l.user_id = ?)
		ORDER BY l.updated_at DESC LIMIT ?`
	all := append(append(append([]any{}, args...), visibleArgs(viewer)...), viewer, limit)
	rows, err := s.db.QueryContext(ctx, q, all...)
	if err != nil {
		return nil, err
	}
	out := []ListSummary{}
	for rows.Next() {
		var l ListSummary
		l.Owner, err = scanPerson(rowPrefix{rows, []any{&l.ID, &l.Title, &l.Description, &l.Ranked, &l.Visibility, &l.UpdatedAt, &l.Hidden, &l.Count}})
		if err != nil {
			rows.Close()
			return nil, err
		}
		l.Mine = l.Owner.ID == viewer
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Covers, err = s.listPreview(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) listPreview(ctx context.Context, listID int64) ([]GameRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id, g.name, COALESCE(g.local_cover_path, ''), COALESCE(g.cover_url, '')
		FROM list_items li JOIN games g ON g.id = li.game_id WHERE li.list_id = ? ORDER BY li.position, li.added_at LIMIT 4`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GameRef{}
	for rows.Next() {
		var g GameRef
		var local, remote string
		if err := rows.Scan(&g.ID, &g.Name, &local, &remote); err != nil {
			return nil, err
		}
		g.Cover = coverURL(local, remote)
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListsOf lists owner's lists that viewer may see.
func (s *Service) ListsOf(ctx context.Context, viewer, owner string) ([]ListSummary, error) {
	return s.listSummaries(ctx, viewer, `l.user_id = ?`, []any{owner}, 100)
}

// ListsWithGame lists the viewer's lists, marking which hold gameID (for
// the "add to list" sheet).
func (s *Service) ListsWithGame(ctx context.Context, viewer string, gameID int64) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id, l.title, EXISTS (SELECT 1 FROM list_items li WHERE li.list_id = l.id AND li.game_id = ?)
		FROM lists l WHERE l.user_id = ? ORDER BY l.updated_at DESC`, gameID, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var title string
		var has bool
		if err := rows.Scan(&id, &title, &has); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "title": title, "has_game": has})
	}
	return out, rows.Err()
}

// List loads one list with its games if viewer may see it.
func (s *Service) List(ctx context.Context, viewer string, id int64) (*List, error) {
	ls, err := s.listSummaries(ctx, viewer, `l.id = ?`, []any{id}, 1)
	if err != nil {
		return nil, err
	}
	if len(ls) == 0 {
		return nil, ErrNotFound
	}
	l := &List{ListSummary: ls[0], Items: []ListItem{}}
	rows, err := s.db.QueryContext(ctx, `SELECT g.id, g.name, COALESCE(g.local_cover_path, ''), COALESCE(g.cover_url, ''), COALESCE(g.first_release_date, 0),
			li.position, li.note, COALESCE(mine.status, '')
		FROM list_items li JOIN games g ON g.id = li.game_id
		LEFT JOIN library_items mine ON mine.user_id = ? AND mine.game_id = g.id
		WHERE li.list_id = ? ORDER BY li.position, li.added_at`, viewer, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it ListItem
		var local, remote string
		if err := rows.Scan(&it.Game.ID, &it.Game.Name, &local, &remote, &it.Year, &it.Position, &it.Note, &it.Status); err != nil {
			return nil, err
		}
		it.Game.Cover = coverURL(local, remote)
		l.Items = append(l.Items, it)
	}
	return l, rows.Err()
}

func (s *Service) CreateList(ctx context.Context, userID string, in ListInput) (*List, error) {
	if err := in.check(true); err != nil {
		return nil, err
	}
	desc, ranked, vis := "", false, Inherit
	if in.Description != nil {
		desc = *in.Description
	}
	if in.Ranked != nil {
		ranked = *in.Ranked
	}
	if in.Visibility != nil {
		vis = *in.Visibility
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lists WHERE user_id = ?`, userID).Scan(&n)
	if n >= 200 {
		return nil, invalid("too_many", "", "You have 200 lists already")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO lists (user_id, title, description, ranked, visibility) VALUES (?, ?, ?, ?, ?)`,
		userID, *in.Title, desc, ranked, vis)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.List(ctx, userID, id)
}

// ownList checks that the list is the user's.
func ownList(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID string, id int64) error {
	var owner string
	err := q.QueryRowContext(ctx, `SELECT user_id FROM lists WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != userID) {
		return ErrNotFound
	}
	return err
}

func (s *Service) EditList(ctx context.Context, userID string, id int64, in ListInput) (*List, error) {
	if err := in.check(false); err != nil {
		return nil, err
	}
	if err := ownList(ctx, s.db, userID, id); err != nil {
		return nil, err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lists SET title = COALESCE(?, title), description = COALESCE(?, description),
			ranked = COALESCE(?, ranked), visibility = COALESCE(?, visibility), updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ?`, in.Title, in.Description, in.Ranked, in.Visibility, id)
	if err != nil {
		return nil, err
	}
	return s.List(ctx, userID, id)
}

func (s *Service) DeleteList(ctx context.Context, userID string, id int64) error {
	if err := ownList(ctx, s.db, userID, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM lists WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity WHERE kind = 'listed' AND ref_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// AddToList appends a game (or updates its note when already there).
func (s *Service) AddToList(ctx context.Context, userID string, id, gameID int64, note string) (*List, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > MaxListNote {
		return nil, invalid("too_long", "note", "Keep notes under 500 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ownList(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	var n, have int
	tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM games WHERE id = ?`, gameID).Scan(&n)
	if n == 0 {
		return nil, invalid("game_not_found", "game_id", "Game not found")
	}
	tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM list_items WHERE list_id = ?`, id).Scan(&n)
	tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM list_items WHERE list_id = ? AND game_id = ?`, id, gameID).Scan(&have)
	if have > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE list_items SET note = ? WHERE list_id = ? AND game_id = ?`, note, id, gameID); err != nil {
			return nil, err
		}
	} else {
		if n >= MaxListItems {
			return nil, invalid("too_many", "game_id", "Lists hold up to 500 games")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO list_items (list_id, game_id, position, note)
			VALUES (?, ?, COALESCE((SELECT MAX(position) + 1 FROM list_items WHERE list_id = ?), 0), ?)`, id, gameID, id, note); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity (user_id, kind, game_id, ref_id) VALUES (?, 'listed', ?, ?)`, userID, gameID, id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lists SET updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.List(ctx, userID, id)
}

func (s *Service) RemoveFromList(ctx context.Context, userID string, id, gameID int64) (*List, error) {
	if err := ownList(ctx, s.db, userID, id); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM list_items WHERE list_id = ? AND game_id = ?`, id, gameID); err != nil {
		return nil, err
	}
	s.db.ExecContext(ctx, `DELETE FROM activity WHERE kind = 'listed' AND ref_id = ? AND game_id = ? AND user_id = ?`, id, gameID, userID)
	return s.List(ctx, userID, id)
}

// Reorder sets the list order; games not named keep their relative order
// after the named ones.
func (s *Service) Reorder(ctx context.Context, userID string, id int64, gameIDs []int64) (*List, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ownList(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT game_id FROM list_items WHERE list_id = ? ORDER BY position, added_at`, id)
	if err != nil {
		return nil, err
	}
	var cur []int64
	for rows.Next() {
		var g int64
		rows.Scan(&g)
		cur = append(cur, g)
	}
	rows.Close()
	inList := map[int64]bool{}
	for _, g := range cur {
		inList[g] = true
	}
	order := []int64{}
	placed := map[int64]bool{}
	for _, g := range gameIDs {
		if inList[g] && !placed[g] {
			placed[g] = true
			order = append(order, g)
		}
	}
	for _, g := range cur {
		if !placed[g] {
			order = append(order, g)
		}
	}
	for i, g := range order {
		if _, err := tx.ExecContext(ctx, `UPDATE list_items SET position = ? WHERE list_id = ? AND game_id = ?`, i, id, g); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lists SET updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.List(ctx, userID, id)
}
