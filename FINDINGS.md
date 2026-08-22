# Cato — Audit Findings (2026-08-20)

Findings from a full review of the code, the production instance at
`http://10.0.0.42:7080`, and browser-level testing against a **copy** of the
prod DB (no prod data was modified; one throwaway account
`qa-tester@example.com` was created in the local copy only).

Severity legend: 🔴 critical · 🟠 high · 🟡 medium · ⚪ low/polish

---

## 1. Confirmed bugs

### 🔴 1.1 Cover URLs are guessed from the cover ID — many are simply wrong
*(the bug behind `#game/194464` and `#game/175604`)*

`internal/igdb/client.go:252` builds the image URL by zero-padding the numeric
cover ID:

```go
fmt.Sprintf("https://images.igdb.com/igdb/image/upload/t_cover_big/co%05d.jpg", coverID)
```

But IGDB's `game.cover` field is an ID into the **covers table**; the URL must
be built from that record's `image_id`. The numeric parts coincide *most* of the
time, which is why most covers work — and why this slipped through.

Evidence:
- Game 194464 (Escape Academy): `cover_id=213263` → `co213263.jpg` → **HTTP 404**
  from the IGDB CDN (verified with curl).
- Game 175604 (Poly Vita): `co229281.jpg` → **HTTP 404**.
- Prod `cover_jobs` rows for both: `attempts=5, last_error="http status 404"`.

The frontend then falls back to the same dead remote URL
(`api.js:getCoverURL` prefers `local_cover_path`, else `cover_url`), so the card
renders a **broken image** (verified in Chromium: `naturalWidth=0`, console 404,
no `onerror` fallback to the placeholder).

**Fix sketch:**
- Request `cover.image_id` directly in the IGDB fields clause
  (`fields name,...,cover.image_id;` — IGDB v4 supports nested fields) and build
  URLs from `image_id`.
- Backfill: for games whose cover job failed with 404, re-fetch the game and
  rewrite `cover_url` from the real `image_id`, then re-enqueue.
- Frontend: add an `onerror` handler on card/modal images that swaps in
  `/covers/<id>.jpg` → placeholder so a dead remote URL never shows a broken
  image.

### 🔴 1.2 Failed cover jobs are stuck forever; ~2.7k games point at a dead domain

- `covers.Worker.nextJob` only picks jobs with `attempts < 5`
  (`internal/covers/covers.go:112`). After 5 failures the row sits there
  forever, and `Store.EnqueueCoverJob` uses `ON CONFLICT DO NOTHING`
  (`internal/games/store.go:317`) — so a stuck job can **never** be re-enqueued
  while the game stays in a library.
- Prod: 7 exhausted jobs, including both games above. Two more failed on DNS:
  `dial tcp: lookup images.themediapedia.com ... server misbehaving` — the old
  Postgres import points **2,716 games** at `images.themediapedia.com`, a domain
  that no longer resolves. These can never succeed even if retried.
- 120 more pending jobs belong to games not in any library and will never be
  picked (worker INNER JOINs `library_items` by design) — harmless but misleading
  backlog.

Also in the same path (`internal/covers/covers.go:129-162`):
- `backoffNext(time.Now(), 0)` always passes `attempt=0`, so retries are a flat
  1 minute — the "exponential backoff" is dead code.
- `downloadAndSave` doesn't check `Content-Type` or JPEG magic bytes and ignores
  `io.Copy` errors — a truncated/HTML response gets saved as `{id}.jpg`, the job
  is deleted, and the corrupt file is served with `immutable` caching forever.
- A crash mid-download leaves the job claimed for 30 min (fine), but a partial
  file may remain on disk and pass the `os.Stat` fast-path on the next attempt.

**Fix sketch:** delete/reset exhausted jobs whose error was transient; re-enqueue
on conflict when the existing job is exhausted; validate image bytes before
rename-into-place (write temp file, check `\xFF\xD8\xFF`, then rename); purge or
re-source `themediapedia.com` URLs during the 1.1 backfill.

### 🔴 1.3 Editing a library item silently wipes `started_at` / `completed_at`

