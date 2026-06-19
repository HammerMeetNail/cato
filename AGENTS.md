# AGENTS.md

## Build & Test

```bash
make test    # go test ./...
make build   # go build -o cato ./cmd/cato
make run     # build + start locally (background, writes .pid)
make stop    # kill background process
```

There is no lint, formatter, or typecheck command defined.

## Architecture

Single Go module (`cato`), single SQLite database, vanilla JS frontend.

```
cmd/cato/       Main entrypoint (does not exist yet)
internal/auth/  Passwords (bcrypt cost 12), sessions (SHA-256 hashed in DB), CSRF, rate limiting (in-memory)
internal/config/ Env-based config (CATO_* prefix)
internal/covers/ Background cover downloader (IGDB images → data/covers/{id}.jpg)
internal/db/    SQLite open + migration runner
internal/games/ Search, types, store, IGDB rate limiter. Defines IGDBClient interface.
internal/http/  HTTP server, handlers, middleware. All JSON responses via writeJSON/errResp.
internal/igdb/  IGDB API v4 client (Twitch OAuth token auth)
internal/importer/ Postgres COPY parser for game seed imports
web/static/     HTML, CSS, JS (vanilla, no build step)
```

## Key Conventions

- **SQLite is single-writer**: `db.Open()` sets `SetMaxOpenConns(1)`. Never increase this.
- **Tests use real SQLite**: No mocks for the database. Pattern: `db.Open(t.TempDir() + "/test.db")` → `db.Migrate()` → use. Every test file that needs a DB repeats this setup.
- **Session storage**: Session IDs stored in cookies are raw tokens; the DB stores SHA-256 hashes. `GetSession` restores the unhashed ID on the returned struct.
- **CSRF**: All unsafe methods (POST/PUT/DELETE) require `X-CSRF-Token` header. Token comes from `GET /api/me`. Middleware: `AuthRequired` then `CSRFRequired`.
- **IGDB is optional**: When `IGDB_CLIENT_ID` is empty, a `noopIGDBClient` is used. IGDB calls auto-fallback to local results on error.
- **Rate limiting**: In-memory only, not shared across processes. `auth.RateLimiter` for login/signup. `games.IGDBRateLimiter` (1 req/sec) for IGDB API.
- **Environment config fallbacks**: `IGDB_CLIENT_ID` falls back to `TWITCH_OAUTH_ID`, `IGDB_CLIENT_SECRET` falls back to `TWITCH_OAUTH_SECRET`. The docker-compose sets both.
- **No codegen, no ORM, no frontend build step**. Raw SQL everywhere. Vanilla JS with no bundler.
