# Frontend (`web/static`)

The frontend is vanilla ES modules with **no build step**. Templates are lit-html 3, vendored in `js/vendor/`; icons are a vendored subset of Lucide.

## Shell, layout and routes

- **`index.html` is the only authenticated page.** `js/app.js` renders the shell:
  - a header with the one search box, the session-timer pill, an offline/queued indicator and the account avatar on the right;
  - `<main id="main">`;
  - navigation: Home, Library, ⊕ Log, Discover, You. It's a bottom bar on phones and a left sidebar from 1024 px.
- **The frame is viewport-sized.**
  - `html`, `body`, `#app` and `.app` are all `height: var(--app-h)` or `100%`. `--app-h` comes from `js/head-init.js`, which handles iOS standalone.
  - `.app` is a CSS grid with `minmax(0, 1fr)` rows.
  - **`<main>` is the one scroll container.** Scroll restore and sticky bars depend on it.
  - Keep `#app { height: 100% }`: without it, the frame grows with its content and nothing scrolls. Firefox showed this first.
  - The nav is a grid row, not `position: fixed`, which avoids the iOS PWA cold-open gap.
- **Routes** (`js/core/router.js`): `#home` (also the empty hash), `#library?shelf=&q=&sort=&view=&dense=`, `#game/<id>`, `#log` (opens the Log sheet over Home), `#discover`, `#discover/search?q=`, `#you`, and these sub-pages:

  | Area | Routes |
  |---|---|
  | You | `#you/stats`, `#you/diary`, `#you/settings`, `#you/help` |
  | People | `#you/lists`, `#you/people`, `#you/inbox`, `#u/<username>`, `#list/<id>` |
  | Reach and delight | `#you/import`, `#you/year/<YYYY>`, `#you/goals` |
  | Admin and dev | `#admin?tab=`, `#dev/ui` (only with `CATO_DEV=1`) |

  Legacy hashes redirect.
- **Links and `go()` move with `pushState`, never a fragment navigation.** `app.js` `followLink` sends a tap on a `#…` link through the router's `go()` (not a modified click, a link with a `target`, or one a handler already took), and `go()` pushes or replaces the entry and dispatches `hashchange` itself. Back and forward still fire the browser's own `hashchange`.
  - Why: on a plain-HTTP page, Safari 27 on iPhone re-checks a fragment navigation's Enhanced Security against how the page was first loaded, and loads the page again in a new process (WebKit bug 320638, fixed in 318304@main). Every tap reloaded Cato (the black frame), and the entry the reload added made back land on the same screen. Pages reached by back/forward weren't affected, and the Simulator never showed it.
  - `pushState`, `replaceState` and same-document back/forward don't ask for that check (WebKit's `FrameLoader::updateURLAndHistory`, `loadSameDocumentItem`).
- **Loading:** Home and the Log sheet load eagerly, and everything else is a dynamic `import()` in `SCREENS` (through `loadScreen`). Home also loads its feed, goals and confetti modules lazily; see the performance budget below.
- **Starting and reloading without a blank page:**
  - `index.html` draws the app's frame (header with search box, bottom nav with the same tabs) before any script runs; a test keeps its tabs equal to the app's.
  - `boot()` starts from the copy on the device (`meta/me` with user, prefs and config in IndexedDB) and draws at once; `/api/v1/me` replaces it a moment later. Writes made before it answers wait for the CSRF token (`awaitCSRF`).
  - The shell draws nothing until the router has its first route: until then `index.html`'s frame stays up. Start-up answers (`/api/v1/me`, Home's goal rings) used to draw Home before a game link was routed, and Home's greeting flashed on phones.
  - Not after the login page: `login.js` sets `cato-signed-in` in `localStorage`, and the next start waits for the server, since whoever signed in may be someone else. A different user from the server clears the device copy and reloads.
  - Home's last answer is kept as `meta/home:<user>` and loaded before the first draw (`primeHome`), so the streak line and sections don't arrive one by one and push the page down.
  - The game hero is top-aligned: the title drawn from the local copy or the tapped tile stays put when the facts arrive.
  - A reload of any screen goes from that frame straight to the finished screen (see the hold below).