The API accepts optional timestamps (`handler_library.go:186-193`) but the
upsert writes whatever the request contained — and the UI never sends them:

- `library.js:652-658` posts only `status/rating/playtime_minutes/tags/notes`.
- `handler_library.go:232-252`: `nil` timestamps become SQL `NULL`, and the
  `ON CONFLICT DO UPDATE SET completed_at = excluded.completed_at` overwrites
  the old value.

Verified end-to-end on the test copy: set `completed_at=2026-08-01` via API →
perform a UI-style edit without timestamps → `completed_at` is `NULL`.

**This is not hypothetical data loss:** prod already has **400 items with
`completed_at` set** (and 0 with `started_at`). Every one of those loses its
completion date the next time the user edits it in the UI.

**Fix sketch:** preserve unspecified columns — e.g. build the UPDATE with
`completed_at = COALESCE(excluded.completed_at, library_items.completed_at)`
(plus an explicit way to clear), or auto-manage timestamps (see §2.1) and drop
them from the client contract entirely.

### 🔴 1.4 "Add to Library" on a game you already own resets it to defaults

Clicking a **search-dropdown result** for a game that is already in your library
opens the blank *Add to Library* form (`library.js:442-445` — search mode always
calls `addGameToLibrary`). Saving performs an upsert that overwrites status
(→ `backlog`), rating (→ 0), playtime, tags, and notes.

Verified in the browser: Escape Academy was in the library as
`playing / rating 80 / tagged`; clicking its dropdown result showed
"Add to Library"; saving produced `backlog / 0 / [] / ""`.

Related: nothing in search results indicates library membership, so users can't
even tell this is about to happen. Same family: a `#game/<id>` deep link for a
game that's in your library but **not on the currently loaded page** also opens
the Add form (`library.js:459-471` — `itemsById` only holds loaded pages).

**Fix sketch:** before opening the Add form, `GET /api/library` membership (add
a cheap `GET /api/library/{gameID}` endpoint returning the item or 404) and open
the edit form instead; show an "in library" badge on search results.

### 🔴 1.5 Stored XSS — `escapeHTML()` does not escape quotes

Both `library.js:701` and `search.js:298` "escape" via
`div.textContent = s; return div.innerHTML;`, which escapes `& < >` but **not
quotes**. Tags/names are interpolated into attributes all over:

- `library.js:403` `data-tag="${escapeHTML(t)}"` (tags = user input)
- `search.js:116` `alt="${g.name}"` — **not escaped at all** (IGDB data)

Verified exploit on the test copy: a tag of
`hover" onmouseover="window.__xss2=1" data-x="` renders as

```html
<span class="tag-chip" data-tag="hover" onmouseover="window.__xss2=1" ...>
```

and `window.__xss2` executed on hover. Combined with the CSRF token being kept
in `localStorage` (`api.js:35-46`), XSS = full account takeover of that session.
Game names come from IGDB (cross-user input), so the unescaped `alt=` in
search.js matters even between users.

**Fix sketch:** replace both helpers with a real escaper that handles `"` and
`'` (or set textContent/attributes via DOM APIs instead of string templates);
fix the raw `${g.name}` in search.js immediately.

---

## 2. The feature gap you noticed: dates are half-built

### 🟠 2.1 Added / started / completed dates exist in the DB and API — the UI just ignores them

- Schema: `library_items.started_at / completed_at / created_at` all exist
  (`migrate.go:136-139`).
- API: `GET /api/library` returns all three (`handler_library.go:88-177`).
- UI: the edit modal shows none of them, offers no way to set them, and cards
  don't show them either. So from the user's perspective the feature doesn't
  exist.

