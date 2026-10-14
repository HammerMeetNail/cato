# Cato Smooth: plan for a Goodreads for videogames

Created 2026-10-13. Baseline commit `0833eb3` (`main`).

## 0. Summary

Cato already has a strong base:
- One Go binary.
- SQLite in WAL mode with separate read and write pools.
- Real-database tests, and 90 browser tests across Chromium and WebKit.
- A strict CSP.
- A tag-gated release process.
- An IGDB-backed catalog with fast local search.

The weak part is the product built on top of that base. It works like a spreadsheet of statuses:
- There is **no game page**.
- **Ratings use a 101-option picker.**
- **Hours use an 801-option picker.**
- The search box searches the global catalog, so **you can't search your own library**.
- Finishing a game takes one tap. It has no undo and never asks how the game was.
- Desktop shows a phone-width column.

The frontend is held together by convention: a 3,540-line `library.js`, about 60 `innerHTML` sinks that depend on remembering to escape, and five overlapping status-filter controls. It also has real bugs (§2).

The plan has seven phases. Each one can ship on its own and leaves Cato better than before:

| Phase | Theme | Outcome |
|---|---|---|
| **P0** | Stabilize | Fix confirmed bugs and the cheapest security and performance items (§2). |
| **P1** | Foundations | Typed `/api/v1`, a library domain package, new journal schema, a frontend store with a local library index, a design system, and automatic asset versioning. |
| **P2** | The core loop | Home dashboard, a ⊕ Log sheet available everywhere, the game page, star ratings, a Finish flow with rating, session logging with undo, and library search with saved views. |
| **P3** | Rich catalog and discovery | Expanded IGDB data (time-to-beat, companies, series, similar games, release dates, media), Discover tab, "what should I play next?" |
| **P4** | People | Usernames, profiles, reviews, lists, follows and an activity feed, all limited to this Cato instance. Invite-only signup. |
| **P5** | Imports, integrations and reach | Steam/CSV import, full export, wishlist price and release alerts, Web Push, Share Target, a library that works offline with queued writes. |
| **P6** | Delight | Year in Review, goals, journaling streaks, a backlog roulette, and polish. |

**North-star targets:**
- Log a game moment in **5 seconds or less, in 2 taps from launch**.
- Answer "what should I play next?" in **under 10 seconds**.
- Every screen interactive in **under 1 second** on a mid-range phone over LAN.

**Owner decisions (2026-10-13), detailed in §12:**
- **HTTPS everywhere**: in development, in tests and in production. Production will eventually run on a real domain. The current HTTP deployment is a quick convenience and is out of scope.
- The 4-tab Nabu layout is replaced.
- Ratings display as 5 stars with halves.
- Social features are limited to this server.
- No PlayStation or Xbox imports.
- Frontend: lit-html, chosen for security, maintainability, usability and testability (§4.2).

**Why HTTPS is a foundation, not an add-on.** Browsers enable the service worker (offline mode and install), passkeys, push notifications, the camera and Web Crypto **only in a secure context**. I verified this in the running app (§2.4). P0 therefore makes Cato an HTTPS app, with a development and test setup that **needs nothing from the owner**: no domain, no Tailscale, no certificate installs. This was proven with a prototype on this machine (§2.4).

---

## 1. How this review was done

- **Code.** I read every Go package and every frontend file, and compared them against `CODE_REVIEW_REPORT.md` (2026-10-04) to find which backlog items are still open.
- **Running app.**
  - I built `Containerfile` with podman and ran it on `127.0.0.1:7380` with a scratch volume.
  - Because no IGDB credentials or catalog exist locally, I seeded 70 real titles with metadata and generated cover art.
  - I created a user through the UI and filled a 53-item library through the API.
  - I drove the app with Playwright as an iPhone 13 (390×844) and as a 1440×900 desktop.
  - Screenshots are in the review scratchpad. They are not committed.
- **Market and data sources.** I researched competitors (Backloggd, Grouvee, Infinite Backlog, HowLongToBeat, GG, Stash, Glitchwave, Letterboxd, Goodreads, StoryGraph), the APIs available and their terms, and PWA support in current browsers (MDN data from 2026-10-08). Results are in §3.2 and §9.

### The hosted instance

I checked `http://10.0.0.42:7080` read-only (no signup or search). It runs an older build (`cato-static-v22`, files from 2026-08-29) with no security headers, and in a browser it is not a secure context. The owner has confirmed it is a quick development convenience, so it is **out of scope** for this plan.

### What I measured (localhost, iPhone 13 emulation, cold cache)

| Metric | Value | Comment |
|---|---|---|
| Signup → home | 0.83 s | Fine. |
| First contentful paint | 96 ms | Fine on localhost. Real cost is transfer size (below). |
| Cold load of `#library` | 35 requests, **406 KB** | JS and CSS are **not compressed**: `library.js` alone is 150 KB raw, `app.css` 79 KB. |
| Initial JS executed | about 250 KB across 9 modules | Stats and Settings load eagerly. There is no `modulepreload`, so imports load in a 3-level waterfall. |
| Library density, phone | about 1.5 rows × 2 columns above the fold | Each card is roughly 180×370 px. |
| Desktop | `max-width: 720px` column | Two-thirds of a 1440 px screen is empty (3 columns). |
| Secure-context features over plain HTTP on a non-loopback hostname (`http://cato.lan:7380`) | `isSecureContext=false`. No `navigator.serviceWorker`, no `PublicKeyCredential`, no `mediaDevices`, no `crypto.subtle` or `randomUUID`. | The same build on `127.0.0.1` has all of them, so today's E2E suite passes only because it runs on loopback. Motivates §2.4. |

### What a first-time user sees

1. **Login and signup.** A generic card with no explanation of what Cato is.
   - A "Sign in with Google" button appears **even when Google isn't configured**. Clicking it shows raw JSON: `{"error":"google_unavailable",…}`.
   - The signup password field uses `autocomplete="current-password"`, so password managers won't offer to generate one.
   - The "Sign up / Login" toggle is an `<a>` with no `href`, so the keyboard can't reach it.
2. **Home** says "Nothing playing right now" with a **Go to backlog** button, but the backlog is also empty. There is no onboarding, import offer or suggestions.
3. **Search** switches you to the Library tab.
   - Each result row has five **26 px icon-only buttons** that differ only by colour. A "Status" tooltip stays visible on top of the first result on touch screens.
   - Tapping a result opens an "Add to Library" form, not a page about the game.
4. **The add/edit sheet:**
   - The rating dial is a disguised `<select>` with 101 options.
   - Hours is a `<select>` with about 801 options and is an **absolute total**, so adding 45 minutes means working out the new total yourself.
   - A "Completed" date field appears for Wishlist items.
   - The button still says "Cancel" after the entry has been saved.
5. **The Playing tab** is the best screen in the app: one tap logs +30m/+1h/+2h.
   - **Finish** has no confirmation, rating prompt or undo.
   - After the list re-renders, the next game's Finish button sits under your finger, so a double-tap finishes two games.
6. **Stats** is a single card: totals, finished by year, tags and recent updates. Accurate, but plain.

---

## 2. P0: Stabilize (confirmed bugs and quick wins)

Every item below was checked against the code or reproduced in the running app. Each fix comes with a regression test.

### 2.1 Correctness bugs

| # | Sev | Bug | Where | Fix |
|---|---|---|---|---|
| 1 | High | Picking a suggestion in the Filters tag autocomplete throws `ReferenceError: segStart is not defined`: the variable is declared inside an IIFE and used outside it. | `web/static/js/library.js:1392-1401` | Use the `start` value the IIFE returns. Add a Playwright test. |
| 2 | High | Autosave **POSTs the full record** built from the grid snapshot taken when it loaded. Playtime logged elsewhere (Playing tab, another device) is overwritten. | `library.js:3096`, `:2721` | Send only the changed fields with PATCH (`api.js:396` already exists). |
| 3 | High | PATCH does a read-modify-write: it reads from the read pool, adds the playtime delta in Go, then writes an absolute value. Two quick `+30m` taps can lose one. | `internal/http/handler_library.go:554-570`, `:810` | Do the whole update in one writer transaction, using SQL arithmetic (`playtime_minutes = playtime_minutes + ?`). |
| 4 | Med | The `$tag` dropdown treats `{items,total,hasMore}` as an array, so it always says "No games tagged…". | `app.js:330-336`, `search.js:500-506` | Read `.items`. |
| 5 | Med | `ownedStatuses` in search is never invalidated. After you remove a game, quick-add says "already in your library" until you reload. There are two separate copies of this map. | `search.js:59`, `library.js:257` | Keep one shared store (P1). As a short-term fix, invalidate the map on every library mutation. |
| 6 | Med | Finish takes one tap, has no undo, and the next card's Finish button ends up under your finger. | `playing.js:165` | Add an undo toast now; the Finish sheet comes in P2. |
| 7 | Med | Closing the edit sheet reloads only page 1 of the grid, so you lose your scroll position. | `library.js:1933` | Update only the affected card (keyed update). |
| 8 | Low | `decodeURIComponent` on the hash isn't guarded, so a malformed `%` breaks routing. | `library.js:40` | Wrap it in try/catch. |
| 9 | Low | `offline.html` promises that your library is "saved locally and will sync". It isn't. | `web/static/offline.html:44` | Fix the copy now. P5 makes it true. |
| 10 | Low | The manifest's "Search" shortcut goes to `/#library` and doesn't focus search. | `manifest.webmanifest:16` | Add a `#search` route that focuses the search box. |

### 2.2 Security quick wins

