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

Cato searches IGDB on-demand when local results are insufficient (fewer than 3 matches, query ≥ 3 characters). Results are cached for 24 hours and permanently stored in the database.

A background refresh loop updates metadata for games older than 90 days (max 100/day, prioritizing games in your library).

Requires a Twitch client ID and secret for the IGDB API.

## Cover Images

Covers from IGDB are automatically downloaded to `data/covers/{id}.jpg` with rate limiting and exponential backoff. Covers are never re-downloaded unless deleted. Library games get download priority over catalog-only games.

## Auth

Email/password signup and login, plus Google OAuth. Sessions are stored as opaque hashed tokens. Unsafe methods require a CSRF token returned by `GET /api/me`.

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
| `GET` | `/api/games/search?q=zelda` | No | Search games |
| `GET` | `/api/games/{id}` | No | Get game detail |
| `GET` | `/api/library?status=backlog` | Session | List library |
| `POST` | `/api/library/{gameID}` | Session+CSRF | Add/update item |
| `DELETE` | `/api/library/{gameID}` | Session+CSRF | Remove item |

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
