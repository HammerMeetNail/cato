package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Size limits for user-supplied library text (characters, not bytes).
const (
	MaxNotesChars     = 10000
	MaxTags           = 32
	MaxTagChars       = 40
	MaxPlatforms      = 32
	MaxPlatformChars  = 64
	MaxSessionMinutes = 1440
)

// Change is a partial update: nil fields keep their stored values.
type Change struct {
	Status *string `json:"status"`
	Rating *int64  `json:"rating"`
	// Minutes sets the total; the difference is recorded as an adjustment
	// session so the total always equals the sum of the item's sessions.
	Minutes *int64 `json:"minutes"`
	// MinutesDelta logs play time: a positive delta up to a day is a
	// session, anything else an adjustment.
	MinutesDelta *int64 `json:"minutes_delta"`
	// PlayedOn is the user's local day (YYYY-MM-DD) for a logged session
	// and for playthrough dates; the server's UTC day when empty.
	PlayedOn string `json:"played_on"`
	// SessionNote annotates the logged session.
	SessionNote string    `json:"session_note"`
	Tags        *[]string `json:"tags"`
	Notes       *string   `json:"notes"`
	// StartedAt/CompletedAt: nil keeps (and auto-tracks on a status change),
	// "" clears, a value (RFC3339 or YYYY-MM-DD) sets.
	StartedAt   *string   `json:"started_at"`
	CompletedAt *string   `json:"completed_at"`
	Platforms   *[]string `json:"platforms"`
	// Platform is the legacy single-platform setter.
	Platform *string `json:"platform"`
	Medium   *string `json:"medium"`
	// Completion is recorded on the current playthrough when the game is
	// (or becomes) finished: main, main_extras, completionist, endless.
	Completion *string `json:"completion"`
}

// WriteResult is what a write changed.
type WriteResult struct {
	Item    *Item    `json:"item"`
	Session *Session `json:"session,omitempty"`
	UndoID  string   `json:"undo_id"`
	Created bool     `json:"created"`
	Rev     int64    `json:"library_rev"`
}

type writeMode int

const (
	modePatch  writeMode = iota // the item must exist
	modeUpsert                  // create or update
)

// Patch updates an existing item.
func (s *Store) Patch(ctx context.Context, userID string, gameID int64, ch Change) (*WriteResult, error) {
	return s.write(ctx, userID, gameID, ch, modePatch)
}

// Upsert adds the game or updates the existing item with ch.
func (s *Store) Upsert(ctx context.Context, userID string, gameID int64, ch Change) (*WriteResult, error) {
	return s.write(ctx, userID, gameID, ch, modeUpsert)
}

// Replace is the legacy full-item write: omitted fields are reset (status
// required), timestamps keep Change semantics.
func (s *Store) Replace(ctx context.Context, userID string, gameID int64, ch Change) (*WriteResult, error) {
	zero := int64(0)
	empty := ""
	none := []string{}
	if ch.Rating == nil {
		ch.Rating = &zero
	}
	if ch.Minutes == nil && ch.MinutesDelta == nil {
		ch.Minutes = &zero
	}
	if ch.Tags == nil {
		ch.Tags = &none
	}
	if ch.Notes == nil {
		ch.Notes = &empty
	}
	if ch.Platforms == nil && ch.Platform == nil {
		ch.Platforms = &none
	}
	if ch.Medium == nil {
		ch.Medium = &empty
	}
	if ch.Status == nil {
		ch.Status = &empty
	}
	return s.write(ctx, userID, gameID, ch, modeUpsert)
}

type itemState struct {
	status    string
	rating    int64
	tags      []string
	notes     string
	platforms []string
	medium    string
}

func (s *Store) today(playedOn string) string {
	if d, ok := parseDay(playedOn); ok {
		return d
	}
	return s.now().UTC().Format("2006-01-02")
}

func parseDay(v string) (string, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	return t.Format("2006-01-02"), true
}

