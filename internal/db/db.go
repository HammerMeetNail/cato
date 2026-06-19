package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=on", path)

	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := database.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	_, err = database.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	database.SetMaxOpenConns(1)

	return database, nil
}
