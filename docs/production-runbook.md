# Cato production runbook

## Supported release

One Cato process with a local SQLite database and persistent cover directory,
served over HTTPS. Do not share the database across containers or
place it on a network filesystem. Public signup, email recovery and
hosted-service policy are future work (see the
[release plan](production-release-plan.md)). Password changes invalidate other
sessions. Google sign-in cannot automatically link an existing password account;
use that account's original sign-in method when an email collision is reported.

## Build and release gate

Use Go 1.26.9 (selected by `go.mod`), Node 24+, Python 3.10+, and sqlite3.
`make release-check` runs build, vet, Go tests and race tests, JavaScript
regressions, backup/release-script tests, dependency scans, and Playwright. On a fresh Linux
host, first install browser system dependencies:

```sh
npm --prefix e2e ci
cd e2e && npx playwright install --with-deps chromium webkit firefox
```

CI executes the same gate on pushes and pull requests. E2E uses fresh disposable
data and disables Google/IGDB credentials; it never uses `data/cato.db`.
Failure traces default to `/tmp/cato-e2e-results` (override with
`CATO_E2E_RESULTS`). The E2E port 7180 must be free. Every Playwright project
runs over HTTPS with a certificate from `cato dev-cert` (cached in
`$TMPDIR/cato-e2e-tls`, override with `CATO_E2E_TLS_DIR`); nothing is installed
in a trust store (see `e2e/README.md`). Treat retries/flaky results
as failures, not permission to ship. Before deploying, check the workflow for
the exact commit being released.

## Configuration and deployment

On the production host the tagged release writes `.env` from GitHub settings
(below); elsewhere, copy `.env.example` to `.env` and keep it private. **Cato is
HTTPS-only** and refuses to start otherwise. Choose one:

- **Reverse proxy (recommended for a real domain).** Caddy, the Synology reverse
  proxy or similar terminates TLS with a Let's Encrypt certificate and forwards to
  Cato's plain listener. Set `CATO_BASE_URL=https://<domain>` and
  `CATO_TRUSTED_PROXIES=<proxy address or CIDR>`; restrict direct access to port
  7080 (`compose.server.yaml` binds 8083 on the host to loopback). The deploy
  health checks use `http://127.0.0.1:8083/healthz` in this mode.
- **Native TLS.** Set `CATO_TLS_CERT_FILE`/`CATO_TLS_KEY_FILE` to files inside the
  container (for example under `/app/data/tls`). Renewed files are reloaded within
  a minute. Optional `CATO_HTTP_REDIRECT_ADDR` answers plain HTTP with a 308. The
  image health check probes HTTP then HTTPS; `scripts/deploy-remote.sh` probes
  only HTTP, so use the proxy mode with the tagged release workflow.
- **LAN testing.** `CATO_DEV_TLS=1` generates a self-signed certificate in
  `/app/data/dev-tls` (no HSTS). Browsers warn until it is trusted.

The legacy plain-HTTP instance (`http://10.0.0.42:7080`) needs
`CATO_ALLOW_INSECURE=1` in its `.env` to keep starting; that flag exists for tests
and is not a supported production mode. Over HTTPS the session cookie is
`__Host-cato_session` (`Secure`, `Path=/`, no `Domain`); the switch signs every
user out once. HSTS (`max-age=31536000`, no preload) and
`upgrade-insecure-requests` are sent whenever `CATO_BASE_URL` is https or Cato
serves a non-dev certificate.

Sign-up is closed by default (`CATO_SIGNUP=closed`): the first account on an
empty database can register and becomes the admin; after that, people join with
invite links. In `closed` mode only admins' invites work; `CATO_SIGNUP=invite`
lets anyone invite. An admin can override the mode on the Admin page (recorded in
the audit log). On upgrade, the oldest existing account becomes the admin. An
admin can also make one-time password reset links (Admin → People), since Cato
sends no password emails.

Optional integrations each need only their key in `.env` (see `.env.example`):
`STEAM_WEB_API_KEY` (Steam import and daily play time sync), `ITAD_API_KEY`
(wishlist price alerts), `UPCITEMDB_KEY` (more barcode lookups), `CATO_SMTP_*`
(weekly email digest) and `CATO_WIKIDATA_LINKS=1`. Web Push needs nothing: VAPID
keys are generated on first use and stored in the database, and the host needs
outbound HTTPS to the browsers' push services. Passkeys need Cato to be reached
by a host name (`CATO_BASE_URL`), not an IP address.

