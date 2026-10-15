# Backend: server, security, API, jobs

## HTTPS everywhere

**`config.Validate()` refuses to start without HTTPS.** Any one of these satisfies it:

- Cato serves TLS itself (`CATO_TLS_CERT_FILE`/`CATO_TLS_KEY_FILE`; renewed files reload).
- `CATO_DEV_TLS=1` generates a self-signed certificate into `data/dev-tls`. `make run` uses this.
- `CATO_BASE_URL` is `https://…` behind a TLS proxy.

`CATO_ALLOW_INSECURE=1` exists for tests and for the legacy plain-HTTP NAS instance. Go tests that build a `config.Config` directly skip `Validate`.

**`cfg.HTTPS()` switches on:**

- `__Host-` cookies;
- HSTS (except with dev certificates);
- the CSP's `upgrade-insecure-requests`.

**Forwarded headers count only from `CATO_TRUSTED_PROXIES`.** Use `auth.ClientIP(r)` and `auth.RequestIsHTTPS(r)`, never `RemoteAddr` or raw `X-Forwarded-*`.

## Security headers and CSP (`securityMiddleware`)

**Every response gets:**

- `nosniff` and `X-Frame-Options: DENY`;
- `Referrer-Policy` and `Permissions-Policy` (camera allowed for this origin only);
- HSTS in HTTPS mode.

**The CSP:**

- `script-src 'self'`, plus the hash of the injected import map. Shipped HTML therefore has no inline `<script>`, no inline handlers (`onerror=`) and no `javascript:` URLs.
- `img-src` allows `self`, `images.igdb.com` and `www.gravatar.com`. Gravatar is opt-in per person.
- `frame-src` allows `www.youtube-nocookie.com`, for click-to-load trailers.
- **Trusted Types:** `require-trusted-types-for 'script'; trusted-types lit-html cato-sw`.
  - DOM XSS sinks accept only lit-html's own policy, and `cato-sw`, which allows exactly `/service-worker.js`.
  - A new script-URL sink needs its own narrow policy added to the CSP.

## Sessions, cookies, CSRF

- The cookie holds the raw token; the database stores its SHA-256 hash.
- Always go through `auth.Cookies`: the cookie is `__Host-cato_session` over HTTPS, and `cato_session` only in insecure mode.
- Sessions last 90 days from the last use. `TouchSession` slides `expires_at` at most hourly, and the middleware then refreshes the cookie.
- Unsafe methods need `X-CSRF-Token`, which comes from `GET /api/v1/me` (or `/api/me` on the login page). Middleware order: `AuthRequired`, then `CSRFRequired` (which reuses the loaded session).
- Auth functions take the `auth.Querier` interface (satisfied by `*sql.DB` and `*db.DB`). Keep `auth` free of `internal/db` imports.
- Passwords use bcrypt cost 12.
- Rate limits are in memory per process:
  - per IP: login, sign-up, password, passkey and search;
  - per account: login backoff (`auth.LoginThrottle`), and a write limit of 300 per minute on `/api/v1`.

## API

**`/api/v1` is the app API** (`internal/http/v1`; reference in `docs/api-v1.md`).

- Go 1.22 method patterns.
- Collections are `{items, next_cursor, total}`; errors are `{error, message, fields}`.
- Mutations return the item plus `library_rev` and `undo_id`.
- Every GET has an ETag (library responses derive theirs from `library_rev`), and `/api/` responses are `no-store`.
- Handlers stay thin, over the domain packages.

**Outside v1**, only these remain:

- `/api/auth/*`, `/api/me` and `/api/config`, which the login page uses;
- `/healthz`, `/metrics` and `/share`;
- `/covers/` (public) and `/avatars/` (signed-in only).

**Request bodies** are capped at 1 MiB, except imports (24 MiB) and image uploads (12 MiB) (`bodyLimit` in `server.go`).

## Background work

