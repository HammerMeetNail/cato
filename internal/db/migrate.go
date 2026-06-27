package db

import (
	"database/sql"
	"fmt"
	"sort"
)

type Migration struct {
	Version int
	Up      string
}

var migrations = []Migration{
	{
		Version: 5,
		// FTS5 trigram virtual table over normalized_name for typo-tolerant
		// search. Trigram tokenizer matches on shared 3-character substrings,
		// so a query like "zleda" still matches "zelda". External-content
		// mode (content='games') avoids duplicating the text column, but
		// FTS5 does not auto-sync external content tables — the three
		// triggers below keep the index in step with INSERT/UPDATE/DELETE
		// on games. This covers every write path including the raw COPY
		// import in internal/importer, which bypasses the Go store layer.
		Up: `CREATE VIRTUAL TABLE IF NOT EXISTS games_fts USING fts5(
		     normalized_name,
		     tokenize='trigram',
		     content='games',
		     content_rowid='rowid'
		     );
		     INSERT INTO games_fts(rowid, normalized_name)
		       SELECT rowid, normalized_name FROM games;

		     CREATE TRIGGER IF NOT EXISTS games_fts_ai AFTER INSERT ON games BEGIN
		       INSERT INTO games_fts(rowid, normalized_name) VALUES (new.rowid, new.normalized_name);
		     END;
		     CREATE TRIGGER IF NOT EXISTS games_fts_ad AFTER DELETE ON games BEGIN
		       INSERT INTO games_fts(games_fts, rowid, normalized_name) VALUES ('delete', old.rowid, old.normalized_name);
		     END;
		     CREATE TRIGGER IF NOT EXISTS games_fts_au AFTER UPDATE ON games BEGIN
		       INSERT INTO games_fts(games_fts, rowid, normalized_name) VALUES ('delete', old.rowid, old.normalized_name);
		       INSERT INTO games_fts(rowid, normalized_name) VALUES (new.rowid, new.normalized_name);
		     END;`,
	},
	{
		Version: 4,
		// Popularity fields fetched from IGDB and a denormalized
		// popularity_score computed at upsert time from
		// follows*3 + hypes*2 + total_rating_count + main-game-released bonus.
		// The score drives search ranking (store.go SearchLocal) and is
		// indexed so the ORDER BY stays an O(limit) index scan.
		Up: `ALTER TABLE games ADD COLUMN rating REAL NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN rating_count INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN total_rating REAL NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN total_rating_count INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN follows INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN hypes INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN igdb_popularity REAL NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN category INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN status INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN version_parent INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN popularity_score INTEGER NOT NULL DEFAULT 0;
		     ALTER TABLE games ADD COLUMN popularity_fetched_at INTEGER NOT NULL DEFAULT 0;
		     CREATE INDEX IF NOT EXISTS idx_games_popularity ON games(popularity_score DESC);
		     CREATE INDEX IF NOT EXISTS idx_games_pop_fetch ON games(popularity_fetched_at) WHERE popularity_fetched_at = 0;`,
	},
	{
		Version: 3,
		// GetStaleGames used a correlated subquery in its ORDER BY which forced
		// a full-table sort of the games table.  Adding an index on
		// source_updated_at lets SQLite satisfy the WHERE + ORDER BY in one
		// O(LIMIT) index scan with no sort step.
		Up: `CREATE INDEX IF NOT EXISTS idx_games_source_updated
		     ON games(source_updated_at);`,
	},
	{
		Version: 2,
		// cover_jobs had no index on next_attempt_at, causing the coordinator
		// goroutine to do a full table scan (potentially 50k+ rows) on every
		// poll iteration.  With SetMaxOpenConns(1) that single slow query
		// blocked every concurrent HTTP handler waiting for the DB connection.
		Up: `CREATE INDEX IF NOT EXISTS idx_cover_jobs_next_attempt
		     ON cover_jobs(next_attempt_at, attempts);`,
	},
	{
		Version: 1,
		Up: `PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT,
  display_name TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  google_subject TEXT UNIQUE,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS games (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  safe_name TEXT NOT NULL DEFAULT '',
  normalized_name TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  storyline TEXT NOT NULL DEFAULT '',
  cover_id INTEGER NOT NULL DEFAULT 0,
  cover_url TEXT NOT NULL DEFAULT '',
  local_cover_path TEXT NOT NULL DEFAULT '',
  first_release_date INTEGER NOT NULL DEFAULT 0,
  aggregated_rating INTEGER NOT NULL DEFAULT 0,
  aggregated_rating_count INTEGER NOT NULL DEFAULT 0,
  platforms_json TEXT NOT NULL DEFAULT '[]',
  genres_json TEXT NOT NULL DEFAULT '[]',
  trailer TEXT NOT NULL DEFAULT '',
  igdb_url TEXT NOT NULL DEFAULT '',
  source_updated_at INTEGER NOT NULL DEFAULT 0,
  imported_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS library_items (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('wishlist', 'backlog', 'playing', 'completed', 'abandoned')),
  rating INTEGER NOT NULL DEFAULT 0 CHECK (rating >= 0 AND rating <= 100),
  playtime_minutes INTEGER NOT NULL DEFAULT 0 CHECK (playtime_minutes >= 0),
  tags_json TEXT NOT NULL DEFAULT '[]',
  notes TEXT NOT NULL DEFAULT '',
  started_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, game_id)
);

CREATE TABLE IF NOT EXISTS igdb_query_cache (
  normalized_query TEXT PRIMARY KEY,
  response_json TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS igdb_sync_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS cover_jobs (
  game_id INTEGER PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
  source_url TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_games_normalized_name ON games(normalized_name);
CREATE INDEX IF NOT EXISTS idx_games_rating_release ON games(aggregated_rating_count, aggregated_rating, first_release_date);
CREATE INDEX IF NOT EXISTS idx_games_slug ON games(slug);
CREATE INDEX IF NOT EXISTS idx_library_user_status ON library_items(user_id, status);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);`,
	},
}

// Migrate runs pending migrations on the writer connection. All DDL/writes go
// through the single writer pool to avoid contending with the read pool.
func Migrate(database *DB) error {
	w := database.Write

	if err := createMigrationsTable(w); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	currentVersion, err := getCurrentVersion(w)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}

		if err := applyMigration(w, m); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.Version, err)
		}
	}

	return nil
}

func createMigrationsTable(database *sql.DB) error {
	_, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	return err
}

func getCurrentVersion(database *sql.DB) (int, error) {
	var version int
	err := database.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

func applyMigration(database *sql.DB, m Migration) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.Up); err != nil {
		return fmt.Errorf("exec migration SQL: %w", err)
	}

	if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.Version); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit()
}
