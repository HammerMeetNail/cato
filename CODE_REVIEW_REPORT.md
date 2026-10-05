# Cato Code Review Report

**Date:** 2026-10-04
**Scope:** Full repository review (Go backend, vanilla-JS PWA frontend, scripts, CI, E2E suite)
**Baseline:** `badc1e1` on `main` + uncommitted working-tree changes from this review
**Method:** Full test/coverage/E2E runs before and after fixes; every finding exercised against the running app, not just read.

---

## Executive Summary

Cato is a well-architected single-module Go app with a clean two-pool SQLite design, disciplined migration versioning, real-DB tests (no mocks), and an unusually strong E2E suite (90 browser tests across desktop/mobile × Chromium/WebKit). The review found no critical backend defects. It did find and **fix** one HIGH data-integrity bug (malformed JSON from a double-write in the library upsert), a set of frontend **fail-open** behaviors (network errors treated as auth failure; failed existence checks treated as "not in library" followed by a destructive upsert), a missing CSP that the codebase's own conventions implied, several XSS-adjacent escaping gaps, a silently-skipped input-validation path in `deploy-remote.sh` (bash 3.2 does not abort on failed `[[ ]]` under `set -e`), and a stale duplicate settings page.

Coverage rose from **72.7% → 74.8%**; all 90 E2E tests pass after the changes; `govulncheck` reports 0 reachable vulnerabilities.

**One action is required from the owner:** `.claude/settings.local.json` contains a **live production session cookie** for `http://10.0.0.42:7080`. It is now gitignored but still on disk — revoke the session server-side (or delete the file) and treat the token as exposed.

## Overall Health Score: **4 / 5**

| Dimension | Score | Notes |
|---|---|---|
| Architecture | 5 | Clear package boundaries, `auth.Querier` seam, two-pool SQLite, no ORM/build-step complexity |
| Security | 4 | CSP now enforced, bcrypt cost 12, hashed session tokens, CSRF middleware; prod token on disk (user action), HTTP-only deployment (no `Secure` cookie), 4 non-reachable module vulns |
| Testing | 4 | 74.8% Go coverage, real-SQLite tests, 90 E2E tests × 4 browser projects; `cmd/cato` at 2.4% |
| Reliability | 4 | Graceful shutdown, lifecycle-job guards, fail-closed frontend flows after fixes; unbounded rate-limiter map |
| Maintainability | 4 | Excellent AGENTS.md; JS layer has duplicated helpers and dead code |
| Deployment | 4 | Tag-gated, fail-closed release workflow, signed images; no cosign verify at deploy, no Dependabot |

---

## Findings Fixed in This Review

