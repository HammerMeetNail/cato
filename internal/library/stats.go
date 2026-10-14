package library

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

type NameCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type YearSummary struct {
	Year         string           `json:"year"`
	Finished     int64            `json:"finished"`
	Minutes      int64            `json:"minutes"`
	ByCompletion map[string]int64 `json:"by_completion"`
}

type DayMinutes struct {
	Day     string `json:"day"`
	Minutes int64  `json:"minutes"`
}

type MonthMinutes struct {
	Month   string `json:"month"`
	Minutes int64  `json:"minutes"`
}

type BacklogGame struct {
	GameID  int64  `json:"game_id"`
	Name    string `json:"name"`
	Cover   string `json:"cover"`
	AddedAt string `json:"added_at"`
}

type Stats struct {
	TotalGames    int64            `json:"total_games"`
	TotalFinished int64            `json:"total_finished"`
	TotalMinutes  int64            `json:"total_minutes"`
	AvgRating     float64          `json:"avg_rating"`
	Counts        map[string]int64 `json:"counts"`

	Year          int   `json:"year"`
	YearMinutes   int64 `json:"year_minutes"`
	YearFinished  int64 `json:"year_finished"`
	YearStarted   int64 `json:"year_started"`
	YearAdded     int64 `json:"year_added"`
	YearSessions  int64 `json:"year_sessions"`
	YearDaysPlayd int64 `json:"year_days_played"`

	ByYear       []YearSummary  `json:"by_year"`
	HoursByMonth []MonthMinutes `json:"minutes_by_month"`
	Heatmap      []DayMinutes   `json:"heatmap"`
	// RatingBuckets[i] counts ratings in (i*10, (i+1)*10]: ten half-star steps.
	RatingBuckets []int64     `json:"rating_buckets"`
	TopTags       []NameCount `json:"top_tags"`
	TopPlatforms  []NameCount `json:"top_platforms"`
	TopGenres     []NameCount `json:"top_genres"`

	BacklogCount   int64         `json:"backlog_count"`
	BacklogOver1y  int64         `json:"backlog_over_1y"`
	BacklogOver2y  int64         `json:"backlog_over_2y"`
	BacklogOldest  []BacklogGame `json:"backlog_oldest"`
	BacklogTTBMins int64         `json:"backlog_ttb_minutes"`
	BacklogWithTTB int64         `json:"backlog_with_ttb"`

	LongestSession *Session `json:"longest_session"`
	StreakCurrent  int      `json:"streak_current"`
	StreakLongest  int      `json:"streak_longest"`
}