Register `<CATO_BASE_URL>/api/auth/google/callback` with Google. Do not set the
test-only `CATO_AUTH_RATE_LIMIT`/`CATO_SEARCH_RATE_LIMIT` overrides in production. Forwarded IP headers are
trusted only from `CATO_TRUSTED_PROXIES`; without it, every client behind a proxy
shares one auth rate-limit bucket.

The default release workflow follows Nabu: `DRY_RUN=1 make deploy` previews the
next annotated version tag; `make deploy` verifies clean, synchronized `main`,
prompts, and pushes the tag. A tag starts the image release and configured
deployment pipeline. **Do not invoke the confirming path or push a `v*` tag when
only preparing a release.** No initial tag has been created as part of this work.

The first authorized release is `v0.1.0`; later releases increment the latest
reachable semver patch tag. CI independently rejects release tags not reachable
from `main`. Main/PR pushes run validation and container builds without registry
publication or production changes. The tag workflow scans and signs the exact
built image digest and rolls out that immutable digest, not a mutable `latest`
tag. See the release configuration section below before enabling a release.

For local legacy Docker, run `make deploy-build` before
`docker compose up -d --build`: the Dockerfile packages a precompiled Linux/amd64
binary. Source-built release images use `Containerfile`. The explicit legacy
Synology workflow is `make deploy-nas`; it ships code/assets and preserves the data
bind mount. `deploy-full` and `deploy-db` now fail before changing anything:
production data replacement must follow the restore procedure below.

### Tagged release configuration

Production runs on the shared Hetzner host (`yob`, also Year of Bingo and Nabu)
at `https://cato.localbytellc.com`. A `v*` tag on `main` runs the release gate,
builds and scans the image, signs it with Cosign, pushes it to Quay and deploys
it; the deploy job runs `cosign verify` (certificate identity: this repository's
`ci.yml` at the release tag, issuer `token.actions.githubusercontent.com`) before
it touches the host. Configure required reviewers on the GitHub `production`
environment if deploys should wait for approval.

| Setting | Kind | Value / purpose |
| --- | --- | --- |
| `IMAGE_REPOSITORY` | Repository variable | `quay.io/nabu/cato` (the default) |
| `QUAY_USERNAME`, `QUAY_PASSWORD` | Repository secrets | Robot credentials with write access to that repository |
| `DEPLOY_HOST` | `production` variable | `ssh.yearofbingo.com` (SSH through the Cloudflare Tunnel) |
| `DEPLOY_USER`, `DEPLOY_PATH` | `production` variables | `deploy`, `/opt/cato` |
| `DEPLOY_CLOUDFLARE_ACCESS` | `production` variable | `true`: SSH through `cloudflared access ssh` |
| `SSH_PRIVATE_KEY` | `production` secret | The `deploy` user's key (the one Year of Bingo and Nabu use) |
| `SSH_KNOWN_HOSTS` | `production` secret | `ssh.yearofbingo.com` followed by the host's `ssh-ed25519` public key |
| `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET` | `production` secrets | Only if an Access policy guards the SSH hostname |
| `CATO_BASE_URL` | `production` variable | `https://cato.localbytellc.com` (required) |
| `CATO_TRUSTED_PROXIES` | `production` variable | `127.0.0.1,5.161.57.131`: rootless podman (pasta) shows cloudflared's connections as the host's address |
| `CATO_SIGNUP`, `CATO_LOG_FORMAT`, `CATO_WIKIDATA_LINKS`, `CATO_PUSH_CONTACT`, `CATO_SMTP_HOST`, `CATO_SMTP_PORT`, `CATO_SMTP_USER`, `CATO_SMTP_FROM` | `production` variables | Optional settings (`.env.example`) |
| `TWITCH_OAUTH_ID`, `TWITCH_OAUTH_SECRET` | `production` secrets | IGDB (search beyond the local catalog) |
| `GOOGLE_KEY`, `GOOGLE_SECRET`, `STEAM_WEB_API_KEY`, `ITAD_API_KEY`, `UPCITEMDB_KEY`, `CATO_SMTP_PASSWORD`, `CATO_METRICS_TOKEN` | `production` secrets | Optional integrations; unset ones are left out |
| `GITLEAKS_LICENSE` | Optional repository secret | If required by Gitleaks for the repository owner |