| # | Severity | Finding | Location | Fix |
|---|---|---|---|---|
| 1 | **HIGH** | `upsertLibraryItem` ignored `writeLibraryItem`'s return value and appended `{"ok":true}` to error responses → malformed JSON bodies, validation messages lost, clients saw success-shaped garbage on 4xx | `internal/http/handler_library.go:414` | Check return; propagate error response. Regression assertions added to `TestLibraryInvalidStatus` |
| 2 | **HIGH** | Fail-open auth flow: any network hiccup in `checkAuth()` redirected authenticated users to `/login` (false logout) | `web/static/js/api.js`, `app.js` | Distinguish network errors from 401 rejection; offline retry panel instead of logout bounce |
| 3 | **HIGH** | Fail-open library add: `library.check()` returned `false` on fetch failure, then `quickAdd` performed a destructive upsert that could overwrite an existing library entry with default status | `web/static/js/search.js` | `check()` returns `null` on failure; quickAdd aborts and shows retry |
| 4 | **HIGH** | No Content-Security-Policy header despite repo conventions implying `script-src 'self'` | `internal/http/server.go` `securityMiddleware` | Added CSP: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' https://images.igdb.com data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'` |
| 5 | **HIGH** | Inline `<script>` blocks and inline `onerror=` cover fallbacks (would break under the new CSP; latent XSS surface via `data:`/attribute injection) | `login.html`, `index.html`, `search.js`, `library.js`, `playing.js` | Externalized to `js/theme-init.js` + `js/login.js`; cover fallbacks via delegated `error` listeners + `data-cover-fallback` |
| 6 | MEDIUM | Unescaped interpolation into HTML attributes (`src="${coverUrl}"`, `err.message` into innerHTML) in playing/library views | `web/static/js/playing.js`, `library.js` | `escapeHTML` on all interpolated values; shared `escapeHTML` now imported from `api.js` (was duplicated in settings.js) |
| 7 | MEDIUM | `deploy-remote.sh` input validation silently skipped on macOS bash 3.2: a failed `[[ … ]]` under `set -e` does not abort, so path/host/env guards never rejected bad input locally; test suite was red (28 failures / 2 errors) on clean HEAD | `scripts/deploy-remote.sh`, `scripts/deploy_remote_test.py` | Explicit `|| reject` pattern on every guard + comment explaining the bash 3.2 trap; `flock` stub in the Python test harness (macOS has no `flock(1)`). Suite now 7/7 OK |
| 8 | MEDIUM | Stale duplicate `settings.html` unreachable (server redirects `/settings` → SPA `#settings` tab) | `web/static/settings.html` | Deleted |
| 9 | MEDIUM | Hours dropdown generated unbounded option lists (1000+ DOM options for heavy playtimes) | `library.js`, `playing.js` | Capped at 1000h with sensible step |
| 10 | MEDIUM | Recent-searches localStorage not cleared on logout / account deletion | `app.js`, `settings.js`, `search.js` | `clearRecentSearches()` exported and wired into logout + account delete |
| 11 | MEDIUM | Service worker `CACHE_NAME` stale after shipped-asset changes (repo rule) | `web/static/service-worker.js` | v23 → v24, asset list updated (`theme-init.js`, `login.js` added, `settings.html` removed); pinned test `TestServiceWorkerCacheBumped` updated |
| 12 | LOW | `e2e-server.sh` bound to `localhost` (IPv6 ambiguity) and no SQLite busy timeout | `scripts/e2e-server.sh` | Default `127.0.0.1:7180`, `sqlite3 .timeout 15000` |
| 13 | LOW | `make deploy` DRY_RUN path not fail-closed on unexpected state | `scripts/deploy.sh` | Explicit fail-closed `case` statement |
| 14 | LOW | Local tooling artifacts not ignored (`.claude/settings.local.json`, `.opencode/`, `backups/`) | `.gitignore` | Added |
| 15 | LOW | Untested public lifecycle starters (`StartPlatformSync`, `StartStaleRefresh`, `StartCoverRepair`, `StartNormalizationRepair`, `StartQueryCacheRefresh`) and `handlePlatforms` | new `internal/games/lifecycle_start_test.go`, `internal/http/handler_platforms_test.go` | Added with a `queryCacheInitialDelay` test seam mirroring existing seam patterns |

## Not Fixed — Recommended Backlog

