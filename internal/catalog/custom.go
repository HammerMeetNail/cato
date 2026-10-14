package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// CustomGame is a game the catalog doesn't have (a demo, a regional
// release, homebrew), added by a user (plan §6.2). It lives in the games
// table with source='custom' and a negative ID, so every library feature
// works unchanged; an admin can later merge it into an IGDB match.
type CustomGame struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Year     int    `json:"year"`
	Summary  string `json:"summary"`
}

var ErrInvalidCustom = errors.New("a custom game needs a name of at most 200 characters")

// CreateCustom adds a custom game and returns its (negative) ID.
func (s *Store) CreateCustom(ctx context.Context, userID string, c CustomGame) (int64, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Platform = strings.TrimSpace(c.Platform)
	if c.Name == "" || utf8.RuneCountInString(c.Name) > 200 || len(c.Platform) > 64 || utf8.RuneCountInString(c.Summary) > 2000 {
		return 0, ErrInvalidCustom
	}
	if c.Year != 0 && (c.Year < 1950 || c.Year > time.Now().Year()+10) {
		return 0, fmt.Errorf("year must be between 1950 and %d", time.Now().Year()+10)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(COALESCE(MIN(id), 0), 0) - 1 FROM games`).Scan(&id); err != nil {
		return 0, err
	}
	var release int64
	if c.Year != 0 {
		release = time.Date(c.Year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
	platforms := "[]"
	if c.Platform != "" {
		// Known platforms are stored by IGDB ID, others by name.
		var pid int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM platforms WHERE LOWER(name) = LOWER(?) OR LOWER(abbreviation) = LOWER(?) LIMIT 1`, c.Platform, c.Platform).Scan(&pid)
		var v any = c.Platform
		if err == nil {
			v = pid
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		b, _ := json.Marshal([]any{v})
		platforms = string(b)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO games (id, name, slug, safe_name, normalized_name, summary, first_release_date,
			platforms_json, source, created_by, details_fetched_at, aliases_fetched_at, popularity_fetched_at, version_parent_fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'custom', ?, ?, ?, ?, ?)`,
		id, c.Name, fmt.Sprintf("custom%d", -id), c.Name, NormalizeName(c.Name), strings.TrimSpace(c.Summary), release,
		platforms, userID, time.Now().Unix(), time.Now().Unix(), time.Now().Unix(), time.Now().Unix()); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// SetCustomCover records an uploaded cover (a new URL each time, so the
// immutable cover cache never serves the old image).
func (s *Store) SetCustomCover(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE games SET local_cover_path = ? WHERE id = ? AND source = 'custom'`,
		fmt.Sprintf("/covers/%d.jpg?u=%d", id, time.Now().Unix()), id)
	return err
}

// CustomOwner returns who created a custom game ("" for catalog games).
func (s *Store) CustomOwner(ctx context.Context, id int64) (string, error) {
	var owner sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT created_by FROM games WHERE id = ? AND source = 'custom'`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner.String, err
}

// MergeCustom moves every library item, journal entry and relation from a
// custom game onto a catalog game, then deletes the custom row. Users who
// already have the target keep their item; the custom one's sessions move
// onto it.
func (s *Store) MergeCustom(ctx context.Context, customID, intoID int64) error {
	if customID >= 0 || intoID <= 0 {
		return errors.New("merge a custom game (negative ID) into a catalog game")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM games WHERE id = ?`, intoID).Scan(&n); err != nil || n == 0 {
		return errors.New("no such catalog game")
	}
	rows, err := tx.QueryContext(ctx, `SELECT user_id, EXISTS (SELECT 1 FROM library_items x WHERE x.user_id = li.user_id AND x.game_id = ?)
		FROM library_items li WHERE li.game_id = ?`, intoID, customID)
	if err != nil {
		return err
	}
	type owner struct {
		user string
		has  bool
	}
	var owners []owner
	for rows.Next() {
		var o owner
		if err := rows.Scan(&o.user, &o.has); err != nil {
			rows.Close()
			return err
		}
		owners = append(owners, o)
	}
	rows.Close()
	for _, o := range owners {
		if !o.has {
			// Create the target item from the custom one, move the journal,
			// then drop the old item.
			if _, err := tx.ExecContext(ctx, `INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes,
					started_at, completed_at, created_at, updated_at, platform, medium, owned_platforms_json)
				SELECT user_id, ?, status, rating, 0, tags_json, notes, started_at, completed_at, created_at, updated_at, platform, medium, owned_platforms_json
				FROM library_items WHERE user_id = ? AND game_id = ?`, intoID, o.user, customID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE playthroughs SET game_id = ? WHERE user_id = ? AND game_id = ?`, intoID, o.user, customID); err != nil {
			return err
		}
		// Moving sessions: delete and re-insert so the playtime triggers
		// move minutes from the old item to the new one.
		if _, err := tx.ExecContext(ctx, `INSERT INTO play_sessions (user_id, game_id, playthrough_id, played_on, minutes, note, source, created_at)
			SELECT user_id, ?, playthrough_id, played_on, minutes, note, source, created_at FROM play_sessions WHERE user_id = ? AND game_id = ?`,
			intoID, o.user, customID); err != nil {
			return err
		}
		if !o.has {
			if _, err := tx.ExecContext(ctx, `UPDATE library_items SET current_playthrough_id =
					(SELECT current_playthrough_id FROM library_items WHERE user_id = ? AND game_id = ?)
				WHERE user_id = ? AND game_id = ?`, o.user, customID, o.user, intoID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM games WHERE id = ?`, customID); err != nil {
		return err
	}
	return tx.Commit()
}

// BaseGame follows version_parent to the game an edition belongs to (the
// edition's own ID when it isn't one).
func (s *Store) BaseGame(ctx context.Context, id int64) (int64, error) {
	var parent int64
	err := s.db.QueryRowContext(ctx, `SELECT version_parent FROM games WHERE id = ?`, id).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return id, nil
	}
	if err != nil {
		return 0, err
	}
	if parent == 0 {
		return id, nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM games WHERE id = ?`, parent).Scan(&exists); err != nil {
		return id, nil // parent not in the catalog: keep the edition
	}
	return parent, nil
}
