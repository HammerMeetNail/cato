# Cato API v1

All endpoints live under `/api/v1`, need a session cookie, and are served over HTTPS. Handlers are in `internal/http/v1`; the domain logic is in `internal/library` and `internal/catalog`.

## Conventions

- **JSON, snake_case.** Times are RFC 3339 (`updated_at`), calendar days are `YYYY-MM-DD` (`played_on`), release dates are Unix seconds.
- **Writes** (`POST`, `PUT`, `PATCH`, `DELETE`) need the `X-CSRF-Token` header (from `GET /api/v1/me`). They are rate limited to 300 per minute per account (`429 rate_limited` with `Retry-After`).
- **Collections** return `{ "items": [...], "next_cursor": "...", "total": n }`. `total` is only on the first page. Follow `next_cursor` with `?cursor=`.
- **Errors** return `{ "error": "code", "message": "Readable text", "fields": { "rating": "…" } }`. `fields` names invalid inputs.
- **Mutations** return the changed item with `library_rev` and an `undo_id`. Send `POST /api/v1/undo/{undo_id}` within 15 minutes to roll the change back on the server.
- **Caching.** Every `GET` has an `ETag`; send it back as `If-None-Match` to get `304`. Library responses derive their tag from the per-user `library_rev` (sent as `X-Library-Rev`), so a `304` costs one indexed lookup. Responses are `Cache-Control: no-store`: the app keeps its own copy in IndexedDB.

## Library

