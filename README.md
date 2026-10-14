# Cato

A local-network videogame library. Go + SQLite + vanilla JS.

Browse, search, rate, and organize your game collection. Keep your data on your own hardware.

## Quick Start

```bash
make run   # builds, then serves HTTPS with a self-signed dev certificate
```

Open `https://localhost:7080` and accept the certificate warning once. Cato is
HTTPS-only: browsers enable the service worker (install/offline), passkeys and
push only in a secure context. `make run` sets `CATO_DEV_TLS=1`, which creates a
30-day ECDSA certificate in `data/dev-tls/` with Go's standard library (no
openssl, mkcert or trust-store changes). `cato dev-cert --out DIR` writes one
explicitly and prints its SPKI SHA-256 pin. To use the dev server from a phone,
see [docs/dev-https.md](docs/dev-https.md) (optional).

## Docker

```bash
make deploy-build  # Dockerfile packages the Linux/amd64 binary
docker compose up -d --build
```

Copy `.env.example` to `.env` and choose how HTTPS is provided first. See the
[production runbook](docs/production-runbook.md) for HTTPS, release validation,
backups, restore and rollback.

Release automation follows Nabu's version-tag workflow: `DRY_RUN=1 make deploy`
previews the next tag; confirming `make deploy` pushes it and triggers image
scanning/signing and the configured deployment. Main/PR pushes only validate and
build. The original NAS transport is now `make deploy-nas`. Configure the registry
and production environment using the runbook before cutting a release tag.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CATO_LISTEN_ADDR` | `:7080` | Listen address |
| `CATO_DB_PATH` | `data/cato.db` | SQLite database path |
| `CATO_STATIC_DIR` | `web/static` | Static files directory |
| `CATO_COVER_DIR` | `data/covers` | Cover image cache directory |
| `CATO_BASE_URL` | — | Externally visible `https://` origin. Required behind a TLS-terminating proxy and for Google OAuth from other devices (the OAuth redirect is built from it). Non-https values refuse to start. |
| `CATO_TLS_CERT_FILE` / `CATO_TLS_KEY_FILE` | — | Serve HTTPS natively (TLS 1.2+, HTTP/2). Renewed files are picked up without a restart. |
| `CATO_DEV_TLS` | — (`1` under `make run`) | Generate/reuse a self-signed dev certificate in `CATO_DEV_TLS_DIR` (`data/dev-tls`). No HSTS is sent with it. |
| `CATO_HTTP_REDIRECT_ADDR` | — | Optional plain-HTTP listener that answers every request with a 308 to HTTPS. Needs native TLS. |
| `CATO_TRUSTED_PROXIES` | — | Comma-separated CIDRs/IPs whose `X-Forwarded-For`/`X-Forwarded-Proto` are believed (rate limiting, scheme). Ignored from anyone else. |
| `CATO_ALLOW_INSECURE` | — | `1` permits plain HTTP. For tests only. |
| `CATO_SECURE_COOKIES` | `false` | Legacy override; HTTPS already forces `Secure`. |

Over HTTPS the session cookie is `__Host-cato_session` (`Secure`, `Path=/`, no
`Domain`), and responses add HSTS (`max-age=31536000`, not with dev certificates)
and `upgrade-insecure-requests`. Upgrading from an HTTP deployment signs everyone
out once.
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

Cato searches IGDB on-demand: any query of 3+ characters that hasn't been seen in the last 24 hours schedules a bounded background refresh (regardless of how many local matches exist). Local results return immediately, and successful search markers, including empty responses, are cached for 24 hours.

A background refresh loop updates metadata for games older than 90 days — up to 100 per cycle, every 6 hours (~400/day), ordered purely by `source_updated_at`. Games deleted from IGDB are marked refreshed so the queue advances instead of retrying them forever.

Cover URLs are built from IGDB's `cover.image_id` (the authoritative covers-table field) — not guessed from numeric IDs.

Requires a Twitch client ID and secret for the IGDB API.

## Cover Images

Covers from IGDB are downloaded to `data/covers/{id}.jpg` (up to 5 in parallel). Downloads are validated (HTTP 200, size cap, image magic bytes) and written atomically. Failures retry with exponential backoff (2m → 16m); after 5 attempts a job is exhausted but is revived automatically if the game's cover URL changes or it is re-enqueued (e.g. after a metadata refresh). Only games in someone's library are downloaded.

At startup Cato also repairs cover data poisoned by earlier bugs: URLs pointing at the defunct `images.cato.com` host are cleared, and exhausted jobs are re-fetched with corrected URLs.

## Auth

Email/password signup and login, plus Google OAuth. Sessions are stored as opaque hashed tokens. Unsafe methods require a CSRF token returned by `GET /api/me`. Expired sessions and cache entries are swept daily by a background job.

Google OAuth needs `CATO_BASE_URL` set to the `https://` address users type into their browser (a warning is logged at startup otherwise). Google also accepts `https://localhost` redirect URIs for development.

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
# Verified online snapshot (Python 3.10+)
make db-backup

# Stop every process using the database before restoring
make stop
make db-restore FILE=backup/CHOSEN-SNAPSHOT.db SERVICE_STOPPED=1
```

Restore preserves a pre-restore snapshot. Follow the
[NAS restore procedure](docs/production-runbook.md#restore-and-rollback) for Docker;
never copy over a running SQLite database.

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
make release-check # build, vet, race, frontend, backup, dependency and browser checks
```

Go 1.26.9 is selected by `go.mod`. Release checks also require Node 24+, Python
3.10+, sqlite3, and Playwright's Chromium/WebKit system dependencies (see the runbook).
The [browser coverage map](e2e/README.md) lists tested journeys and device limits.