// StatsFor computes the You → Stats numbers for year (0 = this year).
// today is the user's local day for streaks (server UTC day when empty).
func (s *Store) StatsFor(ctx context.Context, userID string, year int, today string) (*Stats, error) {
	now := s.now().UTC()
	if year <= 0 {
		year = now.Year()
	}
	if _, ok := parseDay(today); !ok {
		today = now.Format("2006-01-02")
	}
	y := strconv.Itoa(year)
	st := &Stats{Year: year, RatingBuckets: make([]int64, 10), ByYear: []YearSummary{}, HoursByMonth: []MonthMinutes{},
		Heatmap: []DayMinutes{}, TopTags: []NameCount{}, TopPlatforms: []NameCount{}, TopGenres: []NameCount{}, BacklogOldest: []BacklogGame{}}
	q := s.db

	var avg sql.NullFloat64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(status = 'completed'), 0), COALESCE(SUM(playtime_minutes), 0), AVG(NULLIF(rating, 0))
		FROM library_items WHERE user_id = ?`, userID).Scan(&st.TotalGames, &st.TotalFinished, &st.TotalMinutes, &avg); err != nil {
		return nil, err
	}
	if avg.Valid {
		st.AvgRating = float64(int64(avg.Float64*10+0.5)) / 10
	}
	var err error
	if st.Counts, err = s.Counts(ctx, userID); err != nil {
		return nil, err
	}

	from, to := y+"-01-01", y+"-12-31"
	q.QueryRowContext(ctx, `SELECT COALESCE(SUM(minutes), 0), COUNT(*) FILTER (WHERE source IN ('manual', 'timer', 'steam')),
			COUNT(DISTINCT played_on) FILTER (WHERE source IN ('manual', 'timer', 'steam'))
		FROM play_sessions WHERE user_id = ? AND played_on BETWEEN ? AND ?`, userID, from, to).Scan(&st.YearMinutes, &st.YearSessions, &st.YearDaysPlayd)
	q.QueryRowContext(ctx, `SELECT COUNT(*) FROM playthroughs WHERE user_id = ? AND finished_on BETWEEN ? AND ? AND COALESCE(completion, '') != 'dropped'`,
		userID, from, to).Scan(&st.YearFinished)
	q.QueryRowContext(ctx, `SELECT COUNT(*) FROM playthroughs WHERE user_id = ? AND started_on BETWEEN ? AND ?`, userID, from, to).Scan(&st.YearStarted)
	q.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_items WHERE user_id = ? AND substr(created_at, 1, 4) = ?`, userID, y).Scan(&st.YearAdded)

	// Finished per year by completion type, plus minutes per year.
	rows, err := q.QueryContext(ctx, `SELECT substr(finished_on, 1, 4) AS yr, COALESCE(completion, 'unknown'), COUNT(*)
		FROM playthroughs WHERE user_id = ? AND finished_on IS NOT NULL AND COALESCE(completion, '') != 'dropped'
		GROUP BY 1, 2 ORDER BY 1 DESC`, userID)
	if err != nil {
		return nil, err
	}
	byYear := map[string]*YearSummary{}
	var order []string
	for rows.Next() {
		var yr, comp string
		var n int64
		if err := rows.Scan(&yr, &comp, &n); err != nil {
			rows.Close()
			return nil, err
		}
		ys, ok := byYear[yr]
		if !ok {
			ys = &YearSummary{Year: yr, ByCompletion: map[string]int64{}}
			byYear[yr] = ys
			order = append(order, yr)
		}
		ys.Finished += n
		ys.ByCompletion[comp] += n
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, `SELECT substr(played_on, 1, 4), SUM(minutes) FROM play_sessions WHERE user_id = ? GROUP BY 1`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var yr string
		var m int64
		if err := rows.Scan(&yr, &m); err != nil {
			rows.Close()
			return nil, err
		}
		if ys, ok := byYear[yr]; ok {
			ys.Minutes = m
		} else if m > 0 {
			byYear[yr] = &YearSummary{Year: yr, Minutes: m, ByCompletion: map[string]int64{}}
			order = append(order, yr)
		}
	}
	rows.Close()
	sortDesc(order)
	for _, yr := range order {
		st.ByYear = append(st.ByYear, *byYear[yr])
	}

	// Minutes per month for the last 12 months (real sessions only: legacy
	// totals have no real date).
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -11, 0)
	monthly := map[string]int64{}
	rows, err = q.QueryContext(ctx, `SELECT substr(played_on, 1, 7), SUM(minutes) FROM play_sessions
		WHERE user_id = ? AND played_on >= ? AND source NOT IN ('legacy', 'adjust') GROUP BY 1`, userID, start.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m string
		var n int64
		if err := rows.Scan(&m, &n); err != nil {
			rows.Close()
			return nil, err
		}
		monthly[m] = n
	}
	rows.Close()
	for i := 0; i < 12; i++ {
		m := start.AddDate(0, i, 0).Format("2006-01")
		st.HoursByMonth = append(st.HoursByMonth, MonthMinutes{Month: m, Minutes: monthly[m]})
	}

	rows, err = q.QueryContext(ctx, `SELECT played_on, SUM(minutes) FROM play_sessions
		WHERE user_id = ? AND played_on BETWEEN ? AND ? AND source NOT IN ('legacy', 'adjust') GROUP BY 1 ORDER BY 1`, userID, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d DayMinutes
		if err := rows.Scan(&d.Day, &d.Minutes); err != nil {
			rows.Close()
			return nil, err
		}
		if d.Minutes > 0 {
			st.Heatmap = append(st.Heatmap, d)
		}
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT (rating - 1) / 10, COUNT(*) FROM library_items WHERE user_id = ? AND rating > 0 GROUP BY 1`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b, n int64
		if err := rows.Scan(&b, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if b >= 0 && b < 10 {
			st.RatingBuckets[b] = n
		}
	}
	rows.Close()

	if st.TopTags, err = nameCounts(ctx, q, `SELECT MIN(tag), COUNT(*) AS c FROM library_tags WHERE user_id = ? GROUP BY LOWER(tag) ORDER BY c DESC LIMIT 8`, userID); err != nil {
		return nil, err
	}
	if st.TopPlatforms, err = nameCounts(ctx, q, `SELECT j.value, COUNT(*) AS c FROM library_items li, json_each(li.owned_platforms_json) j
		WHERE li.user_id = ? GROUP BY LOWER(j.value) ORDER BY c DESC LIMIT 6`, userID); err != nil {
		return nil, err
	}

	oneYear, twoYears := now.AddDate(-1, 0, 0).Format("2006-01-02"), now.AddDate(-2, 0, 0).Format("2006-01-02")
	q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(created_at < ?), 0), COALESCE(SUM(created_at < ?), 0)
		FROM library_items WHERE user_id = ? AND status = 'backlog'`, oneYear, twoYears, userID).Scan(&st.BacklogCount, &st.BacklogOver1y, &st.BacklogOver2y)
	rows, err = q.QueryContext(ctx, `SELECT li.game_id, g.name, g.local_cover_path, g.cover_url, li.created_at
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? AND li.status = 'backlog' ORDER BY li.created_at ASC LIMIT 5`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b BacklogGame
		var local, remote string
		if err := rows.Scan(&b.GameID, &b.Name, &local, &remote, &b.AddedAt); err != nil {
			rows.Close()
			return nil, err
		}
		b.Cover = CoverURL(local, remote)
		st.BacklogOldest = append(st.BacklogOldest, b)
	}
	rows.Close()

	var ls Session
	var local, remote string
	if err := q.QueryRowContext(ctx, `SELECT ps.id, ps.game_id, ps.played_on, ps.minutes, ps.source, g.name, g.local_cover_path, g.cover_url
		FROM play_sessions ps JOIN games g ON g.id = ps.game_id
		WHERE ps.user_id = ? AND ps.source IN ('manual', 'timer') AND ps.played_on BETWEEN ? AND ?
		ORDER BY ps.minutes DESC, ps.played_on DESC LIMIT 1`, userID, from, to).
		Scan(&ls.ID, &ls.GameID, &ls.PlayedOn, &ls.Minutes, &ls.Source, &ls.GameName, &local, &remote); err == nil {
		ls.Cover = CoverURL(local, remote)
		st.LongestSession = &ls
	}

	if st.StreakCurrent, st.StreakLongest, err = s.streaks(ctx, userID, today); err != nil {
		return nil, err
	}
	return st, nil
}

