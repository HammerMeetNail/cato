# Cato performance remediation plan

Status: **diagnosis complete, awaiting review before implementation.**
Author: Claude (Opus 4.8), 2026-06-27. Verified live against production at
`http://10.0.0.42:7080` and the production DB on `nas2`
(`/volume1/Shared/Mediapedia/data/cato.db`).

---

## 1. What I measured (the evidence)

Production scale:

| table          | rows      |
|----------------|-----------|
| games          | 311,901   |
| library_items  | 1,585 (one user has **1,525**) |
| cover_jobs     | **292,207** pending (291,275 with attempts<5) |
| sessions       | 9         |

Covers on disk: **76,642 files, 1.6 GB**, all named `N.jpg`. DB size: 304 MB.

Live latency (measured with `curl` against the running server):

- `GET /api/me` — this is a **single primary-key SELECT against a 9-row table**.
  It returned in **0.6 s, 0.9 s, 2.1 s** on repeated calls (and ~0.01 s when it
  got lucky). A 9-row indexed lookup should be **sub-millisecond**.
- `GET /api/library` — same story, 0.7 s–2.1 s just to reach the handler.
- `GET /covers/<id>.webp` for a cover not on disk — **1.74 s** (it does a DB
  query then 302-redirects to the IGDB CDN).
- `GET /covers/1.jpg` for a cover that *is* on disk — **0.03 s** (fast; no DB).
- `PRAGMA journal_mode` on the live DB returns **`delete`** (rollback journal),
  **not** `wal`.
- `cover_jobs` count dropped 292,111 → 292,109 in 4 s ≈ **0.5 jobs/sec**. At that
  rate the worker will keep writing to the DB **continuously for ~1 week** to
  drain the backlog.

The pattern is unambiguous: trivial reads intermittently take 0.5–2 s because
they are **queued behind a writer that holds an exclusive lock on the whole
database**, and that writer (the cover worker) **never stops**.

---

## 2. Root causes (in priority order)

### RC-1 — DB PRAGMAs are silently ignored; DB runs in rollback-journal mode on a single connection  *(CRITICAL)*

`internal/db/db.go` opens the DB with:

```go
dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", path)
database, _ := sql.Open("sqlite", dsn)      // driver = modernc.org/sqlite
...
database.SetMaxOpenConns(1)
```

Those `_journal_mode` / `_busy_timeout` / `_foreign_keys` query params are the
**`mattn/go-sqlite3` DSN syntax**. This project uses **`modernc.org/sqlite`**
(`go.mod` line 9), which **does not understand them** — it uses a `_pragma=...`
syntax instead. So **none of the three PRAGMAs are applied**. Proof: the live DB
reports `journal_mode=delete` and there is a `cato.db-journal` file on disk
(WAL would create `-wal`/`-shm`).

Consequences, all confirmed live:

1. **Rollback-journal mode**: every write takes an EXCLUSIVE lock on the entire
   database and blocks *all* readers for the duration of the write.
2. **No busy timeout**: a reader that hits a locked DB gets `SQLITE_BUSY`
   *immediately* — it doesn't even wait. Combined with `GetSession` returning
   `err != nil` → the user gets a spurious **401 "Invalid or expired session"**.
   (This is almost certainly why the app feels like it randomly logs people out.)
3. `SetMaxOpenConns(1)` means there is exactly **one** connection for the whole
   process — the cover worker and every HTTP handler fight over it.

Every "perf" commit so far (splitting queries to avoid temp-sort B-trees, adding
indexes) was treating *symptoms* of this single shared, lock-contended
connection. The indexes are fine and worth keeping, but they are not the cause.

### RC-2 — The cover worker is downloading the entire 311k-game IGDB catalog, not just library covers  *(CRITICAL)*

There are **291k pending cover jobs** — roughly one per game in the catalog, even
though only ~1,585 games are in anyone's library. `covers.nextJob()`
(`internal/covers/covers.go:107`) first tries library-linked jobs, but when those
run out it falls through to:

```sql
SELECT game_id, source_url FROM cover_jobs WHERE attempts < 5 AND next_attempt_at <= ? LIMIT 1
```