| Method and path | Purpose |
|---|---|
| `GET /library` | One page. Filters: `shelf`/`status` (repeatable or comma-separated), `q` (name, notes, tags, aliases), `tag` (repeatable) with `tag_op=or`, `platform` (available on), `owned_platform`, `format` (`digital`, `physical`, `both`, `none`), `year_from`/`year_to`, `release_from`/`release_to`. `sort`: `updated` (default, keyset-paged), `added`, `added_old`, `name`, `name_desc`, `rating`, `rating_low`, `hours`, `hours_low`, `release_new`, `release_old`, `critic_rating`, `critic_rating_low`, `platform`, `available_platform` (offset-paged with `offset`). `limit` ≤ 200. |
| `GET /library/index?since=REV` | The compact on-device index: `{rev, full, fields, items: [[...]], deleted: [game_id], platform_names}`. Without `since` (or with a revision the server doesn't have) it is the whole library (`full: true`); otherwise only rows changed after `since` plus removed games. |
| `GET /library/counts` | `{all, wishlist, backlog, playing, paused, completed, abandoned}`. |
| `GET /library/tags?q=` | Your tags, most used first. |
| `GET /library/platforms` | Platforms you own games on. |
| `GET /library/{game_id}` | One item (404 when not in your library). |
| `PUT /library/{game_id}` | Add (201) or update (200) with a partial body (below). |
| `PATCH /library/{game_id}` | Update an item in your library (404 otherwise). |
| `DELETE /library/{game_id}` | Remove it and its journal: `{undo_id, library_rev}`. |
| `POST /library/bulk` | `{game_ids, status?, add_tags?, remove_tags?, remove?}` → `{updated, undo_id, library_rev}`; one undo restores every item. |
| `GET /library/{game_id}/playthroughs` | Runs of the game, newest first. |
| `POST /undo/{undo_id}` | Restore what that write replaced: `{game_ids, library_rev}`; `410 undo_expired` once used or after 15 minutes. |

**Item body** (`PUT`/`PATCH`; every field optional):

```json
{
  "status": "wishlist|backlog|playing|paused|completed|abandoned",
  "rating": 0,                 // 0–100, 0 = unrated (5 stars = 100)
  "minutes": 600,              // set the total (recorded as an adjustment)
  "minutes_delta": 60,         // log play time (a session; ≤ 1440 per entry)
  "played_on": "2026-10-14",   // your local day for the session and playthrough dates
  "session_note": "",
  "tags": ["co-op"], "notes": "",
  "platforms": ["Nintendo Switch"], "medium": "physical|digital|",
  "started_at": "2026-10-01",  // "" clears; omitted keeps (and auto-tracks)
  "completed_at": "",
  "completion": "main|main_extras|completionist|endless"
}
```

Adding an edition (a game with an IGDB `version_parent`) adds its base game; the edition is recorded on the playthrough. Status changes keep the journal in step: moving to Playing starts a playthrough (a replay if the last one finished), Finished and Dropped close it (Dropped records `completion: dropped`), and Paused keeps it open.

**Item:**

```json
{ "game_id": 1942, "status": "playing", "rating": 90, "minutes": 2280, "tags": [], "notes": "",
  "started_at": "…", "completed_at": null, "created_at": "…", "updated_at": "…",
  "platforms": ["PC (Microsoft Windows)"], "medium": "digital", "current_playthrough_id": 12, "rev": 381,
  "game": { "id": 1942, "name": "The Witcher 3", "slug": "…", "cover": "/covers/1942.jpg",
            "release_date": 1431993600, "platforms": ["PC (Microsoft Windows)", "…"] } }
```

## Journal

| Method and path | Purpose |
|---|---|
| `GET /sessions?from=&to=&game_id=&all=1` | Diary entries, newest first. `all=1` includes adjustments and legacy totals. |
| `POST /sessions` | `{game_id, minutes (1–1440), played_on?, note?}` → item, session, `undo_id`. |
| `PATCH /sessions/{id}` | `{played_on?, minutes?, note?}`. |
| `DELETE /sessions/{id}` | Its minutes leave the total. |
| `PATCH /playthroughs/{id}` | `{platform?, started_on?, finished_on?, completion?, rating?, note?}` (`""` clears dates). |
| `DELETE /playthroughs/{id}` | Removes the run and its sessions. |
| `GET /timer` | `{timer: {game_id, started_at, game_name, cover} | null, server_time}`. |
| `POST /timer` | `{game_id}` starts (or restarts) the session timer, visible on every device. |
| `POST /timer/stop` | `{log, minutes?, played_on?}`: with `log`, the elapsed time (or your corrected `minutes`, capped at a day) becomes a session. `409 no_timer` when none runs. |

The item's `minutes` always equals the sum of its sessions (adjustments included).

## Screens

| Method and path | Purpose |
|---|---|
| `GET /me` | `{user, csrf_token, prefs, library_rev}`. |
| `PATCH /me/prefs` | Merge settings: `theme` (`system`/`light`/`dark`), `rating_display` (`stars`/`ten`/`hundred`), `default_platforms`, `library_layout` (`grid`/`list`), `density` (`comfortable`/`compact`), `library_sort`, `up_next` (≤ 5 game IDs), `streaks`, `week_start`, `onboarding_done`, `haptics`, `confetti`. Unknown keys are rejected. |
| `GET /home?today=` | Playing and paused items, Up Next picks with reasons (your pinned queue first), coming_soon (wishlist games with a release date ahead), recent sessions, the timer, counts, streak, and whether to show onboarding. |
| `GET /devices` | Signed-in sessions `{id, user_agent, created_at, last_seen_at, current}`; `DELETE /devices/{id}` signs one out, `POST /devices/revoke-others` all but this one. |
| `GET /stats?year=&today=` | Totals, the year's minutes/sessions/days/finished, finished per year by completion type, minutes per month (12), the year's heatmap, rating distribution (ten half-star buckets), top tags/platforms/genres, backlog age and the longest session. |
| `GET /export.json` | Everything, re-importable: items with playthroughs and sessions, and settings. |
| `GET /export.csv` | Spreadsheet export (formula-safe cells). |

## Catalog

| Method and path | Purpose |
|---|---|
| `GET /games/{id}` | The game page: `{game, extras, estimate, item, playthroughs, sessions}` (`item` is null when the game isn't in your library). A game seen for the first time fetches its IGDB details first (up to 3 s); stale details refresh in the background. `extras`: `ttb` (minutes: hastily/normally/completely, count), genres, themes, modes, perspectives, game_type, developers, publishers, series (games in release order), related (`similar`, `dlc`, `expansion`, `standalone`, `remake`, `remaster`, `port`, `bundle`), release_dates, screenshots/artworks (IGDB image IDs), videos (YouTube IDs), links (stores first), age_ratings, languages, multiplayer, editions / base_game, source (`igdb` or `custom`). `estimate`: `{minutes, weeks, pace: {multiplier, weekly_minutes, samples}}` at your pace. |
| `POST /games` | Add a custom game: `{name, platform?, year?, summary?, cover?}` (`cover` is a `data:image/...;base64,` URL; JPEG, PNG or GIF, re-encoded). Returns `{game}` with a negative ID. |
| `PUT /games/{id}/cover` | Replace a custom game's cover (its creator only): `{cover}`. |
| `GET /discover` | `{shelves: [{id, title, subtitle, items}]}`: trending, coming-soon (your platforms), new, instance, because, short, gems, classics (popular when little else is known). |
| `GET /up-next?fit=&n=` | Up Next picks with reasons, `fit` = `tonight`, `weekend` or `long`, `n` ≤ 10. |
| `GET /genres` | Genre names for the search facet. |
| `GET /typeahead?q=` | `{library: [Item], catalog: [result]}`; your library first. Each catalog result carries your `status`. |
| `GET /search?q=&sort=&platform=&genre=&max_ttb=&year_from=&year_to=&min_rating=&in_library=&limit=&offset=` | Full catalog search: `{items, total, next_offset}`; `max_ttb` is in hours. Rate limited per IP. |
| `GET /platforms?q=` | Platform names. |
