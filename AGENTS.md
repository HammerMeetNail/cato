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
  `/icons/`, `/favicon.svg`, `/manifest*` (`no-cache`), and `no-store` for
  `/service-worker.js`.
- **IGDB is optional**: when `IGDB_CLIENT_ID` is empty a `noopIGDBClient` is used; IGDB calls
  fall back to local results on error.
- **Rate limiting**: in-memory only (not shared across processes). `auth.RateLimiter` for
  login/signup; `games.IGDBRateLimiter` (~1 req/sec) for the IGDB API.
- **PWA shell & bottom navigation (Nabu parity)**: `web/static/index.html` is the only
  authenticated page. It uses a `height: var(--app-h)` flex-column shell (see
  `web/static/js/head-init.js` for iOS standalone `screen.height` vs `innerHeight`
  logic and `--safe-bottom` for notches) with a static `#bottom-tabs` bar at the
  body level (not `fixed` — avoids iOS PWA cold-open gap). The bar has 4 tabs:
  **Library** (`#`, grid icon), **Playing** (`#now`, play icon), **Stats**
  (`#stats`, bar-chart), **Settings** (`#settings`, gear), each
  `a.tab-item[data-route]` with `aria-current="page"` on active, styled via
  `.tab-item.active`. `handleRoute()` dispatches on `window.location.hash`:
  `#now` (aliases `#now-playing`/`#playing-now`) → `renderPlayingView()`
  (`web/static/js/playing.js`), `#stats` → `renderStatsView()`
  (`web/static/js/stats.js`), `#settings` → `renderSettingsView()`
  (`web/static/js/settings.js`), `#library`/status tabs/`#search/<q>`/
  `#game/<id>` modal → Library, else Playing (empty hash is home). Playing/Stats/Settings data is lazy-fetched when the tab
  becomes active; old `/settings` redirects to `/#settings`. `GET /api/me`,
  search, and hash-back navigation must keep working across tabs; don't fold
  `/login` into the shell. `#library` is the Library canonical URL (empty hash
  is the Playing tab's home — `/#` renders Playing, not the grid). The
  old in-library "Playing now" hero strip has been removed in favor of the
  dedicated Playing tab.
- **PWA installability**: `web/static/manifest.webmanifest` (`name: Cato — Game
  Library`, `display: standalone`, `theme_color: #1a1a2e`, icons in
  `web/static/icons/*`, shortcuts to Library/Stats/Search), `web/static/offline.html`
  (dark, shows when `fetch` for a navigation fails), `web/static/service-worker.js`
  (`CACHE_NAME=cato-static-v3`, pre-caches `css/app.css`, `js/*`, manifest,
  icons, offline; cache-first for `/css/ /js/ /icons/ /covers/`, navigate
  fallback to `offline.html`), and `web/static/js/head-init.js` (sets `--app-h`
  before first paint). `login.html` also links the manifest and registers the SW.
  `staticCacheMiddleware` sends `no-cache` for `/js/ /css/ /icons/ /manifest*`,
  and `no-store` for `/service-worker.js`. Keep all four files and the
  `head-init.js` `<script>` in `<head>` — removing them breaks install/offline.
- **Accessibility**: bottom tabs have `aria-label`, `aria-current`,
  `role="tablist"/tab` on status filters, `aria-live="polite"` on `#mainContainer`
  and `#statsStrip`, skip-link `href="#mainContainer"`, `:focus-visible` outlines,
  `@media (prefers-reduced-motion: reduce)` disables animations,
  `viewport-fit=cover` + `safe-area-inset-*` for notches, and keyboard shortcuts
  `1/2/3/4` for Library/Playing/Stats/Settings. Preserve these.
- **Env config fallbacks**: `IGDB_CLIENT_ID`→`TWITCH_OAUTH_ID`,
  `IGDB_CLIENT_SECRET`→`TWITCH_OAUTH_SECRET` (docker-compose sets both).
- **No codegen, no ORM, no frontend build step**. Raw SQL everywhere; vanilla JS.

## Deployment

Production runs in **Docker on a Synology NAS** (host alias `nas2`,
`/volume1/Shared/Cato`, served at `http://10.0.0.42:7080`). The container bind-mounts
`/volume1/Shared/Cato/data` → `/app/data` (DB + covers persist on the host).

```bash
make deploy        # cross-compile (linux/amd64, CGO disabled) + push binary/static + compose up --build
make deploy-full   # deploy, and also push the local data/cato.db (rarely wanted — clobbers prod data)
make deploy-logs   # tail container logs
```

`make deploy` runs the cross-compiled binary into a fresh image and recreates the container.
When changing any shipped asset under `web/static`, increment the versioned
`CACHE_NAME` in `web/static/service-worker.js` before deploying (for example,
`cato-static-v8` to `cato-static-v9`). The new service worker purges the old
cache on activation; an already-open client may need one reload to activate it.
WAL conversion of an existing DB happens automatically on first open. Back up the prod DB
(`cp cato.db cato.db.bak-...`) before any manual DB surgery; `docker` and `sqlite3` are
available on `nas2` (docker at `/usr/local/bin/docker`, no sudo needed).
