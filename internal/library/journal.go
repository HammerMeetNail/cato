package library

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Session is one entry in the play diary.
type Session struct {
	ID            int64  `json:"id"`
	GameID        int64  `json:"game_id"`
	PlaythroughID *int64 `json:"playthrough_id"`
	PlayedOn      string `json:"played_on"`
	Minutes       int64  `json:"minutes"`
	Note          string `json:"note"`
	Source        string `json:"source"`
	CreatedAt     string `json:"created_at"`
	// GameName and Cover are filled by diary listings.
	GameName string `json:"game_name,omitempty"`
	Cover    string `json:"cover,omitempty"`
}

// Playthrough is one run of a game.
type Playthrough struct {
	ID         int64   `json:"id"`
	GameID     int64   `json:"game_id"`
	Platform   string  `json:"platform"`
	StartedOn  *string `json:"started_on"`
	FinishedOn *string `json:"finished_on"`
	Completion *string `json:"completion"`
	Minutes    int64   `json:"minutes"`
	Rating     *int64  `json:"rating"`
	Note       string  `json:"note"`
	Current    bool    `json:"current"`
}

const maxSessionNoteChars = 500

func insertSession(ctx context.Context, tx *sql.Tx, userID string, gameID, playthroughID int64, day string, minutes int64, note, source string) (*Session, error) {
	if utf8.RuneCountInString(note) > maxSessionNoteChars {
		return nil, invalid("invalid_note", "session_note", "Session notes must be at most 500 characters")
	}
	pt := any(nil)
	if playthroughID != 0 {
		pt = playthroughID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO play_sessions (user_id, game_id, playthrough_id, played_on, minutes, note, source)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, gameID, pt, day, minutes, note, source)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return getSession(ctx, tx, userID, id)
}

func getSession(ctx context.Context, q querier, userID string, id int64) (*Session, error) {
	var s Session
	var pt sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT id, game_id, playthrough_id, played_on, minutes, note, source, created_at
		FROM play_sessions WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&s.ID, &s.GameID, &pt, &s.PlayedOn, &s.Minutes, &s.Note, &s.Source, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.PlaythroughID = ptrInt(pt)
	return &s, nil
}

// SessionRange selects diary entries; empty bounds are open.
type SessionRange struct {
	From, To string // YYYY-MM-DD inclusive
	GameID   int64
	Limit    int
	// IncludeAdjustments returns adjustment and legacy entries too; the
	// diary shows only real sessions by default.
	IncludeAdjustments bool
}

// Sessions lists diary entries, newest first.
func (s *Store) Sessions(ctx context.Context, userID string, r SessionRange) ([]Session, error) {
	where := "ps.user_id = ?"
	args := []any{userID}
	if r.From != "" {
		where += " AND ps.played_on >= ?"
		args = append(args, r.From)
	}
	if r.To != "" {
		where += " AND ps.played_on <= ?"
		args = append(args, r.To)
	}
	if r.GameID != 0 {
		where += " AND ps.game_id = ?"
		args = append(args, r.GameID)
	}
	if !r.IncludeAdjustments {
		where += " AND ps.source NOT IN ('adjust', 'legacy')"
	}
	if r.Limit <= 0 || r.Limit > 1000 {
		r.Limit = 1000
	}
	args = append(args, r.Limit)
	rows, err := s.db.QueryContext(ctx, `SELECT ps.id, ps.game_id, ps.playthrough_id, ps.played_on, ps.minutes, ps.note, ps.source, ps.created_at,
			g.name, g.local_cover_path, g.cover_url
		FROM play_sessions ps JOIN games g ON g.id = ps.game_id
		WHERE `+where+` ORDER BY ps.played_on DESC, ps.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var ss Session
		var pt sql.NullInt64
		var local, remote string
		if err := rows.Scan(&ss.ID, &ss.GameID, &pt, &ss.PlayedOn, &ss.Minutes, &ss.Note, &ss.Source, &ss.CreatedAt,
			&ss.GameName, &local, &remote); err != nil {
			return nil, err
		}
		ss.PlaythroughID = ptrInt(pt)
		ss.Cover = CoverURL(local, remote)
		out = append(out, ss)
	}
	return out, rows.Err()
}

// LogSession records play time for a game in the library.
func (s *Store) LogSession(ctx context.Context, userID string, gameID int64, minutes int64, playedOn, note string) (*WriteResult, error) {
	if minutes <= 0 || minutes > MaxSessionMinutes {
		return nil, invalid("invalid_minutes", "minutes", "A session is 1 to 1,440 minutes")
	}
	return s.Patch(ctx, userID, gameID, Change{MinutesDelta: &minutes, PlayedOn: playedOn, SessionNote: note})
}

// DeleteSession removes a diary entry (its minutes leave the total).
func (s *Store) DeleteSession(ctx context.Context, userID string, sessionID int64) (*WriteResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sess, err := getSession(ctx, tx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	snap, err := snapshotItem(ctx, tx, userID, sess.GameID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM play_sessions WHERE id = ? AND user_id = ?`, sessionID, userID); err != nil {
		return nil, err
	}
	return s.finishWrite(ctx, tx, userID, sess.GameID, []itemSnapshot{snap})
}

