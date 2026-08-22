# Handoff Prompt — Cato Fix Session

Copy everything below the `---` marker and paste it as your first message in a new session.

---

You are working on the **Cato** project — a local-network videogame library (Go + SQLite + vanilla JS). The repo is at `/home/dave/git/cato`.

## Context

I completed a thorough audit of the codebase and the production instance at `http://10.0.0.42:7080` (Synology NAS, Docker). The full audit is saved to **`FINDINGS.md`** at the repo root — **read it first**. It covers ~30 issues organized by severity with file:line evidence, prod DB queries, container logs, and browser-level verification (XSS, broken covers, data-loss on edit, etc.). A short summary of the two user-reported bugs is at the top of that file.

A previous fix session started but was interrupted. **Three files have already been edited and are on disk (not yet committed):**

### Already fixed — do NOT redo blindly; verify and build on them

1. **`internal/auth/ratelimit.go`** — §3.3 rate-limiter keying:
   - Now imports `net` and keys by `host` from `net.SplitHostPort(r.RemoteAddr)` (RemoteAddr includes port, so old code gave every TCP connection a fresh bucket). `X-Forwarded-For` is no longer trusted (client-controlled). Verify the edit is still correct vs `FINDINGS.md` §3.3.

2. **`internal/auth/session.go`** — §3.9 session cleanup:
   - Added `CleanupExpiredSessions(db Querier) (int64, error)` — deletes `WHERE expires_at < now`. Uses `time.Now().Format(time.RFC3339)` (server-local, matching how `CreateSession` writes `expires_at`) so lexicographic string comparison is correct. Check import of `time` if needed and that callers will use it.

3. **`internal/http/handler_auth.go`** — §3.4 OAuth + §3.13 handleMe/login:
   - Added `log` import.
   - Google OAuth redirect now uses `cfg.BaseURL` when set (`strings.TrimSuffix(cfg.BaseURL,"/") + "/api/auth/google/callback"`), otherwise falls back to `http://localhost + ListenAddr` with a `log.Printf` warning. `BaseURL` comes from `CATO_BASE_URL` in `internal/config/config.go:15,29` and was previously loaded but never used.
   - `handleGoogleStart` no longer calls `auth.SetSessionCookie(w, state, ...)` — that line overwrote the user's session cookie with the OAuth state and logged out signed-in users. State now lives only in `cato_oauth_state`.
   - `handleMe` now checks `sql.ErrNoRows` (orphan session → delete + unauthenticated) and real DB errors (500).
   - `handleLogin` now selects `COALESCE(display_name,'')` and returns `email` + `display_name` in the JSON response (was missing vs signup/me).

Run `git diff` and `git status` at the start to see exactly what is on disk.

## Your job

**Fix everything described in `FINDINGS.md` and commit + push when done.**

### Suggested fix order (from FINDINGS.md §6)

