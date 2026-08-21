# UX Plan — easy, friendly game progress & ownership logging

Goal: minimize the number of interactions between "something happened"
(started a game, played an evening, beat it, bought it) and that fact being
recorded in Cato.

Today the core loop costs 4–5 interactions (search → dropdown → modal →
select → save), and the result is nearly invisible: cards don't show status,
playtime requires remembering cumulative hours, failures use native `alert()`.
This plan fixes that in five tiers, each shippable independently.

## Tier 1 — One-click logging (highest impact)

1. **Status badges on every card.** Render the unused `.status-badge`
   treatment (`web/static/css/app.css`) in `buildCardHTML`
   (`web/static/js/library.js`). Completed cards keep their year badge.
   In "All" view you should be able to read your library at a glance.
2. **Quick status actions on cards.** Hover-reveal (always-visible on touch)
   action bar over the cover: ▶ mark playing, ✓ mark finished. Backed by a
   new **PATCH /api/library/{gameID}** that merges partial updates so quick
   actions can't zero out rating/tags/notes the way a full POST upsert would.
   Optimistic UI + toast; card animates out when the active tab filter no
   longer matches.
3. **"Playing now" hero strip.** Horizontal row above the status tabs listing
   `status=playing` games with: day counter (from `started_at`),
   **+30m / +1h / +2h** playtime buttons (server-side
   `playtime_delta_minutes` — no read-modify-write races), and a **✓ Finished**
   button. Hidden while browsing search results; hidden entirely when nothing
   is being played.

## Tier 2 — Friendlier forms & feedback

4. **Kill native dialogs.** Themed toast for errors; removal drops `confirm()`
   in favor of optimistic remove + **Undo** toast (re-adds the previous
   payload via the upsert endpoint).
5. **Playtime steppers** (done via Tier 1 hero; modal keeps absolute hours
   for backfilling).
6. **Human status labels** in the modal (`<select>` shows "Playing", not
   "playing").
7. **Date affordances**: surface auto-set dates as editable hints rather than
   read-only rows discovered late. *(later)*

## Tier 3 — Real ownership modeling

8. Per-item `platform` (choices from `games.platforms_json`) and
   `physical|digital`. Needs a migration (`internal/db/migrate.go`), API
   fields, modal field, card subtext.
9. **Wishlist → owned** transition ("I bought this" action on wishlist cards).
10. Helpful empty states: suggested popular games on a fresh library. *(later)*

## Tier 4 — Make the payoff visible

11. Stats strip / year-in-review (completed count, hours logged, top tags) —
    derivable from existing tables. *(later)*
12. Recent-activity feed from `updated_at`. *(later)*

## Tier 5 — Polish

13. Simplify search copy; move `$tag` syntax docs behind a `?` popover. *(later)*
14. Mobile: bottom nav, FAB for add, bigger touch targets. *(partially: quick
    actions are touch-friendly)*
15. Settings page content (display name, CSV/JSON export, theme). *(later)*
16. Keyboard shortcuts (`/` focuses search). *(later)*

---

## Phase 1 scope (done)

Items 1, 2, 3, 4, 6 plus the PATCH API that powers them: status badges on
cards, hover quick actions, Playing-now hero (+time / finish), undo-able
removal, human status labels.

## Phase 2 scope (done)

Ownership modeling (items 8, 9) plus quick wins from Tiers 4–5:

- **Migration v6**: `library_items.platform` + `library_items.medium`
  ('' = unset; medium ∈ {physical, digital}).
- **API**: platform/medium flow through list/get/upsert/PATCH (merge rules
  identical to other fields); library rows now carry the game's `platforms`
  array so the edit form can offer suggestions without an extra fetch.
- **Modal**: "Owned on" input with datalist + "Format" select.
- **Cards**: ownership chip in the tag row ("Switch 2 · Physical");
  wishlist cards get a **$ Bought** quick action that moves the game to the
  backlog.
- **Stats strip** under the tabs: "N games · M finished · ~Xh logged"
  (`total_minutes` added to /api/library/counts).
- **CSV export**: GET /api/library/export (session-cookie auth so it works
  as a plain download link).
- **Settings page** replaces the stub: account info, library summary, export.
- Friendly contextual empty states with a search CTA; `/` focuses search.

## Phase 3 scope (done)

- **Editable dates in the modal (2.7)**: started/completed are now
  `<input type="date">` fields instead of read-only rows. Only changed values
  are sent, so untouched edits never disturb server-managed dates and the
  auto-track behavior still fills them on status transitions. Manual fixes
  ("I actually beat it yesterday") finally stick.
- **Suggested games for fresh libraries (3.10)**: new endpoint
  `GET /api/library/suggestions` — popular catalog games (popularity_score)
  the caller doesn't own, covers only. The empty state renders them as a
  tap-to-add grid; adding drops straight into Backlog.
- **Search copy simplified (5.13)**: placeholder is just "Search games…";
  the `$tag` syntax doc collapsed to two short lines shown on focus.
- **Mobile add FAB (5.14)**: floating "+" on small screens that jumps to the
  search box.

## Phase 4 scope (done — plan complete)

- **Stats dialog (4.11–4.12)**: the stats strip is now clickable, opening a
  "Library in numbers" dialog (`GET /api/library/stats`): lifetime totals,
  this-year activity line, finished-per-year bar chart (year in review),
  most-used tags, platforms, and a **recent-updates feed** whose rows
  deep-link into the game modal.
- **Display-name editing**: `PATCH /api/me {display_name}` (inline session +
  CSRF validation matching the route's no-middleware design) + editor in
  Settings; the topbar picks it up on next load.
- **Theme support**: light theme via `[data-theme="light"]` CSS variable
  overrides only (no component duplication), persisted in localStorage,
  applied pre-paint by a tiny inline script on all three pages to avoid
  flash-of-dark. Dark remains the default.
- **Mobile tabs (5.14)**: single-row horizontally scrollable tab strip
  instead of wrapping onto two lines. A separate bottom nav was considered
  and rejected: six categories don't fit a bottom bar, and the FAB already
  covers the primary action.

### Verification

- `go vet ./...`, `make test`, `make build`.
- New tests: PATCH merge semantics (fields preserved/cleared, playtime delta,
  timestamp clear), ownership round-trip/preservation/clearing/validation,
  CSRF requirement.
- Manual smoke via throwaway DB + browser: badges, quick actions, hero time
  logging, undo removal, wishlist→bought, ownership editing with datalist,
  stats strip, CSV download, settings page, `/` shortcut.