i.e. it grabs **any** job and downloads covers for the whole catalog forever. Each
claimed job does an `UPDATE cover_jobs SET next_attempt_at=...`, and each finished
download does `DELETE FROM cover_jobs` + `UPDATE games SET local_cover_path=...`.
In rollback-journal mode each of those write statements locks the whole DB. The
worker loop runs every ~100 ms indefinitely. **This is the engine that keeps the
DB perpetually write-locked.**

### RC-3 — Cover serving has a `.jpg` vs `.webp` mismatch and a slow DB-backed fallback  *(HIGH)*

- The 76,642 already-downloaded covers are named `N.jpg`. New code
  (`covers.go`) writes `N.webp` and sets `local_cover_path = /covers/N.webp`.
- When the browser requests `/covers/N.webp` but only `N.jpg` exists, `ServeCover`
  misses on disk, **runs a DB query** (`SELECT cover_url FROM games WHERE id=?`)
  and **302-redirects to the IGDB CDN**. That DB query blocks on the lock →
  measured **1.74 s per missing cover**. On the "All" tab that is multiplied
  across hundreds of images.
- Cover cache headers are weak: `max-age=86400` (1 day) for on-disk hits,
  `max-age=3600` (1 hour) for redirects. The user wants covers cached "for quite
  some time."

### RC-4 — The "All" tab returns all 1,525 items at once and fires 1,525 image requests  *(HIGH)*

`listLibrary` (`internal/http/handler_library.go:60`) has no `LIMIT`/pagination.
For the main user it returns 1,525 rows in one JSON blob, and the front end
renders 1,525 `<img>` tags. Even with `loading="lazy"`, the browser opens a large
number of connections to `/covers/...`, each of which (today) may do a
DB-querying redirect. Once RC-1–RC-3 are fixed this is far less painful, but
pagination/windowing is still the right call for instant loads.

### Secondary

- **S-1**: Static JS/CSS are served with **no `Cache-Control`** (only
  `Last-Modified` → a 304 round-trip every load). Cheap win.
- **S-2**: No gzip/Brotli on JSON responses. The 1,525-item library payload is
  large; compression helps.
- **S-3**: `EnqueueMissingCovers()` and `StartStaleRefresh()` exist in
  `service.go` but are **not wired into `main.go`** — so whatever enqueued the
  291k jobs was a previous build or a manual run. Worth confirming nothing
  re-enqueues the whole catalog after we purge it.

---

## 3. The fix

Two changes (RC-1 and RC-2) account for essentially all of the user-visible
slowness. Do them first; verify; then do the polish.

### Step 1 — Fix the SQLite connection (RC-1)  ⟵ do this first

**File: `internal/db/db.go`.** Replace `Open` with a version that (a) applies the
PRAGMAs in a way modernc actually honors, and (b) uses a small read pool plus a
single dedicated writer.

Recommended bold version — **two handles** (the idiomatic SQLite-in-Go pattern):

```go
package db

import (
	"database/sql"
	"fmt"
	"runtime"

	_ "modernc.org/sqlite"
)

// DB bundles a read pool (many concurrent readers, safe under WAL) and a single
// writer connection (SQLite allows only one writer at a time; serializing in Go
// avoids SQLITE_BUSY storms).
type DB struct {
	Read  *sql.DB
	Write *sql.DB
}

// modernc.org/sqlite honors PRAGMAs via repeated _pragma= params, NOT the
// mattn-style _journal_mode=/_busy_timeout= params used before (which were
// silently ignored, leaving the DB in rollback-journal mode with no busy
// timeout — the root cause of the lock contention).
func dsn(path string, extraPragmas ...string) string {
	base := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"+
		"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", path)
	for _, p := range extraPragmas {
		base += "&_pragma=" + p
	}
	return base
}

func Open(path string) (*DB, error) {
	read, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	read.SetMaxOpenConns(max(4, runtime.NumCPU()))

	write, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	write.SetMaxOpenConns(1) // SQLite: exactly one writer

	for _, d := range []*sql.DB{read, write} {
		if err := d.Ping(); err != nil {
			return nil, fmt.Errorf("ping sqlite: %w", err)
		}
	}
	return &DB{Read: read, Write: write}, nil
}

func max(a, b int) int { if a > b { return a }; return b }
```

