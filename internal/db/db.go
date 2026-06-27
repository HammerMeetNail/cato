package db

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"

	_ "modernc.org/sqlite"
)

// DB bundles two connection pools against the same SQLite file:
//
//   - Read:  many concurrent connections. Safe under WAL because WAL allows
//     any number of readers concurrently with a single writer.
//   - Write: exactly one connection. SQLite permits only one writer at a time;
//     serializing writes in Go (MaxOpenConns(1)) avoids SQLITE_BUSY storms.
//
// DB also exposes the common database/sql method set so it is a near drop-in
// replacement for *sql.DB. Reads (Query*/QueryRow*) are routed to the read
// pool; writes (Exec*) and transactions (Begin*) are routed to the single
// writer. This means existing call sites that do db.Query(...) / db.Exec(...)
// keep working unchanged while getting correct read/write routing for free.
type DB struct {
	Read  *sql.DB
	Write *sql.DB
}

// dsn builds a modernc.org/sqlite DSN. IMPORTANT: modernc.org/sqlite does NOT
// understand the mattn/go-sqlite3 style params (_journal_mode=, _busy_timeout=,
// _foreign_keys=). It uses repeated _pragma=NAME(VALUE) params instead. The old
// DSN used the mattn syntax, so every PRAGMA was silently ignored and the DB
// ran in rollback-journal mode with no busy timeout — the root cause of the
// lock contention this change fixes.
func dsn(path string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"+
			"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)",
		path,
	)
}

func Open(path string) (*DB, error) {
	read, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	readConns := runtime.NumCPU()
	if readConns < 4 {
		readConns = 4
	}
	read.SetMaxOpenConns(readConns)

	write, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		read.Close()
		return nil, fmt.Errorf("open writer: %w", err)
	}
	write.SetMaxOpenConns(1)

	for _, d := range []*sql.DB{read, write} {
		if err := d.Ping(); err != nil {
			read.Close()
			write.Close()
			return nil, fmt.Errorf("ping sqlite: %w", err)
		}
	}

	return &DB{Read: read, Write: write}, nil
}

func (db *DB) Close() error {
	werr := db.Write.Close()
	rerr := db.Read.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

func (db *DB) Ping() error { return db.Read.Ping() }

// --- read routing ---

func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.Read.Query(query, args...)
}

func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.Read.QueryContext(ctx, query, args...)
}

func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	return db.Read.QueryRow(query, args...)
}

func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.Read.QueryRowContext(ctx, query, args...)
}

// --- write routing ---

func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	return db.Write.Exec(query, args...)
}

func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.Write.ExecContext(ctx, query, args...)
}

func (db *DB) Begin() (*sql.Tx, error) { return db.Write.Begin() }

func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return db.Write.BeginTx(ctx, opts)
}
