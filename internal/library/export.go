package library

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ExportItem is one library entry in the JSON export, with its journal.
type ExportItem struct {
	Item
	Playthroughs []Playthrough `json:"playthroughs"`
	Sessions     []Session     `json:"sessions"`
}

// ExportAll returns every item with its playthroughs and sessions
// (including adjustments, so totals re-import exactly).
func (s *Store) ExportAll(ctx context.Context, userID string) ([]ExportItem, error) {
	var items []Item
	cursor := ""
	for {
		page, err := s.List(ctx, userID, ListQuery{Sort: "updated", Cursor: cursor, Limit: MaxPageSize, Offset: 0})
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sessions, err := s.Sessions(ctx, userID, SessionRange{IncludeAdjustments: true, Limit: 1 << 30})
	if err != nil {
		return nil, err
	}
	byGame := map[int64][]Session{}
	for _, ss := range sessions {
		ss.GameName, ss.Cover = "", ""
		byGame[ss.GameID] = append(byGame[ss.GameID], ss)
	}
	out := make([]ExportItem, 0, len(items))
	for _, it := range items {
		pts, err := s.Playthroughs(ctx, userID, it.GameID)
		if err != nil {
			return nil, err
		}
		ss := byGame[it.GameID]
		if ss == nil {
			ss = []Session{}
		}
		out = append(out, ExportItem{Item: it, Playthroughs: pts, Sessions: ss})
	}
	return out, nil
}

// CSVSafe defuses spreadsheet formula injection: a cell starting with
// = + - @ tab or carriage return gets a leading apostrophe.
func CSVSafe(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}

// CSVHeader is the column list of WriteCSV (also accepted by the importer).
var CSVHeader = []string{"game", "igdb_id", "status", "platform", "medium", "rating", "playtime_hours",
	"started_at", "completed_at", "added_at", "tags", "notes"}

// WriteCSV writes the library as a spreadsheet-friendly CSV.
func (s *Store) WriteCSV(ctx context.Context, userID string, w io.Writer) error {
	rows, err := s.db.QueryContext(ctx, `SELECT li.game_id, li.status, li.rating, li.playtime_minutes,
			li.owned_platforms_json, li.medium, li.tags_json, li.notes,
			COALESCE(li.started_at, ''), COALESCE(li.completed_at, ''), COALESCE(li.created_at, ''), g.name
		FROM library_items li JOIN games g ON g.id = li.game_id
		WHERE li.user_id = ? ORDER BY g.name COLLATE NOCASE`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	for rows.Next() {
		var id, rating, minutes int64
		var status, owned, medium, tags, notes, started, completed, added, name string
		if err := rows.Scan(&id, &status, &rating, &minutes, &owned, &medium, &tags, &notes, &started, &completed, &added, &name); err != nil {
			return err
		}
		rec := []string{name, strconv.FormatInt(id, 10), status, strings.Join(decodeStrings(owned), "; "), medium,
			strconv.FormatInt(rating, 10), fmt.Sprintf("%.2f", float64(minutes)/60), started, completed, added,
			strings.Join(decodeStrings(tags), "; "), notes}
		for i := range rec {
			rec[i] = CSVSafe(rec[i])
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}
