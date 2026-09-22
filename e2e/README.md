# Browser regression coverage

Run `cd e2e && npm ci && npx playwright install --with-deps chromium webkit`,
then `npm run test:e2e`. Playwright starts a disposable real Go server with a fresh
SQLite database; no production data or external IGDB account is used. Catalog
IDs 1–2 support common workflows; 100–164 exercise pagination. Each test creates
its own user, and separate browser contexts have independent cookies/storage.
Artifacts default to `/tmp/cato-e2e-results` (override with `CATO_E2E_RESULTS`).

| Spec | Regression contract |
| --- | --- |
| `auth.spec.ts` | Unauthenticated redirect; UI signup, logout and login; wrong password; authenticated login redirect |
| `library.spec.ts` | Seeded grid and status counts; real local search dropdown; hash navigation; authenticated deletion removes persisted item |
| `filters.spec.ts` | Transactional advanced filters discard on close/backdrop/Escape; Apply commits; search filter staging; immediate status filtering |
| `journeys.spec.ts` | UI library creation and autosave; rating, hours, literal notes, tags, platform, format and corrected completion date after reload; remove/undo/permanent remove |
| `journeys.spec.ts` | 65-item infinite scroll, unique appended IDs, filter reset and subsequent pagination |
| `journeys.spec.ts` | Playing time and finish update previously visited Stats and Settings; totals, average rating, tags and platforms |
| `journeys.spec.ts` | Profile/theme persistence; mismatched/incorrect password rejection; typed deletion, cancel/reset, deletion and subsequent authentication denial |
| `journeys.spec.ts` | Native required/email/password length validation; separate real browser sessions, private library reads and writes, foreign/missing CSRF rejection |
| `resilience.spec.ts` | Failed autosave retains the draft/route, close retries persistence; Stats error/retry and recent-item route; actual CSV download filename and contents |
| `release.spec.ts` | Tab/history consistency after logged time; password change retains current session, revokes another session and rejects old credentials |
| `platform.spec.ts` | Auth/CSRF API boundaries, cookie attributes, failed login semantics, health/database, missing covers/game, PWA headers, search injection |
| `pwa.spec.ts` | Real service worker install/control, precache, activation removes old static caches and retains covers/unrelated caches, online worker interception |
| `offline.spec.ts` | Chromium offline asset and navigation fallback, online retry |

Browser actions wait on observable DOM state, network responses registered before
the triggering action, or persisted API state. There are no fixed sleeps. API
setup creates prerequisites without substituting for the UI behavior under test.
The resilience spec blocks service workers so Playwright owns deliberate API
failure injection; the PWA spec runs the actual shipped worker.

The PWA activation test seeds an old cache then installs the shipped worker; it
verifies the actual activation cleanup contract, not a two-deployment update or
an operating-system installation prompt. Offline navigation runs in Chromium:
Playwright WebKit `setOffline(true)` produces an internal navigation error even
with an active controlling worker and a verified cache hit online. The offline
spec is explicitly Chromium-only; WebKit activation/cache tests still run.
Physical Safari offline behavior remains a manual check.
[Playwright documents service worker support as Chromium-only](https://playwright.dev/docs/service-workers).
Chromium/WebKit device projects test
responsive rendering and touch/browser behavior, not physical iOS standalone
cold launches. OAuth provider interaction, real IGDB/download availability,
production restore/deployment, and rate-limit saturation are outside this local
browser suite; rate-limit behavior and service-worker edge cases have focused
server/JavaScript tests. This suite does not claim exhaustive visual or WCAG
conformance coverage.
