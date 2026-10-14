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
| `PATCH /me/prefs` | Merge settings: `theme` (`system`/`light`/`dark`), `rating_display` (`stars`/`ten`/`hundred`), `default_platforms`, `library_layout` (`grid`/`list`), `density` (`comfortable`/`compact`), `library_sort`, `up_next` (≤ 5 game IDs), `streaks`, `week_start`, `onboarding_done`, `haptics`, `confetti`, `saved_views`, `notify`, `email_digest`, `release_alerts`, `release_week`, `price_alerts`. Unknown keys are rejected. |
| `GET /home?today=` | Playing and paused items, Up Next picks with reasons (your pinned queue first), coming_soon (wishlist games with a release date ahead), recent sessions, the timer, counts, streak, and whether to show onboarding. |
| `GET /devices` | Signed-in sessions `{id, user_agent, created_at, last_seen_at, current}`; `DELETE /devices/{id}` signs one out, `POST /devices/revoke-others` all but this one. |
| `GET /stats?year=&today=` | Totals, the year's minutes/sessions/days/finished, finished per year by completion type, minutes per month (12), the year's heatmap, rating distribution (ten half-star buckets), top tags/platforms/genres, backlog age and the longest session. |
| `GET /export.json` | Everything, re-importable (`version: 2`): items with playthroughs and sessions, settings, profile, reviews, lists and follows. |
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

## People

Everything is limited to this server. Profiles are private by default. Who sees what is decided in one place (`internal/social`):

- you always see your own things;
- a block, in either direction, hides everything, and disabled accounts are hidden;
- `private` is you only; `followers` adds people whose follow you accepted; `instance` is everyone with an account here;
- reviews and lists have their own `visibility`; `inherit` uses your profile's.

| Method and path | Purpose |
|---|---|
| `GET /me/profile`, `PATCH /me/profile` | Your profile: `{username, display_name, bio (≤ 500), visibility, favourites (≤ 4 game IDs), use_gravatar}`. Usernames are `[a-z0-9_.-]{1,32}`, unique ignoring case. Going `instance` accepts waiting follow requests. |
| `PUT /me/avatar`, `DELETE /me/avatar` | `{avatar: "data:image/...;base64,..."}`: re-encoded to a 256 px JPEG (metadata dropped) and served from `/avatars/` to signed-in people only. |
| `GET /users?q=` | Find people (prefix of username or name). Private profiles only match their exact username; blocks hide people both ways. |
| `GET /users/{username}` | `{person, relationship: {self, following, follows_you, blocked, blocks_you, can_see, visibility}, bio, favourites, playing, recent_finishes, stats, lists, reviews, followers, following}`. Content fields are empty unless `can_see`. 404 when they blocked you. |
| `POST`/`DELETE /users/{username}/follow` | Follow (`{following: "accepted" | "pending"}`) or unfollow / withdraw. |
| `POST`/`DELETE /users/{username}/block` | Block (ends follows both ways) or unblock. |
| `POST /followers/{username}/accept`, `DELETE /followers/{username}` | Approve a request; decline one or remove a follower. |
| `GET /people` | `{followers, following, requests, blocked}`. |
| `GET /feed?before=` | Activity of people you follow, newest first: `started`, `finished` (rating), `dropped`, `wishlisted`, `added`, `rated`, `logged`, `reviewed`, `listed`. A person's logging, adds, wishlists and additions to one list on one day are one entry (`games` lists them, `minutes` sums). An undone library write takes its activity back. |
| `GET /notifications?before=`, `POST /notifications/read` | The inbox `{items, unread}`; `{up_to}` marks read (0 = all). Kinds: `follow`, `follow_request`, `follow_accepted`, `like`, `invite_used`, and alerts. |
| `GET /games/{id}/social` | `{friends: [{person, status, rating, minutes, review_id}], community: {players, ratings, average, histogram[10]}, reviews}`. Reviews: yours, then friends', then most liked. The community counts only people you may see. |
| `GET /games/{id}/lists` | Your lists with `has_game`. |
| `POST /reviews` | `{game_id, body (≤ 10,000), spoiler, visibility, playthrough_id?}`; carries your current rating. `GET`/`PATCH`/`DELETE /reviews/{id}`. |
| `POST`/`DELETE /reviews/{id}/like` | Like or unlike a review you can see. |
| `POST /reports` | `{kind: review|list|user, target, reason}` for something you can see. |
| `GET /lists?user=` | A person's lists you may see (yours by default). |
| `POST /lists`, `GET`/`PATCH`/`DELETE /lists/{id}` | `{title (≤ 100), description (≤ 2,000), ranked, visibility}`. |
| `PUT`/`DELETE /lists/{id}/items/{game_id}` | Add (with `{note}` ≤ 500) or remove; ≤ 500 games. `POST /lists/{id}/order` `{game_ids}` reorders. |
| `GET /invites`, `POST /invites`, `DELETE /invites/{id}` | Your invite links (`{note}`): single use, 7 days. The response has the `path` (`/login?invite=…`) once. Whoever signs up with it and you follow each other. |

