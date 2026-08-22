# Search UX improvements plan

Status: **approved — implement all items.** Companion to `SEARCH_PLAN.md`
(relevance/FTS work, already shipped) and `UX_PLAN.md`. This plan covers the
*experience* of searching: speed, mobile ergonomics, and new capability.

Ground rules:

- **No functionality subtracted.** Every existing behavior (keyboard nav,
  `$tag` filters, hash-routed full results, IGDB live fallback, relevance
  floor) keeps working.
- **No build step.** Vanilla JS + CSS only.
- **Backend stays raw SQL** on the two-pool SQLite design; migrations are
  append-only (`internal/db/migrate.go`).

---

## 1. Input & dropdown polish (frontend)

| # | Item | Where |
|---|------|-------|
| 1.1 | Mobile keyboard attributes on the search input: `enterkeyhint="search"`, `spellcheck="false"`, `autocapitalize="none"`, `autocorrect="off"` | `index.html` |
| 1.2 | In-input clear (✕) button — clears, refocuses, hides when empty (CSS sibling selector, like `.search-hint`) | `index.html`, `app.css` |
| 1.3 | Debounce 400ms → 200ms for game search (tag path already 200ms) | `search.js` |
| 1.4 | Loading state: show a "Searching…" spinner row while a request is in flight instead of nothing | `search.js`, `app.css` |
| 1.5 | Client-side result cache (Map keyed by query, capped ~50): backspacing re-renders instantly with no network call; AbortController still guards in-flight races | `search.js` |
| 1.6 | `scrollIntoView({block:'nearest'})` on ArrowUp/ArrowDown so the selection can never be invisible | `search.js` |
| 1.7 | Match highlighting: wrap the matched substring in `<mark>` (case-insensitive find on the display name; no highlight when the trigram match isn't contiguous) | `search.js`, `app.css` |
| 1.8 | ARIA combobox pattern: input gets `role="combobox" aria-expanded aria-controls aria-activedescendant`; results get `role="listbox"`; rows get `role="option"` + ids + `aria-selected` | `search.js`, `index.html` |

## 2. Mobile friendliness

| # | Item | Where |
|---|------|-------|
| 2.1 | Sticky search bar: `.search-wrap` sticks below the topbar while scrolling so search is always reachable (FAB remains as a shortcut that also scrolls home) | `app.css` |
| 2.2 | Dropdown becomes a near-full-height sheet on small viewports: `max-height: calc(100dvh - <input bottom>)` so it fills the visible viewport next to the virtual keyboard and scrolls internally | `app.css` |
| 2.3 | Voice search via Web Speech API — mic button inside the input row, hidden unless `webkitSpeechRecognition` exists; result is placed into the input and searched normally. Progressive enhancement, zero dependency | `search.js`, `index.html`, `app.css` |

## 3. Dropdown context & actions

| # | Item | Where |
|---|------|-------|
| 3.1 | "In library ✓" badge on dropdown rows — after rendering results, batch-check ownership via `/api/library/check?ids=…` (same endpoint the full results page uses) | `search.js` |
| 3.2 | Quick-add button per dropdown row ("+"): one tap adds to Backlog via `POST /api/library/{id}` without leaving the dropdown; flips to ✓ on success, toast confirms. Row click still opens the add/edit modal | `search.js`, `library.js` (export `showToast`) |
| 3.3 | Recent searches: last 8 queries in `localStorage` (`cato-recent-searches`), shown when an empty focused input has no tag prefix; tap to re-run, per-row ✕ to remove, "Clear all" footer. Recorded on submit paths only (not every keystroke) | `search.js` |

## 4. Backend capability

| # | Item | Where |
|---|------|-------|
| 4.1 | **Alias search**: new `game_aliases(game_id, normalized_alias)` table + `aliases_fts` trigram FTS5 table (migration v7), populated from IGDB `alternative_names.name`. Searching "botw", "acnh", or a localized title finds the main game. Alias matches rank **below every name match** (tier 3, then source tiebreaker). Aliases populate lazily: IGDB refresh paths (live search fallback + stale refresh) write them; existing catalog rows gain aliases as they're touched. No catalog-wide crawl at startup | migration v7, `igdb/client.go`, `games/store.go` |
| 4.2 | **Word-order-insensitive recall**: when the strict phrase MATCH returns nothing (e.g. "kingdom tears"), retry with unquoted token-AND MATCH before falling back to LIKE | `games/search.go`, `games/store.go` |
| 4.3 | **Total count** for the full results page: `GET /api/games/search?full=1` now sets `X-Total-Count` (COUNT over the same predicate, floor included) so the header can say "N results" | `games/store.go`, `http/handler_games.go`, `library.js` |
| 4.4 | **Sort + filter params** on the full search endpoint: `sort=relevance\|release_new\|release_old\|rating\|popularity\|name`, `year_from`, `year_to`, `min_rating`. Applied identically to the results query and the COUNT query | `games/store.go`, `handler_games.go` |

Explicitly out of scope (documented future work):

- Platform/genre filter chips — `platforms_json`/`genres_json` hold numeric
  IGDB IDs only; filtering needs ID→name lookup tables from IGDB. Sort +
  year/rating filters ship now.
- Server-side highlight spans (client-side highlighting is sufficient).
- Per-user server-side search history (localStorage recents cover it).

## 5. Results page

| # | Item | Where |
|---|------|-------|
| 5.1 | Header shows "Results for \"q\" · N games" from X-Total-Count | `library.js` |
| 5.2 | Collapsible "Sort & filter" bar under the header: sort select + year-from/to + min-rating select; applies to subsequent page loads, resets on new search | `library.js`, `app.css` |

## 6. Verification checklist

- [x] `go vet ./...` clean
- [x] `make test` green, including new tests:
  - alias search returns the main game for an alias query (abbrev + localized)
  - alias matches never outrank direct name matches; no duplicate rows
  - short-query LIKE path still works with aliases present
  - word-order-insensitive retry ("kingdom tears" → Tears of the Kingdom)
  - X-Total-Count reflects floor + filters (`TestSearchGamesPagedTotalSortFilters`)
  - sort orders actually reorder (release_new vs popularity)
- [x] `make build` succeeds
- [x] Manual smoke (scratch DB, real browser):
  - desktop dropdown: typing, ArrowUp/Down + Enter opens modal, Escape,
    ✕ clear, quick-add flips to "In library ✓", recents panel records on
    submit and re-runs from tap, mic button present in Chromium
  - mobile viewport 375×812: sticky bar under topbar, dropdown sheet at
    `calc(100dvh - 118px)`, keyboard attrs verified via computed styles
  - `$tag` flow unchanged ("Search tag …" chip renders)
  - full results page: "Results for \"q\" · N games" header, Sort & filter
    bar applies (release_new reordered live), filters hit X-Total-Count

Implementation notes / deviations discovered while building:

- The word-order retry runs only as an extra FTS token-AND pass; a healthy
  FTS path returning zero rows does **not** fall back to LIKE (that would
  turn every miss into a full-table scan).
- Alias matches always pass the relevance floor regardless of popularity.
- Aliases populate lazily via IGDB refresh paths (live search fallback,
  stale refresh); existing catalog rows gain them as they're touched.
- Platform/genre filter chips remain future work (numeric IGDB IDs only;
  needs ID→name lookup tables).
