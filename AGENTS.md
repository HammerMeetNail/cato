# AGENTS.md

## Build & Test

```bash
make test    # go test ./...
make build   # go build -o cato ./cmd/cato
make run     # build + start locally over HTTPS (CATO_DEV_TLS=1; background, writes .pid)
make stop    # kill background process
make vet     # go vet ./...
make release-check # unit/race, vet, builds, JS, backups, security, desktop/mobile E2E
```

`make fmt-check` fails on unformatted Go files (part of `make release-check`).
`make vet` runs `go vet ./...` and is expected to be clean. Go 1.26.9 is selected by go.mod. Release checks also need Node 24+,
Python 3.10+, sqlite3 and Playwright Chromium/WebKit dependencies; see
`docs/production-runbook.md`. Keep generated logs/traces outside the repository.

## Architecture

Single Go module (`cato`), single SQLite database, vanilla JS frontend (no build step).

```
cmd/cato/        Entrypoint: run(args) with subcommands serve (default), dev-cert, import-games,
                 backfill <aliases|popularity|covers|editions|categories>.
internal/auth/   Passwords (bcrypt cost 12), sessions (SHA-256 hashed in DB), CSRF, rate limiting (in-memory)
internal/config/ Env-based config (CATO_* prefix)
internal/covers/ Background cover downloader (IGDB images → data/covers/{id}.jpg) + /covers/ handler
internal/devtls/ Self-signed dev certificates (crypto/x509) + SPKI pins; `cato dev-cert --out DIR`
internal/db/     SQLite open (two-pool), migration runner, pre-migration snapshots
internal/catalog/ Game search, store, service, IGDB rate limiter (two lanes), backfills, catalog jobs.
internal/library/ The library domain: typed items, filters/sorts, writes with the journal
                 (playthroughs, sessions), undo, compact index, bulk, timer, stats, prefs, export.
internal/jobs/   Background job runner (persisted schedule, jitter, cancellation).
internal/metrics/ In-process counters/histograms in Prometheus text format.
internal/http/   HTTP server, legacy /api handlers, middleware, static/asset versioning.
internal/http/v1/ /api/v1 handlers (docs/api-v1.md): thin, over internal/library and catalog.
internal/integrations/igdb/ IGDB API v4 client (Twitch OAuth token auth)
internal/importer/ Postgres COPY parser for game seed imports (opens its own *sql.DB)
web/static/      HTML, CSS, JS (vanilla ES modules, no bundler; lit-html vendored)
tests/js/        node --test suites for the pure frontend modules
```

## Database (read this before touching `internal/db`)

- **Driver is `modernc.org/sqlite`** (pure Go, no cgo). It does **NOT** understand the
  mattn/go-sqlite3 DSN params (`_journal_mode=`, `_busy_timeout=`, `_foreign_keys=`) — it
  **silently ignores them**. Use the `_pragma=NAME(VALUE)` syntax instead. The current DSN
  sets `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`, `synchronous(NORMAL)`,
  `cache_size(-16000)`, `mmap_size(134217728)` and `temp_store(MEMORY)`; read connections add
  `query_only(1)` and the writer `_txlock=immediate`.
  Using the wrong syntax here previously left prod in rollback-journal mode and was the cause
  of severe lock contention — do not regress it.
- **Two-pool design**: `db.Open()` returns a `*db.DB{ Read, Write *sql.DB }`.
  - `Read` is a pool of several connections (WAL allows many concurrent readers). They are
    `query_only`: a write routed there fails instead of competing with the writer.
  - `Write` is a single connection (`SetMaxOpenConns(1)` — SQLite allows only one writer).
    Its transactions are `BEGIN IMMEDIATE`. Inside a writer transaction use the `*sql.Tx` for
    every statement: calling `db.Exec` while holding it deadlocks on the single connection.
  - `db.Optimize` (`PRAGMA optimize=0x10002`, bounded by `analysis_limit`) runs in the daily
    maintenance sweep and at shutdown.
  - `*db.DB` exposes proxy methods so it is a near drop-in for `*sql.DB`:
    `Query*/QueryRow*` route to the read pool; `Exec*` and `Begin*` route to the writer.
    **So most call sites just use `db.Query(...)` / `db.Exec(...)` unchanged** — the routing
    is automatic by method name. Put SELECTs through `Query*`, mutations through `Exec*`.
- **Migrations** are versioned in `internal/db/migrate.go` (`migrations` slice, highest-first
  in source, applied lowest-first). Add a new `{Version: N, Up: "..."}` entry; never edit an
  applied migration. `Migrate(*db.DB)` runs them on the writer.