- **No flicker between screens: a screen is drawn once, finished.**
  - One second after `load`, `features/idle.js` prefetches Library, Discover, You and the game page (through `loadScreen`), so a first visit doesn't wait for code.
  - A screen whose code is still loading keeps the current screen for 200 ms before drawing the loading grid.
  - **The hold.** Moving to another screen, the shell calls its `enter(route)` before drawing it and draws nothing until that settles, for at most 250 ms: the current screen stays up meanwhile. So `enter` resolves when the screen has what it shows first: its data, and the covers at the top, decoded with `ui/decode.js` `decodeCovers` (the game page waits for its details, its reviews and its cover; Discover for its shelves and their first covers; Library, profiles, lists and Year in Review for their first covers). Past 250 ms the screen draws what it has.
  - **No hold** for a filter change (same screen, new query: Library's search draws per keystroke), for Home (drawn from its device copy, refreshed after), or for back/forward. The router marks each history entry with the page load's id when it first routes it, so `route.revisit` means a screen this page drew, with its data still in memory. Holding there left the old screen up under Safari's swipe-back snapshot, which flashed it.
  - **Covers decoded ahead** (`decoded` in `ui/parts.js`), and eager ones, load `eager` and decode `sync`: a lazy image waits for layout, and Safari decodes an image again, asynchronously, once it has been off the page, so even cached covers drew as empty boxes for a frame or two. Alt text is transparent while a cover loads (Firefox drew it in the box).
  - **No View Transitions.** The game page's cross-fade painted a black frame in Firefox and ghosted one screen over the other in Safari.
  - **The game page:** pressing on a game link (`pointerdown`, from `features/idle.js`) starts that game's request before the tap ends, and the page uses it once. The hero's blurred backdrop is an `<img>` with the cover's own sources, so it reuses the image the cover loaded (a CSS background at another size arrived a frame late). Past the hold, the game page draws the copy from an earlier visit this session (the last 50 are kept), else the tapped tile: the shell keeps the last click target in `app.tapped`, and the game page reads the tile's name and cover from it.
  - **The lit tab:** a game, a profile or a list keeps the tab it was opened from (`tab(name, prev)`); opened directly, a game sits under Library and a profile or list under You.
- **Swipes are reported too.** Each sideways gesture of 40 px or more posts `/api/v1/diagnostics/swipe` with what came of it (`back`, `forward`, `too short`, `cancelled by the browser`, or `ignored:` and why), logged as `swipe gesture`; at most 20 a page.
- **Unexpected reloads are reported.** `features/idle.js` notes each in-app navigation in `sessionStorage`, with the page's last few events (link taps, navigations and who began them where the Navigation API exists, errors, `pagehide` and whether it was `persisted`), rewritten as each happens so a killed page leaves it too. If the tab's next page starts within 5 s of one, it posts `/api/v1/diagnostics/reload`, and the server logs `unexpected reload` with the trail, where the new page started and the user agent. Read those log lines first when someone reports a flicker or lost state on a device you can't test. They found the fragment-navigation reloads above: a tap, `navigate push … hash`, then a new page 60–100 ms later, with no script navigation in between.
- **The initial JS budget is nearly full** (59.1–59.7 of 60 KB: the measure moves by a few hundred bytes from run to run). New shell code goes into `features/idle.js` or another lazy module. Comments ship too (there's no build step), so keep the long explanations here rather than in eager modules.

## Data and writes

- **The library syncs to IndexedDB** as a compact index (`/api/v1/library/index?since=rev`). Screens render from `app.store`, which filters, sorts and searches on the device (`js/core/filter.js`), so screens work offline.
- **Every library write goes through `features/actions.js`:** optimistic update, then the outbox (replayed when back online), then an Undo toast.
- **Writes send `played_on: localDay()`**, the person's local day.

## Rendering rules

- **Render only through lit-html.** No `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `unsafeHTML`, `eval` or `javascript:` URLs; `scripts/lint-js.cjs` enforces this.
- **Trusted Types is enforced by the CSP.** Script URLs need a named policy (see `cato-sw` in `app.js` and `login.js`).
- **Cover images fall back** through `@error` handlers and delegated listeners, never inline `onerror`.
- **Icons:** `icon(name)` only knows the names in `scripts/icon-list.txt`. Add a name there and run `scripts/vendor-update.sh`. Vendored files change only through that script; a Go test checks `js/vendor/CHECKSUMS`.
- **CSS:**
  - files are `css/tokens.css` (design tokens; light and dark), `base.css`, `components.css` and `views.css`;
  - use the tokens, never raw colours;
  - status is never shown by colour alone.

## Assets and the service worker

- **Versioning is automatic** (`internal/http/assets.go`). The server hashes every file under `web/static` and:
  - rewrites local `src`/`href` in HTML to `?v=<hash>`;
  - injects an import map and modulepreload hints at the `<!-- assets -->` marker (keep that marker in the `<head>` of every app page);
  - stamps `__CATO_ASSET_VERSION__` into the service worker;
  - serves `/asset-manifest.json`.

  There's no cache name to bump. Import modules by relative or absolute `/js/...` paths.
- **The service worker** (`service-worker.js`) caches `cato-shell-<version>`:
  - precache from the asset manifest;
  - versioned assets cache-first;
  - navigations network-first, falling back to the cached shell;
  - covers in a 2,000-entry LRU;
  - it never touches `/api/`;
  - it handles `push` and `notificationclick`.

  Its regression harness is `scripts/cache-regression.cjs`.
- **Keep these in the `<head>` of `index.html` and `login.html`:**
  - `js/head-init.js` (sets `--app-h` before first paint);
  - `js/theme-init.js` (applies the theme before first paint);
  - the manifest link;
  - the `<!-- assets -->` marker.

  Removing them breaks install, offline or first paint.
- **The manifest** (`manifest.webmanifest`) has the shortcuts (Log time, Search, Up Next, Wishlist) and the `share_target` (`/share`). `offline.html` is the fallback for non-app routes, and `login.html` also registers the service worker.

## Gestures and the clipboard

- **Swipe navigation** (`features/swipe.js`) is loaded on the first touch. A long horizontal swipe goes back (right) or forward (left). It ignores:
  - swipes starting within 24 px of the screen edges (the OS's own gesture zone);
  - horizontal scrollers, fields and the rating slider;
  - open sheets and menus.

  `prefs.swipe_nav` turns it off. Use touch events, not pointer events: browsers cancel pointer streams when they start scrolling.
- **Copying** goes through `ui/clipboard.js` `copyText`. On plain HTTP (the NAS) there's no Clipboard API, so it copies from a selected field.

## Browser differences we've hit

- **WebKit:** asking `PushManager.getSubscription()` before notification permission is granted can wedge later navigations. Only look it up once permission is `granted`, and with a timeout (`features/reach.js`).
- **Firefox:**
  - date inputs draw "mm / dd / yyyy" wider, with spaces and a calendar button, so give them room (`.date-field`);
  - layout bugs that Chrome tolerates show up here (see the frame above).
- **Secure context:** on plain HTTP (the NAS), service worker, push, passkeys, clipboard and share target are unavailable. The app must keep working without them, so feature-detect, never assume.

## Accessibility (enforced by axe in E2E)

- One `role=status` live region (the toast region).
- Sheets are modal `<dialog>`s, and focus returns to the opener.
- The status control is a radio group; the rating is a slider (arrow keys move in half stars).
- Touch targets are at least 44 px; `:focus-visible` outlines and a skip link are present.
- `prefers-reduced-motion` turns off animation and confetti.
- `viewport-fit=cover` with safe-area insets.
- Keyboard: `1`–`4` switch tabs, `L` logs, `/` searches, `Cmd/Ctrl-K` opens the palette; the Library adds `j`/`k`/`Enter`/`s`/`r`/`l`. Shortcuts never fire while typing or while a sheet or menu is open.
- New screens go into `e2e/tests/a11y.spec.ts`.

## Performance budget (`e2e/tests/perf.spec.ts`)

For a cold load of Home:

- initial JS at most 60 KB gzipped;
- CSS at most 20 KB;
- at most 12 render-blocking requests;
- LCP under 1 s;
- no module waterfall: every eager module is requested straight from the HTML;
- INP under 100 ms.

Anything Home doesn't need for first paint goes behind a dynamic `import()`.