Recommended implementation:
1. **Server-side auto-tracking** (don't rely on clients):
   - On insert: `created_at = now` (already defaulted).
   - Status transition into `playing` and `started_at IS NULL` → set it.
   - Transition into `completed` → set `completed_at = now`;
     leaving `completed` → clear it (or keep first-completion date — decide).
   - Keep honoring explicit client values if provided (validate them! see 🟡 3.2).
2. **UI**: show "Added", "Started", "Completed" (read-only, plus a small clear
   button) in the edit modal; optionally a "✓ <year>" ribbon or the completion
   date on cards in the Completed tab; sortable later.
3. **Fix the clobbering bug (§1.3)** first, or step 1 will be undone by edits.

### ⚪ 2.2 Timestamps are stored in two different formats and timezones

Same row, verified from prod:

```
created_at = 2026-08-21 02:11:10            (SQLite CURRENT_TIMESTAMP, UTC, no zone)
updated_at = 2026-08-20T22:11:10-04:00      (RFC3339, server-local, written by Go)
```

Older rows use microsecond variants (`2024-01-04 22:13:59.054264`). Anything
that parses these (future date display, `ORDER BY` comparisons, other tools)
must handle both, and `new Date("2026-08-21 02:11:10")` is non-standard
(Safari returns Invalid Date). Pick one format (recommend: SQLite strings in
UTC `strftime('%Y-%m-%dT%H:%M:%fZ')` or unix seconds) and normalize old rows in
a migration. Note `cover_jobs.next_attempt_at` mixes both formats too
(default `CURRENT_TIMESTAMP` vs RFC3339 writes) — lexicographic comparison
happens to work today but is fragile.

---

## 3. Other bugs & correctness issues

### 🟠 3.1 Stale-refresh loop is a treadmill: same 100 games forever

Prod logs since **June 28**: every 6 hours, `refreshing 100 games older than 90
days … refreshed 47/100` — identical every cycle. Games whose IGDB fetch fails
(53 of the oldest 100, likely deleted IDs) keep `source_updated_at` unchanged,
so `GetStaleGames` (`store.go:240`) re-selects exactly the same 100 rows
forever. Nothing newer than those ever refreshes, and ~400 requests/day are
burned on repeat failures (README says "max 100/day").

**Fix sketch:** on permanent failure (IGDB returns empty = deleted game), mark
the row (e.g. set `source_updated_at = now` or a `refresh_failed_at`) so the
queue advances; log failures with reasons.

### 🟠 3.2 No validation of `started_at` / `completed_at`

`POST /api/library/194464 {"completed_at":"not-a-date"}` → `200 OK`, garbage
stored (verified). Validate as RFC3339 (or `YYYY-MM-DD`) before storing.

### 🟠 3.3 Login rate limiting barely works

`auth.RateLimiter.Middleware` keys by `r.RemoteAddr` — which **includes the TCP
port**, so every connection gets a fresh bucket (`ratelimit.go:50`). It also
prefers a client-controlled `X-Forwarded-For` header, so any attacker can rotate
identities. Key by `net.SplitHostPort(RemoteAddr)` host only, and only trust XFF
from a known proxy.

### 🟠 3.4 Google OAuth is broken in the Docker deployment

`handler_auth.go:36-38` builds the redirect URL from `CATO_LISTEN_ADDR`
(`:7080`) → `http://localhost:7080/api/auth/google/callback`. From any device
except the server itself, Google redirects to the wrong host. The `BaseURL`
config (`config.go:15,29`, `CATO_BASE_URL`) exists precisely for this but is
**never used anywhere**. Use `CATO_BASE_URL` (with a startup warning if Google
auth is enabled without it).

Also: `handleGoogleStart` (`handler_auth.go:238`) calls `SetSessionCookie(w,
state, …)` — it overwrites the user's **session cookie** with the OAuth state
string, logging out any already-signed-in user who clicks "Sign in with Google".
The state belongs only in `cato_oauth_state`.

### 🟡 3.5 gzip middleware produces corrupt responses and misses `Vary`

`server.go:74-104` sets `Content-Encoding: gzip` unconditionally (when the
client accepts it), then passes through to handlers that understand `Range`:

- Verified: `Range: bytes=0-99` + `Accept-Encoding: gzip` on `/js/api.js`
  returns `206 Partial Content`, `Content-Encoding: gzip`, with **100 raw
  uncompressed bytes** — any gzip-decoding client chokes.
- No `Vary: Accept-Encoding`, so any caching proxy can poison clients.
- It also wraps 304 responses and small JSON where gzip gains nothing.

**Fix sketch:** skip gzip when the handler will serve ranges (static files), or
wrap only the JSON API paths; add `Vary: Accept-Encoding`.

### 🟡 3.6 Search dropdown keyboard navigation selects invisible items

Dropdown renders `results.slice(0, 8)` (`search.js:107`) but ArrowDown indexes
into all 10 `currentResults` (`search.js:259`). Verified: pressing ArrowDown
past the 8th item leaves **no visible selection**, and Enter then adds a game
the user never saw. Clamp selection to rendered count (or render all 10).

### 🟡 3.7 Playtime input fights the user (minutes vs hours)

`library.js:552-554`: label says **Hours**, preview shows hours, but the number
input holds **minutes** (`step=15`). Typing `2` (meaning 2 hours) shows preview
`0.0` and stores 2 minutes. Verified in-browser. Either make the input hours
(float, store minutes) or relabel to minutes.

### 🟡 3.8 `igdb_query_cache` accumulates write-only data

`service.go:96-97` caches each IGDB result under `"igdb:" + normalized_name` —
nothing ever reads those keys (only `"search:"+query` markers are read by
`shouldAskIGDB`). Entries expire out of the *read* path lazily but are never
purged from the DB. Either implement the per-game cache read path or delete the
write; add a periodic/lazy sweep for expired rows (prod currently: 105 rows /
103 expired — small today, unbounded over time).

### 🟡 3.9 Expired sessions are only deleted when presented

`session.go:71-74` deletes a session only if that exact cookie comes back.
Abandoned sessions accumulate forever. Add a cheap daily cleanup
(`DELETE FROM sessions WHERE expires_at < now`).

### 🟡 3.10 Library list endpoint robustness nits

- `rows.Scan` errors are silently skipped (`handler_library.go:141-145`) — a
  schema drift would silently hide items. At least log/count.
- Invalid `limit` (>200, non-numeric) silently becomes the default 60 rather
  than clamping/erroring — surprising for API consumers.
- No total count returned, so the UI can't show "N games" per tab and infinite
  scroll can't know it's done except by coming up one item short (which also
  misfires when the total is an exact multiple of the page size).
- `nullStr()` maps NULL→`""` for created/updated but started/completed use real
  `null` — pick one representation.

### 🟡 3.11 `upsertLibraryItem` ignores non-"no rows" errors on the existence check

`handler_library.go:221` only handles `sql.ErrNoRows`; a transient DB error
falls through to the INSERT and surfaces as a generic 500 FK failure.

### ⚪ 3.12 LIKE-pattern injection in fallback paths

`searchLikeFallback` (`store.go:77`) and `/api/library/tags?q=` build
`LIKE '%'+input+'%'` without escaping `%`/`_`. Searching `100%` matches far more
than intended (verified: returns 10 rows). Escape with `ESCAPE '\'`.

### ⚪ 3.13 Misc

- `handleMe` ignores the user-lookup DB error (`handler_auth.go:83`) → empty
  email/name on transient failures.
- Login response omits `email`/`display_name` (signup and `/api/me` include
  them) — inconsistent client contract.
- Double rate-limiting on IGDB calls: `games.Service` and `igdb.Client` each
  keep their own limiter, so backfill waits ~2 s/request.
- `EnqueueMissingCovers` (`service.go:123`) is dead code (intentionally
  uncalled per AGENTS.md — consider deleting or gating behind a flag).
- `main.go:111-115`: no graceful shutdown (`srv.Shutdown` never called;
  SIGTERM just exits); `log.Fatalf` inside a goroutine skips defers.
- `make deploy-db` pipes a **live WAL-mode SQLite file** with `cat` — can copy a
  torn snapshot. Use `sqlite3 .backup` like `db-backup` does.
- Dockerfile has no `HEALTHCHECK` despite `/healthz` existing.

---

## 4. UX gaps worth addressing

| Area | Gap |
|---|---|
| Library overview | No per-tab counts ("Backlog (43)"), no total, no sort options (fixed `updated_at DESC`), no text filter within library |
| Search results | No "in your library ✓" badge; no status/rating shown for owned games; adding gives no success feedback (modal just closes) |
| Edit modal | Doesn't show year (Add modal does); no dates (see §2.1); Remove has no undo; save errors use `alert()` |
| Covers | Broken remote images render as broken `<img>` (no `onerror` → placeholder swap); placeholder SVG says "No Cover" with no retry affordance |
| Infinite scroll | `loadMore` failures are silent (`console.error` only) — scrolling appears broken with no retry |
| Routing | Modals use `replaceState`, so Back doesn't close them; unknown hashes (e.g. `#foo`) leave the URL dirty and silently mean "All" |
| Settings | Stub page ("coming soon") — linked from nowhere on desktop nav, exists at `/settings` |
| Accessibility | Modal has no focus trap (Tab escapes to background); tag-chip remove buttons appear on hover only (CSS) — verify keyboard reachability |
| Sessions | Logout on one tab doesn't invalidate CSRF in other tabs' localStorage until next `/api/me` |

---

## 5. Documentation drift (README vs behavior)

- README: IGDB is queried "when local results are insufficient (fewer than 3
  matches…)". Code: whenever the query is ≥3 chars and not seen in the last 24 h
  regardless of result count (`service.go:235-241`).