- **Journal (v18)**: `library_items.playtime_minutes` always equals the sum of the item's
  `play_sessions` (triggers keep it so; adjustments are `source='adjust'` sessions, legacy
  totals one `source='legacy'` session). Status changes open/close `playthroughs` in
  `internal/library`. Every change to a library row (and to a game's name/cover/release date)
  bumps the owner's `user_state.library_rev` and stamps `library_items.rev`; deletions leave
  `library_tombstones`. Triggers must not use `INSERT OR IGNORE/REPLACE`: an outer UPSERT's
  conflict handling overrides the trigger's. Rebuilding `library_items` again needs
  `foreign_keys=OFF` (DROP TABLE would cascade into the journal).
- **Library writes** go through `library.Store` (one IMMEDIATE transaction, journal
  bookkeeping, an undo snapshot in `undo_log`). Don't write `library_items` from handlers.
- **Before a schema upgrade** `serve` snapshots the database with `VACUUM INTO`
  (`CATO_BACKUP_DIR`, default `data/backups`, newest 5 kept).
- **Tests use real SQLite**: no DB mocks. Pattern: `db.Open(t.TempDir()+"/test.db")` →
  `db.Migrate(database)` → use. Helpers return `*db.DB`.

## Key Conventions

- **HTTPS only** (docs/cato_smooth.md §2.4). `config.Validate()` refuses to start unless
  Cato serves TLS (`CATO_TLS_CERT_FILE`/`CATO_TLS_KEY_FILE`, or `CATO_DEV_TLS=1` which
  generates a cert in `data/dev-tls/`) or `CATO_BASE_URL` is `https://` behind a TLS proxy.
  `CATO_ALLOW_INSECURE=1` is for tests only. `cfg.HTTPS()` switches on `__Host-` cookies,
  HSTS (not for dev certs), and CSP `upgrade-insecure-requests`. `Server.Listen()` binds
  (TLS 1.2+, HTTP/2, cert files reloaded on change) plus the optional
  `CATO_HTTP_REDIRECT_ADDR` 308 listener. Forwarded headers count only from
  `CATO_TRUSTED_PROXIES`: use `auth.ClientIP(r)` / `auth.RequestIsHTTPS(r)`, never
  `RemoteAddr` or raw `X-Forwarded-*`. Go tests that build a `config.Config` directly
  don't call `Validate`, so they can stay on plain HTTP.
- **Session storage**: cookie holds the raw token; the DB stores its SHA-256 hash.
  Always go through `auth.Cookies` (`SessionID`, `SetSession`, `ClearSession`,
  OAuth state): the name is `__Host-cato_session` over HTTPS and `cato_session` only
  in insecure test mode; the unprefixed name is ignored over HTTPS.
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
    is enqueued when a game is added to a library (`upsertLibraryItem`), and
    `games.Store.EnqueueCoverJob` is a no-op for games in nobody's library (so search and
    refresh results never queue jobs). Library writes call `Worker.Wake()`; the idle worker
    otherwise re-checks every 30 s. `covers.PruneJobs` drops orphaned jobs in the daily
    sweep. Do not reintroduce a catalog-wide `EnqueueMissingCovers` at startup.
- **API v1** (`internal/http/v1`, docs/api-v1.md) is what the app uses: Go 1.22 method
  patterns, `{items, next_cursor, total}` collections, `{error, message, fields}` errors,
  mutations return the item + `library_rev` + `undo_id`, ETags on every GET (library ones from
  `library_rev`), and a per-account write rate limit. The legacy `/api/library*` routes below
  remain until the old frontend is gone; their writes also go through `library.Store`.
- **Background work** runs on `internal/jobs` (registered in `cmd/cato/main.go`: catalog
  jobs from `catalog.Service.Jobs`, the daily `maintenance` sweep). `/healthz` reports the
  most overdue job. Don't start ad-hoc goroutine loops. IGDB requests pass the two-lane
  limiter (4 req/s): tag user-facing calls with `catalog.WithLane(ctx, LaneInteractive)`.
- **Observability**: slog (`CATO_LOG_FORMAT=text|json`, `CATO_LOG_LEVEL`); one request log
  line per request (request ID, hashed user ID, route pattern, status, duration); statements
  over 50 ms are logged as slow; `/metrics` (Prometheus text) needs `CATO_METRICS_TOKEN`
  as a bearer token, or a loopback/private-network client when unset.
- **Library API is paginated**: `GET /api/library?status=&limit=&offset=` (default limit 60,
  max 200). The default order (`updated`) pages by keyset: follow the opaque `X-Next-Cursor`
  header with `?cursor=` (index-backed, stable when edits reorder items); other sorts use
  `offset`. `X-Total-Count` is only sent with the first page. The frontend
  (`web/static/js/library.js`) does infinite scroll and appends pages.
- **Asset versioning is automatic** (internal/http/assets.go): the server hashes every
  file under `web/static`, rewrites local `src`/`href` in HTML to `?v=<hash>` (served
  `immutable`), injects an import map pinning every `/js/**.js` module plus modulepreload
  hints for the entry module's static imports at the `<!-- assets -->` marker, allows that
  import map in the CSP by hash, stamps `__CATO_ASSET_VERSION__` into the service worker and
  serves its precache list at `/asset-manifest.json`. There is no `CACHE_NAME` to bump. Keep
  the marker in `<head>` of every app page, and import modules with relative or absolute
  `/js/...` paths (both are mapped).
