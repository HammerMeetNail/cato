# Database (read before touching `internal/db` or writing SQL)

Cato uses one SQLite file (`data/cato.db`) through `modernc.org/sqlite`, a pure-Go driver with no cgo.

## Opening the database

**The DSN uses `_pragma=NAME(VALUE)`.** The driver silently ignores the mattn/go-sqlite3 parameters (`_journal_mode=`, `_busy_timeout=`, `_foreign_keys=`). That mistake once left production in rollback-journal mode with severe lock contention, so don't reintroduce it.

The DSN sets:

- `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`, `synchronous(NORMAL)`
- `cache_size(-16000)`, `mmap_size(134217728)`, `temp_store(MEMORY)`

Read connections add `query_only(1)`; the writer adds `_txlock=immediate`.

**`db.Open()` returns `*db.DB{Read, Write *sql.DB}`, two pools:**

- `Read` holds several `query_only` connections. A write sent there fails instead of competing with the writer.
- `Write` is one connection (`SetMaxOpenConns(1)`), and its transactions are `BEGIN IMMEDIATE`. Inside a writer transaction, use the `*sql.Tx` for every statement: calling `db.Exec` while holding the transaction deadlocks.
- `*db.DB` routes by method name: `Query*`/`QueryRow*` go to `Read`; `Exec*`/`Begin*` go to `Write`. So SELECTs go through `Query*` and mutations through `Exec*`.

`db.Optimize` (`PRAGMA optimize=0x10002`) runs in the daily `maintenance` job and at shutdown.

## Migrations

- Migrations live in `internal/db/migrate.go`, in the `migrations` slice: highest version first in the source, applied lowest first. They are currently at v23.
- Add a new `{Version: N, Up: "..."}` entry. **Never edit an applied migration.**
- Before a schema upgrade, `serve` snapshots the database with `VACUUM INTO` into `CATO_BACKUP_DIR` (default `data/backups`, newest 5 kept).
- Migration history:
  - v18: the journal (below).
  - v19: devices.
  - v20: rich catalog.
  - v21: people.
  - v22: reach (imports, push, passkeys…).
  - v23: goals.

## The library journal (v18)

- `library_items.playtime_minutes` always equals the sum of the item's `play_sessions`. Triggers keep it so.
  - Adjustments are `source='adjust'` sessions; legacy totals are one `source='legacy'` session.
  - Steam sync writes `steam` sessions and imports write `import` sessions.
- Status changes open and close `playthroughs` in `internal/library`. Only one playthrough is open at a time, and it is listed first.
- Every change to a library row, or to a game's name, cover or release date, bumps the owner's `user_state.library_rev` and stamps `library_items.rev`. Deletions leave `library_tombstones`. The app's delta sync relies on this.
- Triggers must not use `INSERT OR IGNORE/REPLACE`: an outer UPSERT's conflict handling overrides the trigger's.
- Rebuilding `library_items` again needs `foreign_keys=OFF`, because DROP TABLE would cascade into the journal.

## Writing library data

- **All library writes go through `library.Store`.** Each write is:
  - one IMMEDIATE transaction;
  - journal bookkeeping;
  - an undo snapshot in `undo_log` (15 minutes);
  - feed `activity` rows carrying the write's `undo_id`, so an undo removes them too.

  Never write `library_items` from handlers.
- **Dates are the person's local day.** API writes carry `played_on` (`YYYY-MM-DD`, from the client); the server's UTC day is only a fallback. Mixing the two can put a playthrough on the wrong day.
- **Imports** use `library.Store.Import`: one undoable `import_batches` row, idempotent, which never overwrites recorded data and never writes `activity`.

## Tests

Tests use real SQLite and no database mocks: `db.Open(t.TempDir()+"/test.db")`, then `db.Migrate(database)`, then use it. `internal/library/fixture_test.go` builds a production-shaped catalog (300k games, a 5k-item library) with timing gates; on slow machines `CATO_PERF_SLACK=2` loosens the gates.

## Production data

- Back up with SQLite `.backup` (`sqlite3 data/cato.db ".backup backup/x.db"`). Never copy a live WAL database.
- Never write to a database from the host while a server has it open.
- Restore procedures: `docs/production-runbook.md`.
