package library

import (
	"context"
	"strconv"
)

// RGame is a game in the Year in Review.
type RGame struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Cover    string `json:"cover"`
	Rating   int64  `json:"rating,omitempty"`
	Minutes  int64  `json:"minutes,omitempty"`
	Day      string `json:"day,omitempty"`
	Released int64  `json:"release_date,omitempty"`
}

// Run is one playthrough on the year's timeline.
type Run struct {
	Game     RGame  `json:"game"`
	Started  string `json:"started,omitempty"`
	Finished string `json:"finished,omitempty"`
	Dropped  bool   `json:"dropped,omitempty"`
}

// YearReview is #you/year/YYYY (plan §10).
type YearReview struct {
	Year        int     `json:"year"`
	Minutes     int64   `json:"minutes"`
	Sessions    int64   `json:"sessions"`
	DaysPlayed  int64   `json:"days_played"`
	GamesPlayed int64   `json:"games_played"`
	Finished    []RGame `json:"finished"`
	FirstFinish *RGame  `json:"first_finish,omitempty"`
	TopRated    []RGame `json:"top_rated"`
	MostPlayed  []RGame `json:"most_played"`
	Longest     *RGame  `json:"longest_session,omitempty"`
	BusiestDay  *RGame  `json:"busiest_day,omitempty"`
	TopGenre    string  `json:"top_genre,omitempty"`
	TopPlatform string  `json:"top_platform,omitempty"`
	Months      []int64 `json:"months"`
	Timeline    []Run   `json:"timeline"`
	OldestGame  *RGame  `json:"oldest_game,omitempty"`
	// Instance compares you with people on this server who share beyond
	// themselves (only when at least three do).
	Instance *Compare `json:"instance,omitempty"`
	Years    []int    `json:"years"` // years with any play, for navigation
}

type Compare struct {
	People          int     `json:"people"`
	HoursPercentile int     `json:"hours_percentile"` // share of people who played less
	MedianHours     int64   `json:"median_hours"`
	MedianFinished  float64 `json:"median_finished"`
}

const realPlay = `source IN ('manual', 'timer', 'steam', 'import')`