// UpdateSession edits a diary entry's day, minutes or note.
func (s *Store) UpdateSession(ctx context.Context, userID string, sessionID int64, playedOn *string, minutes *int64, note *string) (*WriteResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sess, err := getSession(ctx, tx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	snap, err := snapshotItem(ctx, tx, userID, sess.GameID)
	if err != nil {
		return nil, err
	}
	if playedOn != nil {
		d, ok := parseDay(*playedOn)
		if !ok {
			return nil, invalid("invalid_date", "played_on", "played_on must be YYYY-MM-DD")
		}
		sess.PlayedOn = d
	}
	if minutes != nil {
		if *minutes <= 0 || (*minutes > MaxSessionMinutes && sess.Source != "legacy" && sess.Source != "import" && sess.Source != "steam") {
			return nil, invalid("invalid_minutes", "minutes", "A session is 1 to 1,440 minutes")
		}
		sess.Minutes = *minutes
	}
	if note != nil {
		if utf8.RuneCountInString(*note) > maxSessionNoteChars {
			return nil, invalid("invalid_note", "note", "Session notes must be at most 500 characters")
		}
		sess.Note = strings.TrimSpace(*note)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE play_sessions SET played_on = ?, minutes = ?, note = ? WHERE id = ? AND user_id = ?`,
		sess.PlayedOn, sess.Minutes, sess.Note, sessionID, userID); err != nil {
		return nil, err
	}
	return s.finishWrite(ctx, tx, userID, sess.GameID, []itemSnapshot{snap})
}

// finishWrite records the undo snapshot, reloads the item and commits.
func (s *Store) finishWrite(ctx context.Context, tx *sql.Tx, userID string, gameID int64, snaps []itemSnapshot) (*WriteResult, error) {
	return s.finishWriteEvents(ctx, tx, userID, gameID, snaps, nil)
}

func (s *Store) finishWriteEvents(ctx context.Context, tx *sql.Tx, userID string, gameID int64, snaps []itemSnapshot, evs []Event) (*WriteResult, error) {
	res := &WriteResult{}
	var err error
	if res.UndoID, err = saveUndo(ctx, tx, userID, snaps); err != nil {
		return nil, err
	}
	if err := recordEvents(ctx, tx, userID, res.UndoID, evs); err != nil {
		return nil, err
	}
	if res.Item, err = s.get(ctx, tx, userID, gameID); err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT library_rev FROM user_state WHERE user_id = ?`, userID).Scan(&res.Rev); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// Playthroughs lists a game's runs, newest first.
func (s *Store) Playthroughs(ctx context.Context, userID string, gameID int64) ([]Playthrough, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.game_id, p.platform, p.started_on, p.finished_on, p.completion, p.minutes, p.rating, p.note,
			COALESCE(li.current_playthrough_id = p.id, 0)
		FROM playthroughs p JOIN library_items li ON li.user_id = p.user_id AND li.game_id = p.game_id
		WHERE p.user_id = ? AND p.game_id = ?
		ORDER BY COALESCE(p.started_on, p.finished_on, p.created_at) DESC, p.id DESC`, userID, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Playthrough{}
	for rows.Next() {
		var p Playthrough
		var so, fo, comp sql.NullString
		var rating sql.NullInt64
		if err := rows.Scan(&p.ID, &p.GameID, &p.Platform, &so, &fo, &comp, &p.Minutes, &rating, &p.Note, &p.Current); err != nil {
			return nil, err
		}
		p.StartedOn, p.FinishedOn, p.Completion, p.Rating = ptrString(so), ptrString(fo), ptrString(comp), ptrInt(rating)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PlaythroughChange edits a playthrough; nil keeps, "" clears dates.
type PlaythroughChange struct {
	Platform   *string `json:"platform"`
	StartedOn  *string `json:"started_on"`
	FinishedOn *string `json:"finished_on"`
	Completion *string `json:"completion"`
	Rating     *int64  `json:"rating"`
	Note       *string `json:"note"`
}

// UpdatePlaythrough edits one playthrough.
func (s *Store) UpdatePlaythrough(ctx context.Context, userID string, id int64, ch PlaythroughChange) (*WriteResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var gameID int64
	if err := tx.QueryRowContext(ctx, `SELECT game_id FROM playthroughs WHERE id = ? AND user_id = ?`, id, userID).Scan(&gameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	snap, err := snapshotItem(ctx, tx, userID, gameID)
	if err != nil {
		return nil, err
	}
	day := func(v *string, field string) (any, error) {
		if strings.TrimSpace(*v) == "" {
			return nil, nil
		}
		d, ok := parseDay(*v)
		if !ok {
			return nil, invalid("invalid_date", field, field+" must be YYYY-MM-DD")
		}
		return d, nil
	}
	sets := []string{}
	args := []any{}
	if ch.Platform != nil {
		if len(*ch.Platform) > MaxPlatformChars {
			return nil, invalid("invalid_platform", "platform", "Platform must be at most 64 characters")
		}
		sets, args = append(sets, "platform = ?"), append(args, strings.TrimSpace(*ch.Platform))
	}
	if ch.StartedOn != nil {
		v, err := day(ch.StartedOn, "started_on")
		if err != nil {
			return nil, err
		}
		sets, args = append(sets, "started_on = ?"), append(args, v)
	}
	if ch.FinishedOn != nil {
		v, err := day(ch.FinishedOn, "finished_on")
		if err != nil {
			return nil, err
		}
		sets, args = append(sets, "finished_on = ?"), append(args, v)
	}
	if ch.Completion != nil {
		if *ch.Completion != "" && !validCompletion(*ch.Completion) {
			return nil, invalid("invalid_completion", "completion", "Completion must be main, main_extras, completionist, endless or dropped")
		}
		sets, args = append(sets, "completion = NULLIF(?, '')"), append(args, *ch.Completion)
	}
	if ch.Rating != nil {
		if *ch.Rating < 0 || *ch.Rating > 100 {
			return nil, invalid("invalid_rating", "rating", "Rating must be between 0 and 100")
		}
		sets, args = append(sets, "rating = NULLIF(?, 0)"), append(args, *ch.Rating)
	}
	if ch.Note != nil {
		if utf8.RuneCountInString(*ch.Note) > MaxNotesChars {
			return nil, invalid("invalid_note", "note", "Notes must be at most 10,000 characters")
		}
		sets, args = append(sets, "note = ?"), append(args, *ch.Note)
	}
	if len(sets) > 0 {
		args = append(args, id, userID)
		if _, err := tx.ExecContext(ctx, `UPDATE playthroughs SET `+strings.Join(sets, ", ")+` WHERE id = ? AND user_id = ?`, args...); err != nil {
			return nil, err
		}
		// The item row reflects the journal: touch it so library_rev moves.
		if _, err := tx.ExecContext(ctx, `UPDATE library_items SET rev = rev WHERE user_id = ? AND game_id = ?`, userID, gameID); err != nil {
			return nil, err
		}
	}
	return s.finishWrite(ctx, tx, userID, gameID, []itemSnapshot{snap})
}

// DeletePlaythrough removes a run and its sessions.
func (s *Store) DeletePlaythrough(ctx context.Context, userID string, id int64) (*WriteResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var gameID int64
	if err := tx.QueryRowContext(ctx, `SELECT game_id FROM playthroughs WHERE id = ? AND user_id = ?`, id, userID).Scan(&gameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	snap, err := snapshotItem(ctx, tx, userID, gameID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM playthroughs WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE library_items SET current_playthrough_id = NULL WHERE user_id = ? AND game_id = ? AND current_playthrough_id = ?`, userID, gameID, id); err != nil {
		return nil, err
	}
	return s.finishWrite(ctx, tx, userID, gameID, []itemSnapshot{snap})
}

// Timer is the running session timer (at most one per user).
type Timer struct {
	GameID    int64  `json:"game_id"`
	StartedAt string `json:"started_at"`
	GameName  string `json:"game_name"`
	Cover     string `json:"cover"`
}

// ErrNoTimer means no timer is running.
var ErrNoTimer = errors.New("no timer running")

// GetTimer returns the running timer, or nil.
func (s *Store) GetTimer(ctx context.Context, userID string) (*Timer, error) {
	var t Timer
	var gameID sql.NullInt64
	var started sql.NullString
	var local, remote sql.NullString
	var name sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT us.timer_game_id, us.timer_started_at, g.name, g.local_cover_path, g.cover_url
		FROM user_state us LEFT JOIN games g ON g.id = us.timer_game_id WHERE us.user_id = ?`, userID).
		Scan(&gameID, &started, &name, &local, &remote)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !gameID.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.GameID, t.StartedAt, t.GameName, t.Cover = gameID.Int64, started.String, name.String, CoverURL(local.String, remote.String)
	return &t, nil
}

// StartTimer starts (or restarts) the session timer for a library game.
func (s *Store) StartTimer(ctx context.Context, userID string, gameID int64) (*Timer, error) {
	if _, err := s.get(ctx, s.db, userID, gameID); err != nil {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO user_state (user_id, timer_game_id, timer_started_at) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET timer_game_id = excluded.timer_game_id, timer_started_at = excluded.timer_started_at`,
		userID, gameID, now); err != nil {
		return nil, err
	}
	return s.GetTimer(ctx, userID)
}