| # | Sev | Item | Fix |
|---|---|---|---|
| S2 | Med-High | `GET /api/games/search` is **public**. Each new query of 3+ characters triggers an IGDB call and writes up to 10 games, 10 cover jobs and a cache row. An anonymous user can burn the IGDB quota and grow the database. | Require a session for IGDB-backed refreshes, add a per-IP limiter, and cap `q` at 100 characters. |
| S3 | Med | Signup is always open, with no switch. | Add `CATO_SIGNUP=open\|invite\|closed`. Default to `invite` once P4 lands, `closed` until then. |
| S4 | Med | Login throttling is per IP only. There is no per-account backoff, and no trusted-proxy setting. | Add a per-email exponential backoff and `CATO_TRUSTED_PROXIES`. |
| S5 | Med | The rate-limiter map is never evicted (B2). CSV export doesn't neutralize formula injection (B3). There is no limit on note length, tag count or tag length (B4). | Run a sweep job. Prefix cells starting with `=+-@\t\r` with `'`. Limit notes to 10k characters, 32 tags and 40 characters per tag. |
| S6 | Low | Directory listing is on (`GET /js/` lists files). | Wrap `http.FileServer` so directories return 404. |
| S7 | Low | CSRF tokens are compared with `strings.EqualFold`. | Use `subtle.ConstantTimeCompare`. |
| S8 | Low | Signup returns 409 `email_taken`, and login skips bcrypt for unknown emails. Both reveal whether an account exists. | Return a generic error, and run a dummy bcrypt compare for unknown emails. |
| S9 | Low | `DELETE /api/me` doesn't ask for the password again. | Require the password, or a recent sign-in for OAuth-only accounts. |
| S10 | Low | The IGDB client reads its token without the lock (data race). A 401 doesn't retry. `io.ReadAll` is unbounded. There is no 429 backoff. | Fix all of these and add tests under `-race`. |
| S11 | Low | The "Sign in with Google" button is shown when OAuth isn't configured. | Expose `google_enabled` from `/api/config`, hide the button when it's false, and render callback errors as HTML. |

### 2.3 Performance quick wins

| # | Item | Fix |
|---|---|---|
| F1 | Static JS and CSS are not compressed. `gzipMiddleware` only compresses `/api/` (`server.go:112`). AGENTS.md says otherwise. | Gzip text assets, keeping Range requests safe, or precompress them at startup and serve the cached copy. This cuts about 300 KB to about 75 KB. Update AGENTS.md. |
| F2 | There is no `library_items(game_id)` index. The cover worker scans every user's library twice a second, and `ON DELETE CASCADE` from games is slow. | Migration: `CREATE INDEX idx_library_game ON library_items(game_id)`. |
| F3 | `games_fts_au` fires on **every** update to games: cover path, refresh markers, popularity. It deletes and reinserts the FTS row each time. | Migration: recreate the trigger as `AFTER UPDATE OF normalized_name`. Also skip platform rewrites when nothing changed. |
| F4 | Search refreshes queue a cover job for **every result**. AGENTS.md forbids this, and `cover_jobs` is never pruned. | Queue cover jobs only when a game is added to a library. Prune the table. Replace the 500 ms poll with a wake channel. |
| F5 | Sorting by `available_platform` runs `GROUP BY` over all of `game_platforms` on every request. Suggestions do a full scan and sort of the catalog. | Limit the platform subquery to the user's library. Keep a precomputed "popular" table for suggestions. |
| F6 | The library default sort uses a temporary B-tree. There is a COUNT and an OFFSET on every page. | Add a `(user_id, updated_at DESC)` index, use keyset pagination, and count only on the first page. |
| F7 | `ANALYZE` / `PRAGMA optimize` never runs. The DSN has no `cache_size`, `mmap_size` or `temp_store`. | Run `PRAGMA optimize` daily and at shutdown. Tune the pragmas, and open the read pool as `query_only`. |
| F8 | Typeahead materializes every FTS match. Two-character queries do a `LIKE '%q%'` scan. | Cap the candidates (500, ordered by popularity) and use prefix matching for queries under 3 characters. |
| F9 | A capture-phase, non-passive `touchstart` listener on `document` delays every scroll. | Make it passive, and close popovers on `pointerdown` instead. |
| F10 | Every API response is `no-store`, with no ETag. | Add ETags to `/api/v1` responses keyed on a per-user library revision (P1), and make catalog responses cacheable. |

**P0 exit criteria:**
- Every item above has a test.
- `make release-check` is green.
- Cold load of the app shell (HTML, JS, CSS and API calls) is under 120 KB transferred. Cover images are measured separately: on a phone the grid lazy-loads about 30 covers (~130 KB), which only smaller cover sizes (P3 `srcset`) can reduce. *(Clarified during P0: F1's own estimate, 406 KB − 300 KB + 75 KB ≈ 180 KB, could not reach 120 KB including images.)*
- The service-worker `CACHE_NAME` is bumped.
- `make run` and every Playwright project run over HTTPS, and the HTTPS-only features (service worker, WebAuthn) work in tests with no setup on the machine (§2.4).

### 2.4 HTTPS everywhere: dev, test and production

**Why (verified 2026-10-13).** I ran the same Cato container under Playwright in three ways:

| Origin | Secure context | Service worker | WebAuthn | Camera | `crypto.subtle` |
|---|---|---|---|---|---|
| `http://127.0.0.1` (today's E2E setup) | yes | yes | yes | yes | yes |
| `http://cato.lan` (non-loopback, like any real deployment) | **no** | **no** | **no** | **no** | **no** |
| `https://cato.test` (throwaway certificate, set up as described below) | yes | yes | yes | yes | yes |

Web Push needs the service worker. WebAuthn also refuses IP addresses as the relying-party ID. Google OAuth only accepts HTTPS redirect URIs (or `localhost`). **So Cato is an HTTPS app.** Plain HTTP is not supported, except for an optional redirect listener.

**Server changes (P0):**
- **Native TLS.**
  - Add `CATO_TLS_CERT_FILE` and `CATO_TLS_KEY_FILE` and serve with `ListenAndServeTLS`: TLS 1.2 minimum (1.3 preferred), HTTP/2 automatic.
  - Optional `CATO_HTTP_REDIRECT_ADDR` answers plain HTTP with a 308 redirect to HTTPS.
- **Development certificates with no tools.**
  - `CATO_DEV_TLS=1`, the default for `make run`, generates a self-signed ECDSA P-256 certificate at startup using Go's `crypto/x509` when none exists. No openssl and no mkcert.
  - The certificate is valid for 30 days and saved under `data/dev-tls/` with mode 0600.
  - Its names cover `localhost`, `127.0.0.1`, `::1`, `cato.test` and the machine's own hostname and LAN addresses.
  - A new `cato dev-cert --out DIR` subcommand writes a certificate and key and prints the SPKI SHA-256 fingerprint for the test harness.
- **Strict settings when `CATO_BASE_URL` is `https://`:**
  - The `Secure` cookie flag is always set.
  - Cookies use the `__Host-` prefix (`__Host-cato_session`). The prefix requires `Secure`, `Path=/` and no `Domain`, so a sibling subdomain can't plant or overwrite the cookie. Existing sessions end once at upgrade, which is acceptable.
  - HSTS (`max-age=31536000`; no preload by default).
  - `upgrade-insecure-requests` in the CSP.
  - `Permissions-Policy` (`camera=(self)`, everything else off).
  - Startup refuses a non-HTTPS `CATO_BASE_URL` unless `CATO_ALLOW_INSECURE=1` is set, which is meant only for the unit tests that need it.
- **Behind a reverse proxy:** `CATO_TRUSTED_PROXIES` (a list of CIDR ranges) controls when `X-Forwarded-For` and `X-Forwarded-Proto` are trusted. This also fixes the shared rate-limit bucket (S4).

**Testing over HTTPS with no machine setup** (prototyped and verified here):
- The Playwright config, when it loads, runs `go run ./cmd/cato dev-cert --out <tmp>` to get a certificate, a key and the SPKI fingerprint. `scripts/e2e-server.sh` then starts Cato with TLS on `127.0.0.1:7180`.
- **Chromium projects** use these launch arguments:
  - `--host-resolver-rules=MAP cato.test 127.0.0.1`, with base URL `https://cato.test:7180`. This is a non-loopback name, so loopback-only assumptions get caught.
  - `--ignore-certificate-errors-spki-list=<spki>`.

  Verified: secure context, and the **service worker registers**. With `ignoreHTTPSErrors` alone, Chromium refuses to register the worker ("An SSL certificate error occurred when fetching the script"), so the SPKI pin is required.
- **WebKit projects** use `ignoreHTTPSErrors: true` with base URL `https://127.0.0.1:7180`. Verified: secure context, and the service worker registers, even on a non-loopback IP and with the iPhone profile.
- Nothing is installed in any trust store, and no domain, mkcert or Tailscale is needed, locally or in CI.
- **Go tests** use `httptest.NewTLSServer` for redirect, HSTS, `__Host-` cookie and proxy-header tests.
- **Passkeys E2E:** the Chromium virtual authenticator (CDP `WebAuthn.enable` and `WebAuthn.addVirtualAuthenticator`) covers registration, sign-in and removal.
- **Push E2E:** the Go sender is tested against a local fake push endpoint that checks the VAPID JWT and `aes128gcm` payload encryption. The service worker's `push` handler is triggered with CDP `ServiceWorker.deliverPushMessage`.
- Update `platform.spec.ts` (cookie attributes now `Secure` with the `__Host-` prefix), `scripts/e2e_server_test.py` and the e2e README.
- **Optional, never required:** `docs/dev-https.md` explains how to make a real phone trust the development server (mkcert, or `tailscale serve`) for hands-on device testing.