| # | Severity | Finding | Location | Recommendation |
|---|---|---|---|---|
| B1 | **HIGH (user action)** | Live production session cookie (`cato_session=5b5e…`, full value present) stored in a local file for `http://10.0.0.42:7080` | `.claude/settings.local.json` | Revoke the session server-side, delete the file, treat token as exposed. Now gitignored, but on-disk copy remains |
| B2 | MEDIUM | `auth.RateLimiter` map keys are never evicted → unbounded memory growth from unique IP/email keys over a long-lived process | `internal/auth/ratelimit.go:12` | Add periodic purge of expired buckets (lifecycle-job pattern already exists) |
| B3 | MEDIUM | CSV export does not neutralize spreadsheet formula injection (`=`, `+`, `-`, `@` leading chars in titles/tags/notes) | `internal/http/handler_library.go:1146` | Prefix cells starting with formula characters with `'` |
| B4 | MEDIUM | `notes` field length unvalidated server-side (client caps it, API does not) | `internal/http/handler_library.go` (`buildLibraryItemInput`) | Enforce a max length (e.g. 4000) with 400 response |
| B5 | MEDIUM | Dead code: `#statusTabs` / `#libFilterFab` markup + branches unreachable in current shell | `web/static/index.html:170`, `library.js:2444`, `app.js:190` | Remove or re-wire |
| B6 | MEDIUM | CI gaps: no `cosign verify` before deploy (images are signed but never verified at deploy); image is pushed before trivy scan runs (deploy is gated on scan-sign, but the pushed tag exists unscanned in between); no Dependabot/renovate config; `make deploy-nas` bypasses the tag/release gate | `.github/workflows/ci.yml` (`scan-sign`, `deploy` jobs), repo root | Add verify step in `deploy`, scan-before-push or ephemeral digest scan, add `.github/dependabot.yml`, restrict `deploy-nas` to emergency docs |
| B7 | MEDIUM | Session cookie lacks `Secure` flag (acceptable on LAN HTTP today, but blocks a future TLS move and weakens any proxied deployment) | `internal/auth/session.go` cookie creation | Gate `Secure` on an env flag |
| B8 | LOW | Duplicated JS helpers: `fmtHours` ×3, date parsers ×4, platform-rank maps ×2 (`escapeHTML` was deduplicated in this review) | `library.js:2407/2846`, `playing.js:5`, `stats.js:23` | Extract a shared `js/util.js` module |
| B9 | LOW | `showTagMenu` re-registers document listeners without removing previous ones (leak per modal open) | `web/static/js/library.js:3382` | Track and remove on close |
| B10 | LOW | Scroll listener registered twice on re-render path | `web/static/js/library.js:2343-2381` | Guard with a flag or `AbortController` |
| B11 | LOW | `manifest.webmanifest` `id`/`start_url` mismatch with the shell's canonical `#library` URL; `offline.html` copy promises full offline availability the SW doesn't provide for API data | `web/static/manifest.webmanifest`, `offline.html` | Align `start_url`/`id`; soften offline copy |
| B12 | LOW | `cmd/cato` coverage 2.4% (`main()` 0%) — wiring code untested | `cmd/cato/main.go` | Extract a testable `run(args) error` and table-test subcommand dispatch |
| B13 | LOW | govulncheck: 4 vulnerabilities in required modules, none reachable from code (indirect/test-only paths) | `go.mod` | Track in dep updates; re-scan per release |
| B14 | LOW | Rate limiting and IGDB limiter are in-memory per-process — fine for single container, silently ineffective if ever scaled horizontally | `internal/auth/ratelimit.go`, `internal/games` | Document explicitly (AGENTS.md already notes it) |

---

## OWASP Top 10 Checklist

