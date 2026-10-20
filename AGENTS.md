# AGENTS.md

Cato is a private, self-hosted game tracker for a household or a group of friends. It's one Go binary with one SQLite database, and a vanilla-JS PWA (lit-html, no build step). This file is the entry point: the rules below always apply, and the topic guides in `docs/agents/` hold the detail. Read the guide for the area you're changing before you change it.

## Commands

```bash
make run            # build + start locally over HTTPS (dev TLS; background, writes .pid) — make stop
make test           # go test ./...            (make test-race for -race)
make vet            # go vet ./...  — must be clean;  make fmt-check — gofmt must be clean
make test-js        # frontend lint (no HTML-string sinks), node --test, service-worker harness
make test-e2e       # Playwright: Chromium, WebKit, Firefox, phone Chromium/WebKit, over HTTPS
make release-check  # everything above + builds, backup/deploy script tests, security scan
```

Requirements:

- Go: go.mod pins `toolchain go1.26.9`, and `make security` scans with it.
- Node 24+, Python 3.10+, sqlite3.
- Playwright Chromium, WebKit and Firefox.

Keep generated logs, traces and scratch scripts outside the repo.

## Map

| Path | What |
|---|---|
| `cmd/cato/` | `run(args)`: `serve` (default), `dev-cert`, `import-games`, `backfill …`; registers jobs |
| `internal/db/` | SQLite (two pools), migrations (v24), pre-upgrade snapshots |
| `internal/library/` | The library: items, journal (playthroughs, sessions), undo, index, bulk, stats, prefs, goals, Year in Review, import batches |
| `internal/catalog/` | Games, search, IGDB limiter (two lanes), details/backfills/sync jobs, moods |
| `internal/discover/` | Discover shelves, Up Next, estimates at your pace |
| `internal/social/` | People: visibility rule, profiles, follows/blocks, reviews, lists, feed, inbox, invites, sign-up, admin |
| `internal/imports/` | CSV/JSON/Steam/RetroAchievements imports, matching, barcodes |
| `internal/alerts/`, `internal/notify/`, `internal/push/` | Wishlist alerts, delivery (push, email), Web Push (RFC 8291/8292) |
| `internal/webauthn/` | Passkeys |
| `internal/auth/`, `internal/config/`, `internal/devtls/` | Sessions/CSRF/rate limits, env config, dev certificates |
| `internal/http/`, `internal/http/v1/` | Server, middleware, assets, `/api/auth`; the app API (`docs/api-v1.md`) |
| `internal/jobs/`, `internal/metrics/`, `internal/covers/` | Background jobs, Prometheus metrics, cover worker and handler |
| `internal/integrations/` | IGDB client, Wikidata crosswalk |
| `web/static/` | The PWA: `js/app.js` shell, `js/features/*` screens, `js/core/*` (no DOM), `css/`, service worker |
| `e2e/`, `tests/js/` | Playwright suite; `node --test` for the pure modules |

## Rules that always apply

- **HTTPS only.** Cato refuses to start without TLS or an `https://` `CATO_BASE_URL`. `CATO_ALLOW_INSECURE=1` is for tests and the legacy NAS. Trust forwarded headers only via `auth.ClientIP` / `auth.RequestIsHTTPS`.
- **SQLite:**
  - Pragmas use `_pragma=NAME(VALUE)`: mattn-style DSN parameters are silently ignored.
  - SELECTs go through `Query*` (read pool) and writes through `Exec*`/`Begin*` (the single writer).
  - Inside a writer transaction, use the `*sql.Tx`.
  - Never edit an applied migration.
- **Library writes go through `library.Store`**, never straight to tables from handlers. Writes carry the person's local day (`played_on`).
- **Someone else's content is filtered by `social.CanSee` / `visibleSQL`, always.** Extend `TestAuthorizationMatrix` for new content types.
- **Frontend:**
  - Render only with lit-html: no `innerHTML`, no `javascript:` URLs, no inline scripts or handlers. The CSP enforces Trusted Types.
  - Asset versions are automatic: never add cache names.
  - Keep `<main>` the scroll container (`#app` is height-bound).
  - A screen draws once, finished: its `enter(route)` resolves when what it shows first is ready (data, first covers via `decodeCovers`), and the shell holds the previous screen until then (≤ 250 ms). No View Transitions.
  - Navigate with the router's `go()` (pushState), never `location.hash =`; the shell sends taps on `#…` links through it. On plain HTTP, iPhone Safari loads the page again after a fragment navigation (WebKit bug 320638).
- **Background work runs on `internal/jobs`**, never in ad-hoc goroutine loops. Tag user-facing IGDB calls with the interactive lane.
- **Tests:**
  - Regression test first.
  - Real SQLite, no database mocks.
  - Playwright for UI changes, with layout and form changes checked in Firefox too.
  - New screens go into the axe spec.
  - Keep the perf budget: initial JS ≤ 60 KB gzipped.
- **No codegen, no ORM, no frontend build step.** No new dependencies for push, passkeys and the like: they're implemented here on purpose.
- **Production data:**
  - Back up with `sqlite3 … .backup`; never copy a live WAL database.
  - Never write to a database a running server has open.
  - **Never deploy, push or create or push a release tag unless explicitly asked.**

## Before you change… read

| Area | Guide |
|---|---|
| Schema, migrations, SQL, the journal, undo | [docs/agents/database.md](docs/agents/database.md) |
| Server, TLS, CSP/Trusted Types, sessions/CSRF, API conventions, jobs, IGDB, covers, config | [docs/agents/backend.md](docs/agents/backend.md) |
| People and visibility, sign-up/invites, imports, notifications/push, passkeys, share target, goals, Year in Review | [docs/agents/features.md](docs/agents/features.md) |
| Shell, layout, routes, store/outbox, lit-html, icons, assets, service worker, browser quirks, accessibility, perf budget | [docs/agents/frontend.md](docs/agents/frontend.md) |
| Go/JS/Playwright tests, the release gate, **running Firefox** (including from a sandboxed shell) | [docs/agents/testing.md](docs/agents/testing.md) |
| The NAS, deploy paths, backups, rollback | [docs/agents/deployment.md](docs/agents/deployment.md) |

## Other references

- `docs/api-v1.md`: the API.
- `docs/cato_smooth.md`: the product plan and why things are the way they are (implementation notes per phase).
- `docs/production-runbook.md`: configuration, releases, backup and restore.
- `docs/dev-https.md`: local HTTPS.
- `e2e/README.md`: what each spec covers.
