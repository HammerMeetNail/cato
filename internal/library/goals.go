package library

import (
	"context"
	"errors"
	"strconv"
)

// Goal is a yearly target with its progress (plan §10).
type Goal struct {
	ID       int64  `json:"id"`
	Year     int    `json:"year"`
	Kind     string `json:"kind"` // finish, backlog, retro, hours
	Target   int64  `json:"target"`
	Param    int64  `json:"param,omitempty"` // retro: released before this year
	Progress int64  `json:"progress"`
	Label    string `json:"label"`
}

// MaxGoals per year.
const MaxGoals = 10

func goalLabel(g Goal) string {
	n := strconv.FormatInt(g.Target, 10)
	y := strconv.Itoa(g.Year)
	switch g.Kind {
	case "finish":
		return "Finish " + n + " games in " + y
	case "backlog":
		return "Clear " + n + " from the backlog in " + y
	case "retro":
		return "Play " + n + " games from before " + strconv.FormatInt(g.Param, 10)
	case "hours":
		return "Play " + n + " hours in " + y
	}
	return g.Kind
}

// Goals lists the year's goals with progress:
//   - finish: playthroughs finished in the year (not dropped);
//   - backlog: of those, games that were in the library before the year;
//   - retro: games played in the year that were released before Param;
//   - hours: hours of play logged in the year.
func (s *Store) Goals(ctx context.Context, userID string, year int) ([]Goal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, year, kind, target, param FROM goals WHERE user_id = ? AND year = ? ORDER BY id`, userID, year)
	if err != nil {
		return nil, err
	}
	out := []Goal{}
	for rows.Next() {
		var g Goal
		if err := rows.Scan(&g.ID, &g.Year, &g.Kind, &g.Target, &g.Param); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	rows.Close()
	y := strconv.Itoa(year)
	from, to := y+"-01-01", y+"-12-31"
	for i := range out {
		g := &out[i]
		var q string
		args := []any{userID, from, to}
		switch g.Kind {
		case "finish":
			q = `SELECT COUNT(*) FROM playthroughs WHERE user_id = ? AND finished_on BETWEEN ? AND ? AND COALESCE(completion, '') != 'dropped'`
		case "backlog":
			q = `SELECT COUNT(DISTINCT pt.game_id) FROM playthroughs pt JOIN library_items li ON li.user_id = pt.user_id AND li.game_id = pt.game_id
				WHERE pt.user_id = ? AND pt.finished_on BETWEEN ? AND ? AND COALESCE(pt.completion, '') != 'dropped' AND li.created_at < ?`
			args = append(args, from)
		case "retro":
			q = `SELECT COUNT(DISTINCT ps.game_id) FROM play_sessions ps JOIN games g ON g.id = ps.game_id
				WHERE ps.user_id = ? AND ps.played_on BETWEEN ? AND ? AND ps.minutes > 0 AND ps.source != 'adjust'
				  AND g.first_release_date > 0 AND g.first_release_date < CAST(strftime('%s', ? || '-01-01') AS INTEGER)`
			args = append(args, strconv.FormatInt(g.Param, 10))
		case "hours":
			q = `SELECT COALESCE(SUM(minutes), 0) / 60 FROM play_sessions WHERE user_id = ? AND played_on BETWEEN ? AND ? AND minutes > 0 AND source != 'adjust'`
		}
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&g.Progress); err != nil {
			return nil, err
		}
		g.Label = goalLabel(*g)
	}
	return out, nil
}

// AddGoal sets a target for a year.
func (s *Store) AddGoal(ctx context.Context, userID string, g Goal) (*Goal, error) {
	switch g.Kind {
	case "finish", "backlog", "hours":
	case "retro":
		if g.Param == 0 {
			g.Param = 2000
		}
		if g.Param < 1970 || g.Param > 2100 {
			return nil, invalid("invalid_goal", "param", "Choose a year between 1970 and 2100")
		}
	default:
		return nil, invalid("invalid_goal", "kind", "Choose finish, backlog, retro or hours")
	}
	if g.Target <= 0 || g.Target > 100000 {
		return nil, invalid("invalid_goal", "target", "Choose a target above zero")
	}
	if g.Year < 1970 || g.Year > 2100 {
		return nil, invalid("invalid_goal", "year", "Choose a year")
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM goals WHERE user_id = ? AND year = ?`, userID, g.Year).Scan(&n)
	if n >= MaxGoals {
		return nil, invalid("too_many", "", "Ten goals a year is plenty")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO goals (user_id, year, kind, target, param) VALUES (?, ?, ?, ?, ?)`, userID, g.Year, g.Kind, g.Target, g.Param)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	goals, err := s.Goals(ctx, userID, g.Year)
	if err != nil {
		return nil, err
	}
	for _, x := range goals {
		if x.ID == id {
			return &x, nil
		}
	}
	return nil, errors.New("goal vanished")
}

func (s *Store) DeleteGoal(ctx context.Context, userID string, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM goals WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