// streaks counts consecutive days with a real session: the run ending today
// (or yesterday, so an unplayed morning doesn't reset it) and the longest.
func (s *Store) streaks(ctx context.Context, userID, today string) (int, int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT played_on FROM play_sessions
		WHERE user_id = ? AND source IN ('manual', 'timer', 'steam') AND played_on <= ? ORDER BY played_on DESC`, userID, today)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var days []time.Time
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return 0, 0, err
		}
		if t, err := time.Parse("2006-01-02", d); err == nil {
			days = append(days, t)
		}
	}
	if len(days) == 0 {
		return 0, 0, rows.Err()
	}
	todayT, _ := time.Parse("2006-01-02", today)
	current := 0
	if gap := todayT.Sub(days[0]).Hours() / 24; gap <= 1 {
		current = 1
		for i := 1; i < len(days) && days[i-1].Sub(days[i]).Hours()/24 == 1; i++ {
			current++
		}
	}
	longest, run := 1, 1
	for i := 1; i < len(days); i++ {
		if days[i-1].Sub(days[i]).Hours()/24 == 1 {
			run++
		} else {
			run = 1
		}
		longest = max(longest, run)
	}
	return current, longest, rows.Err()
}

func nameCounts(ctx context.Context, q querier, query string, args ...any) ([]NameCount, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("stats: %w", err)
	}
	defer rows.Close()
	out := []NameCount{}
	for rows.Next() {
		var nc NameCount
		if err := rows.Scan(&nc.Name, &nc.Count); err != nil {
			return nil, err
		}
		out = append(out, nc)
	}
	return out, rows.Err()
}

func sortDesc(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] > v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