**The host's `.env` comes from these settings.** `scripts/deploy-remote.sh`
writes it on the runner from the job's environment (single-quoted values, so a
value can't contain `'` or a newline) and streams it over SSH as data; the
previous one is kept in `backups/env-<id>`. Change a setting in GitHub, not on
the host: the next deploy replaces the file.

**The host** runs rootless podman as `deploy` with `podman-compose`; Python
3.10+, flock, curl and base64 are needed. One-time setup, already done on yob:

- `/opt/cato/{data/covers,backups}`, with `data` owned by the container's user:
  `podman unshare chown -R 10001:10001 /opt/cato/data` (UID 10001 in the image
  maps to a subordinate UID on the host). `compose.server.yaml` mounts it with
  `:Z` for SELinux.
- `/etc/systemd/system/cato.service` (`podman-compose up -d` in `/opt/cato` as
  `deploy`, like `nabu.service`) starts it at boot. The deploy writes the digest
  into `compose.yaml`, so the unit needs no variables.
- The Cloudflare Tunnel (`~deploy/.cloudflared/config.yml`, tunnel
  `yearofbingo`) routes `cato.localbytellc.com` to `http://127.0.0.1:8083`, and
  the DNS record is a proxied CNAME to `<tunnel id>.cfargotunnel.com`.
  `compose.server.yaml` binds 8083 to loopback only (Year of Bingo has 80, Nabu
  8082).
- Quay images must be public, or the host needs a `podman login`.

Deployment locks the target, verifies a SQLite snapshot, preserves the previous
compose file, image reference and `.env`, and waits for `/healthz` on
127.0.0.1:8083. `.release-state.json` records an attempted version before
rollout, preventing a delayed older tag from downgrading a database even after a
failed health check. Recovery/rollback is an explicit operator procedure. The
old NAS `Dockerfile` remains a separate binary-packaging path.

Before each deployment:

1. Save a verified database snapshot and retain the previous binary, static
   assets, Dockerfile and Compose file as a versioned release bundle.
2. Run the release gate and verify the exact commit's CI result.
3. Deploy, check `/healthz`, then exercise login, search, library editing,
   Playing time logging, Stats and Settings. Test Google/IGDB if configured.

Shipped web assets are versioned automatically (content hashes in URLs and in the
service worker), so there is no cache name to bump. Cato snapshots the database
into `CATO_BACKUP_DIR` (default `data/backups`, five kept) before applying a schema
upgrade; restore one with the stopped-service procedure below if an upgrade goes
wrong.

The container has a 30-second stop grace period; the application budgets 20
seconds to drain HTTP and stop workers. A shutdown error requires checking logs
before proceeding. Health checks have a two-second database deadline and check
both connection pools. `/healthz` checks database connectivity, not disk capacity,
integrity, successful downloads, or third-party service availability.

## Backups

`make db-backup` uses Python's SQLite backup API, validates integrity and foreign
keys, and writes a unique standalone mode-0600 snapshot in `backup/`. It is safe
while Cato is running. Override `DB_PATH` and `BACKUP_DIR` as needed. The source
must exist; an empty or unrelated database is rejected. Backups contain personal
data, password hashes and sessions: limit access and encrypt off-host copies.

On the NAS, sqlite3 is already available. A scheduler task can run this as the
account owning `/volume1/Shared/Cato/data` (no Docker restart needed):

```sh
set -eu
umask 077
cd /volume1/Shared/Cato
mkdir -p backup
snapshot=$(mktemp backup/cato-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX.db)
sqlite3 data/cato.db ".timeout 30000" ".backup '$snapshot'"
sqlite3 "$snapshot" 'PRAGMA journal_mode=DELETE;' >/dev/null
test "$(sqlite3 "$snapshot" 'PRAGMA integrity_check;')" = ok
test -z "$(sqlite3 "$snapshot" 'PRAGMA foreign_key_check;')"
echo "Verified backup: $snapshot"
```

Schedule daily, retain at least 7 daily and 4 weekly verified snapshots, and copy
them off the NAS to encrypted storage. Configure an alert for scheduler failure
and verify off-host copies before pruning. This repository does not provision
the scheduler, storage destination, encryption keys, or alert delivery. Target a
24-hour recovery point initially and measure restore time in the drill below.
Covers are reproducible cache data; optionally back up `data/covers` to avoid
redownloads. Never copy the live database file alone or discard its WAL files.