**Production on a real domain (later).**
- Put Cato behind a reverse proxy with a Let's Encrypt certificate, such as Caddy or the Synology reverse proxy. Alternatively, use native TLS with a certificate the proxy or ACME client maintains.
- Using a real domain may make Cato reachable from the internet, so **design for that now**:
  - S2–S10 are required before production;
  - plus invite-only signup, per-account login throttling, the trusted-proxy setting, backups kept off the NAS, and session/device management.
- Google sign-in works once the real domain exists. In development, Google accepts `https://localhost` redirect URIs.
- **Interim (optional, owner's choice):** `tailscale serve` to try the PWA on real phones before the domain exists.

**Offline away from home.** With HTTPS, the service worker plus the IndexedDB library (P1) let the installed app **open without a connection**. You can browse your library and **log sessions, which are queued and sync when you're back online.** This becomes a headline feature, not a fallback.

### 2.5 P0 implementation notes (2026-10-14)

Where the implementation differs from the tables above, and why:

- **HTTPS (§2.4).** HSTS is skipped for `CATO_DEV_TLS` certificates: HSTS applies per host, not per port, so it would force HTTPS on every other `localhost` project. `Permissions-Policy` is sent in every mode. Certificate files are reloaded when they change, so an ACME client can renew them without a restart. Serving TLS and HTTP/2 makes the Go 1.26.8 `net/http`/`crypto/tls` advisories reachable, so the toolchain moved to 1.26.9. The legacy plain-HTTP NAS instance needs `CATO_ALLOW_INSECURE=1` to keep starting.
- **#3** also opens writer transactions with `_txlock=immediate`, so a read-modify-write can't fail late with `SQLITE_BUSY_SNAPSHOT`.
- **Extra bugs found while fixing §2.1:** on desktop, the toolbar's Status control painted over the open search dropdown (a flex item's `z-index`), blocking quick-add. Picking a Filters tag suggestion also closed the panel (the option removes itself, so the outside-click check missed it). On WebKit the suggestion list rendered under the sticky footer and hid itself on scroll.
- **S3.** The default is `closed`, but every mode allows the first account on an empty server, so fresh installs can bootstrap. `invite` behaves like `closed` until P4 adds invites. Google sign-up follows the same rule.
- **S10.** The token race was invisible to the race detector with a real `http.Transport` (its connection-pool mutex adds a happens-before edge). The regression test uses an in-process `RoundTripper` without shared state.
- **F5.** Suggestions use a partial index that matches their `ORDER BY` (`WHERE local_cover_path != ''`) instead of a precomputed table. It is an index walk with an early stop, just as cheap, always current, and needs no refresh job.
- **F7.** The bundled SQLite is 3.41, which predates `PRAGMA optimize=0x10000`. `db.Optimize` runs a bounded `ANALYZE` (`analysis_limit=400`) when no statistics exist, then `PRAGMA optimize`.
- **F8.** Queries under 3 characters now match name and alias prefixes only (index range scans), so `ha` no longer finds "Shadow of the Colossus".
- **F10.** Catalog responses (`/api/games/{id}`, `/api/platforms`) have ETags and a short public max-age. Library ETags wait for P1's `library_rev`.
- **Test harness.** Headless Chromium intermittently crashes (SIGSEGV) when closing a context that used a CDP virtual authenticator, so the WebAuthn test runs in its own browser.

**Measured cold load** (iPhone 13 emulation, Chromium, empty cache, service worker blocked, 53-item library, 70 seeded titles with generated covers):

| Route | Before (baseline `fe09adb`, HTTP) | After P0 (HTTPS, HTTP/2) |
|---|---|---|
| `#library`, total | 35 requests, 488.6 KB (shell 348.5 KB, covers 140.1 KB) | 35 requests, 219.5 KB (shell 91.9 KB, covers 127.6 KB) |
| `/` (Playing), total | 22 requests, 404.5 KB (shell 345.7 KB) | 22 requests, 144.4 KB (shell 89.7 KB) |

---

## 3. Product vision

### 3.1 Who it's for and what they're trying to do

Cato is for people who play games and want a **private, fast, attractive** record of what they play, what they thought of it, and what's next. Sharing happens only with the people they invite. The way people use it is **short visits**: open, log, close.

