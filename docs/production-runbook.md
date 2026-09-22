# Cato production runbook

## Supported release

One Cato process with a local SQLite database and persistent cover directory,
on a trusted private network. Do not share the database across containers or
place it on a network filesystem. Public signup, email recovery, trusted-proxy
rate limiting, and hosted-service policy are future work (see the
[release plan](production-release-plan.md)). Password changes invalidate other
sessions. Google sign-in cannot automatically link an existing password account;
use that account's original sign-in method when an email collision is reported.

## Build and release gate

Use Go 1.26.8 (selected by `go.mod`), Node 24+, Python 3.10+, and sqlite3.
`make release-check` runs build, vet, Go tests and race tests, JavaScript
regressions, backup/release-script tests, dependency scans, and Playwright. On a fresh Linux
host, first install browser system dependencies:

```sh
npm --prefix e2e ci
cd e2e && npx playwright install --with-deps chromium webkit
```

CI executes the same gate on pushes and pull requests. E2E uses fresh disposable
data and disables Google/IGDB credentials; it never uses `data/cato.db`.
Failure traces default to `/tmp/cato-e2e-results` (override with
`CATO_E2E_RESULTS`). The E2E port 7180 must be free. Treat retries/flaky results
as failures, not permission to ship. Before deploying, check the workflow for
the exact commit being released.

## Configuration and deployment

Copy `.env.example` to `.env` on the deployment host; keep it private. Compose
passes `CATO_BASE_URL` and `CATO_SECURE_COOKIES` into the container. On private
HTTP use `false`. With an HTTPS reverse proxy use the external `https://` origin
and `true`, restrict direct access to port 7080, and register
`<CATO_BASE_URL>/api/auth/google/callback` with Google. Do not set the test-only
`CATO_AUTH_RATE_LIMIT` override in production. Forwarded IP headers are not
trusted, so a reverse proxy shares one auth rate-limit bucket.

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

Create the Quay repository and configure these GitHub settings before a future
release. This task prepares the files; setting up hosting and pushing a release
tag are separate operator actions. Configure required reviewers on the GitHub
`production` environment to gate the deployment job.

| Setting | Kind | Purpose |
| --- | --- | --- |
| `IMAGE_REPOSITORY` | Repository variable | Default `quay.io/nabu/cato`; choose Cato's actual Quay repository |
| `QUAY_USERNAME`, `QUAY_PASSWORD` | Repository secrets | Robot credentials scoped to that image repository |
| `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_PATH` | Production environment variables | SSH target and pre-provisioned directory (for example `/opt/cato`) |
| `DEPLOY_PORT` | Production environment variable | SSH port, default 22 |
| `SSH_PRIVATE_KEY`, `SSH_KNOWN_HOSTS` | Production environment secrets | Deployment identity and independently verified host public key |
| `DEPLOY_CLOUDFLARE_ACCESS` | Production environment variable | `true` enables the Cloudflare Access SSH proxy like Nabu |
| `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET` | Production environment secrets | Service-token credentials when Cloudflare is enabled |
| `GITLEAKS_LICENSE` | Optional repository secret | If required by Gitleaks for the repository owner |

`Containerfile` builds amd64/arm64 release images from source. Trivy scans both
architectures at the returned digest; Cosign signs that digest using GitHub OIDC
before release notes and deployment. Main/PR image builds do not log into Quay or
publish. The old NAS `Dockerfile` remains a separate binary-packaging path.

Prepare the target with Docker Compose v2 supporting `up --wait`, Bash,
Python 3.10+, flock, curl and base64. Create `.env` with application settings and
`data/covers` writable by UID/GID **10001**. The deployer checks access but never
changes ownership. Existing NAS data needs a planned ownership transition before
using this different image. Quay images must be public or the host must already
have a registry login. CI never rewrites the host's application `.env`.

`compose.server.yaml` binds port 7080 to host loopback. Configure its reverse
proxy/tunnel, matching base URL and cookie policy. Deployment locks the target,
verifies a SQLite snapshot, preserves the previous compose/image reference, and
waits for health. `.release-state.json` records an attempted version before
rollout, preventing a delayed older tag from downgrading a database even after a
failed health check. Recovery/rollback is an explicit operator procedure.
This workflow does not provision a server, DNS, tunnel, registry, off-host backup
destination, or environment approvals.

Before each deployment:

1. Save a verified database snapshot and retain the previous binary, static
   assets, Dockerfile and Compose file as a versioned release bundle.
2. Run the release gate and verify the exact commit's CI result.
3. If shipped web assets changed, bump `CACHE_NAME` in the service worker.
4. Deploy, check `/healthz`, then exercise login, search, library editing,
   Playing time logging, Stats and Settings. Test Google/IGDB if configured.

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
