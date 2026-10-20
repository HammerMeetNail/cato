# Testing

Write the regression test first; use real SQLite for Go and Playwright for the UI. Before calling a change done, run the relevant targets, and for anything substantial run `make release-check`.

## The gate

`make release-check` runs, in order:

| Target | What it checks |
|---|---|
| `fmt-check` | `gofmt -l` is empty |
| `test` | `go test ./...` |
| `test-race` | `go test -race ./...` |
| `vet` | `go vet ./...` is clean |
| `build`, `deploy-build` | local and NAS binaries build |
| `test-js` | `scripts/lint-js.cjs` (no HTML-string sinks), `node --test tests/js`, and the service-worker harness `scripts/cache-regression.cjs` |
| `test-backup`, `test-release` | the Python backup and deploy script tests |
| `security` | `govulncheck` with the toolchain **go.mod pins** (what release images build with), and `npm audit` |
| `test-e2e` | installs Playwright browsers, then runs the whole suite |

It needs:

- Go (go.mod pins `toolchain go1.26.9`);
- Node 24+, Python 3.10+ and sqlite3;
- Playwright Chromium, WebKit and Firefox.

`make test-e2e` installs the browsers; on Linux, CI uses `--with-deps`.

## Go tests

- **Real SQLite and no mocks.** See `database.md` for the pattern. HTTP tests use `httptest` against the real handlers.
- **Security-critical tests to keep green and extend:**
  - `internal/social` `TestAuthorizationMatrix`, covering every visibility combination;
  - `internal/push` against the RFC 8291 vector;
  - `internal/webauthn` with a software authenticator;
  - the fuzz targets `FuzzParseCSV` (`internal/imports`) and `FuzzDecodeCBOR` (`internal/webauthn`). Run one with `go test -run XXX -fuzz FuzzParseCSV -fuzztime 30s ./internal/imports/`.
- **The production-shaped fixture** (`internal/library/fixture_test.go`) is skipped under `-short`. `CATO_PERF_SLACK=2` loosens its timing gates on slow machines.

## Browser tests (`e2e/`)

`make test-e2e`, or `cd e2e && npx playwright test`.

**One disposable HTTPS server.** `scripts/e2e-server.sh` starts it on 127.0.0.1:7180 with a fresh database and seeds:

- catalog games with IDs 1–2, 100–164 and 200–215 (`G` in `tests/helpers.ts`);
- a fixed admin, `admin@e2e.test`, with the shared E2E password.

**Projects** (`playwright.config.ts`):

| Project | Opens | Runs |
|---|---|---|
| `chromium` | `https://cato.test:7180`, certificate pinned by SPKI | everything |
| `webkit` | `https://127.0.0.1:7180`, `ignoreHTTPSErrors` | all but offline, perf, visual |
| `firefox` | `https://127.0.0.1:7180`, `ignoreHTTPSErrors` | all but offline, perf, visual |
| `mobile-chromium` | Pixel 7, `cato.test` | journeys, home, game, offline, regressions, a11y, pwa, https, perf, visual, gestures (real touches through CDP) |
| `mobile-webkit` | iPhone 13, `127.0.0.1` | journeys, home, game, regressions, a11y, pwa, https |

`e2e/README.md` has the contract of each spec.

**Conventions:**

- Each test signs up its own user. Set up data through `/api/v1`, and drive the behaviour under test through the UI.
- **All projects share one server.** Make names unique, use exact or scoped locators (for example, the "Popular" shelf versus "Popular on this server"), and restore any global state you change, such as the sign-up mode.
- Send `played_on: localDay()` when seeding dates. The server's UTC day differs from the local day in the evening.
- No fixed sleeps. Wait on the DOM, on responses, or poll the API (`eventually`).
- New screens go into `a11y.spec.ts` (axe, serious or critical violations fail, light and dark).
- **Visual baselines** (`tests/__screenshots__`) are per platform.
  - Review the diff in the results folder before running `--update-snapshots`.
  - Full-page captures expand the app frame for the screenshot, because `<main>` is the scroller.
- **The perf budget is a gate.** If initial JS grows, lazy-load it (see `frontend.md`).

## Firefox

**Running it normally:** `npx playwright test --project=firefox` (after `npx playwright install firefox`).

**Testing on Firefox matters.** It surfaced layout bugs Chrome tolerated:

- the app frame wasn't height-bound, so nothing scrolled;
- date inputs were clipped.

When you touch layout or forms, run the Firefox project.

**From a sandboxed agent shell on macOS** (for example Claude Code), Firefox can't start. It fails with `sandbox_extension_issue_file_to_process … Operation not permitted` or `RenderCompositorSWGL failed mapping default framebuffer`, then times out at launch. Don't disable the sandbox. Run Firefox from Playwright's Linux image in podman instead:

1. **Start the E2E server on the host** and wait for `CATO_E2E_READY`:

   ```sh
   E2E_ADDR=127.0.0.1:7180 bash scripts/e2e-server.sh > /tmp/e2e-server.log 2>&1 &
   ```

