# AGENTS.md

## Build & Test

```bash
make test    # go test ./...
make build   # go build -o cato ./cmd/cato
make run     # build + start locally (background, writes .pid)
make stop    # kill background process
go vet ./...  # vet (no Makefile target, but clean and worth running)
```

There is no lint or formatter command defined. `go vet ./...` is expected to be clean.

## Architecture

Single Go module (`cato`), single SQLite database, vanilla JS frontend (no build step).

```
cmd/cato/        Main entrypoint. Also has an `import-games` subcommand (Postgres COPY import).
internal/auth/   Passwords (bcrypt cost 12), sessions (SHA-256 hashed in DB), CSRF, rate limiting (in-memory)
internal/config/ Env-based config (CATO_* prefix)
internal/covers/ Background cover downloader (IGDB images → data/covers/{id}.jpg) + /covers/ handler
internal/db/     SQLite open (two-pool), migration runner
internal/games/  Search, types, store, service, IGDB rate limiter. Defines IGDBClient interface.
internal/http/   HTTP server, handlers, middleware. All JSON responses via writeJSON/errResp.
internal/igdb/   IGDB API v4 client (Twitch OAuth token auth)
internal/importer/ Postgres COPY parser for game seed imports (opens its own *sql.DB)
web/static/      HTML, CSS, JS (vanilla, no bundler)
```

## Database (read this before touching `internal/db`)

- **Driver is `modernc.org/sqlite`** (pure Go, no cgo). It does **NOT** understand the
  mattn/go-sqlite3 DSN params (`_journal_mode=`, `_busy_timeout=`, `_foreign_keys=`) — it
  **silently ignores them**. Use the `_pragma=NAME(VALUE)` syntax instead. The current DSN
  sets `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`, `synchronous(NORMAL)`.
  Using the wrong syntax here previously left prod in rollback-journal mode and was the cause
  of severe lock contention — do not regress it.
- **Two-pool design**: `db.Open()` returns a `*db.DB{ Read, Write *sql.DB }`.
  - `Read` is a pool of several connections (WAL allows many concurrent readers).
  - `Write` is a single connection (`SetMaxOpenConns(1)` — SQLite allows only one writer).
  - `*db.DB` exposes proxy methods so it is a near drop-in for `*sql.DB`:
    `Query*/QueryRow*` route to the read pool; `Exec*` and `Begin*` route to the writer.
    **So most call sites just use `db.Query(...)` / `db.Exec(...)` unchanged** — the routing
    is automatic by method name. Put SELECTs through `Query*`, mutations through `Exec*`.
- **Migrations** are versioned in `internal/db/migrate.go` (`migrations` slice, highest-first
  in source, applied lowest-first). Add a new `{Version: N, Up: "..."}` entry; never edit an
  applied migration. `Migrate(*db.DB)` runs them on the writer.
- **Tests use real SQLite**: no DB mocks. Pattern: `db.Open(t.TempDir()+"/test.db")` →
  `db.Migrate(database)` → use. Helpers return `*db.DB`.

## Key Conventions

- **Session storage**: cookie holds the raw token; the DB stores its SHA-256 hash.
  `GetSession` restores the unhashed ID on the returned struct. (You can't forge a session
  from a DB row via curl — the row is the hash.)
- **CSRF**: unsafe methods (POST/PUT/DELETE) require `X-CSRF-Token` (from `GET /api/me`).
  Middleware order: `AuthRequired` then `CSRFRequired`. GET/HEAD/OPTIONS skip CSRF.
- **`auth.Querier`**: auth functions take the `auth.Querier` interface (Query/QueryRow/Exec),
  satisfied by both `*sql.DB` (used in `auth` tests) and `*db.DB` (production). Keep it that
  way so the auth package doesn't have to import `internal/db`.
- **Covers**:
  - On-disk files are `data/covers/{id}.jpg` (canonical). Some legacy `.webp` files exist; the
    serving + existence checks accept both.
  - `covers.ServeCover(coverDir)` does **no DB query and no redirect** — it serves the file
    from disk with `Cache-Control: public, max-age=31536000, immutable`, or an inline SVG
    placeholder with a short cache (`max-age=300`) on a miss. Keep the hot path DB-free.
  - The download worker only fetches covers for games that are in someone's **library**
    (`nextJob` INNER JOINs `library_items`); it does not crawl the whole catalog. A cover job
    is enqueued when a game is added to a library (`upsertLibraryItem`). Do not reintroduce a
    catalog-wide `EnqueueMissingCovers` at startup.
- **Library API is paginated**: `GET /api/library?status=&limit=&offset=` (default limit 60,
  max 200). The frontend (`web/static/js/library.js`) does infinite scroll and appends pages.
- **HTTP middleware**: `gzipMiddleware` (skips `/covers/`) wraps the whole mux via
  `Server.Handler()`; `staticCacheMiddleware` adds cache headers for `/js/`, `/css/`,
  `/favicon.svg`.
- **IGDB is optional**: when `IGDB_CLIENT_ID` is empty a `noopIGDBClient` is used; IGDB calls
  fall back to local results on error.
- **Rate limiting**: in-memory only (not shared across processes). `auth.RateLimiter` for
  login/signup; `games.IGDBRateLimiter` (~1 req/sec) for the IGDB API.
- **Env config fallbacks**: `IGDB_CLIENT_ID`→`TWITCH_OAUTH_ID`,
  `IGDB_CLIENT_SECRET`→`TWITCH_OAUTH_SECRET` (docker-compose sets both).
- **No codegen, no ORM, no frontend build step**. Raw SQL everywhere; vanilla JS.

## Deployment

Production runs in **Docker on a Synology NAS** (host alias `nas2`,
`/volume1/Shared/Mediapedia`, served at `http://10.0.0.42:7080`). The container bind-mounts
`/volume1/Shared/Mediapedia/data` → `/app/data` (DB + covers persist on the host).

```bash
make deploy        # cross-compile (linux/amd64, CGO disabled) + push binary/static + compose up --build
make deploy-full   # deploy, and also push the local data/cato.db (rarely wanted — clobbers prod data)
make deploy-logs   # tail container logs
```

`make deploy` runs the cross-compiled binary into a fresh image and recreates the container.
WAL conversion of an existing DB happens automatically on first open. Back up the prod DB
(`cp cato.db cato.db.bak-...`) before any manual DB surgery; `docker` and `sqlite3` are
available on `nas2` (docker at `/usr/local/bin/docker`, no sudo needed).