Then thread `Read`/`Write` through the call sites: handlers that only `SELECT`
use `db.Read`; anything doing `INSERT/UPDATE/DELETE` uses `db.Write`. The cover
worker uses `db.Write` for its writes and `db.Read` for its `nextJob` SELECT.

**Minimum-effort alternative** if the two-handle refactor is too invasive for one
pass: keep the single `*sql.DB` but (1) build the DSN with the `_pragma=` syntax
above, and (2) raise `SetMaxOpenConns` to `max(4, NumCPU())`. WAL + a real
busy_timeout alone removes >90% of the contention because readers stop blocking
on the writer. Start here if needed; the two-handle split is the durable fix.

**One-time migration on the existing DB.** WAL mode is persistent once set, but
the current file is in `delete` mode. After deploying, confirm it flipped (see
verification). If it doesn't auto-convert, run once:
`sqlite3 /app/data/cato.db "PRAGMA journal_mode=WAL;"` while the app is stopped.

### Step 2 — Stop the catalog-wide cover download; only cache library covers (RC-2)

1. **Purge the non-library backlog** (one-time, app stopped). On `nas2`:

   ```sh
   D=/usr/local/bin/docker
   $D stop cato
   sqlite3 /volume1/Shared/Mediapedia/data/cato.db \
     "DELETE FROM cover_jobs WHERE game_id NOT IN (SELECT DISTINCT game_id FROM library_items);"
   sqlite3 /volume1/Shared/Mediapedia/data/cato.db "VACUUM;"
   $D start cato
   ```

   This drops ~291k jobs down to ~1.5k. (Keep a DB backup first: `cp cato.db cato.db.bak`.)

2. **Change the worker so it only ever works on library-linked jobs.** In
   `internal/covers/covers.go`, delete the "Step 2 / grab any pending job"
   fallback in `nextJob()` — keep only the INNER JOIN against `library_items`.
   When there are no library jobs, the worker should idle (the existing
   `time.Sleep(500ms)` on `gameID == 0` already does this).