func (s *Store) write(ctx context.Context, userID string, gameID int64, ch Change, mode writeMode) (*WriteResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	snap, res, err := s.applyChange(ctx, tx, userID, gameID, ch, mode)
	if err != nil {
		return nil, err
	}
	if res.UndoID, err = saveUndo(ctx, tx, userID, []itemSnapshot{snap}); err != nil {
		return nil, err
	}
	if res.Item, err = s.get(ctx, tx, userID, gameID); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT library_rev FROM user_state WHERE user_id = ?`, userID).Scan(&res.Rev); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if s.OnWrite != nil {
		s.OnWrite()
	}
	return res, nil
}

// applyChange performs one item write inside tx and returns the snapshot of
// what it replaced.
func (s *Store) applyChange(ctx context.Context, tx *sql.Tx, userID string, gameID int64, ch Change, mode writeMode) (itemSnapshot, *WriteResult, error) {
	var snap itemSnapshot
	if ch.Minutes != nil && ch.MinutesDelta != nil {
		return snap, nil, invalid("invalid_playtime", "minutes", "Send minutes or minutes_delta, not both")
	}
	if ch.PlayedOn != "" {
		if _, ok := parseDay(ch.PlayedOn); !ok {
			return snap, nil, invalid("invalid_date", "played_on", "played_on must be YYYY-MM-DD")
		}
	}
	prev, err := s.get(ctx, tx, userID, gameID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return snap, nil, err
	}
	if prev == nil && mode == modePatch {
		return snap, nil, ErrNotFound
	}
	if snap, err = snapshotItem(ctx, tx, userID, gameID); err != nil {
		return snap, nil, err
	}

	st := itemState{tags: []string{}, platforms: []string{}}
	if prev != nil {
		st = itemState{status: prev.Status, rating: prev.Rating, tags: prev.Tags, notes: prev.Notes, platforms: prev.Platforms, medium: prev.Medium}
	}
	if ch.Status != nil {
		st.status = strings.TrimSpace(*ch.Status)
	}
	if prev == nil && st.status == "" {
		st.status = StatusBacklog
	}
	if ch.Rating != nil {
		st.rating = *ch.Rating
	}
	if ch.Tags != nil {
		st.tags = *ch.Tags
	}
	if ch.Notes != nil {
		st.notes = *ch.Notes
	}
	switch {
	case ch.Platforms != nil:
		st.platforms = *ch.Platforms
	case ch.Platform != nil:
		if p := strings.TrimSpace(*ch.Platform); p == "" {
			st.platforms = []string{}
		} else {
			st.platforms = []string{p}
		}
	}
	if ch.Medium != nil {
		st.medium = strings.TrimSpace(*ch.Medium)
	}
	if err := validateState(&st); err != nil {
		return snap, nil, err
	}
	if ch.Completion != nil && *ch.Completion != "" && !validCompletion(*ch.Completion) {
		return snap, nil, invalid("invalid_completion", "completion", "Completion must be main, main_extras, completionist, endless or dropped")
	}

	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM games WHERE id = ?", gameID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snap, nil, ErrGameNotFound
		}
		return snap, nil, err
	}

	startClear, startVal, err := parseTimestamp(ch.StartedAt, "started_at")
	if err != nil {
		return snap, nil, err
	}
	endClear, endVal, err := parseTimestamp(ch.CompletedAt, "completed_at")
	if err != nil {
		return snap, nil, err
	}
	now := s.now().UTC().Format(time.RFC3339)
	insertStarted := insertTimestamp(startClear, startVal, st.status == StatusPlaying, now)
	insertCompleted := insertTimestamp(endClear, endVal, st.status == StatusCompleted, now)
	primary := ""
	if len(st.platforms) > 0 {
		primary = st.platforms[0]
	}

	// Timestamps: an unspecified value is preserved, and auto-set only when
	// the status moves into playing/completed (a date the user cleared stays
	// cleared through later edits).
	if _, err := tx.ExecContext(ctx, `INSERT INTO library_items (user_id, game_id, status, rating, playtime_minutes, tags_json, notes,
			started_at, completed_at, platform, owned_platforms_json, medium, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, game_id) DO UPDATE SET
			status = excluded.status,
			rating = excluded.rating,
			tags_json = excluded.tags_json,
			notes = excluded.notes,
			started_at = CASE
				WHEN ? THEN NULL
				WHEN ? THEN ?
				WHEN library_items.started_at IS NOT NULL THEN library_items.started_at
				WHEN excluded.status = 'playing' AND library_items.status != 'playing' THEN excluded.updated_at
				ELSE NULL
			END,
			completed_at = CASE
				WHEN ? THEN NULL
				WHEN ? THEN ?
				WHEN library_items.completed_at IS NOT NULL THEN library_items.completed_at
				WHEN excluded.status = 'completed' AND library_items.status != 'completed' THEN excluded.updated_at
				ELSE NULL
			END,
			platform = excluded.platform,
			owned_platforms_json = excluded.owned_platforms_json,
			medium = excluded.medium,
			updated_at = excluded.updated_at`,
		userID, gameID, st.status, st.rating, encodeStrings(st.tags), st.notes,
		insertStarted, insertCompleted, primary, encodeStrings(st.platforms), st.medium, now, now,
		startClear, startVal != "", nullIfEmpty(startVal),
		endClear, endVal != "", nullIfEmpty(endVal)); err != nil {
		return snap, nil, fmt.Errorf("save library item: %w", err)
	}
	// Only games in someone's library get their cover downloaded.
	if _, err := tx.ExecContext(ctx, `INSERT INTO cover_jobs (game_id, source_url)
		SELECT id, cover_url FROM games WHERE id = ? AND cover_url != '' AND local_cover_path = ''
		ON CONFLICT(game_id) DO NOTHING`, gameID); err != nil {
		return snap, nil, err
	}

	ptID, err := s.updatePlaythroughs(ctx, tx, userID, gameID, prev, st, ch)
	if err != nil {
		return snap, nil, err
	}

	res := &WriteResult{Created: prev == nil}
	var delta int64
	source := "adjust"
	switch {
	case ch.MinutesDelta != nil:
		delta = *ch.MinutesDelta
		if delta > 0 && delta <= MaxSessionMinutes {
			source = "manual"
		}
		if delta < 0 {
			// Never below zero: clamp the adjustment to the stored total.
			var total int64
			tx.QueryRowContext(ctx, `SELECT playtime_minutes FROM library_items WHERE user_id = ? AND game_id = ?`, userID, gameID).Scan(&total)
			if -delta > total {
				delta = -total
			}
		}
	case ch.Minutes != nil:
		if *ch.Minutes < 0 {
			return snap, nil, invalid("invalid_playtime", "minutes", "Play time must be non-negative")
		}
		var total int64
		if err := tx.QueryRowContext(ctx, `SELECT playtime_minutes FROM library_items WHERE user_id = ? AND game_id = ?`, userID, gameID).Scan(&total); err != nil {
			return snap, nil, err
		}
		delta = *ch.Minutes - total
	}
	if delta != 0 {
		sess, err := insertSession(ctx, tx, userID, gameID, ptID, s.today(ch.PlayedOn), delta, strings.TrimSpace(ch.SessionNote), source)
		if err != nil {
			return snap, nil, err
		}
		res.Session = sess
	}

	return snap, res, nil
}

// updatePlaythroughs keeps the journal in step with a status change and
// returns the current playthrough ID (0 when there is none).
func (s *Store) updatePlaythroughs(ctx context.Context, tx *sql.Tx, userID string, gameID int64, prev *Item, st itemState, ch Change) (int64, error) {
	prevStatus := ""
	if prev != nil {
		prevStatus = prev.Status
	}
	var curID sql.NullInt64
	var startedAt, completedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT current_playthrough_id, started_at, completed_at FROM library_items WHERE user_id = ? AND game_id = ?`,
		userID, gameID).Scan(&curID, &startedAt, &completedAt); err != nil {
		return 0, err
	}
	var finishedOn sql.NullString
	if curID.Valid {
		err := tx.QueryRowContext(ctx, `SELECT finished_on FROM playthroughs WHERE id = ?`, curID.Int64).Scan(&finishedOn)
		if errors.Is(err, sql.ErrNoRows) {
			curID.Valid = false
		} else if err != nil {
			return 0, err
		}
	}
	day := s.today(ch.PlayedOn)
	platform := ""
	if len(st.platforms) > 0 {
		platform = st.platforms[0]
	}
	create := func(startedOn any) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO playthroughs (user_id, game_id, platform, started_on) VALUES (?, ?, ?, ?)`,
			userID, gameID, platform, startedOn)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		curID = sql.NullInt64{Int64: id, Valid: true}
		finishedOn = sql.NullString{}
		_, err = tx.ExecContext(ctx, `UPDATE library_items SET current_playthrough_id = ? WHERE user_id = ? AND game_id = ?`, id, userID, gameID)
		return err
	}
	dayOf := func(ts sql.NullString) any {
		if ts.Valid && len(ts.String) >= 10 {
			return ts.String[:10]
		}
		return nil
	}
	firstStart := func() any {
		// The first playthrough starts when the item says it did: an explicit
		// date, or one recorded before this write (not the auto-set "now").
		if ch.StartedAt != nil || (prev != nil && prev.StartedAt != nil) {
			if v := dayOf(startedAt); v != nil {
				return v
			}
		}
		return day
	}

	if st.status != prevStatus {
		switch st.status {
		case StatusPlaying:
			// Resuming a paused run keeps it; anything finished starts a replay.
			if !curID.Valid {
				if err := create(firstStart()); err != nil {
					return 0, err
				}
			} else if finishedOn.Valid {
				if err := create(day); err != nil {
					return 0, err
				}
			}
		case StatusCompleted, StatusAbandoned:
			if !curID.Valid {
				if err := create(dayOf(startedAt)); err != nil {
					return 0, err
				}
			}
			finished := day
			completion := any(nil)
			if st.status == StatusCompleted {
				if v := dayOf(completedAt); v != nil {
					finished = v.(string)
				}
			} else {
				completion = "dropped"
			}
			rating := any(nil)
			if st.rating > 0 {
				rating = st.rating
			}
			if _, err := tx.ExecContext(ctx, `UPDATE playthroughs SET finished_on = ?, completion = ?, rating = COALESCE(?, rating), platform = CASE WHEN platform = '' THEN ? ELSE platform END WHERE id = ?`,
				finished, completion, rating, platform, curID.Int64); err != nil {
				return 0, err
			}
		}
	}
	if !curID.Valid {
		return 0, nil
	}
	// Explicit edits flow into the current playthrough.
	if ch.Completion != nil && (st.status == StatusCompleted || st.status == StatusAbandoned) {
		if _, err := tx.ExecContext(ctx, `UPDATE playthroughs SET completion = NULLIF(?, '') WHERE id = ?`, *ch.Completion, curID.Int64); err != nil {
			return 0, err
		}
	}
	if ch.Rating != nil && st.status == StatusCompleted {
		if _, err := tx.ExecContext(ctx, `UPDATE playthroughs SET rating = NULLIF(?, 0) WHERE id = ?`, st.rating, curID.Int64); err != nil {
			return 0, err
		}
	}
	if ch.StartedAt != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE playthroughs SET started_on = ? WHERE id = ?`, dayOf(startedAt), curID.Int64); err != nil {
			return 0, err
		}
	}
	if ch.CompletedAt != nil && st.status == StatusCompleted {
		if _, err := tx.ExecContext(ctx, `UPDATE playthroughs SET finished_on = ? WHERE id = ?`, dayOf(completedAt), curID.Int64); err != nil {
			return 0, err
		}
	}
	return curID.Int64, nil
}

