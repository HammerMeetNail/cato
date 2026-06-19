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

func Migrate(database *sql.DB) error {
	if err := createMigrationsTable(database); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	currentVersion, err := getCurrentVersion(database)
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

		if err := applyMigration(database, m); err != nil {
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
