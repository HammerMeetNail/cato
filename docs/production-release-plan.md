# Production release plan

Created: 2026-09-22. Baseline: `c015bdb`.

## Release scope

Ship a reliable single-instance, self-hosted Cato release for a private network,
matching the README and current NAS deployment. Keep Go, SQLite WAL, vanilla JS,
the existing PWA shell, and optional IGDB. Public internet hosting is a separate
release decision requiring the additional controls listed below. This work does
not deploy to the NAS or modify production data.

The plan is committed and pushed before implementation. Implementation commits
will include validation results and update the checklist below. Existing local
files `docs/tabbed-ui-plan.md` and `scripts/commit-and-push.sh` are outside scope.

## Findings and implementation sequence

### 1. Close account security gaps — release blocker

- [x] Reject automatic Google-to-password account linking by matching email.
  Require a nonempty provider subject and verified email; reject conflicting
  accounts with an actionable error. Existing subject-linked accounts still work.
- [x] Enforce disabled-user status during session lookup and session issuance,
  including Google sign-in.
- [x] Change passwords transactionally and revoke other sessions while preserving
  the current session. Prevent an in-flight login with the old password from
  creating a surviving session after the change.
- [x] Bound Google token/profile calls and reject overlong signup passwords.

Evidence: `internal/http/handler_auth.go` automatically merges on email, only
password login checks `disabled`, and password changes leave all sessions alive;
`internal/auth/session.go` does not check the owning user.

Acceptance: real-SQLite regression tests cover email collisions, invalid Google
profiles, disabled users, session revocation, and the concurrent-login boundary.
An independent security review approves the final diff.

### 2. Bound HTTP work and make shutdown deterministic

- [x] Add header/read/write/idle timeouts, API body limits (including chunked
  bodies), conservative security headers, and `no-store` for API responses.
- [x] Give health checks a deadline and check both SQLite pools.
- [x] Make cover downloads, catalog refreshes, and daily maintenance cancellable;
  stop admission, drain requests, stop/join workers, then close the database.
  Preserve detached, bounded search refresh behavior during ordinary requests.

Evidence: `internal/http/server.go` has no timeouts or body limits; background
loops in `cmd/cato/main.go`, `internal/covers/covers.go`, and
`internal/games/service.go` outlive HTTP shutdown.

Acceptance: focused body-limit/header/health tests, deterministic cancellation
tests, race detector, and process SIGTERM smoke test. An independent logic review
checks ownership and test synchronization before the full validation gate.

### 3. Make backup and restore safe and repeatable

- [x] Provide consistent SQLite online backups, unique filenames, restrictive
  permissions, and integrity checks without copying a live WAL database.
- [x] Replace blind restore-copy instructions/targets with a guarded SQLite
  restore procedure, preserve a pre-restore backup, and require an explicit
  stopped-service acknowledgement for destructive restore operations.
- [x] Guard `deploy-full`/database replacement and document NAS backup scheduling,
  off-host retention, restore drills, and version rollback.

Evidence: `make db-restore` and README currently copy directly over the database;
`deploy-full` replaces the production database without a quiesced restore.

Acceptance: disposable WAL database round-trip, integrity/content verification,
and negative cases for missing/corrupt backups and unacknowledged restore. No
tests touch the real local or production database.

### 4. Establish an automated release gate

- [x] Add CI for Go build, vet, unit/integration tests with race detection,
  frontend regression scripts, and the existing Playwright journeys.
- [x] Add reproducible local Make targets for these checks and dependency scans.
- [x] Scan Go dependencies/toolchain and browser-test dependencies; upgrade
  versions needed to remove reachable vulnerabilities and lock tool versions.
- [x] Isolate the E2E server's temporary state and credentials, fail on startup
  errors, and always reap its child process.
- [x] Add browser coverage for Playing/Stats/Settings navigation and password
  change/session behavior, including a mobile viewport where useful.

Evidence: no `.github` workflow; existing E2E/regression scripts are disconnected
from `make test`; E2E uses a fixed temporary directory and inherits integrations.

Acceptance: all release commands pass locally on disposable data, CI runs on the
implementation push, and any unavailable infrastructure is reported explicitly.
Use [Go's vulnerability tooling](https://go.dev/doc/security/vuln/) for actual
call-path findings rather than treating every old version as exploitable.

### 5. Document and wire the production operating contract

- [x] Forward public base URL and secure-cookie settings through Compose; provide
  a safe example environment and document private HTTP versus HTTPS settings.
- [x] Document supported single-instance operation, health monitoring, disk/log
  management, deployment/rollback, data ownership, backup retention, and release
  checks. Preserve NAS bind mounts and compatibility with its Compose setup.
- [x] Record test outcomes and unresolved release conditions in this plan.

Acceptance: configuration documentation agrees with code and Compose; no secrets,
runtime data, or browser artifacts are committed. Existing data needs no schema
migration for these changes. Use SQLite's
[online backup guidance](https://www.sqlite.org/backup.html).

## Later product additions / public-service prerequisites

These are intentionally outside this private-network release implementation:

- Email verification, password recovery with an email provider, explicit account
  linking, and operator-managed signup/invitation policy before public signup.
- A trusted reverse-proxy policy for per-client rate limiting, search abuse
  controls, and stronger browser CSP after removing current inline scripts.
- User library export/import, accessible onboarding/help, and a privacy/support
  policy for a hosted offering.
- Metrics dashboards, alert delivery, and automated off-host encrypted backups
  once their hosting/storage destinations are chosen.
- Physical iOS/Android install, offline, and upgrade smoke tests; real Google and
  IGDB integration checks require configured accounts and devices.

## Release evidence

Implementation completed on 2026-09-22; no schema migrations or shipped web
assets changed. Independent security and lifecycle reviews found no remaining
blockers after fixes. The implementation push runs the new CI workflow; its
remote result is reported with the commit in the release handoff.

| Check | Result |
| --- | --- |
| `make release-check` | Passed, exit 0 |
| Go unit/integration tests and race detector | All packages passed |
| `go vet ./...`, native build, Linux/amd64 static build | Passed |
| Frontend regression scripts | 3 passed |
| SQLite backup/recovery regressions | 8 passed, including crash-left WAL and interrupted restore |
| Playwright | 24 passed; desktop plus mobile Chromium, no skips/retries |
| Dependency scans | 0 reachable Go vulnerabilities; npm audit 0 vulnerabilities |
| Process restart/SIGTERM | Two clean exits; persisted account and integrity verified |
| Compose configuration | Validated locally using the Podman Compose provider |
| Container packaging/health/SIGTERM | Passed with build host networking; first attempt hit Alpine repository network errors |
| E2E cleanup | No temporary server directories remained |

The dependency scan originally found reachable `GO-2026-5970` in search Unicode
normalization. Updated x/text, x/crypto and their dependencies, pinned the Go
1.26.8 toolchain and govulncheck v1.8.0. Four reported vulnerabilities remain in
required modules on code paths the scanner does not find reachable; continue
scanning as code and advisory data change.

Local logs live outside the repository at `/tmp/cato-release-validation/`:
`release-check.log`, `process-smoke.log`, and `container-smoke-retry.log`.
Security and lifecycle regression logs are also under `/tmp/cato-*`.

A production restore drill, off-host backup scheduling, external monitoring, and
physical-device/configured OAuth/IGDB smoke tests remain operator release checks.
No production deployment or data changes were performed. Public hosting still
requires the additional controls above; local passes do not establish that scope.