func (s *Store) YearInReview(ctx context.Context, userID string, year int) (*YearReview, error) {
	y := strconv.Itoa(year)
	from, to := y+"-01-01", y+"-12-31"
	r := &YearReview{Year: year, Finished: []RGame{}, TopRated: []RGame{}, MostPlayed: []RGame{}, Months: make([]int64, 12), Timeline: []Run{}, Years: []int{}}
	q := s.db
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(minutes), 0), COUNT(*), COUNT(DISTINCT played_on), COUNT(DISTINCT game_id)
		FROM play_sessions WHERE user_id = ? AND played_on BETWEEN ? AND ? AND `+realPlay, userID, from, to).
		Scan(&r.Minutes, &r.Sessions, &r.DaysPlayed, &r.GamesPlayed); err != nil {
		return nil, err
	}
	games := func(query string, args ...any) ([]RGame, error) {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []RGame{}
		for rows.Next() {
			var g RGame
			var local, remote string
			if err := rows.Scan(&g.ID, &g.Name, &local, &remote, &g.Rating, &g.Minutes, &g.Day, &g.Released); err != nil {
				return nil, err
			}
			g.Cover = CoverURL(local, remote)
			out = append(out, g)
		}
		return out, rows.Err()
	}
	const gcols = `g.id, g.name, COALESCE(g.local_cover_path, ''), COALESCE(g.cover_url, '')`
	var err error
	if r.Finished, err = games(`SELECT `+gcols+`, COALESCE(pt.rating, li.rating, 0), pt.minutes, pt.finished_on, COALESCE(g.first_release_date, 0)
		FROM playthroughs pt JOIN games g ON g.id = pt.game_id LEFT JOIN library_items li ON li.user_id = pt.user_id AND li.game_id = pt.game_id
		WHERE pt.user_id = ? AND pt.finished_on BETWEEN ? AND ? AND COALESCE(pt.completion, '') != 'dropped'
		ORDER BY pt.finished_on, pt.id`, userID, from, to); err != nil {
		return nil, err
	}
	if len(r.Finished) > 0 {
		f := r.Finished[0]
		r.FirstFinish = &f
	}
	for _, g := range r.Finished {
		if g.Rating > 0 {
			r.TopRated = append(r.TopRated, g)
		}
	}
	for i := 1; i < len(r.TopRated); i++ {
		for j := i; j > 0 && r.TopRated[j].Rating > r.TopRated[j-1].Rating; j-- {
			r.TopRated[j], r.TopRated[j-1] = r.TopRated[j-1], r.TopRated[j]
		}
	}
	if len(r.TopRated) > 5 {
		r.TopRated = r.TopRated[:5]
	}
	if r.MostPlayed, err = games(`SELECT `+gcols+`, COALESCE(li.rating, 0), SUM(ps.minutes) AS m, '', COALESCE(g.first_release_date, 0)
		FROM play_sessions ps JOIN games g ON g.id = ps.game_id LEFT JOIN library_items li ON li.user_id = ps.user_id AND li.game_id = ps.game_id
		WHERE ps.user_id = ? AND ps.played_on BETWEEN ? AND ? AND ps.`+realPlay+`
		GROUP BY ps.game_id ORDER BY m DESC LIMIT 5`, userID, from, to); err != nil {
		return nil, err
	}
	if l, err := games(`SELECT `+gcols+`, 0, ps.minutes, ps.played_on, 0 FROM play_sessions ps JOIN games g ON g.id = ps.game_id
		WHERE ps.user_id = ? AND ps.played_on BETWEEN ? AND ? AND ps.source IN ('manual', 'timer') ORDER BY ps.minutes DESC, ps.played_on LIMIT 1`, userID, from, to); err == nil && len(l) == 1 {
		r.Longest = &l[0]
	}
	if b, err := games(`SELECT 0, '', '', '', 0, SUM(minutes) AS m, played_on, 0 FROM play_sessions
		WHERE user_id = ? AND played_on BETWEEN ? AND ? AND `+realPlay+` GROUP BY played_on ORDER BY m DESC LIMIT 1`, userID, from, to); err == nil && len(b) == 1 {
		r.BusiestDay = &b[0]
	}
	if o, err := games(`SELECT `+gcols+`, 0, SUM(ps.minutes), '', g.first_release_date FROM play_sessions ps JOIN games g ON g.id = ps.game_id
		WHERE ps.user_id = ? AND ps.played_on BETWEEN ? AND ? AND ps.`+realPlay+` AND g.first_release_date > 0
		GROUP BY ps.game_id ORDER BY g.first_release_date LIMIT 1`, userID, from, to); err == nil && len(o) == 1 {
		r.OldestGame = &o[0]
	}
	q.QueryRowContext(ctx, `SELECT l.name FROM play_sessions ps JOIN games g ON g.id = ps.game_id, json_each(g.genres_json) je
		JOIN lookups l ON l.kind = 'genres' AND l.id = je.value
		WHERE ps.user_id = ? AND ps.played_on BETWEEN ? AND ? AND ps.`+realPlay+` GROUP BY l.name ORDER BY SUM(ps.minutes) DESC LIMIT 1`,
		userID, from, to).Scan(&r.TopGenre)
	q.QueryRowContext(ctx, `SELECT pt.platform FROM play_sessions ps JOIN playthroughs pt ON pt.id = ps.playthrough_id
		WHERE ps.user_id = ? AND ps.played_on BETWEEN ? AND ? AND ps.`+realPlay+` AND pt.platform != ''
		GROUP BY pt.platform ORDER BY SUM(ps.minutes) DESC LIMIT 1`, userID, from, to).Scan(&r.TopPlatform)
	if r.TopPlatform == "" {
		q.QueryRowContext(ctx, `SELECT je.value FROM play_sessions ps JOIN library_items li ON li.user_id = ps.user_id AND li.game_id = ps.game_id,
			json_each(li.owned_platforms_json) je
			WHERE ps.user_id = ? AND ps.played_on BETWEEN ? AND ? AND ps.`+realPlay+` GROUP BY je.value ORDER BY SUM(ps.minutes) DESC LIMIT 1`,
			userID, from, to).Scan(&r.TopPlatform)
	}
	rows, err := q.QueryContext(ctx, `SELECT CAST(substr(played_on, 6, 2) AS INTEGER), SUM(minutes) FROM play_sessions
		WHERE user_id = ? AND played_on BETWEEN ? AND ? AND `+realPlay+` GROUP BY 1`, userID, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m int
		var mins int64
		rows.Scan(&m, &mins)
		if m >= 1 && m <= 12 {
			r.Months[m-1] = mins
		}
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT `+gcols+`, COALESCE(pt.started_on, ''), COALESCE(pt.finished_on, ''), COALESCE(pt.completion, '') = 'dropped'
		FROM playthroughs pt JOIN games g ON g.id = pt.game_id
		WHERE pt.user_id = ? AND COALESCE(pt.started_on, pt.finished_on, '') != ''
		  AND COALESCE(pt.started_on, ?) <= ? AND (pt.finished_on IS NULL OR pt.finished_on >= ?)
		  AND EXISTS (SELECT 1 FROM play_sessions ps WHERE ps.playthrough_id = pt.id AND ps.played_on BETWEEN ? AND ?
		              UNION SELECT 1 WHERE pt.finished_on BETWEEN ? AND ?)
		ORDER BY COALESCE(pt.started_on, pt.finished_on) LIMIT 60`, userID, from, to, from, from, to, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var run Run
		var local, remote string
		if err := rows.Scan(&run.Game.ID, &run.Game.Name, &local, &remote, &run.Started, &run.Finished, &run.Dropped); err != nil {
			rows.Close()
			return nil, err
		}
		run.Game.Cover = CoverURL(local, remote)
		r.Timeline = append(r.Timeline, run)
	}
	rows.Close()
	if r.Instance, err = s.compare(ctx, userID, from, to, r.Minutes, int64(len(r.Finished))); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT DISTINCT CAST(substr(played_on, 1, 4) AS INTEGER) FROM play_sessions WHERE user_id = ? AND `+realPlay+` ORDER BY 1 DESC`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var yy int
		rows.Scan(&yy)
		r.Years = append(r.Years, yy)
	}
	rows.Close()
	return r, nil
}

// compare places you among people on this server who aren't private.
func (s *Store) compare(ctx context.Context, userID, from, to string, minutes, finished int64) (*Compare, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id,
			COALESCE((SELECT SUM(minutes) FROM play_sessions ps WHERE ps.user_id = u.id AND ps.played_on BETWEEN ?1 AND ?2 AND ps.`+realPlay+`), 0),
			(SELECT COUNT(*) FROM playthroughs pt WHERE pt.user_id = u.id AND pt.finished_on BETWEEN ?1 AND ?2 AND COALESCE(pt.completion, '') != 'dropped')
		FROM users u WHERE u.disabled = 0 AND u.id != ?3 AND u.visibility != 'private'`, from, to, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mins []int64
	var fins []int64
	for rows.Next() {
		var id string
		var m, f int64
		if err := rows.Scan(&id, &m, &f); err != nil {
			return nil, err
		}
		if m > 0 || f > 0 {
			mins, fins = append(mins, m), append(fins, f)
		}
	}
	if len(mins) < 2 {
		return nil, rows.Err()
	}
	mins, fins = append(mins, minutes), append(fins, finished)
	sortInts(mins)
	sortInts(fins)
	less := 0
	for _, m := range mins {
		if m < minutes {
			less++
		}
	}
	c := &Compare{People: len(mins), HoursPercentile: less * 100 / len(mins), MedianHours: mins[len(mins)/2] / 60}
	if len(fins)%2 == 1 {
		c.MedianFinished = float64(fins[len(fins)/2])
	} else {
		c.MedianFinished = float64(fins[len(fins)/2-1]+fins[len(fins)/2]) / 2
	}
	return c, rows.Err()
}

func sortInts(xs []int64) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