3. **Only enqueue covers for games that enter a library.** The right trigger is
   "when a `library_items` row is upserted, enqueue that game's cover." Add an
   `EnqueueCoverJob(writeDB, gameID, coverURL)` call inside
   `upsertLibraryItem` (`handler_library.go`) after a successful insert. Make
   sure nothing calls `EnqueueMissingCoverJobs()` (catalog-wide) at startup —
   confirm `main.go` does not wire `EnqueueMissingCovers()` (today it doesn't).

Result: the worker has ~1.5k covers to fetch once, then goes idle. The DB stops
being perpetually write-locked.

### Step 3 — Make cover serving instant and long-cached (RC-3)

In `internal/covers/covers.go`:

1. **Serve whichever file exists, prefer existing.** In `ServeCover`, when the
   requested name is `N.webp` but only `N.jpg` exists on disk (or vice-versa),
   serve the file that exists instead of falling through to the DB/redirect path.
   Simplest: parse the game ID, then check `N.webp` then `N.jpg` on disk and
   serve the first hit.
2. **Long cache for real covers**: set
   `Cache-Control: public, max-age=31536000, immutable` on disk hits. Covers
   effectively never change, and the URL is keyed by immutable game ID. (A year
   is the HTTP max practical value; `immutable` stops revalidation entirely.)
3. **No DB query in the hot path on a miss.** Replace the
   `SELECT cover_url ... 302 redirect` with serving the inline SVG placeholder
   directly (`servePlaceholder`) and a *short* cache (e.g. `max-age=300`) so the
   browser retries soon after the worker downloads the real cover. This removes
   the 1.74 s DB-backed redirect entirely.
4. **Stop emitting `.webp` `local_cover_path` for files saved as `.jpg`.** Pick
   one canonical extension. Given 76k existing `.jpg` files, the lowest-risk
   choice is: keep writing/serving the actual saved extension and store that
   exact path. (The WebP download optimization can stay for *new* downloads, but
   `publicCoverPath`/`local_cover_path` must match the file actually written.)

Optionally normalize `local_cover_path` in the DB once so it matches on-disk
files (a one-time UPDATE), but Step 3.1 makes the app robust regardless.

### Step 4 — Paginate / window the library (RC-4)

Once Steps 1–3 land, re-measure. If "All" with 1,525 items still isn't instant:

- Add `LIMIT`/`OFFSET` (or keyset pagination on `updated_at, game_id`) to
  `listLibrary`, e.g. `?limit=60&offset=0`, and add infinite-scroll / "load
  more" in `library.js`. Page size ~60 covers a couple of viewport-heights.
- The grid already uses `loading="lazy"` + `fetchpriority`, which is enough once
  covers are on-disk and long-cached.

### Step 5 — Cheap polish (S-1, S-2)

- Add `Cache-Control: public, max-age=3600` (or versioned filenames +
  `immutable`) for `/js`, `/css`, `/favicon.svg` by wrapping the static
  `FileServer` in `server.go`.
- Add gzip to JSON responses (a small `gzipMiddleware`, or `gziphandler`), which
  shrinks the library payload substantially.

---

## 4. Suggested order & rollout

1. **Step 1** (DB PRAGMAs + connection pool). Deploy. Verify WAL is on and
   `/api/me` latency drops to single-digit ms.
2. **Step 2** (purge backlog + restrict worker). Deploy. Verify `cover_jobs`
   stays small and the worker idles.
3. **Step 3** (cover serving). Deploy. Verify covers are 200-from-disk with a
   1-year cache and zero redirects.
4. Re-measure the "All" tab end-to-end. Do **Step 4** only if still needed.
5. **Step 5** polish.

Steps 1 and 2 are the ones that unblock production. They are small and
independent.

---

## 5. How to verify each step (copy-paste)

```sh
SID=...                         # a real browser cookie value, or just watch /api/me latency
HOST=http://10.0.0.42:7080

# WAL actually on now? (expect: wal)
ssh nas2 'sqlite3 "file:/volume1/Shared/Mediapedia/data/cato.db?mode=ro" "PRAGMA journal_mode;"'

# Auth/read latency — expect < 0.02s consistently after Step 1
for i in $(seq 5); do curl -s -o /dev/null -w "t=%{time_total}s\n" $HOST/api/me; done

# Cover backlog stays small after Step 2
ssh nas2 'sqlite3 "file:/volume1/Shared/Mediapedia/data/cato.db?mode=ro" "SELECT COUNT(*) FROM cover_jobs;"'

# Cover serves from disk with long cache, no redirect, after Step 3
curl -s -D - -o /dev/null $HOST/covers/1.jpg | grep -i 'cache\|HTTP'   # expect 200 + max-age=31536000

# Final: time a full library load in the browser devtools Network tab (DOMContentLoaded
# and last cover settle). Target: covers + grid visible "instantly" (<1s warm cache).
```

Acceptance criteria (from the brief):
- All tabs load instantly (sub-second on a warm cache; ≤ ~1 s cold on LAN).
- Covers load instantly and stay cached for a long time (1-year immutable).
- No spurious 401s under cover-worker load.

---

## 6. Notes / open questions for review

- **Two-handle DB split vs. single-pool-WAL**: I recommend the two-handle split
  as the durable fix, but the single-pool variant is a valid smaller first step.
  Which do you want for the first pass?
- **Pagination UX**: infinite-scroll vs. an explicit "Load more" button — your
  preference?
- **Keeping the catalog covers**: we have 1.6 GB / 76k covers already downloaded
  for non-library games. I propose we *keep the files* (harmless) but stop
  enqueuing/downloading more. Fine to leave them, or do you want them pruned to
  reclaim space?
- **`.jpg`/`.webp`**: lowest-risk path keeps existing `.jpg` files as-is and
  serves whatever exists. A full re-encode to WebP is possible later but not
  needed for performance.
```
