package importer

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

const batchSize = 1000

type GameRow struct {
	ID                    int64
	Name                  string
	Slug                  string
	SafeName              string
	NormalizedName        string
	Summary               string
	Storyline             string
	CoverID               int64
	CoverURL              string
	FirstReleaseDate      int64
	AggregatedRating      int64
	AggregatedRatingCount int64
	PlatformsJSON         string
	GenresJSON            string
	Trailer              string
	IGDBURL               string
	SourceUpdatedAt       int64
}

func Import(inputPath, dbPath string) (int64, error) {
	database, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return 0, fmt.Errorf("open sqlite: %w", err)
	}
	defer database.Close()

	database.SetMaxOpenConns(1)

	data, err := os.ReadFile(inputPath)
	if err != nil {
		return 0, fmt.Errorf("read input file: %w", err)
	}

	lines := strings.Split(string(data), "\n")

	colMap, copyIdx, err := parseCopyHeader(lines)
	if err != nil {
		return 0, fmt.Errorf("parse copy header: %w", err)
	}

	rows, err := parseCopyRows(lines, copyIdx+1, colMap)
	if err != nil {
		return 0, fmt.Errorf("parse copy rows: %w", err)
	}

	imported, err := writeBatch(database, rows)
	if err != nil {
		return 0, fmt.Errorf("write batch: %w", err)
	}

	return imported, nil
}

func parseCopyHeader(lines []string) (map[string]int, int, error) {
	for i, line := range lines {
		if strings.HasPrefix(line, "COPY public.games (") {
			start := strings.Index(line, "(")
			end := strings.Index(line, ") FROM stdin;")
			if start < 0 || end < 0 {
				return nil, 0, fmt.Errorf("malformed COPY header: %s", line)
			}
			cols := strings.Split(line[start+1:end], ", ")
			colMap := make(map[string]int)
			for j, col := range cols {
				colMap[strings.TrimSpace(col)] = j
			}
			return colMap, i, nil
		}
	}
	return nil, 0, fmt.Errorf("COPY header not found in input")
}

func parseCopyRows(lines []string, startIdx int, colMap map[string]int) ([]GameRow, error) {
	var rows []GameRow

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		if line == `\.` {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")

		row := GameRow{
			ID:                    parseNullableInt(getField(fields, colMap, "id")),
			Name:                  parseNullableString(getField(fields, colMap, "name")),
			Slug:                  parseNullableString(getField(fields, colMap, "slug")),
			SafeName:              parseNullableString(getField(fields, colMap, "safe_name")),
			Summary:               parseNullableString(getField(fields, colMap, "summary")),
			Storyline:             parseNullableString(getField(fields, colMap, "storyline")),
			CoverID:               parseNullableInt(getField(fields, colMap, "cover")),
			CoverURL:              parseNullableString(getField(fields, colMap, "cover_url")),
			FirstReleaseDate:      parseNullableInt(getField(fields, colMap, "first_release_date")),
			AggregatedRating:      parseNullableInt(getField(fields, colMap, "aggregated_rating")),
			AggregatedRatingCount: parseNullableInt(getField(fields, colMap, "aggregated_rating_count")),
			PlatformsJSON:         pgArrayToJSONText(getField(fields, colMap, "platforms")),
			GenresJSON:            pgArrayToJSONText(getField(fields, colMap, "genres")),
			Trailer:              parseNullableString(getField(fields, colMap, "trailer")),
			IGDBURL:               parseNullableString(getField(fields, colMap, "url")),
			SourceUpdatedAt:       parseNullableInt(getField(fields, colMap, "updated_at")),
		}

		name := row.Name
		if name == "" && row.SafeName != "" {
			name = row.SafeName
		}
		row.NormalizedName = normalizeName(name)

		rows = append(rows, row)
	}

	return rows, nil
}

func getField(fields []string, colMap map[string]int, name string) string {
	idx, ok := colMap[name]
	if !ok || idx >= len(fields) {
		return ""
	}
	return fields[idx]
}

func writeBatch(database *sql.DB, rows []GameRow) (int64, error) {
	const upsertSQL = `INSERT INTO games (
  id, name, slug, safe_name, normalized_name, summary, storyline,
  cover_id, cover_url, first_release_date, aggregated_rating,
  aggregated_rating_count, platforms_json, genres_json, trailer,
  igdb_url, source_updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  slug = excluded.slug,
  safe_name = excluded.safe_name,
  normalized_name = excluded.normalized_name,
  summary = excluded.summary,
  storyline = excluded.storyline,
  cover_id = excluded.cover_id,
  cover_url = excluded.cover_url,
  first_release_date = excluded.first_release_date,
  aggregated_rating = excluded.aggregated_rating,
  aggregated_rating_count = excluded.aggregated_rating_count,
  platforms_json = excluded.platforms_json,
  genres_json = excluded.genres_json,
  trailer = excluded.trailer,
  igdb_url = excluded.igdb_url,
  source_updated_at = excluded.source_updated_at`

	var imported int64

	for i := 0; i < len(rows); i += batchSize {
		end := i + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]

		tx, err := database.Begin()
		if err != nil {
			return imported, fmt.Errorf("begin tx: %w", err)
		}

		stmt, err := tx.Prepare(upsertSQL)
		if err != nil {
			tx.Rollback()
			return imported, fmt.Errorf("prepare upsert: %w", err)
		}

		for _, row := range batch {
			_, err := stmt.Exec(
				row.ID, row.Name, row.Slug, row.SafeName, row.NormalizedName,
				row.Summary, row.Storyline, row.CoverID, row.CoverURL,
				row.FirstReleaseDate, row.AggregatedRating, row.AggregatedRatingCount,
				row.PlatformsJSON, row.GenresJSON, row.Trailer, row.IGDBURL,
				row.SourceUpdatedAt,
			)
			if err != nil {
				stmt.Close()
				tx.Rollback()
				return imported, fmt.Errorf("exec upsert for game %d: %w", row.ID, err)
			}
		}

		stmt.Close()
		if err := tx.Commit(); err != nil {
			return imported, fmt.Errorf("commit batch: %w", err)
		}

		imported += int64(len(batch))
	}

	return imported, nil
}