2. **Write a container-only config in a scratch directory**, for example `$SCRATCH/firefox.config.mjs`. The repo config shells out to Go for TLS, so it can't be used in the container.

   ```js
   import { defineConfig, devices } from '/ABS/PATH/cato/e2e/node_modules/@playwright/test/index.mjs';
   export default defineConfig({
     testDir: '/ABS/PATH/cato/e2e/tests', workers: 1, retries: 0, reporter: 'list',
     timeout: 30_000, expect: { timeout: 7_000 }, outputDir: process.env.OUT,
     snapshotPathTemplate: '{testDir}/__screenshots__/{testFilePath}/{arg}-{projectName}-{platform}{ext}',
     projects: [{ name: 'firefox', testIgnore: /(offline|perf|visual)\.spec\.ts/,
       use: { ...devices['Desktop Firefox'], baseURL: 'https://host.containers.internal:7180', ignoreHTTPSErrors: true } }],
   });
   ```

3. **Run it.** Match the image tag to `@playwright/test` in `e2e/package.json` (currently 1.62.1):

   ```sh
   podman run --rm --ipc=host -e OUT=$SCRATCH/results \
     -v /ABS/PATH/cato/e2e:/ABS/PATH/cato/e2e:ro -v $SCRATCH:$SCRATCH -w /ABS/PATH/cato/e2e \
     mcr.microsoft.com/playwright:v1.62.1-noble npx playwright test -c $SCRATCH/firefox.config.mjs
   ```

   `host.containers.internal` reaches the host's loopback.

4. **Stop the server afterwards:** `pkill -f scripts/e2e-server.sh`. Port 7180 must be free for the next `make test-e2e`.

**To probe a running Cato container** (for example the local `cato-local` image) instead of the E2E server, run the Playwright image with `--network container:<name>` and open `https://localhost:7080`. The `firefox` export of `@playwright/test` works the same way inside the container.

## Real Mobile Safari (iOS Simulator)

Playwright's `mobile-webkit` is desktop WebKit with an iPhone viewport, not Mobile Safari. For iOS-only behaviour, use the iOS Simulator (Xcode is installed; there's no Simulator window, everything is command line):

1. **Boot a device and open Cato:** `xcrun simctl boot <udid>` (pick a generic iPhone from `xcrun simctl list devices available`), then `xcrun simctl openurl <udid> http://10.0.0.42:7080/login`. Screenshots: `xcrun simctl io <udid> screenshot out.png`.
2. **Run JavaScript in its Safari:** find the inspector socket with `lsof -U | grep webinspectord_sim`, start `ios_webkit_debug_proxy -s unix:<socket>`, and talk to `ws://127.0.0.1:9222/devtools/page/1`. Newer iOS wraps commands in `Target.sendMessageToTarget` to the target from `Target.targetCreated`. `Runtime.evaluate` can sign in, mark `window` to detect reloads, and read state.
3. **Real taps** (synthetic `click()` and `TouchEvent`s don't go through UIKit): `idb_companion --udid <udid> --grpc-port 10882` (Homebrew `facebook/fb/idb-companion`), the `fb-idb` client in a scratch venv, then `idb ui tap --udid <udid> X Y` in points (screenshot pixels ÷ 3).

**A realistic rig:** run the current code over plain HTTP on the Mac's LAN address (`CATO_ALLOW_INSECURE=1 CATO_LISTEN_ADDR=0.0.0.0:7090 CATO_STATIC_DIR=web/static`, a scratch database with a few games and cover JPEGs in the cover directory), open `http://<LAN IP>:7090` in the Simulator (not 127.0.0.1, which counts as a secure context), and sign up and fill a library with `fetch` calls through the inspector. To see flicker, record with `xcrun simctl io <udid> recordVideo`, split frames with `ffmpeg -vf fps=30`, and diff consecutive frames: a screen swap is one large change, and anything after it (covers arriving, sections pushing the page) shows up as small changes in the next frames.

**Realistic data without anyone's account:** copy only the public catalog from a production backup (`games` and the `game_*` tables, `lookups`, `platforms`) into a freshly migrated database (start the server once on an empty file; the migration seeds `platforms`, so upsert it), then sign up a rig user and seed a library through the API. Home, Discover and the game pages then look like production. Tap targets: read an element's `getBoundingClientRect()` through the inspector and add Safari's top bar (62 pt on an iPhone 17 Pro) to `y`.

**Edge swipes:** `idb ui swipe` from the screen edge doesn't trigger Safari's own back gesture, even on a plain page, so the native swipe-back can't be checked here. A swipe from inside the page exercises `features/swipe.js`.

The Simulator has no device memory limits, so a page that iOS kills on a real phone survives there. For that, read the server's `unexpected reload` log lines (see `frontend.md`). The reloads the iPhone reported after every tap on a game all began as the game page's View Transition did; the Simulator never showed them.

**Phone-sized Firefox:** Playwright's Firefox in podman with `viewport: 390×844`, `deviceScaleFactor: 3`, `hasTouch: true` and `page.touchscreen.tap()` is close to Firefox's responsive mode. Record what each animation frame shows with a `requestAnimationFrame` probe (the screen's heading, visible covers still loading): its video is software-rendered and captures repaints part-done (black bands, a white box), which a real Firefox doesn't show.

## Manual checks agents do

- **Real app over HTTPS:** `make run` (dev TLS). Keep your own Playwright scripts in the scratchpad, never in the repo.
- **Plain HTTP like the NAS:** `CATO_ALLOW_INSECURE=1`, opened through a non-loopback host name. 127.0.0.1 counts as a secure context even over HTTP and hides missing-feature problems; with Chromium, `--host-resolver-rules=MAP nas.test 127.0.0.1` works.