## Restore and rollback

Practice on a disposable database first. Confirm the snapshot date and destination.
Stop **all** processes accessing the destination, including import/backfill jobs.
For a local install:

```sh
make stop
# Stop any other manually started Cato processes before acknowledging this.
make db-restore FILE=backup/CHOSEN-SNAPSHOT.db SERVICE_STOPPED=1
make run
```

The helper validates the source before modifying the destination and creates a
`pre-restore-*.db` snapshot of the old destination. `SERVICE_STOPPED=1` is an
operator acknowledgement, not automatic process detection. It uses SQLite's
backup API for replacement; it does not copy over the live database file or
manually delete WAL/SHM files. Retain the printed pre-restore path until the
restored app has been checked. A restore failure leaves that recovery snapshot
available; investigate before starting Cato.

For the NAS (substitute the chosen snapshot path):

```sh
set -eu
cd /volume1/Shared/Cato
/usr/local/bin/docker stop --time 30 cato
# Confirm imports/backfills are stopped too.
umask 077
test "$(sqlite3 backup/CHOSEN-SNAPSHOT.db 'PRAGMA integrity_check;')" = ok
test -z "$(sqlite3 backup/CHOSEN-SNAPSHOT.db 'PRAGMA foreign_key_check;')"
# Check expected Cato tables before replacement.
sqlite3 backup/CHOSEN-SNAPSHOT.db '.tables'
previous=$(mktemp backup/pre-restore-XXXXXX.db)
sqlite3 data/cato.db ".backup '$previous'"
test "$(sqlite3 "$previous" 'PRAGMA integrity_check;')" = ok
sqlite3 data/cato.db ".timeout 30000" ".restore 'backup/CHOSEN-SNAPSHOT.db'"
test "$(sqlite3 data/cato.db 'PRAGMA integrity_check;')" = ok
/usr/local/bin/docker start cato
```

Run NAS commands in a shell with `set -eu` so a failed check stops the procedure.
Restore only trusted Cato snapshots. Verify `/healthz`, login, library counts,
notes/tags, playtime, and representative covers. Do a restore drill before the
first release and after changes to storage or migrations; record snapshot date,
integrity result, app checks, elapsed restore time, and who performed it.

For rollback, stop Cato, restore the previous release's binary/static/Compose
bundle, and rebuild/restart. This hardening release introduces no schema changes.
Future migrations may prevent running an older binary against a newer database:
restore the corresponding pre-upgrade snapshot first, accepting that writes since
that snapshot will be lost. Never use `make clean` on a production checkout:
it removes runtime data.

If the destination is corrupt, the normal restore deliberately refuses to
continue because it cannot verify its pre-restore backup. Keep every process
stopped. Preserve the entire damaged file set in a private quarantine directory
before restoring into a missing destination:

```sh
set -eu
umask 077
quarantine=$(mktemp -d backup/quarantine-XXXXXXXX)
for file in data/cato.db data/cato.db-wal data/cato.db-shm data/cato.db-journal; do
  if [ -e "$file" ]; then mv "$file" "$quarantine/"; fi
done
# On a host with Python 3.10+:
make db-restore FILE=backup/CHOSEN-SNAPSHOT.db SERVICE_STOPPED=1
# On the NAS, use sqlite3 .restore from the procedure above instead.
```

Retain the quarantine for diagnosis/recovery; do not delete it after a failed
attempt. Use paths on the same filesystem so moving each file is atomic. If a
move fails, stop and reconcile the preserved files before restarting the app.

## Monitoring and release sign-off

- Check `/healthz` externally every minute and alert on repeated failures; Docker
  health alone does not send an alert or restart an unhealthy live process.
- Watch database/cover filesystem free space and backup job age. Investigate
  `maintenance`, cover download, refresh, and shutdown errors in container logs.
- Compose rotates logs at 10 MB with three files. Set filesystem permissions for
  the deployment account and data mount; the legacy NAS image runs as root, while
  the source-built image uses UID/GID 10001. Prepare and test volume ownership
  before switching between these deployment paths.
- Before public distribution, perform real-device iOS/Android install/offline/
  upgrade checks and configured Google/IGDB smoke tests. Automated browser tests
  do not establish Safari, OAuth-provider or physical-device compatibility.
