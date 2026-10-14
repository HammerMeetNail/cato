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
| `CATO_SIGNUP` | `closed` | `open`, `invite` or `closed`. The first account on an empty server is always allowed and becomes the admin. `invite` accepts anyone's invite link; `closed` only an admin's. Admins can override it on the Admin page. |
| `CATO_SEARCH_RATE_LIMIT` | `120` | Per-IP `/api/games/search` requests per minute. |
| `CATO_SECURE_COOKIES` | `false` | Legacy override; HTTPS already forces `Secure`. |
| `CATO_METRICS_TOKEN` | — | Bearer token for `/metrics` (otherwise private-network clients only). |
| `CATO_LOG_FORMAT` / `CATO_LOG_LEVEL` | `text` / `info` | `json` for structured logs. |
| `CATO_BACKUP_DIR` | `backups` next to the DB | Automatic snapshots taken before a schema upgrade (five kept). |
| `CATO_WIKIDATA_LINKS` | — | `1` adds HowLongToBeat/PCGamingWiki links via Wikidata (sends game slugs to wikidata.org). |
| `STEAM_WEB_API_KEY` | — | Steam import and the opt-in daily play time sync. |
| `ITAD_API_KEY` | — | IsThereAnyDeal price alerts for wishlists. |
| `UPCITEMDB_KEY` | — | UPCitemdb paid tier for barcode lookups (free tier: 100/day without it). |
| `CATO_PUSH_CONTACT` | `CATO_BASE_URL` | `mailto:`/`https:` contact sent to push services. VAPID keys are generated on first use and stored in the database. |
| `CATO_SMTP_HOST`, `_PORT`, `_USER`, `_PASSWORD`, `_FROM` | port `587` | Enables the optional weekly email digest (465 = implicit TLS, otherwise STARTTLS). |

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

Covers from IGDB are downloaded to `data/covers/{id}.jpg` (up to 5 in parallel), only for games in someone's library: adding a game queues its cover and wakes the downloader, and jobs for games no longer in any library are pruned daily. Downloads are validated (HTTP 200, size cap, image magic bytes) and written atomically. Failures retry with exponential backoff (2m → 16m); after 5 attempts a job is exhausted but is revived automatically if the game's cover URL changes or it is re-enqueued (e.g. after a metadata refresh). Only games in someone's library are downloaded.

At startup Cato also repairs cover data poisoned by earlier bugs: URLs pointing at the defunct `images.cato.com` host are cleared, and exhausted jobs are re-fetched with corrected URLs.

## Auth

Email/password signup and login, plus Google OAuth. Sessions are stored as opaque hashed tokens. Unsafe methods require a CSRF token returned by `GET /api/me` (compared exactly, in constant time). Expired sessions and cache entries are swept daily by a background job.

- Sign-up follows the effective mode (Admin page, else `CATO_SIGNUP`; default `closed`). People join with invite links (You → People → Invite; single use, 7 days); an invite makes the inviter and the newcomer follow each other.
- Passkeys (WebAuthn) can be added in Settings and used on the sign-in page; they need Cato to be reached by a host name, not an IP address.
- Sessions last 90 days from the last use on that device. Admins can make one-time password reset links (24 hours) for people who forget theirs.
- Login is limited per IP and per account: after 5 consecutive failures an account waits 1 s, 2 s, 4 s… (up to 15 minutes) whether or not it exists. Unknown emails cost a bcrypt compare, and duplicate sign-ups get a generic error, so responses don't reveal which emails have accounts.
- Deleting an account needs the current password (or, for Google-only accounts, a sign-in within the last 10 minutes).
- Anonymous catalog searches are local only; only signed-in searches refresh from IGDB. Queries are capped at 100 characters.
- Library notes are limited to 10,000 characters, with at most 32 tags of 40 characters each. CSV export prefixes cells that start with `= + - @`, tab or CR with `'`.

Google OAuth needs `CATO_BASE_URL` set to the `https://` address users type into their browser (a warning is logged at startup otherwise). Google also accepts `https://localhost` redirect URIs for development.

## API

The app uses `/api/v1` (sessions + CSRF); see [docs/api-v1.md](docs/api-v1.md). The public endpoints are:

| Method | Path | Description |
|---|---|---|
| `GET` | `/healthz` | Health, including background job lag |
| `GET` | `/metrics` | Prometheus metrics (token or private network) |
| `GET` | `/api/me` | `{authenticated, csrf_token, …}` |
| `GET` | `/api/config?invite=` | What the sign-in page offers: Google, sign-up, whether an invite is valid |
| `POST` | `/api/auth/signup` | `{email, password, invite?}` |
| `POST` | `/api/auth/login`, `/api/auth/logout` | Password sign-in and sign-out |
| `POST` | `/api/auth/passkey/begin`, `/api/auth/passkey/finish` | Passkey sign-in |
| `POST` | `/api/auth/reset` | `{token, password}` from an admin-made reset link |
| `GET` | `/api/auth/google/start?invite=`, `/api/auth/google/callback` | Google sign-in |
| `GET` | `/share?title=&text=&url=` | PWA share target → the Log sheet (or the game page) |
| `GET` | `/covers/{id}.jpg`, `/avatars/{file}` | Covers (public, immutable) and avatars (signed-in only) |

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
internal/catalog       Local-first search, ranking, IGDB fallback orchestration
internal/http          HTTP server, handlers, middleware
internal/integrations/igdb IGDB API v4 client with Twitch token auth
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