- README: refresh loop "max 100/day, prioritizing games in your library". Code:
  up to 400/day (every 6 h × 100), ordered purely by `source_updated_at`, no
  library prioritization (`service.go:111-121`, `store.go:240`).
- README: "Covers are never re-downloaded unless deleted" — accurate, but see
  §1.2: they're also *never retried* after 5 failures, and 2.7k imported URLs
  can never succeed at all.
- README API table omits `GET /api/library/tags`, `PUT /api/library/{id}`
  (accepted), and the pagination/tag params.

---

## 6. Suggested fix order

1. **Stop the bleeding on data loss**: §1.3 (timestamp clobber) + §1.4
   (add-overwrites-owned-game) — small diffs, protect user data immediately.
2. **XSS**: §1.5 escaper replacement (one helper, few call sites).
3. **Covers**: §1.1 `cover.image_id` + backfill of bad URLs, §1.2 retry/job
   hygiene, frontend `onerror` fallback.
4. **Dates feature**: §2.1 auto-tracking + modal display (builds directly on #1).
5. **Background jobs**: §3.1 stale-refresh treadmill; expired-session/cache
   sweeps (§3.8, §3.9).
6. **Auth hardening**: §3.3 rate-limit keying, §3.4 OAuth redirect/state cookie.
7. **Protocol polish**: §3.5 gzip/range, §3.6–3.12 as touched.
8. UX table (§4) opportunistically; README corrections (§5) in the same PR that
   changes the related behavior.

---

## Appendix A — Evidence collected

- Prod DB (read-only queries via `sqlite3 ?mode=ro` on nas2):
  - 311,910 games; 76,104 with local covers; 235,780 relying on remote URLs;
    28 with no cover at all.
  - `cover_jobs`: 127 total, 7 exhausted (`attempts=5`), incl. games 194464 &
    175604 (`http status 404`) and 4 DNS failures to `images.themediapedia.com`;
    120 pending, all for games not in any library.
  - 2,716 games have `cover_url` on the dead `images.themediapedia.com` domain.
  - `library_items`: 1,589 items, 3 users; **400 have `completed_at` set, 0 have
    `started_at`**; `created_at`/`updated_at` in mixed formats (see §2.2).
  - 6 library games have no local cover; 4 of those point at the dead domain.
  - Sessions: 12 total, 10 expired. `igdb_query_cache`: 105 rows, 103 expired.
- Container logs (nas2, since 2026-06-28): identical
  `stale refresh: refreshed 47/100` every 6 h.
- Browser tests (Chromium via Playwright against a local copy of prod data):
  broken-image rendering + console 404 for game 194464; edit modal contains no
  date fields; playtime `2` → preview `0.0`; ArrowDown past 8th dropdown result
  → invisible selection; "Add to Library" on owned game wiped rating/tags/
  notes/status; tag attribute-injection executed `window.__xss2=1` on hover.
- `go vet ./...` clean; `go test ./...` passing (no coverage in `covers`,
  `igdb`, `cmd/cato`).