## Admin

Admins only (`403` otherwise). Every change is written to the audit log.

| Method and path | Purpose |
|---|---|
| `GET /admin/users`, `PATCH /admin/users/{id}` | People with role, last seen and library size; `{role, disabled}` (disabling signs out everywhere; the last admin can't be demoted or disabled: `409 last_admin`). |
| `POST /admin/users/{id}/reset-link` | A single-use password link (`/login?reset=…`, 24 hours). Using it (`POST /api/auth/reset {token, password}`) signs the account out everywhere. |
| `GET`/`PATCH /admin/settings` | `{signup: open|invite|closed}` overrides `CATO_SIGNUP` (`""` returns to it). |
| `GET /admin/invites` | Every invite. |
| `GET /admin/reports`, `POST /admin/reports/{id}/resolve` | Open reports with an excerpt; `{hide}` hides the review or list (its author still sees it, marked). |
| `POST /admin/hide` | `{kind: review|list, id, hidden}`. |
| `GET /admin/audit` | Admin actions, newest first. |
| `GET /admin/system` | Database and WAL size, last backup, cover queue, IGDB lanes, background jobs and lag. `POST /admin/jobs/{name}/run` runs one now. |
| `POST /admin/games/merge` | `{custom_id, into_id}`: fold a custom game into its catalog entry. |

## Import

Every source previews first and writes nothing; `POST /import/commit` writes the games you chose as one batch. A batch never overwrites what you recorded: a game already in your library only gains an empty rating, notes, platform or dates, extra tags, and more play time. Running the same import twice changes nothing the second time, and imports don't appear in friends' feeds.

| Method and path | Purpose |
|---|---|
| `GET /import` | `{steam_configured, steam: {steam_id, persona, sync, last_sync_at, last_error} | null, ra_user, batches, presets, fields}`. |
| `POST /import/csv/preview` | `{csv, mapping?}` → `{headers, preset, mapping, rows, items}`. The preset (Grouvee, HowLongToBeat, Infinite Backlog, Cato, generic) and the column mapping are detected; send `mapping: {columns: {title: 0, …}, rating_scale, statuses}` to override. Up to 10,000 rows. |
| `POST /import/json/preview` | `{data}` (a Cato export, as text or JSON) → `{items, extras: {reviews, lists}}`. |
| `POST /import/steam/preview` | `{profile}` (a profile link, custom URL name or SteamID64) → `{steam, items}`. The profile and its game details must be public (`400` explains otherwise; `503 not_configured` without `STEAM_WEB_API_KEY`). Suggested status: played in the last 30 days → Playing, played before → Backlog tagged `played`, never → Backlog. |
| `PATCH /import/steam` | `{sync}`: the daily job logs new Steam play time as `steam` sessions. `DELETE /import/steam` disconnects. |
| `POST /import/ra/preview` | `{user, key, remember}` → RetroAchievements progress (beaten/mastered → Finished). |
| `POST /import/commit` | `{source, entries: [{game_id, row}], extras?}` → `{batch_id, added, updated, unchanged, reviews, lists, library_rev}`. |
| `POST /import/batches/{id}/undo` | Puts every game the batch touched back as it was (games it added are removed). Works any time, once. |

**Preview items** are `{index, row, match: {id, name, year, cover}, confidence, state, alternatives, in_library}`. `state` is `matched` (an IGDB or Steam ID, or a confident name and year), `review` (a likely match to confirm, with alternatives) or `none`.

## Notifications, passkeys and barcodes

| Method and path | Purpose |
|---|---|
| `GET /push` | `{public_key (VAPID), devices, email, price_alerts, steam}`: what this server can do. |
| `POST /push/subscriptions` | A `PushSubscription.toJSON()` (`{endpoint, keys: {p256dh, auth}}`; https endpoints only). `DELETE` with `{endpoint}` removes it. |
| `POST /push/test` | Sends a test notification to your devices. |
| `GET /passkeys` | `{items, available, reason}`; passkeys need a host name, not an IP address. |
| `POST /passkeys/begin` | `PublicKeyCredentialCreationOptions` (base64url fields). Then `POST /passkeys` with `{name, id, rawId, response: {clientDataJSON, attestationObject, transports}}`. `DELETE /passkeys/{id}`. |
| `GET /barcode/{upc}` | `{code, title, confirmed?, candidate?}`: the product title (looked up once, ever) and the catalog match. `PUT /barcode/{upc}` `{game_id}` remembers the right game for everyone. |

Notification delivery follows `prefs.notify[kind].push` (release, release_soon and price are on by default; social kinds are off). Alerts: `release_alerts` (day of release on your platforms), `release_week` (a week ahead), `price_alerts: {enabled, min_cut, low_only, country}`, `email_digest` (weekly, when the server has SMTP).
