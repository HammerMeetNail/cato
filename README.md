# Cato

A local-network videogame library. Go + SQLite + vanilla JS.

Browse, search, rate, and organize your game collection. Keep your data on your own hardware.

## Quick Start

```bash
go build -o cato ./cmd/cato
mkdir -p data/covers
./cato
```

Open `http://localhost:7080`.

## Docker

```bash
docker compose up -d --build
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CATO_LISTEN_ADDR` | `:7080` | Listen address |
| `CATO_DB_PATH` | `data/cato.db` | SQLite database path |
| `CATO_STATIC_DIR` | `web/static` | Static files directory |
| `CATO_COVER_DIR` | `data/covers` | Cover image cache directory |
| `CATO_SECURE_COOKIES` | `false` | Enable for HTTPS |
| `CATO_BASE_URL` | — | Externally visible base URL (e.g. `http://10.0.0.42:7080`). Required for Google OAuth when Cato is reached from other devices — the OAuth redirect is built from it. |
| `GOOGLE_KEY` | — | Google OAuth client ID |
| `GOOGLE_SECRET` | — | Google OAuth client secret |
| `IGDB_CLIENT_ID` | — | Twitch client ID for IGDB API |
| `IGDB_CLIENT_SECRET` | — | Twitch client secret for IGDB API |

## Seeding From A Postgres Dump

```bash
pg_restore --data-only --table=games --file=/tmp/games-copy.sql your_dump.sql
cato import-games --input /tmp/games-copy.sql --db data/cato.db
```

The import is idempotent — safe to run multiple times.

## IGDB Integration

Cato searches IGDB on-demand: any query of 3+ characters that hasn't been seen in the last 24 hours triggers an IGDB refresh (regardless of how many local matches exist). Search-marker results are cached for 24 hours.

A background refresh loop updates metadata for games older than 90 days — up to 100 per cycle, every 6 hours (~400/day), ordered purely by `source_updated_at`. Games deleted from IGDB are marked refreshed so the queue advances instead of retrying them forever.

Cover URLs are built from IGDB's `cover.image_id` (the authoritative covers-table field) — not guessed from numeric IDs.

Requires a Twitch client ID and secret for the IGDB API.

## Cover Images

Covers from IGDB are downloaded to `data/covers/{id}.jpg` (up to 5 in parallel). Downloads are validated (HTTP 200, size cap, image magic bytes) and written atomically. Failures retry with exponential backoff (2m → 16m); after 5 attempts a job is exhausted but is revived automatically if the game's cover URL changes or it is re-enqueued (e.g. after a metadata refresh). Only games in someone's library are downloaded.

At startup Cato also repairs cover data poisoned by earlier bugs: URLs pointing at the defunct `images.cato.com` host are cleared, and exhausted jobs are re-fetched with corrected URLs.

## Auth

Email/password signup and login, plus Google OAuth. Sessions are stored as opaque hashed tokens. Unsafe methods require a CSRF token returned by `GET /api/me`. Expired sessions and cache entries are swept daily by a background job.

Google OAuth needs `CATO_BASE_URL` set to the address users type into their browser (a warning is logged at startup otherwise).

## API

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/healthz` | No | Health check |
| `GET` | `/api/me` | No | Current user info + CSRF token |
| `POST` | `/api/auth/signup` | No | Create account |
| `POST` | `/api/auth/login` | No | Login |
| `POST` | `/api/auth/logout` | No | Logout |
| `GET` | `/api/auth/google/start` | No | Google OAuth start |
| `GET` | `/api/auth/google/callback` | No | Google OAuth callback |
| `GET` | `/api/games/search?q=zelda` | No | Search games (`full=1` enables paginated results with `limit`/`offset`) |
| `GET` | `/api/games/{id}` | No | Get game detail |
| `GET` | `/api/library?status=backlog&limit=&offset=&tag=` | Session | List library (paginated; returns `X-Total-Count` / `X-Has-More` headers) |
| `GET` | `/api/library/{gameID}` | Session | Get one library item (404 if not owned) |
| `POST`/`PUT` | `/api/library/{gameID}` | Session+CSRF | Add/update item. Omitted timestamps are preserved and auto-tracked on status changes; `""` clears them. Timestamps accept RFC3339 or `YYYY-MM-DD`. |
| `DELETE` | `/api/library/{gameID}` | Session+CSRF | Remove item |
| `GET` | `/api/library/check?ids=1,2,3` | Session | Which of these game IDs are in the library |
| `GET` | `/api/library/counts` | Session | Per-status item counts |
| `GET` | `/api/library/tags?q=ps` | Session | Tag autocomplete |

## Backup

```bash
# Binary backup (fast, identical restore)
sqlite3 data/cato.db ".backup 'backup/cato-$(date +%F).db'"

# Restore
cp backup/cato-YYYY-MM-DD.db data/cato.db
```

## Architecture

```text
cmd/cato/main.go       Entry point and CLI (also serves import-games subcommand)
internal/auth          Password hashing, sessions, CSRF, rate limiting
internal/config        Environment configuration
internal/covers        Cover image downloader with background worker
internal/db            SQLite connection and migration runner
internal/games         Local-first search, ranking, IGDB fallback orchestration
internal/http          HTTP server, handlers, middleware
internal/igdb          IGDB API v4 client with Twitch token auth
internal/importer      Postgres COPY parser for game seed imports
web/static             Vanilla HTML, CSS, and JavaScript frontend
```

## Development

```bash
make test    # run all tests
make build   # compile
make run     # start locally (foreground in background)
make stop    # stop locally
```
