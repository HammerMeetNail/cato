package db

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"time"

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

	// observe, when set, sees every proxied statement's duration (the
	// slow-query log and metrics). Statements on Read/Write directly and
	// inside transactions are not observed.
	observe func(query string, d time.Duration)
}

// SetObserver installs fn to receive each proxied statement's duration.
// Call it before serving traffic.
func (db *DB) SetObserver(fn func(query string, d time.Duration)) { db.observe = fn }

func (db *DB) track(query string, start time.Time) {
	if db.observe != nil {
		db.observe(query, time.Since(start))
	}
}

// dsn builds a modernc.org/sqlite DSN. IMPORTANT: modernc.org/sqlite does NOT
// understand the mattn/go-sqlite3 style params (_journal_mode=, _busy_timeout=,
// _foreign_keys=). It uses repeated _pragma=NAME(VALUE) params instead. The old
// DSN used the mattn syntax, so every PRAGMA was silently ignored and the DB
// ran in rollback-journal mode with no busy timeout — the root cause of the
// lock contention this change fixes.
//
// Per-connection tuning: a 16 MB page cache (cache_size is in KiB when
// negative), 128 MB of memory-mapped reads, and temp tables/sorts in memory.
//
// The writer also uses _txlock=immediate: every writer transaction takes the
// write lock at BEGIN, so a read-modify-write transaction can't fail late
// with SQLITE_BUSY_SNAPSHOT when another process wrote after its first read.
// Read-pool connections are query_only, so a write routed to them by mistake
// fails loudly instead of competing with the single writer.
func dsn(path string, writer bool) string {
	d := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"+
			"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"+
			"&_pragma=cache_size(-16000)&_pragma=mmap_size(134217728)&_pragma=temp_store(MEMORY)",
		path,
	)
	if writer {
		d += "&_txlock=immediate"
	} else {
		d += "&_pragma=query_only(1)"
	}
	return d
}

// Optimize keeps the query planner's statistics current as the catalog and
// libraries grow. PRAGMA optimize=0x10002 (SQLite 3.46+) analyzes every table
// whose statistics are missing or stale, bounded by analysis_limit samples per
// index, so it is cheap enough for startup, the daily sweep and shutdown.
func (db *DB) Optimize(ctx context.Context) error {
	conn, err := db.Write.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA analysis_limit=400"); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "PRAGMA optimize=0x10002")
	return err
}

func Open(path string) (*DB, error) {
	read, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	readConns := runtime.NumCPU()
	if readConns < 4 {
		readConns = 4
	}
	read.SetMaxOpenConns(readConns)

	write, err := sql.Open("sqlite", dsn(path, true))
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
	defer db.track(query, time.Now())
	return db.Read.Query(query, args...)
}

func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	defer db.track(query, time.Now())
	return db.Read.QueryContext(ctx, query, args...)
}

func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	defer db.track(query, time.Now())
	return db.Read.QueryRow(query, args...)
}

func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	defer db.track(query, time.Now())
	return db.Read.QueryRowContext(ctx, query, args...)
}

// --- write routing ---

func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	defer db.track(query, time.Now())
	return db.Write.Exec(query, args...)
}

func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	defer db.track(query, time.Now())
	return db.Write.ExecContext(ctx, query, args...)
}

func (db *DB) Begin() (*sql.Tx, error) { return db.Write.Begin() }

func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return db.Write.BeginTx(ctx, opts)
}