- **Everything runs on `internal/jobs`**: persisted schedule, jitter, cancellation, and `RunNow`. Don't start ad-hoc goroutine loops.
- `Server.Jobs()` registers:
  - the catalog jobs;
  - `push.deliver` (every minute, woken early by `social.OnNotify`);
  - `alerts.releases`;
  - `alerts.prices` (with an IsThereAnyDeal key);
  - `steam.sync` (with a Steam key);
  - `email.digest` (with SMTP).
- `cmd/cato/main.go` adds the daily `maintenance` sweep and the opt-in Wikidata crosswalk.
- `/healthz` reports the most overdue job, and the admin System page shows them all.

## IGDB and the catalog

- **IGDB is optional.** Without `IGDB_CLIENT_ID`, a no-op client is used and search stays local.
  - Env fallbacks: `IGDB_CLIENT_ID` ← `TWITCH_OAUTH_ID`, and `IGDB_CLIENT_SECRET` ← `TWITCH_OAUTH_SECRET`.
- **Requests pass a two-lane limiter** at 4 per second. Tag user-facing calls with `catalog.WithLane(ctx, catalog.LaneInteractive)`.
- **Anonymous searches** run with `catalog.LocalOnly(ctx)`.
- **Rich catalog (v20):**
  - genres, themes, modes, perspectives and game types are `lookups`;
  - time to beat lives on `games`, in minutes;
  - relations, release dates, companies, series and external IDs have their own tables;
  - media, links and the like are one JSON row in `game_details`.
- **Freshness:**
  - `catalog.Service.EnsureDetails` fetches details when a game page is viewed.
  - Jobs refresh library games weekly, run an `updated_at` delta sync every 6 hours, sync lookups weekly and PopScore daily.
  - `catalog.Service.EnsureGames` stores games first seen through an import.
- **IDs:**
  - Custom games have negative IDs and `source='custom'`.
  - Editions roll up to their base game.
  - External IDs with source ≥ 1000 are Cato's own (Wikidata), and IGDB refreshes leave them alone.

## Covers

- Files are `data/covers/{id}.jpg` (some legacy `.webp` files are accepted), plus `{id}@2x.jpg`.
- `covers.ServeCover` does no database query and no redirect: it sends the file `immutable`, or an SVG placeholder with `max-age=300`. Keep this hot path database-free.
- The download worker only fetches covers for games in someone's library (`nextJob` joins `library_items`).
  - Jobs are queued inside library writes, and `OnWrite` wakes the worker.
  - `covers.PruneJobs` drops orphaned jobs daily.
  - Never add a catalog-wide cover crawl.

## Static assets and compression

- **Asset versioning** is automatic and described in `frontend.md`.
- **`gzipMiddleware`** compresses `/api/` JSON only.
- **`gzipStatic`** serves `.js .css .html .svg .json .webmanifest` gzipped: compressed once per file version, held in memory, sent with `http.ServeContent`.
- **`staticCacheMiddleware`** caching:
  - `no-cache` for unversioned `/js/ /css/ /icons/ /manifest*`;
  - `immutable` for `?v=` URLs;
  - `no-store` for `/service-worker.js`.

## Observability

- Logging uses slog (`CATO_LOG_FORMAT=text|json`, `CATO_LOG_LEVEL`): one line per request, and statements over 50 ms are logged as slow.
- `/metrics` serves Prometheus text. It needs `CATO_METRICS_TOKEN` as a bearer token; without a token, only loopback and private-network clients may read it.

## Configuration

Everything is environment variables (`internal/config`). `.env.example` and `docker-compose.yml` list them, including the optional integrations:

- `STEAM_WEB_API_KEY`, `ITAD_API_KEY`, `UPCITEMDB_KEY`
- `CATO_SMTP_*`, `CATO_PUSH_CONTACT`
- `CATO_WIKIDATA_LINKS`

An integration without its key hides itself.