1. **Stop data loss:** §1.3 timestamp clobber on `PUT/POST /api/library/{id}` (nil timestamps → `NULL` wiping `started_at`/`completed_at`; 400 prod rows at risk) + §1.4 "Add to Library" on an owned game resetting it to defaults (search dropdown + `#game/<id>` deep link; add a membership check endpoint like `GET /api/library/check?ids=` and `GET /api/library/{id}` single-item).
2. **XSS:** §1.5 `escapeHTML()` in `web/static/js/library.js:701` and `web/static/js/search.js:298` doesn't escape quotes; `search.js:116` `alt="${g.name}"` not escaped at all. Replace with a real escaper (`"`, `'`, `&`, `<`, `>`) or use DOM APIs. Remove the `localStorage` CSRF fallback in `web/static/js/api.js` (keep in-memory only) if you touch it.
3. **Covers:** §1.1 `internal/igdb/client.go:252` guesses `co%05d.jpg` from cover ID — must request `cover.image_id` via nested IGDB field (`cover.image_id`) and build URL from that; backfill bad URLs + re-enqueue exhausted `cover_jobs` (7 rows with `http status 404`, plus 2716 games on dead `images.cato.com`); §1.2 job hygiene: `backoffNext` always called with 0, no image validation, `ON CONFLICT DO NOTHING` prevents revive, `downloadAndSave` ignores `io.Copy` errors / truncated files. Add frontend `onerror` fallback to `/covers/<id>.jpg` → placeholder SVG.
4. **Dates feature (§2.1 + §3.2):** auto-track `started_at` when entering `playing` and `completed_at` when entering `completed` (if empty), validate timestamps (RFC3339 / `YYYY-MM-DD` → normalize to UTC RFC3339), show Added/Started/Completed in the edit modal and a completion badge on cards. Normalize new writes to UTC; frontend parser must handle mixed legacy formats (`CURRENT_TIMESTAMP` UTC vs RFC3339 local). Detail in FINDINGS §2.1/§2.2.
5. **Background jobs:** §3.1 stale-refresh treadmill (same 100 rows every 6h, 47/100 succeed, 53 deleted IDs never advance — mark nil results as refreshed); add periodic sweeps for expired `sessions` and `igdb_query_cache` (also remove write-only `igdb:` cache entries in `internal/games/service.go:96-97`). Wire a ticker in `cmd/cato/main.go`.
6. **Auth hardening:** §3.3 already done; §3.4 already done — verify completeness.
7. **Protocol polish:** §3.5 gzip `Range` + missing `Vary: Accept-Encoding` in `internal/http/server.go:74-104`; §3.6 dropdown keyboard selects invisible items (10 results, 8 rendered); §3.7 playtime minutes-vs-hours input (`library.js:552-554` shows `0.0` for `2`); §3.8/§3.9 cache/session sweeps; §3.10 list endpoint scan-error swallowing, `nullStr` inconsistency, no `X-Total-Count` / `hasMore` edge at exact multiples; §3.11 existence check only handles `ErrNoRows`; §3.12 `LIKE` wildcard injection (`%`/`_`) in search + tags.
8. **UX polish (§4 table):** per-tab counts, search "in library ✓" badges, success toast, year in edit modal, `loadMore` retry UI, focus trap, etc. — opportunistically.
9. **Docs drift (§5):** update `README.md` (IGDB trigger condition, refresh loop cadence/prioritization, cover retry semantics, API table missing endpoints), add `CATO_BASE_URL` docs, note cover `image_id` fix.

### Project conventions (from `AGENTS.md` — read the file for full details)

- **DB driver is `modernc.org/sqlite`** (pure Go). It does NOT understand `mattn/go-sqlite3` DSN params — use `_pragma=NAME(VALUE)` syntax. Current DSN sets `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(ON)`, `synchronous(NORMAL)` in `internal/db/db.go`.
- **Two-pool design:** `db.DB{Read, Write *sql.DB}` — `Read` many conns (WAL), `Write` `MaxOpenConns(1)`. Proxy methods: `Query*` → read pool, `Exec*`/`Begin*` → writer. Most call sites just use `db.Query`/`db.Exec`.
- **Migrations** are versioned in `internal/db/migrate.go` (`migrations` slice). Add new `{Version: N, Up: "..."}`; never edit applied migrations. Run via `db.Migrate(*db.DB)` on writer.
- **Tests use real SQLite:** pattern `db.Open(t.TempDir()+"/test.db")` → `db.Migrate`. No DB mocks. `auth.Querier` interface is satisfied by both `*sql.DB` and `*db.DB`.
- **Covers:** on-disk `data/covers/{id}.jpg` (canonical), legacy `.webp` served too; `covers.ServeCover` is DB-free with `Cache-Control: immutable`; download worker only handles library games (`INNER JOIN library_items`); cover job enqueued on library upsert.
- **CSRF:** unsafe methods require `X-CSRF-Token` from `GET /api/me`; middleware order `AuthRequired` then `CSRFRequired`.
- **IGDB optional:** `noopIGDBClient` when `IGDB_CLIENT_ID` empty; local results fallback.
- **No codegen/ORM/frontend build step** — raw SQL, vanilla JS.
- **Build & test:**
  ```bash
  make test    # go test ./...
  make build   # go build -o cato ./cmd/cato
  go vet ./... # must be clean (no Makefile target, but required)
  ```
  No lint/formatter command. `cato` binary is gitignored via `.gitignore` pattern.

- **Deployment:** Docker on Synology NAS (`nas2`, `/volume1/Shared/Cato`, `http://10.0.0.42:7080`). `make deploy` cross-compiles + pushes static + `compose up --build`. `make deploy-full` also pushes DB (rarely wanted). `make deploy-logs` tails logs. Prod logs showed `stale refresh: refreshed 47/100` every 6h since 2026-06-28.

### Execution notes

- Work incrementally and keep `make test` + `go vet ./...` green after each batch. Add/adjust tests where behavior changes (especially `internal/http/handler_library_test.go`, `internal/games/games_test.go`, `internal/igdb` if you add parsing).
- The prod DB is reachable at `nas2:/volume1/Shared/Cato/data/cato.db` via `ssh -o BatchMode=yes nas2` (read-only queries with `sqlite3 'file:...?mode=ro'`). **Do not mutate prod.** For local testing against prod data, copy via `sqlite3 '.backup /tmp/cato-inspect.db'` on the NAS then `ssh nas2 "cat /tmp/cato-inspect.db" > /tmp/opencode/cato-test/cato.db` and run a local `cato` on a different port (e.g. `:7081`) — the previous session left helpers for this in `/tmp/opencode`.
- `FINDINGS.md` appendix has exact prod counts and verification steps (e.g. broken-image `naturalWidth=0`, tag injection `window.__xss2=1`).
- When finished, `git add` the changed files (respect `.gitignore`), commit with a descriptive message, and push.

### What to do first

1. `read FINDINGS.md` + `read AGENTS.md` + `git diff` + `git status`
2. `make test` to confirm current baseline still passes with the 3 already-edited files.
3. Continue implementation in the order above, committing in logical chunks if you prefer (at minimum one final commit).
4. Final `make test && go vet ./... && make build` then `git push`.