| Situation | What they want | What Cato must do |
|---|---|---|
| "I just played for an hour." | Log the time | ⊕ → game (pre-filled with what they're playing) → +1h. **2 taps.** |
| "I just beat it!" | Mark it done, rate it, maybe write a few words | Finish → stars → (optional review) → done. Undo is available. |
| "A friend mentioned a game." / "I saw a trailer." | Save it for later | Share sheet → Cato → Wishlist. Or ⊕ → type → Want. |
| "What should I play tonight?" | A short list of good options | Up Next: backlog filtered by time-to-beat, owned platform and mood. "Surprise me." |
| "Is it out yet? Is it on sale?" | Hear about it at the right moment | Release-day and price-drop alerts for the wishlist (push or email). |
| "What did I play this year?" | Look back | Year in Review, stats, a calendar diary. |
| "What are my friends playing and what did they think?" | Stay in touch | An activity feed, their reviews shown first on game pages, shared lists. |

### 3.2 What the market teaches (details in §9)

- **Backloggd** (the leader, built on IGDB) has proven the model: journal and playthroughs, lists, a feed, half-star ratings and a personalized Coming Soon.
  - Its most-requested features are still unshipped: native or mobile apps (20K and 14K votes), **Steam import (19K)**, **recommendations (17K)**, more game information, **CSV export (7.3K)** and advanced filters.
  - It charges for yearly recaps and all-time stats.
- **Infinite Backlog** imports from every store, but updating a status is "a flood of text boxes and sliders". This is the anti-pattern to avoid.
- **Letterboxd** has the logging pattern to copy: a one-tap action row (status, heart, ½-star strip) plus an optional "Log…" sheet where every field is optional. It also charges for stats and for watchlist-availability alerts.
- **The StoryGraph** shows that an **Up Next queue capped at 5**, mood and pace tags, and half- or quarter-star ratings win over people leaving Goodreads.
- **Gaps nobody fills well:**
  - a fast mobile experience;
  - Steam import together with **export**;
  - per-day session logging;
  - automatic status changes;
  - a "what to play next" engine;
  - wishlist price and availability alerts;
  - estimates based on your own pace ("~3 weeks at your pace").

**Cato's angle:** the fastest logger available, with free stats and a free Year in Review, private by default, your data on your own hardware, and full import and export.

### 3.3 Design principles

1. **Two taps to log.** Every frequent action can be done from Home or the ⊕ sheet in two taps. Anything longer needs a reason.
2. **Undo, don't ask.** Never show "Are you sure?" for routine actions. Do the action at once, and show a 6–8 second undo toast that rolls it back on the server.
3. **The game page is the hub.** Every game shown anywhere opens the same page, and every action on a game is also available on that page.
4. **Fast because it's local.** The user's library is cached on the device, so library search, filtering and sorting are instant and work offline. The server is the source of truth. Writes are optimistic and queued.
5. **Rich data, calm layout.** Show the 3 facts that drive a decision (time-to-beat, platforms you own, rating). Everything else goes behind "More".
6. **Private by default.** A new account shows nothing to anyone. Sharing is opt-in, per user, and limited to this instance.
7. **One way to do each thing.** One status control, one filter model, one toast system and one sheet component.

### 3.4 New information architecture

Bottom navigation on phones, and a left sidebar on desktop (1024 px and wider):

```
┌──────────────────────────────────────────────┐
│  🔍 Search your library and all games  (/)   │  ← one search box, always visible
├──────────────────────────────────────────────┤
│                    (view)                    │
├────────┬─────────┬─────────┬────────┬────────┤
│  Home  │ Library │   ⊕     │Discover│  You   │
└────────┴─────────┴─────────┴────────┴────────┘
```

- **Home** (`#home`, also the empty hash):
  - Now Playing cards with one-tap time chips.
  - A "Resume" session timer.
  - Up Next (3 suggestions from your backlog).
  - Coming Soon from your wishlist.
  - Friends' activity (P4).
  - First-run onboarding, shown only while the library is empty: import, pick a few favourites, set platforms.
- **Library** (`#library`):
  - Shelves as segmented chips: All · Playing · Backlog · Wishlist · Finished · Paused · Dropped.
  - An instant search over your own library.
  - Saved views, grid or list layout, and a density setting.
- **⊕ Log**, the centre button, also `L` on the keyboard:
  - A bottom sheet. Search comes first; with an empty query it shows your Playing and recently touched games.
  - Each row has chips: +15m · +30m · +1h · +2h · Finish · status.
- **Discover** (`#discover`): Trending, Coming Soon on your platforms, Popular with friends, Because you liked X, Short & great, Hidden gems. Full catalog search results also live here.
- **You** (`#you`): Profile (P4), Stats, Diary, Year in Review, Lists, Goals, Settings, Import/Export.

**A note on Nabu parity.** AGENTS.md records 4-tab parity with Nabu (Library / Playing / Stats / Settings). This plan replaces it:
- Playing becomes Home.
- Stats and Settings move under You.
- The ⊕ button and Discover are new.

Keyboard shortcuts become `1` Home, `2` Library, `3` Discover, `4` You, `L` Log and `/` Search. AGENTS.md needs updating in the same PR (see the open decisions in §12).

### 3.5 Key screens

**Game page** (`#game/<id>`). A full-height sheet on phones, a page with a side column on desktop.
- **Hero:** artwork backdrop, cover, title, year, developer and publisher, platforms (the ones you own highlighted), time-to-beat (Main / +Extras / 100%), and the IGDB critic and user rating.
- **Your row:** a status segmented control (one tap), ★★★★½ (tap or drag), a "Log time" chip row, a "Write a review" link, and your playthroughs as a timeline (started → sessions → finished, with completion type).
- **Friends:** their status and rating chips, and their reviews first.
- **About:** summary (expandable), genres, themes, modes, perspectives, series/franchise, DLC and editions, remakes and ports.
- **Media:** a screenshot carousel and the trailer (a privacy-preserving click-to-load YouTube embed, with the CSP widened only for `frame-src https://www.youtube-nocookie.com`).
- **More like this** (IGDB `similar_games` mixed with tag co-occurrence on this instance).
- **Where to get it:** store links, plus the price from IsThereAnyDeal (P5).

**Finish sheet**, which opens when you mark a game done from anywhere:
```
  You finished Hades 🎉
  ☆☆☆☆☆   (tap or drag, half stars)
  [Main story] [Main + extras] [100%] [No ending]
  Finished: Today ▾      Hours: 38h (+ add)
  [ Write a quick review… ]                [Done]
                                           Skip
```

**Log sheet (⊕):**
```
  🔍 Log…                     (focus is here, keyboard is up)
  PLAYING
  [cover] Hades II      38h   [+30m][+1h][+2h][✓ Finish]
  [cover] Silksong      12h   [+30m][+1h][+2h][✓ Finish]
  RECENT
  [cover] Balatro    Backlog  [▶ Start] [Want] …
  typing "zel" → mixed results: your library first, then the catalog,
  each row with one-tap shelf chips (text labels, not colour-only icons)
```

**Library:**
- Phone: 3-column covers by default (2-column optional), or a dense list view with a cover thumbnail, title, status, ★ and hours.
- Sticky shelf chips. The search box filters your own library instantly; a "Search all games" row at the bottom hands off to Discover.
- Long-press, or right-click on desktop, opens a quick-action menu: status, rate, log time, add to list, remove.
- On desktop, hover reveals the actions, and `j`/`k`, `s`, `r`, `l` and `Enter` work as keyboard shortcuts.

### 3.6 Data-model concepts (what the user sees)

| Concept | Today | Proposed |
|---|---|---|
| **Status** | wishlist, backlog, playing, completed, abandoned | **Want**, **Backlog**, **Playing**, **Paused** (new), **Finished**, **Dropped**. Display labels change; database values stay the same, plus `paused` is added. |
| **Completion** | — | On a playthrough: `main`, `main_extras`, `completionist`, `endless` (games with no ending), `dropped`. |
| **Rating** | integer 0–100 (0 = unrated) | Stored as 0–100 as today, so existing data needs no migration. The **default display is 5 stars with half steps** (value ÷ 20, rounded to the nearest half). A setting offers 10-point or 100-point display. New 5-star ratings are stored as multiples of 10. |
| **Playthroughs** | one `started_at` / `completed_at` pair, overwritten when you replay | A `playthroughs` table, so replays keep their own history. |
| **Play sessions** | one aggregate `playtime_minutes`; `+time` leaves no record | A `play_sessions` table (date, minutes, optional note). The aggregate is kept in step by triggers. This gives you a diary, streaks and correct per-year stats. |
| **Reviews** | private `notes` only | A `reviews` table with a spoiler flag and visibility. Notes stay private. |
| **Lists** | tags only | `lists` (ranked or unranked, with a description and visibility). Tags remain private filters. |
| **Ownership** | `owned_platforms_json` and `medium` | Kept. Shown more prominently (it powers "playable now" filters). |

---

## 4. P1: Foundations

P1 is mostly invisible to users. It is what makes P2–P6 cheap and safe. Each item ships behind the existing UI until the new screens take over.

### 4.1 Backend restructure

**Packages.**
- `internal/library`: a typed `Item` struct and store, filter and sort builder, stats and export. This replaces the `map[string]interface{}` items and the 1,451-line `handler_library.go`, which currently holds the library domain inline.
- `internal/catalog`: renamed from `games`. The five near-duplicate backfill loops become one job framework.
- `internal/journal` (playthroughs, sessions), `internal/social` (P4), `internal/integrations/{igdb,steam,itad}`.
- `internal/http/v1`: thin handlers only.

**API v1** under `/api/v1`, served alongside the old routes until the frontend has moved over, then the old routes are removed:
- snake_case throughout, with consistent `game_id` / `game` objects. No internal fields such as `normalized_name` or `platforms_json`.
- Collections return `{ "items": [...], "next_cursor": "...", "total": n }` (total only on the first page).
- Errors keep the current `{error, message}` shape and add `fields` for validation errors.
- Mutations return the updated resource plus `library_rev`.
- `ETag` / `If-None-Match` on every GET. Library ETags come from a per-user `library_rev` counter, incremented by a trigger, so a 304 costs one indexed lookup.
- Use Go 1.22+ method patterns (`mux.HandleFunc("PATCH /api/v1/library/{id}", …)`) instead of method checks inside handlers.

**`/api/v1/library/index`** returns a compact array covering the whole library: `id, name, status, rating, minutes, updated, platforms owned, tags, release year, ttb_main`. It is about 60 bytes per item, so 5,000 items is about 300 KB raw or about 60 KB gzipped. It accepts `?since=<rev>` and then returns only the changes. This powers instant search, filtering and sorting on the device, and offline use.

**One job runner.** Background jobs (IGDB refresh, cover download, cleanup sweeps, digests, alerts) share one runner with:
- context cancellation and jitter;
- persisted `next_run_at`;
- **two IGDB priority lanes**: interactive search before background refresh. The limiter becomes a 4 requests/second token bucket, matching IGDB's documented limit (the current limiter allows 1/s).

**Observability.**
- Structured request logs (`slog`) with request ID, user ID hash, route, status and duration.
- A slow-query log (over 50 ms).
- `/metrics` in Prometheus text format, bound to the private network or protected by a token.
- Startup logs the configuration without secrets.

**Database migrations (v17+):**
```sql
-- shelf/journal
ALTER TABLE library_items ADD COLUMN current_playthrough_id INTEGER;
-- status gets 'paused' (table rebuild because of the CHECK constraint)
CREATE TABLE playthroughs (id INTEGER PRIMARY KEY, user_id TEXT NOT NULL, game_id INTEGER NOT NULL,
  platform_id INTEGER, started_on TEXT, finished_on TEXT, completion TEXT, minutes INTEGER NOT NULL DEFAULT 0,
  rating INTEGER, note TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (user_id, game_id) REFERENCES library_items(user_id, game_id) ON DELETE CASCADE);
CREATE INDEX idx_pt_user_finished ON playthroughs(user_id, finished_on DESC);
CREATE TABLE play_sessions (id INTEGER PRIMARY KEY, user_id TEXT NOT NULL, game_id INTEGER NOT NULL,
  playthrough_id INTEGER REFERENCES playthroughs(id) ON DELETE CASCADE, played_on TEXT NOT NULL,
  minutes INTEGER NOT NULL CHECK (minutes > 0 AND minutes <= 1440), note TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'manual', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX idx_sessions_user_day ON play_sessions(user_id, played_on DESC);
CREATE TABLE user_state (user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  library_rev INTEGER NOT NULL DEFAULT 0, prefs_json TEXT NOT NULL DEFAULT '{}');
-- perf (also P0)
CREATE INDEX idx_library_game ON library_items(game_id);
CREATE INDEX idx_library_user_updated ON library_items(user_id, updated_at DESC);
```

**Backfill.** Each item with dates becomes one playthrough. Each item with `playtime_minutes > 0` gets one `source='legacy'` session dated `completed_at`, `started_at` or `updated_at`, so totals don't change. Run it in a transaction and test it against a copy of the production database (restored from `make db-backup`).

### 4.2 Frontend architecture (still no build step)

- **Stack (decided).** Vendored **lit-html 3** for rendering, plain ES modules, and no build step. It was chosen against the owner's criteria:

  | Criterion | How the stack meets it |
  |---|---|
  | **Secure** | Values in tagged templates are escaped automatically; only explicit `unsafeHTML` (banned by lint) bypasses it. That removes escaping-by-convention across the roughly 60 `innerHTML` sinks. It works under `script-src 'self'` and is compatible with **Trusted Types**, so the CSP can add `require-trusted-types-for 'script'` where browsers support it, making any leftover `innerHTML` sink fail loudly. |
  | **Maintainable** | It's one small, widely used library (about 7 KB gzipped) with a stable API. No virtual DOM, hooks or build chain to keep up with. Modules are split by feature (below). There's no shadow DOM, so the design tokens in one stylesheet apply everywhere. |
  | **Easy for users** | Efficient updates mean only what changed re-renders: no "Loading…" flashes, no lost scroll position, fast interactions. |
  | **Testable** | Logic lives in pure modules with no DOM: store, sync and outbox, router, filters and sorting, formatting, rating conversion, Up Next scoring. These run under `node --test` with no dependencies. Views are thin functions from state to template, tested in a real browser (below). |

  **Alternatives considered:**
  - Preact + htm: also good, but hooks bring re-render subtleties, and htm isn't standard template syntax.
  - Full Lit components: shadow DOM works against one global token stylesheet.
  - A hand-written renderer: it would be security-critical code that we would own forever.

  **Vendoring policy:**
  - Keep a pinned version with its licence file in `js/vendor/`.
  - A Go test checks the file's SHA-256 against `js/vendor/CHECKSUMS`.
  - `scripts/vendor-update.sh` downloads a new version from npm and prints the diff for review.

  **Guardrails:** a small Node lint script, run in `make test-js`, fails on `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write` and `unsafeHTML` outside `vendor/`.

  **Test layers:**
  1. `node --test` for pure modules, run on every commit in under 2 seconds.
  2. A **component gallery** at `/#dev/ui`, only served when `CATO_DEV=1`. It renders every component in each state. Playwright uses it for per-component interaction tests, axe checks and visual snapshots in light and dark.
  3. End-to-end journeys with tap budgets.
- **Modules:**
  ```
  js/core/      api.js (transport), store.js (library index + revision + events + outbox),
                router.js (hash routes incl. filter state: #library?shelf=backlog&q=zel),
                format.js (status/rating/hours/date: single source), dom.js (delegation, cover fallback)
  js/ui/        sheet.js (<dialog> + inert, focus restore, swipe-dismiss), toast.js (with undo),
                stars.js, chips.js, menu.js, segmented.js, empty.js, skeleton.js
  js/features/  home/, library/, log/, game/, discover/, you/ (stats, diary, settings)
  ```
  `home` and `log` load eagerly. Everything else loads with dynamic `import()` when its route is first visited.
- **Store and local library index.** The library index is kept in IndexedDB, keyed by `library_rev`. On startup the app renders from IndexedDB straight away, then syncs with `?since=`. Mutations are applied optimistically, re-render only the affected card, and go into an **outbox**. The outbox is retried when the device reconnects, and through Background Sync where the browser supports it. This makes the "library works offline" promise true.
- **Automatic asset versioning, replacing the manual `CACHE_NAME` bump.**
  - At startup the Go server hashes every file under `web/static`.
  - It serves `index.html` with `?v=<hash>` on each asset URL, via an import map for modules.
  - Versioned URLs get `immutable` caching.
  - The service worker precache list comes from `/asset-manifest.json`, which the server computes at startup.

  This is a runtime step, not codegen, so the "no codegen, no build step" rule still holds. It removes a manual release step that has gone wrong before (the review found `CACHE_NAME` stale).
- **Service worker.** App shell and assets use precache plus stale-while-revalidate. Navigations are network-first with a fallback to the cached shell, so the installed app opens offline. API GETs are network-first with a short timeout and a fallback to the IndexedDB data. The covers cache becomes an LRU capped at about 2,000 entries.
- **Performance budget, checked in CI** by a small Node script:
  - initial JS 60 KB or less gzipped;
  - CSS 20 KB or less gzipped;
  - 12 or fewer requests before the first meaningful paint;
  - LCP under 1.0 s;
  - INP under 100 ms on the Playwright "Moto G" profile with 4× CPU throttling.

### 4.3 Design system

- **Tokens on `:root`:**
  - colour roles: `--bg`, `--surface-1..3`, `--text`, `--text-muted`, `--border`, `--accent`, `--accent-contrast`, `--success`, `--warning`, `--danger`;
  - one variable per status: `--status-want`, `--status-backlog` and so on;
  - a type scale (12/14/16/18/22/28/36), a spacing scale on a 4 px base, radii (6/10/16/999), elevation (3 levels) and motion (120/200/320 ms, standard easing).
- **Themes:** Light, Dark and **System** (the default, following `prefers-color-scheme`). `<meta name="theme-color">` is updated per theme.
- **Palette.** Replace the stock ColorHunt palette (`#1a1a2e/#16213e/#0f3460/#e94560`) with a neutral slate base and one confident accent. Every text/background pair must reach **4.5:1** contrast, including status pills (several currently measure 2.2–3.8:1).
- **Icons.** One inline SVG sprite based on Lucide (ISC licence), replacing the mix of Unicode glyphs and inline SVG.
- **Components.** Exactly one each of button (primary / secondary / ghost / danger × sm/md), chip, segmented control, sheet, menu, toast, stars, tabs, empty state, skeleton and card. A living style guide at `/#dev/ui`, available only when `CATO_DEV=1`.
- **CSS.** Rewrite `app.css` (3,747 lines, 41 `!important`) into layered files: `tokens.css`, `base.css`, `components.css`, `views.css`. Build mobile-first with `min-width` breakpoints at 600 and 1024 px plus container queries for cards. The target is about 1,500 lines with no `!important` outside utilities.
- **Accessibility:**
  - Cards become focusable `<a>` or `<button>` elements.
  - The `aria-live` region on the whole of `<main>` is replaced by one dedicated `role=status` region.
  - Sheets use `<dialog>`, make the background `inert` and return focus to whatever opened them.
  - Every touch target is at least 44 px.
  - Status is never shown by colour alone.
  - Playwright runs axe-core with zero serious or critical violations as a CI gate.
- **Desktop.** At 1024 px and wider: a left sidebar, a library grid sized to the screen (6–8 columns), and the game page as a two-column layout. `Cmd/Ctrl-K` opens a command palette (search, log, go to).

### 4.4 P1 exit criteria

- The new `/api/v1` has contract tests; the old endpoints still work.
- Migrations are tested against a production-shaped copy of the database, with an integrity check.
- The store, outbox and router have unit tests (run with `node --test`; the repo already uses Node scripts).
- Performance budgets and the axe gate are in CI.
- AGENTS.md is updated: architecture, the end of the Nabu tab parity, asset versioning replacing manual `CACHE_NAME`, and gzip scope.

### 4.5 P1 implementation notes (2026-10-14)

Where the implementation differs from §4.1–4.4, and why:

- **Packages.** The journal (playthroughs, sessions, the timer) lives in `internal/library`, not a separate `internal/journal`: every library write opens or closes a playthrough or adds a session in the same transaction. `internal/social` and the Steam/ITAD integrations arrive with P4/P5.
- **Schema (v18).** `playthroughs.platform` is a platform name (ownership is stored by name), not an ID. `play_sessions.source` is `manual`, `timer`, `legacy`, `adjust`, `steam` or `import`; `adjust` entries may be negative, so "set total to 10h" keeps the total equal to the sum of sessions. Sessions also reference their library item, so removing a game removes its journal. Added `undo_log` (server-side undo for 15 minutes), `library_tombstones` (for `?since=` deltas) and a `jobs` table (the runner's schedule).
- **Library ETags** use `library_rev` as planned, which also replaces the short-term #5 fix: the store re-syncs by revision.
- **Legacy routes.** `/api/library*` stays until the new screens replace the old ones in P2; its writes already go through `internal/library`.
- **Frontend.** P1 ships the libraries, design system, store, outbox, service worker and asset versioning; the screens switch over in P2, together with the performance budget and axe gates (they measure the new screens).
- **Migration test.** Tested against a production-shaped fixture (300k games, 5k-item library, §11) instead of a copy of the production database, which this work may not read. `serve` now snapshots the database before any schema upgrade, so a bad upgrade can be rolled back with the restore procedure.
- **Typeahead.** The fixture caught typeahead at 216 ms p95. It now ranks a bounded candidate set and drives the page query from the candidates: about 11 ms p95.
- **Toolchain.** `modernc.org/sqlite` moved to v1.60.1 (SQLite 3.53), which brings `PRAGMA optimize=0x10002`.

---

## 5. P2: The core loop

This is the part the user feels. Ship it in this order:

1. **Shell and navigation.** New tabs, sidebar on desktop, one search box, the ⊕ button, keyboard shortcuts and the command palette.
2. **Game page.** Uses what the catalog already has. P3 adds the rest.
3. **Status control, stars, Log sheet and Finish sheet**, with undo toasts everywhere.
   - Log chips: +15m / +30m / +1h / +2h and **Custom** (a stepper with a minutes/hours toggle, never a `<select>` with hundreds of options).
   - **Session timer:** "Start session" stores `started_at` on the server, shows a running pill in the header on every device, and "Stop & log" creates a session. If the app is closed it keeps running, and you're asked to confirm when you come back.
4. **Library 2.0.**
   - Search runs instantly against the local index (name, alias, tag, platform, notes). `$tag` and `@platform` become visible filter chips instead of hidden syntax.
   - Shelf chips. Sorting: recent, title, rating, hours, release date, time-to-beat, date added.
   - Saved views, list and grid layouts, density, multi-select bulk actions (status, add to list, remove).
   - Long-press quick actions.
5. **Home:** Now Playing, Up Next and Recent activity, plus the first-run onboarding: pick your platforms, add 5 games you love, import from Steam or CSV (P5).
6. **Stats and Diary under You:**
   - a calendar heatmap of sessions;
   - hours per month;
   - finished per year by completion type;
   - rating distribution;
   - top genres and platforms;
   - backlog age ("you've had 14 games in your backlog for more than 2 years");
   - "time to clear backlog" using time-to-beat data.
7. **Settings:** profile, theme, rating display, default platform(s), notifications, connected accounts, import/export, sessions and devices (list and revoke), delete account (password required).

**UX acceptance tests,** run in Playwright with a tap counter helper:

| Journey | Max taps from launch | Notes |
|---|---|---|
| Log +1h to a game you're playing | 2 | ⊕ → +1h, or Home card → +1h |
| Finish and rate | 3 | Finish → ★ → Done |
| Add a new game to Want | 3 + typing | ⊕ → type → Want |
| Find a game in your library | 1 + typing | search box → first result |
| Change status from the game page | 1 | segmented control |
| Undo any of the above | 1 | toast |

---

## 6. P3: Rich catalog and discovery

### 6.1 IGDB expansion

| Data | IGDB source | Used for |
|---|---|---|
| Genre, theme, mode and perspective **names** | `genres`, `themes`, `game_modes`, `player_perspectives`, synced as lookup tables like `platforms`. Today genres are stored as raw IDs and are **never displayed**. | Game page, filters, stats, "similar" scoring |
| Developer and publisher | `involved_companies.company.name` | Game page, search by studio |
| Series | `collections`, `franchises` | Series strip on the game page, "next in series" suggestions |
| Related games | `similar_games`, `dlcs`, `expansions`, `standalone_expansions`, `remakes`, `remasters`, `ports`, `bundles` | More like this, edition grouping (replacing the `version_parent`/`parent_game` heuristics) |
| Time to beat | `game_time_to_beats` (`hastily`, `normally`, `completely`, `count`) | Game page, Up Next, backlog hours, "short games tonight" |
| Release dates per platform and region | `release_dates` | Coming Soon, wishlist release alerts, correct dates per platform |
| Media | `screenshots`, `artworks`, `videos` | Game page hero and carousel. Cache only the hero image on disk; load the rest directly from the IGDB CDN at `t_screenshot_med` / `t_720p`. |
| Links | `websites` (official, Steam, GOG, eShop, PS Store, Xbox, Wikipedia, Discord…) | "Where to get it" |
| External IDs | `external_games` (Steam app ID, GOG ID and others) | Steam import matching, ITAD price lookup |
| Popularity | `popularity_primitives` / `popularity_types` | The Trending shelf. Replaces the hand-built `popularity_score`. |
| Age ratings, languages, multiplayer modes | `age_ratings`, `language_supports`, `multiplayer_modes` | Game page details: co-op and couch co-op badges |

**Mechanics:**
- Fetch details on demand when a game page opens, if the cached data is older than N days. That request uses the interactive lane.
- Refresh library games weekly through `multiquery` and `where id = (…)` batches of up to 500.
- A delta sync with `updated_at > last_sync` replaces the refresh-by-age loop.
- Store media as image IDs, not full URLs, and build the URL for the size each screen needs. Covers get `srcset` (`t_cover_small`, `t_cover_big`, `t_cover_big_2x`).
- Add `t_cover_big_2x` to the local cache for library games, to stay sharp on high-density screens.

### 6.2 Discover tab and "What should I play next?"

- **Shelves:** Trending (IGDB popularity); Coming soon on *your* platforms; New releases; Popular on this instance; Because you rated X highly (`similar_games` weighted by your ratings and owned platforms); Short & highly rated (time-to-beat under 8 h, rating 85 or more); Hidden gems (high rating, low popularity); Classics you haven't played.
- **Up Next.** A ranked pick from your backlog. The score combines:
  - owned on a platform you've recently used;
  - time-to-beat that fits the time you have (a chip row: "1h tonight", "a weekend", "a long haul");
  - affinity: genre, theme and developer matches against your high ratings;
  - backlog age;
  - "series continuity", when you've finished the previous game in the series.

  Show **3** picks with a short reason for each ("~9h · on Switch · you loved Celeste"). "Surprise me" spins among the top 10. Like StoryGraph, the user can pin up to 5 games to an **Up Next queue**.
- **Estimates at your pace.** Compare your logged hours with IGDB `normally` on finished games to get a personal multiplier, then show "≈ 3 weeks at your usual pace" on game pages and Up Next. No surveyed competitor does this.
- **Missing games and editions.**
  - Users can create a **custom entry** (title, platform, year, optional cover upload) for demos, regional versions, homebrew or games IGDB doesn't have. It is stored in the same `games` table with `source='custom'` and IDs from a reserved negative range, so all library features work unchanged.
  - An admin can later merge a custom entry into an IGDB match.
  - **Edition combining**, as Backloggd does: entries roll up to the base game through `version_parent`, but the edition you played is recorded on the playthrough.
- **Search results page:** facets for platform, genre, year, time-to-beat, rating and "in my library". Results always show your status badge.

---

### 6.3 P3 implementation notes (2026-10-14)

- **Data model:** migration v20 adds `lookups`, time to beat on `games` (minutes), and tables for details, relations, release dates, companies, series, external IDs and PopScore. Media, links, ratings, languages and multiplayer are one JSON row per game (`game_details`), because they are only ever read whole on the game page.
- **Freshness:** a first visit to a game page fetches its details (up to 3 s, then the page renders without them). Jobs refresh library games weekly, run an `updated_at` delta sync every 6 hours, and sync lookups weekly and PopScore daily. The delta sync replaces the old refresh-by-age loop. IGDB webhooks were not used, because they need a publicly reachable URL, which the NAS doesn't have.
- **Editions roll up at the API:** adding an edition adds its base game and records the edition on the playthrough (`edition_game_id`), so stats and Up Next count the game once.
- **Wikidata is opt-in** (`CATO_WIKIDATA_LINKS=1`). It sends game names to a third party, so it is off by default. Crosswalk IDs use sources ≥ 1000, which IGDB refreshes leave alone.
- **Search facets:** genre and maximum time to beat join the existing filters; `max_ttb` is in hours.
- **Not done:** OpenCritic and SteamGridDB (marked optional in §9).

## 7. P4: People (limited to this instance, private by default)

For a household or friends server. **Decided:** social features are limited to this server and there is no federation. Even if the production domain is reachable from the internet, nothing is visible without an account, and accounts are invite-only. The "instance" visibility level means everyone with an account on this server.

- **Accounts:**
  - `username` (unique, used for `#u/<username>`), `bio`, avatar (uploaded or Gravatar; uploads stored in `data/avatars`, re-encoded, with EXIF removed).
  - `role` (`user` or `admin`). An admin page manages users, invites, sign-up mode, IGDB status and job health.
  - **Invites:** an admin or user creates a link valid for 7 days.
- **Privacy:**
  - per-user visibility: `private` (the default), `followers` or `instance`;
  - per-review visibility can override it;
  - spoiler flag;
  - blocked users.
- **Profiles:** favourite 4 games (Letterboxd-style), currently playing, recent finishes, stats summary, lists, reviews.
- **Reviews:** short or long text, spoiler flag, tied to a playthrough. Likes. Friends' reviews appear first on the game page. Moderation: report, and admin hide.
- **Lists:** ranked or unranked, with a description and per-entry notes. Shareable inside the instance. Suggested starters: "All-time favourites", "Games to play with my partner", "Comfort games".
- **Follows and activity feed.** Events: started, finished (with rating), reviewed, added to list, wishlisted.
  - Generated on write into an `activity` table.
  - Read with fan-out-on-read through `follows`, which is plenty at SQLite scale.
  - Grouped: "Alex logged 6h across 2 games".
- **Social on game pages.** "3 friends played this" avatars, the instance average rating with a histogram, and "Ask a friend" (a deep link to their review).

**Security work for P4:**
- authorization tests for every visibility combination;
- rate limits on writes;
- render user content only through lit-html (automatic escaping), keeping CSP and Trusted Types (`require-trusted-types-for 'script'`, worth trying once all `innerHTML` is gone);
- size limits on bio, review and list text;
- an audit log for admin actions.

### 7.1 P4 implementation notes (2026-10-14)

- **One visibility rule.** `internal/social` decides who sees what in one Go function (`CanSee`) and one SQL fragment (`visibleSQL`), and every query for someone else's content uses them. `TestAuthorizationMatrix` checks 96 combinations: 3 profile levels × 8 relationships (self, stranger, pending, follower, blocked either way, disabled owner, admin) × 4 item levels. Each combination is checked against profiles, reviews, lists, the feed, the game page community numbers and likes.
- **Admins get no read bypass.** Admins see reported text on the Reports page, but otherwise follow the same visibility rules as everyone else.
- **Sign-up modes:**
  - The admin page can override `CATO_SIGNUP`.
  - `closed` still accepts invites made by admins, which is how a closed server adds people.
  - `invite` accepts invites from anyone.
  - The first account on an empty server becomes the admin. On upgrade, migration v21 makes the oldest account the admin.
- **Invites make a mutual follow**, so a household is connected at once.
- **Activity is written with library writes** and carries the write's undo ID, so undoing a status change also removes it from friends' feeds. The feed groups a person's logging, adds, wishlists and additions to one list per day.
- **Trusted Types are enforced.** The CSP is `require-trusted-types-for 'script'; trusted-types lit-html cato-sw`. lit-html has its own policy. The only other sink is service worker registration, which has a policy that allows exactly `/service-worker.js`.
- **Avatars and Gravatar:** avatars are re-encoded (which drops EXIF) and served only to signed-in people. Gravatar is opt-in per account, because it loads an image from a third party.
- **Social notifications:** P4 creates them in the in-app inbox only, with an unread badge (also on the app icon via the Badging API). Push and email delivery, with per-kind settings that are off by default, come with P5.
- **"Ask a friend"** links to the friend's review on the game page. There are no direct messages.

---

## 8. P5: Imports, integrations and reach

### 8.1 Imports and exports (make it easy to arrive and to leave)

- **Steam.** The user enters a SteamID or vanity URL; the owner sets `STEAM_WEB_API_KEY`. Uses `GetOwnedGames` (`include_appinfo=1`, `include_played_free_games=1`).
  - Steam app IDs are matched to IGDB games through `external_games` (category Steam).
  - The import is a preview list with suggested statuses: any playtime and played in the last 30 days → Playing; more than 0 hours → Backlog (marked "played"); 0 hours → Backlog.
  - The user confirms before anything is written.
  - An optional daily playtime sync creates `play_sessions` with `source='steam'` from the change in `playtime_forever`.
  - Requires the user's Steam profile and game details to be public; the UI explains this.
- **CSV import** with column mapping and presets: Grouvee (it has an export), Infinite Backlog, a generic CSV, and Cato's own export. A HowLongToBeat preset depends on whether HLTB still offers an export *(unverified)*. Backloggd has **no export**, so offer a paste-your-profile helper that doesn't scrape, or document the community scripts.
  - Fuzzy title match uses the existing FTS and aliases plus the release year.
  - Uncertain matches go to a review queue ("Did you mean…?").
  - Imports are idempotent, and a whole import batch can be undone.
- **Export:** a full JSON export (library, playthroughs, sessions, reviews, lists, follows) and CSV, with formula-safe cells.
- **Other sources:** RetroAchievements (official API, user key) for retro progress, as an optional add-on. **PlayStation and Xbox imports are excluded** (owner decision). Console players use CSV, Steam, or quick manual adds.

### 8.2 Alerts and notifications

- **Wishlist release alerts** from IGDB `release_dates` on your platforms: a day-of notification, and an optional "next week" digest.
- **Price alerts** through IsThereAnyDeal for wishlist games that have Steam/GOG IDs: "Hollow Knight: Silksong is 25% off on GOG (historical low)". Settings: a target price or historical low; regions.
- **Delivery:**
  - **Web Push** with VAPID keys generated on first boot and stored in the database.
    - It needs HTTPS, which §2.4 makes standard everywhere.
    - The NAS needs outbound access to the browser push services (Apple, Google, Mozilla). It already makes outbound calls to IGDB.
    - Notifications reach the phone even when it's away from home.
    - On iOS, push works only for PWAs installed to the home screen (iOS 16.4+); the UI explains the install step.
  - An optional **email digest** if `CATO_SMTP_*` is configured.
  - An in-app inbox is always available.
- **Social notifications** (P4): new follower, like, reply. Off by default, each with its own setting.

### 8.3 Always within reach

- **Manifest shortcuts:** "Log time", "Search", "Up Next", "Wishlist". These work on Android and desktop Chrome only; iOS ignores them, and the ⊕ button covers the same need there.
- **Web Share Target** (`share_target` in the manifest). Sharing a Steam, PS Store, eShop, IGDB or YouTube link, or plain text, opens the ⊕ sheet pre-searched with the parsed title or app ID, so adding is one more tap. This works on Android; iOS Safari doesn't support it, so iOS users get a bookmarklet and a Shortcuts recipe instead.
- **App badge** (Badging API): number of unread notifications. Supported by iOS 16.4+ installed PWAs and desktop Chrome, but not Android Chrome.
- **Offline:** the full library and recent game pages are readable offline, and writes are queued in the outbox (P1).
- **Physical games:** a barcode scan to add (BarcodeDetector on Android, with a vendored ZXing-WASM fallback on iOS, where it is behind a flag).
  - UPC→title lookup through UPCitemdb's free tier (100 requests/day), or PriceCharting if the owner pays for it. The returned title is fuzzy-matched to IGDB.
  - Confirmed matches are cached in a `upc_games` table, so each barcode is resolved only once.
  - This is a nice extra, not on the critical path.
- **Login that lasts:**
  - Sliding 90-day sessions on trusted devices.
  - **Passkeys (WebAuthn)** as a passwordless option. This is the biggest improvement to getting in quickly, and it removes the need for a "forgot password" email on a server without SMTP. Passkeys need HTTPS and a hostname, because the relying-party ID can't be an IP address. Both are standard after §2.4.
  - Google sign-in works on the production domain and on `https://localhost` in development. Hide the button when it isn't configured (S11).
  - Admin-generated reset links as a fallback.
  - A list of sessions and devices.

### 8.4 P5 implementation notes (2026-10-14)

- **Imports always preview first, and every import can be undone.** Each source (CSV, Cato JSON, Steam, RetroAchievements) produces rows, and the matcher tries IGDB ID, then Steam app ID, then name and year. Rows it is unsure about wait for "Is this right?". Committing writes one batch that keeps a snapshot of every game it touched, so the whole import can be undone at any time, not just inside the 15-minute window.
- **Imports never overwrite and never repeat.** An imported game already in your library only gains what it lacked: rating, notes, platform, dates, tags, and more play time. Importing the same file twice changes nothing the second time.
- **Presets:**
  - Grouvee: detected by its `shelves` and `giantbomb_id` columns; ratings out of 5.
  - HowLongToBeat and Infinite Backlog: detected by column names, then mapped by synonyms *(their exact export columns are unverified)*.
  - Backloggd: has no export, so it is read through the generic mapping of community-script CSVs.
  - Cato's own CSV: the formula-safety apostrophes are removed again.
  - Anything else: the column-mapping editor.
- **The Cato JSON export** (version 2) adds reviews, lists, follows and profile. Re-importing it restores sessions for games that are new to the library, plus reviews and lists. Follows are kept only as a record, because people differ between servers.
- **Steam:** the import marks played-but-not-current games with a `played` tag on the Backlog shelf. The daily sync is opt-in and logs only play time that happens after the import.
- **Web Push and passkeys are implemented from the RFCs instead of with libraries.** Web Push encryption (RFC 8291 aes128gcm, RFC 8292 VAPID) is about 200 lines and matches the RFC 8291 test vector. Passkeys use a minimal CBOR decoder and accept any attestation without checking it; the CBOR decoder is fuzzed. This keeps the dependency list unchanged.
- **Notification delivery:**
  - Push delivery runs within seconds of a notification, through a job woken by social writes, and at least every minute.
  - A subscription is dropped on 404 or 410, or after five failures in a row.
  - The weekly email lists unread notifications from the past week.
- **Release alerts use the UTC day.** A game counts as released for you when a release date on one of your platforms (your default platforms, or platforms you own games on) falls on that day. With no platforms set, any platform counts.
- **Price alerts:**
  - They cover wishlist games that have a Steam app ID, using IsThereAnyDeal's Steam lookup and overview endpoints.
  - An alert fires on a cut of at least your minimum, or on a historical low.
  - Each deal is announced once.
  - The response fields are unverified beyond the documentation: if IsThereAnyDeal changes them, alerts stop and the job reports the error on the System page.
- **Barcodes:** the camera uses BarcodeDetector where it exists. Elsewhere, including iOS, you type the number. **Deferred:** the vendored ZXing-WASM fallback; typing the number covers iOS without adding a large WASM blob.
- **Share target:** a Steam or IGDB link to a game Cato knows opens that game's page; anything else opens the ⊕ sheet with the search filled in. iOS gets a bookmarklet in Help. Building this exposed a bug where `#log` closed its own sheet (which also broke the "Log time" shortcut); it is fixed.
- **Not built:** OAuth "waitlist" sync with IsThereAnyDeal, because polling covers the need; and PriceCharting, which is paid.

---

## 9. External data sources

Research as of 2026-10-13. Items marked *(unverified)* could not be confirmed from primary sources.

| Source | Access and limits | Terms | Value to Cato | Verdict |
|---|---|---|---|---|
| **IGDB v4** | Twitch client credentials. **4 requests/second, 8 open at once.** `limit` max 500. `multiquery` takes up to 10 sub-queries. Webhooks for create/update/delete. | Free for non-commercial use under the Twitch Developer Service Agreement. Show "Powered by IGDB" in the footer and on the About page, as Backloggd does (exact attribution wording *(unverified)*). | Catalog, time-to-beat (`game_time_to_beats`: `hastily`/`normally`/`completely`, **in seconds**, plus `count`), PopScore (`popularity_primitives`, updated daily: Visits, Want to Play, Playing, Played, Steam peak players/reviews/top sellers, Most Wishlisted Upcoming, Twitch hours watched), `release_dates`, `external_games` (`external_game_source`: Steam=1, GOG=5, Microsoft=11, Epic=26, itch=30, PS Store US=36), relations and media. | **Core.** Move off the deprecated fields: `category`→`game_type`, `status`→`game_status`, `collection`→`collections`, and `follows`, which is going away (the current `popularity_score` uses it; replace it with PopScore). Use webhooks or `updated_at` deltas instead of refreshing by age. |
| **Steam Web API** | Free key, 100k calls/day. `IPlayerService/GetOwnedGames` (`playtime_forever`, `rtime_last_played`) works only when the user's game details are public. | Keep the key secret, and tell users what Steam data is stored. | The most-requested import in the category (Backloggd roadmap: "Steam import" 19K votes, still not shipped). Playtime sync. | **P5, first integration.** Match games by app ID through IGDB `external_games`. |
| **IsThereAnyDeal** | API key. OAuth2 with PKCE for user waitlists. 1,000 requests per 5 minutes. `/games/lookup/v1` (by Steam app ID), `/games/prices/v3` (200 IDs per call), `/games/overview/v2` (current best price + **historical low**), waitlist webhooks. | Credit ITAD. Do **not** alter its data or affiliate tags. Private personal use is allowed. | Wishlist price-drop and historical-low alerts. No surveyed competitor ties these to the wishlist. | **P5.** |
| **RetroAchievements** | Free user key. Rate-limited, with burst allowed (no number published). `GetUserCompletionProgress` returns 500 entries per page. | Official API. | Retro progress and imports. | **P5, optional.** |
| **Wikidata** | SPARQL/REST, free. | CC0. | An ID crosswalk: IGDB (P5794) → Steam (P1733), HLTB (P2816), GOG (P2725), MobyGames (P11688), PCGamingWiki (P6337). | **P3.** Use it for outbound links (HLTB, PCGamingWiki) with no terms-of-service risk. |
| **OpenCritic** (RapidAPI) | Free tier: 25 searches and 200 requests per day. $19/mo for more. | Commercial use allowed on all tiers. | Critic score and badge. | Optional. Cache scores per game. IGDB `aggregated_rating` already covers most of the need. |
| Xbox via OpenXBL | Free tier: 150 requests/hour. | Third-party proxy. | Xbox import. | **Excluded** (owner decision). |
| PSN via psn-api | Unofficial. An NPSSO token that is equivalent to the user's password. | Terms-of-service grey area. | PSN import. | **Excluded** (owner decision). |
| **SteamGridDB** | API key. Lookup by Steam, EGS, eShop and other IDs. | Art is user-uploaded; copyright unclear; rate limits *(unverified)*. | Hero banners and logos. | Optional, after IGDB artworks. |
| **PCGamingWiki** | 60 requests/minute. Since August 2026, `cargoquery` needs a bot-password login. | CC BY-NC-SA. | PC fixes and settings. | Link out only, using the app ID redirect. |
| **ProtonDB** | No API. Report dumps published under ODbL. | ODbL (attribution and share-alike). | Steam Deck/Linux badge. | Link out. |
| **UPC lookup**, for barcode scanning | UPCitemdb free tier: 100 requests/day. PriceCharting: paid, 1 request/second, also gives physical prices. | Per provider. | Adding physical games by scanning. | P5, optional. Cache UPC→IGDB matches locally once confirmed. |
| HowLongToBeat | No API. Anti-bot protection. Ziff Davis terms. | Scraping is risky. | Time-to-beat. | **Skip.** IGDB `game_time_to_beats` covers it; link out using the Wikidata ID. |
| RAWG | 20K requests/month. A hyperlink is required on every page that uses the data. No redistribution. | Restrictive. | Duplicates IGDB. | Avoid. |
| MobyGames | Paid subscription. 720 requests/hour. | Paid. | Regional covers. | Optional, not planned. |
| Metacritic, GOG library, Nintendo | No public API (GOG has only undocumented endpoints; Nintendo *(unverified)*). | — | — | CSV or manual entry only. |

### Browser support (MDN browser-compat-data, 2026-10-08)

| Feature | Android Chrome | iOS Safari (installed PWA) | Plan |
|---|---|---|---|
| Web Push | Yes | 16.4+, **home-screen installs only** | Prompt only after a user action. Explain the install step. Needs HTTPS. |
| `share_target` | Yes | **No** (WebKit bug 194593 is open) | Android: share sheet → Cato. iOS: paste-a-link box plus an Apple Shortcut that opens `/#add?url=…`. |
| Manifest `shortcuts` | Yes | **No** | Android only. On iOS, the ⊕ button provides the same quick entry. |
| Badging API | No | 16.4+ | iOS only, which is a nice coincidence. |
| `BarcodeDetector` | Yes | Behind a flag only | Use a vendored ZXing-WASM fallback on iOS. |
| `navigator.share` (outbound) | Yes | Yes | Share a Year in Review image, list links. |

---

## 10. P6: Delight

- **Year in Review** (`#you/year/2026`): hours, games finished, top-rated, longest session, most-played genre and platform, a "first finish of the year", your playthrough timeline, and how you compare to the instance. Can be shared as an image rendered client-side on a canvas.
- **Goals:** "Finish 24 games in 2026", "Clear 10 from the backlog", "Play 5 games from before 2000". Progress rings on Home.
- **Streaks and diary:** an optional, gentle "played 4 days this week" (never guilt-inducing; can be turned off).
- **Backlog roulette:** "Surprise me" with a short spin animation (respects reduced-motion).
- **Moods and tags:** suggested tags such as cozy, co-op, short, story-rich and challenging, seeded from IGDB themes and keywords. They power filters and Up Next.
- **Micro-polish:**
  - skeleton loaders instead of "Loading…" text;
  - View Transitions API for opening game pages (with a fallback);
  - haptic feedback through `navigator.vibrate` on Android when logging;
  - a confetti burst on Finish (respects reduced-motion);
  - empty states that suggest the next action.

---

## 11. Quality, testing and operations

- **Tests:**
  - Keep the real-SQLite rule.
  - Add **API contract tests** for `/api/v1`, a migration test against a production-shaped fixture (about 300k games, a 5k-item library) with timing assertions, and `node --test` for the store, outbox, router and formatters.
  - Playwright: every P2 journey with tap budgets; axe-core; visual snapshots of the key screens in light and dark, at phone and desktop sizes; offline outbox replay. All projects run over **HTTPS**: Chromium on the non-loopback `cato.test` name with an SPKI pin, WebKit with `ignoreHTTPSErrors` (§2.4). The suite covers service worker, offline, passkey (virtual authenticator) and push-handler journeys.
- **Performance gates:**
  - typeahead p95 under 30 ms;
  - library index full sync under 150 ms and delta sync under 20 ms for a 5k library;
  - library page p95 under 25 ms;
  - game page under 40 ms when cached locally.

  All measured with Go benchmarks on the fixture.
- **Security gates:** `govulncheck`, `npm audit`, the CSP regression test, an authorization test matrix for P4 visibility, and a fuzz test for CSV import parsing.
- **Operations:**
  - The release workflow stays the same: tagged releases with `make deploy`.
  - Migrations take an automatic `.backup` snapshot before they run, at startup, when the schema version changes, kept with rotation.
  - `/healthz` reports job lag.
  - A new admin "System" page shows IGDB quota and lane queues, the cover queue, database size, WAL size and last backup.
- **Docs:** update AGENTS.md for each phase; an API reference (`docs/api-v1.md`); a user guide page in the app (`/#you/help`).

---

## 12. Owner decisions (resolved 2026-10-13)

| # | Question | Decision | Effect on the plan |
|---|---|---|---|
| 1 | Exposure and transport | **HTTPS everywhere.** Development and tests get HTTPS automatically. Production will eventually be on a real domain. The current HTTP deployment is out of scope. | §2.4 adds native TLS, development certificates and HTTPS-only test projects. Security is designed for possible internet exposure on the real domain: invite-only signup, trusted proxies, HSTS, `__Host-` cookies. Interim real-phone testing through Tailscale is optional. |
| 2 | Navigation | **Drop the 4-tab Nabu layout.** | Use Home / Library / ⊕ / Discover / You (§3.4). Update AGENTS.md (PWA shell, accessibility, keyboard shortcuts) in the same PR that ships the new shell. |
| 3 | Rating display | **5 stars with halves** by default. | Storage stays 0–100 and existing data is untouched. A setting offers 10- and 100-point display. |
| 4 | Social scope | **This server only.** | No federation. Visibility levels are private, followers and instance (§7). |
| 5 | PlayStation and Xbox imports | **Left out.** | Integrations are Steam, CSV, IsThereAnyDeal and RetroAchievements (optional). No unofficial APIs. |
| 6 | Frontend | No strong preference, provided it is **secure, maintainable, easy to use and testable.** | **lit-html 3**, vendored, with no build step, plus pure-module logic tested under `node --test`, a component gallery and E2E journeys (§4.2). |

## 13. Suggested sequencing and size

| Phase | Size | Can start | Notes |
|---|---|---|---|
| P0 | S (about 1 week) | now | Every item is independent; one PR per group (bugs, security, perf, HTTPS everywhere with the HTTPS test harness). |
| P1 | L | after P0 | Backend v1, frontend core and design system can run in parallel. |
| P2 | L | after P1 core | Ship screen by screen: shell → game page → log/finish → library → home → you. |
| P3 | M | in parallel with late P2 | IGDB expansion is backend-heavy. |
| P4 | L | after P2 | Needs decision 1. |
| P5 | M–L | Steam/CSV import can follow P2 | Push and passkeys need HTTPS (decision 1). |
| P6 | M | any time after P2 | |

Every phase ends with:
- `make release-check` green;
- the service-worker/asset version updated (automatic after P1);
- AGENTS.md updated;
- a tagged release through `make deploy`, done only by the owner.

## Appendix A: Smaller findings not covered above

- Five status-filter controls overlap: the hidden `#statusTabs`, the Status popover, the Filters chips, the "Filtered:" chips and the search-mode selects. Replace them with one shelf chip row and one filter sheet.
- The Status popover applies changes immediately, but the Filters sheet only applies on Apply. Use one model: apply immediately, with Reset.
- The "Filtered:" bar shows raw syntax (`owned:ps5`, `@switch`). Show human-readable chips instead.
- Two toast systems (`#toast`, `#toast-container`). Three definitions of status colours, three of status labels ("Finished" vs "Completed"), four copies of `fmtHours`, and two different platform-rank tables.
- Dead code: `htmlToEl`, `removeCardAnimated`, `buildLibraryFilterBarHTML`, `renderLibraryItems`, `openStatsDialog`, `initSettings`, the `settingsLink` reference, `#libFilterFab`, and the `.main-tabs` / `.playing-hero` / stats dialog CSS.
- Keyboard shortcuts `1`–`4` still fire while a modal is open.
- Genres are stored as raw IGDB IDs with no lookup table, so they can never be shown as names. `trailer` is never filled in, and `igdb_popularity` is always 0.
- `category` and `status` are deprecated in IGDB; use `game_type` and `game_status`.
- Every `scanLibraryItem` reloads `PlatformNames()`, and every mutation runs two session lookups (Auth, then CSRF). Cache the platform names in memory and pass the session through the request context.
- `gofmt -l` reports 5 files. Add a `make fmt-check` target to `release-check`.
- `cmd/cato` has 2.4% test coverage. Extract `run(args) error` so it can be tested.
- CI: no `cosign verify` at deploy time, and no Dependabot configuration (B6, still open).