// StopTimer stops the timer. With log, the elapsed time (or minutes, when
// the user corrected it) becomes a session dated playedOn.
func (s *Store) StopTimer(ctx context.Context, userID string, log bool, minutes int64, playedOn string) (*WriteResult, error) {
	t, err := s.GetTimer(ctx, userID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, ErrNoTimer
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE user_state SET timer_game_id = NULL, timer_started_at = NULL WHERE user_id = ?`, userID); err != nil {
		return nil, err
	}
	if !log {
		return &WriteResult{}, nil
	}
	if minutes <= 0 {
		started, err := time.Parse(time.RFC3339, t.StartedAt)
		if err != nil {
			return nil, err
		}
		minutes = int64(s.now().Sub(started).Round(time.Minute) / time.Minute)
	}
	if minutes < 1 {
		minutes = 1
	}
	if minutes > MaxSessionMinutes {
		minutes = MaxSessionMinutes
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	snap, err := snapshotItem(ctx, tx, userID, t.GameID)
	if err != nil {
		return nil, err
	}
	if snap.Row == nil {
		return nil, ErrNotFound
	}
	var pt int64
	if snap.Row.CurrentPT != nil {
		pt = *snap.Row.CurrentPT
	}
	sess, err := insertSession(ctx, tx, userID, t.GameID, pt, s.today(playedOn), minutes, "", "timer")
	if err != nil {
		return nil, err
	}
	res, err := s.finishWriteEvents(ctx, tx, userID, t.GameID, []itemSnapshot{snap},
		[]Event{{Kind: EventLogged, GameID: t.GameID, Data: map[string]any{"minutes": sess.Minutes, "played_on": sess.PlayedOn}}})
	if err != nil {
		return nil, err
	}
	res.Session = sess
	return res, nil
}
