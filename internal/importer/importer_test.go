package importer

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportIntegration(t *testing.T) {
	// Create a mock pg_restore COPY output
	copyData := `--
-- PostgreSQL database dump
--

SET statement_timeout = 0;

COPY public.games (id, name, slug, safe_name, summary, storyline, cover, cover_url, first_release_date, aggregated_rating, aggregated_rating_count, platforms, genres, trailer, url, updated_at) FROM stdin;
1	The Legend of Zelda	the-legend-of-zelda	The Legend of Zelda	Classic adventure game.	Save the princess.	1234	https://images.igdb.com/covers/zelda.jpg	536457600	95	500	{169,6,167}	{5,12,31}	https://youtube.com/watch?v=zelda	https://igdb.com/games/zelda	1640995200
2	Mario Kart	mario-kart	Mario Kart	Racing game.	\N	5678	https://images.igdb.com/covers/mk.jpg	725846400	90	1000	{6,130}	{10}	\N	https://igdb.com/games/mk	1640995200
3	\N	\N	\N	\N	\N	\N	\N	0	0	0	{}	{}	\N	\N	0
\.
`
	inputPath := filepath.Join(t.TempDir(), "games-copy.sql")
	if err := os.WriteFile(inputPath, []byte(copyData), 0644); err != nil {
		t.Fatalf("failed to write test copy file: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	// Create the games table
	_, err = database.Exec(`CREATE TABLE games (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  safe_name TEXT NOT NULL DEFAULT '',
  normalized_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  storyline TEXT NOT NULL DEFAULT '',
  cover_id INTEGER NOT NULL DEFAULT 0,
  cover_url TEXT NOT NULL DEFAULT '',
  first_release_date INTEGER NOT NULL DEFAULT 0,
  aggregated_rating INTEGER NOT NULL DEFAULT 0,
  aggregated_rating_count INTEGER NOT NULL DEFAULT 0,
  platforms_json TEXT NOT NULL DEFAULT '[]',
  genres_json TEXT NOT NULL DEFAULT '[]',
  trailer TEXT NOT NULL DEFAULT '',
  igdb_url TEXT NOT NULL DEFAULT '',
  source_updated_at INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		t.Fatalf("failed to create games table: %v", err)
	}

	count, err := Import(inputPath, dbPath)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 imported rows, got %d", count)
	}

	// Verify row 1
	var name, platformsJSON, genresJSON string
	var aggregatedRating, aggregatedRatingCount int64
	err = database.QueryRow("SELECT name, aggregated_rating, aggregated_rating_count, platforms_json, genres_json FROM games WHERE id = 1").Scan(&name, &aggregatedRating, &aggregatedRatingCount, &platformsJSON, &genresJSON)
	if err != nil {
		t.Fatalf("failed to query game 1: %v", err)
	}
	if name != "The Legend of Zelda" {
		t.Errorf("expected name 'The Legend of Zelda', got %q", name)
	}
	if aggregatedRating != 95 {
		t.Errorf("expected rating 95, got %d", aggregatedRating)
	}
	if aggregatedRatingCount != 500 {
		t.Errorf("expected rating count 500, got %d", aggregatedRatingCount)
	}
	if platformsJSON != "[169,6,167]" {
		t.Errorf("expected platforms [169,6,167], got %s", platformsJSON)
	}
	if genresJSON != "[5,12,31]" {
		t.Errorf("expected genres [5,12,31], got %s", genresJSON)
	}

	// Verify row 2 (has \N values)
	var storyline string
	err = database.QueryRow("SELECT name, storyline FROM games WHERE id = 2").Scan(&name, &storyline)
	if err != nil {
		t.Fatalf("failed to query game 2: %v", err)
	}
	if name != "Mario Kart" {
		t.Errorf("expected name 'Mario Kart', got %q", name)
	}
	if storyline != "" {
		t.Errorf("expected empty storyline, got %q", storyline)
	}

	// Verify row 3 (all \N values)
	var normalizedName string
	err = database.QueryRow("SELECT id, normalized_name FROM games WHERE id = 3").Scan(new(int64), &normalizedName)
	if err != nil {
		t.Fatalf("failed to query game 3: %v", err)
	}
	if normalizedName != "" {
		t.Errorf("expected empty normalized_name, got %q", normalizedName)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	copyData := `COPY public.games (id, name, slug, safe_name, summary, storyline, cover, cover_url, first_release_date, aggregated_rating, aggregated_rating_count, platforms, genres, trailer, url, updated_at) FROM stdin;
1	Zelda	zelda	Zelda	Summary	Story	1	url	1	80	100	{1}	{2}	trailer	igdb	100
\.
`
	inputPath := filepath.Join(t.TempDir(), "idem-copy.sql")
	if err := os.WriteFile(inputPath, []byte(copyData), 0644); err != nil {
		t.Fatalf("failed to write test copy file: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	_, err = database.Exec(`CREATE TABLE games (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  safe_name TEXT NOT NULL DEFAULT '',
  normalized_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  storyline TEXT NOT NULL DEFAULT '',
  cover_id INTEGER NOT NULL DEFAULT 0,
  cover_url TEXT NOT NULL DEFAULT '',
  first_release_date INTEGER NOT NULL DEFAULT 0,
  aggregated_rating INTEGER NOT NULL DEFAULT 0,
  aggregated_rating_count INTEGER NOT NULL DEFAULT 0,
  platforms_json TEXT NOT NULL DEFAULT '[]',
  genres_json TEXT NOT NULL DEFAULT '[]',
  trailer TEXT NOT NULL DEFAULT '',
  igdb_url TEXT NOT NULL DEFAULT '',
  source_updated_at INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		t.Fatalf("failed to create games table: %v", err)
	}

	// First import
	count, err := Import(inputPath, dbPath)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 on first import, got %d", count)
	}

	// Second import (should upsert, not duplicate)
	count, err = Import(inputPath, dbPath)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 on second import (upsert), got %d", count)
	}

	var rowCount int
	database.QueryRow("SELECT COUNT(*) FROM games").Scan(&rowCount)
	if rowCount != 1 {
		t.Errorf("expected 1 row in games, got %d", rowCount)
	}
}

func TestImportMissingInputFile(t *testing.T) {
	_, err := Import("/nonexistent/path/to/file.sql", "/tmp/test.db")
	if err == nil {
		t.Error("expected error for missing input file")
	}
}