| Category | Status | Evidence |
|---|---|---|
| A01 Broken Access Control | ✅ Pass | `AuthRequired` → `CSRFRequired` middleware order on all mutating API routes; per-user library scoping in every SQL query; session tokens stored as SHA-256 hashes; E2E test proves cross-user isolation + CSRF rejection |
| A02 Cryptographic Failures | ✅ Pass | bcrypt cost 12; session cookie stores raw token but DB stores only its hash; HTTPS not used on LAN (accepted for deployment model, see B7) |
| A03 Injection | ✅ Pass | Raw SQL but 100% parameterized (no string concatenation found); SQLite FTS used safely; CSV formula injection remains (B3) |
| A04 Insecure Design | ✅ Pass | Fail-closed semantics restored in frontend after fixes (#2, #3); library pagination capped (max 200) |
| A05 Security Misconfiguration | ✅ Fixed | CSP added (#4); `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy` already present; no inline scripts/handlers remain in shipped HTML |
| A06 Vulnerable Components | ✅ Pass | `govulncheck`: 0 reachable vulns (4 unreachable in module graph, B13); `npm audit` in `e2e/`: 0; Playwright pinned 1.62.1 |
| A07 Auth & Identity Failures | ✅ Pass | Login/signup rate limiting, session revocation on password change (E2E-verified), hashed tokens; rate-limiter memory growth (B2) |
| A08 Integrity/Supply Chain | ⚠️ Partial | Tag-gated release, signed images (cosign), trivy scan gate; but no verify-at-deploy, no Dependabot (B6) |
| A09 Logging & Monitoring | ✅ Pass | Structured request logging, lifecycle job logs, deploy logs target; no secrets logged |
| A10 Server-Side Request Forgery | ✅ Pass | Only outbound calls are IGDB (fixed base URL) and IGDB image CDN (allowlisted in CSP `img-src`) |

---

## Test Coverage: Before → After

| Package | Before | After |
|---|---|---|
| **Total** | **72.7%** | **74.8%** |
| internal/auth | 92.2% | 92.2% |
| internal/config | 100.0% | 100.0% |
| internal/covers | 87.4% | 87.4% |
| internal/db | 83.3% | 83.3% |
| internal/games | 78.0% | **81.0%** |
| internal/http | 79.4% | **82.4%** |
| internal/igdb | 92.9% | 92.9% |
| internal/importer | 88.0% | 88.0% |
| cmd/cato | 2.4% | 2.4% (backlog B12) |

New test files: `internal/http/handler_platforms_test.go`, `internal/games/lifecycle_start_test.go`; extended: `handler_library_test.go` (malformed-JSON regression), `games_test.go` (`fakeIGDB.getFunc` seam), `mobile_regression_test.go` (CACHE_NAME v24 pin).

## E2E / Regression Results (after all changes)

| Suite | Result |
|---|---|
| Playwright E2E (`e2e/`, 9 specs × chromium/webkit/mobile-chromium/mobile-webkit) | **90/90 passed** (1.3m) |
| `go test ./...` | all packages ok |
| `go vet ./...` | clean |
| `make test-js` (syntax + SW asset-list + cache-regression harness) | 3 PASS |
| `make test-backup` | OK |
| `make test-release` (deploy-remote 7, deploy-gate 5, secrets 1) | all OK (was 28 failures / 2 errors on clean HEAD) |
| `govulncheck ./...` | 0 reachable vulnerabilities |
| `npm audit` (e2e) | 0 vulnerabilities |
| `DRY_RUN=1 make deploy` | correctly refuses with dirty tree (fail-closed) |

**Testing caveat discovered:** isolated `npx playwright test` invocations collide with a running full-suite webServer on `127.0.0.1:7180` and produce spurious `about:blank` failures. Run one Playwright process at a time against this suite.

---

## Positive Observations

- **Migration discipline**: versioned, append-only migrations with a documented never-edit rule; WAL/`_pragma` DSN correctly documented with the historical lock-contention incident explained.
- **Two-pool SQLite design** is the right shape for `modernc.org/sqlite` and is documented at the call-site level (`Query*` → read pool, `Exec*`/`Begin*` → writer).
- **Real-DB tests everywhere** — no DB mocks, `db.Open(t.TempDir())` pattern keeps tests honest.
- **E2E suite quality is exceptional for a hobby-scale app**: cross-browser (Chromium + WebKit), mobile viewports, resilience journeys (autosave failure recovery, stats retry), security journeys (cross-session isolation, CSRF rejection), and a service-worker cache-replacement test.
- **Release workflow is genuinely fail-closed**: tags must be reachable from main, dirty trees are rejected, `deploy-full` is disabled to protect production data, images are cosign-signed, deploy job depends on scan + release jobs.
- **AGENTS.md is a model artifact** — it encodes hard-won operational knowledge (DSN syntax trap, cover hot-path rules, PWA shell constraints) in a form an agent or new contributor can actually follow.

## Appendix — Commands Used

```bash
export PATH=/opt/homebrew/bin:$PATH   # arm64 toolchain (avoids x86_64 /usr/local/go)
make test          # go test ./...
make vet           # go vet ./...
go test ./... -coverprofile=/tmp/cato-cover2.out && go tool cover -func=...
cd e2e && npx playwright test          # 90 tests, 4 projects
make test-js test-backup test-release
govulncheck ./...
cd e2e && npm audit --audit-level=high
DRY_RUN=1 make deploy                 # gate preview only — never tagged/pushed
```