func validateState(st *itemState) error {
	if !ValidStatus(st.status) {
		return invalid("invalid_status", "status", "Status must be wishlist, backlog, playing, paused, completed or abandoned")
	}
	if st.rating < 0 || st.rating > 100 {
		return invalid("invalid_rating", "rating", "Rating must be between 0 and 100")
	}
	if utf8.RuneCountInString(st.notes) > MaxNotesChars {
		return invalid("invalid_notes", "notes", "Notes must be at most 10,000 characters")
	}
	tags := make([]string, 0, len(st.tags))
	seen := map[string]bool{}
	for _, t := range st.tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if utf8.RuneCountInString(t) > MaxTagChars {
			return invalid("invalid_tags", "tags", "Tags must be at most 40 characters")
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	if len(tags) > MaxTags {
		return invalid("invalid_tags", "tags", "At most 32 tags per game")
	}
	st.tags = tags
	if st.medium != "" && st.medium != "physical" && st.medium != "digital" {
		return invalid("invalid_medium", "medium", "Medium must be empty, physical, or digital")
	}
	platforms := make([]string, 0, len(st.platforms))
	seen = map[string]bool{}
	for _, p := range st.platforms {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > MaxPlatformChars {
			return invalid("invalid_platform", "platforms", "Platform must be at most 64 characters")
		}
		if seen[strings.ToLower(p)] {
			continue
		}
		if len(platforms) >= MaxPlatforms {
			return invalid("invalid_platform", "platforms", "At most 32 platforms per game")
		}
		seen[strings.ToLower(p)] = true
		platforms = append(platforms, p)
	}
	st.platforms = platforms
	return nil
}

// parseTimestamp: nil = not provided, "" = clear, otherwise RFC3339 or
// YYYY-MM-DD normalized to UTC RFC3339.
func parseTimestamp(v *string, field string) (clear bool, value string, err error) {
	if v == nil {
		return false, "", nil
	}
	raw := strings.TrimSpace(*v)
	if raw == "" {
		return true, "", nil
	}
	t, perr := time.Parse(time.RFC3339, raw)
	if perr != nil {
		t, perr = time.Parse("2006-01-02", raw)
	}
	if perr != nil {
		return false, "", invalid("invalid_timestamp", field, fmt.Sprintf("timestamp must be RFC3339 or YYYY-MM-DD, got %q", raw))
	}
	return false, t.UTC().Format(time.RFC3339), nil
}

func insertTimestamp(clear bool, value string, autoTrack bool, now string) any {
	switch {
	case clear:
		return nil
	case value != "":
		return value
	case autoTrack:
		return now
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Delete removes an item (and its journal) and returns the undo ID.
func (s *Store) Delete(ctx context.Context, userID string, gameID int64) (string, error) {
	return s.deleteMany(ctx, userID, []int64{gameID}, true)
}

func (s *Store) deleteMany(ctx context.Context, userID string, gameIDs []int64, requireAll bool) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var snaps []itemSnapshot
	for _, id := range gameIDs {
		snap, err := snapshotItem(ctx, tx, userID, id)
		if err != nil {
			return "", err
		}
		if snap.Row == nil {
			if requireAll {
				return "", ErrNotFound
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM library_items WHERE user_id = ? AND game_id = ?`, userID, id); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_state SET timer_game_id = NULL, timer_started_at = NULL WHERE user_id = ? AND timer_game_id = ?`, userID, id); err != nil {
			return "", err
		}
		snaps = append(snaps, snap)
	}
	undoID, err := saveUndo(ctx, tx, userID, snaps)
	if err != nil {
		return "", err
	}
	return undoID, tx.Commit()
}
