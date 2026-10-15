# Browser regression coverage

Run `cd e2e && npm ci && npx playwright install --with-deps chromium webkit firefox`,
then `npm run test:e2e`. Playwright starts a disposable real Go server with a fresh
SQLite database; no production data or external IGDB account is used.

Every project runs over **HTTPS** with no machine setup (`tls.ts`). Loading the
config runs `go run ./cmd/cato dev-cert --out $TMPDIR/cato-e2e-tls` (override
with `CATO_E2E_TLS_DIR`, and the Go binary with `GO`), which creates or reuses a
self-signed certificate and prints its SPKI SHA-256 pin; `scripts/e2e-server.sh`
serves those files on `127.0.0.1:7180`.

- Chromium projects open `https://cato.test:7180`, a non-loopback name, so
  loopback secure-context exemptions can't hide HTTPS bugs. Chromium maps it with
  `--host-resolver-rules=MAP cato.test 127.0.0.1` and trusts the certificate with
  `--ignore-certificate-errors-spki-list=<pin>`. `ignoreHTTPSErrors` alone is not
  enough: Chromium then refuses to register the service worker. The config also
  maps `cato.test` in Node's resolver for `page.request`/`request` contexts.
- WebKit and Firefox projects open `https://127.0.0.1:7180` with
  `ignoreHTTPSErrors`, which gives a secure context and a working service worker.
  Firefox runs everything except the Chromium-only offline, performance and
  visual specs.

Contexts a test creates itself (`browser.newContext`, `playwright.request.newContext`)
must pass `ignoreHTTPSErrors: true`. The harness seeds catalog IDs 1–2, 100–164
and named games 200–215 (`tests/helpers.ts` `G`), and runs with `CATO_DEV=1` so
the component gallery (`#dev/ui`) is available. It also seeds one fixed admin,
`admin@e2e.test` (username `e2e_admin`, the shared E2E password), so admin specs
don't depend on which test signs up first. Each test creates its own user
through `/api/auth/signup` and seeds through `/api/v1`; the behaviour under test
is driven through the UI. Artifacts default to `/tmp/cato-e2e-results`
(override with `CATO_E2E_RESULTS`).

| Spec | Contract |
| --- | --- |
| `auth.spec.ts` | Login page explains Cato and hides Google when unconfigured; keyboard-reachable sign-up toggle; `new-password` on sign-up; sign up → Home; sign out; wrong password; signed-out redirect |
| `journeys.spec.ts` | Tap budgets (plan §5): log time to a playing game in 2 taps (⊕ → +1h) or 1 from Home; finish and rate in 3 (Finish → star → Done); want a new game in 3 plus typing; find a library game in 1 plus typing; change status on the game page in 1; undo in 1, checked on the server |
| `home.spec.ts` | First-run onboarding; now-playing chips and Resume; Up Next and recent activity; the server-side session timer survives a reload and Stop & log records a session; the ⊕ sheet's Playing and Recent groups |
| `game.spec.ts` | Adding from the game page; status, keyboard half-star rating, time chips; a replay creates a second playthrough; session delete and undo; notes and tags; remove and undo |
| `library.spec.ts` | Shelf chips with counts; instant search with real keystrokes; sorting; `$tag`/`@platform` suggestions and removable chips; list view; saved views; multi-select bulk status with undo; right-click menu; j/k/Enter |
| `you.spec.ts` | Stats numbers and heatmap; Diary; theme and rating display persist; devices list and signing out another device; Help |
| `discover.spec.ts` | Popular shelf excludes your games; faceted search results; Want from results |
| `offline.spec.ts` | (Chromium) worker-served versioned assets and offline fallback; the app opens offline from the cached shell and IndexedDB, queues a write, survives a reload and sends it on reconnect |
| `regressions.spec.ts` | Plan §2.1 bugs that still apply: re-adding after removal (#5), Finish can't be double-tapped onto the next game and undo restores (#6), malformed `%` hash (#8), offline copy (#9), `#search` shortcut focuses the Library search (#10) |
| `people.spec.ts` | Follow from People search and see their activity in the Home feed and on their profile; a private profile needs an approved follow (request, inbox badge, accept); a spoiler review is hidden until revealed, a friend likes it and the like reaches the inbox; lists from a suggestion, adding from game pages and reordering; an invite link signs someone up already connected; admin people, reports (hide), sign-up mode and the audit log, and 403 for others |
| `reach.spec.ts` | CSV import: uncertain matches wait for “Is this right?”, existing games only gain what they lacked, then the whole batch is undone; passkeys added in Settings and used to sign in (Chromium virtual authenticator); the share target opens the ⊕ sheet searched for a shared Steam link; an admin reset link sets a new password and signs the old session out |
| `delight.spec.ts` | A goal added in Goals shows its ring there and on Home; Year in Review shows the year's numbers and first finish and downloads a share image (Chromium); Up Next mood chips keep only games in that mood |
| `a11y.spec.ts` | axe-core: no serious or critical violations on login, Home, Library (grid and list), game pages, Discover, search results, You, Stats, Diary, Settings, Help, the gallery, the Log and Finish sheets, profiles, game-page reviews, lists, People, Inbox, Edit profile, every admin tab, the Import screens with a review list, Year in Review and Goals, light and dark |
| `visual.spec.ts` | (Chromium) gallery and Home screenshots, light and dark, desktop and phone; baselines per platform under `tests/__screenshots__`, recorded on a platform's first run |
| `perf.spec.ts` | (Chromium) cold Home load: render-blocking requests ≤ 12, no module waterfall (every eager module requested from the HTML), initial JS ≤ 60 KB and CSS ≤ 20 KB gzipped, LCP < 1 s; INP < 100 ms (median of three visits) with 4× CPU throttling |
| `https.spec.ts` | Secure context on `cato.test`/`127.0.0.1`, service worker registration, WebAuthn passkey creation (Chromium virtual authenticator), HSTS/CSP upgrade/Permissions-Policy headers, `__Host-` session cookie |
| `pwa.spec.ts` | Real service worker install/control, precache of the asset manifest, activation removes old shell caches and keeps covers and unrelated caches |

Browser actions wait on observable DOM state, network responses registered before
the triggering action, or persisted API state. There are no fixed sleeps. API
setup creates prerequisites without substituting for the UI behavior under test.
The performance spec blocks service workers to measure a true cold load; the PWA
and offline specs run the actual shipped worker.

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
server/JavaScript tests. The axe gate catches what automated rules can; it is not
a full WCAG audit.