- **Frontend guardrails**: `make test-js` runs `scripts/lint-js.cjs` (no `innerHTML`,
  `insertAdjacentHTML`, `document.write`, `unsafeHTML`, `eval` outside `js/vendor/` and the
  legacy screens it lists), `node --test tests/js`, and the service worker regression.
  Vendored files change only via `scripts/vendor-update.sh`; a Go test checks them against
  `js/vendor/CHECKSUMS`.
- **HTTP middleware**: `gzipMiddleware` wraps the mux via `Server.Handler()` but only
  compresses `/api/` JSON. Static text assets (`.js .css .html .svg .json .webmanifest`)
  are served gzipped by `gzipStatic` (compressed once per file version, kept in memory,
  served with `http.ServeContent` so Range/ETag/304 stay correct); covers are never
  recompressed. Catalog JSON (`/api/games/{id}`, `/api/platforms`) uses
  `writeCacheableJSON` (weak ETag + short public max-age); everything else under `/api/`
  is `no-store`. `staticCacheMiddleware` adds cache headers for `/js/`, `/css/`,
  `/icons/`, `/favicon.svg`, `/manifest*` (`no-cache`), and `no-store` for
  `/service-worker.js`. `securityMiddleware` sets `nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy`, `Permissions-Policy` (camera self-only, other sensors off), HSTS in
  HTTPS mode, and a CSP with `script-src 'self'` — so shipped HTML must not use
  inline `<script>`, inline event handlers (`onerror=`), or `javascript:` URLs; cover
  images fall back via delegated `error` listeners + `data-cover-fallback`, and the
  theme/login bootstrap scripts are external (`js/theme-init.js`, `js/login.js`).
- **IGDB is optional**: when `IGDB_CLIENT_ID` is empty a `noopIGDBClient` is used; IGDB calls
  fall back to local results on error.
- **Rate limiting**: in-memory only (not shared across processes). `auth.RateLimiter`
  (per IP, idle keys evicted each window) for login/signup/password and
  `/api/games/search`; `auth.LoginThrottle` for per-account login backoff;
  `games.IGDBRateLimiter` (~1 req/sec) for the IGDB API. Anonymous searches run with
  `games.LocalOnly(ctx)` and never refresh from IGDB.
- **Signup**: `CATO_SIGNUP` (`open|invite|closed`, default `closed`). Account creation goes
  through `AuthHandler.createUser`, which enforces the mode atomically (first account on an
  empty DB always allowed) for both email and Google sign-up. `GET /api/config` tells the
  login page whether to show sign-up and the Google button.
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
  (shows when a navigation fails), `web/static/service-worker.js` (cache
  `cato-shell-<asset version>`, precaches `/asset-manifest.json`; versioned assets
  cache-first, unversioned stale-while-revalidate; navigations network-first with the
  cached shell `/` as the offline fallback for app routes and `offline.html` otherwise;
  covers cache-first in an LRU of 2,000; never touches `/api/`), and `web/static/js/head-init.js` (sets `--app-h`
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
`/volume1/Shared/Cato`, served at `http://10.0.0.42:7080` — a legacy plain-HTTP convenience,
out of scope for current work; it needs `CATO_ALLOW_INSECURE=1` to keep starting). The
container bind-mounts
`/volume1/Shared/Cato/data` → `/app/data` (DB + covers persist on the host).

```bash
DRY_RUN=1 make deploy # preview next main-only release tag; no tag push or deployment
make deploy        # confirm annotated v* tag push; triggers image release/deploy CI
make deploy-nas    # explicit legacy NAS binary/static transport and container recreation
make deploy-full   # disabled: use the stopped-service restore runbook for data replacement
make deploy-logs   # tail container logs
```

`make deploy` follows the Nabu-style tagged release workflow. A release tag must
be reachable from main; normal main/PR pushes never publish or deploy. See the
production runbook for image/SSH configuration. Never create/push a release tag
when the user has excluded deployment.

`make deploy-nas` runs the cross-compiled binary into a fresh image and recreates the container.
Shipped assets are versioned automatically (see Asset versioning): a changed file gets a new
URL and a new service worker version, which purges the old shell cache on activation; an
already-open client shows "Update available" and reloads when asked.
WAL conversion of an existing DB happens automatically on first open. Back up the prod DB
with SQLite `.backup` (never copy a live WAL database) before any manual DB surgery;
see `docs/production-runbook.md` for verified backup and stopped-service restore.
`docker` and `sqlite3` are
available on `nas2` (docker at `/usr/local/bin/docker`, no sudo needed).
