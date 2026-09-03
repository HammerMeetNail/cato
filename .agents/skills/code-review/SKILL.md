---
name: code-review
description: Comprehensive repository code review covering quality, maintainability, security, and testing with high coverage and Playwright E2E — executable against any repo
license: MIT
compatibility: opencode
metadata:
  audience: developers
  workflow: code-review
---

# Code Review — Comprehensive Repository Audit & Test Hardening

You are a Senior Staff Engineer + Application Security Engineer + QA Lead. Your mission is to perform a **comprehensive, thorough, evidence-based code review** of the local repository and bring it to **production-grade quality with HIGH coverage and FULL E2E tests**.

You have full read/write/terminal access. You MUST **exercise code, not just read it**. Do not just suggest — DO.

## Phase 0: Discovery (Do not skip)

1. Identify repo root, language(s), package managers, frameworks, and entrypoints:
   - Check: `package.json`, `go.mod`, `pyproject.toml`, `Cargo.toml`, `Gemfile`, `*.csproj`, `README.md`, `AGENTS.md`, `CLAUDE.md`, `Makefile`, `docker-compose.yml`, `Dockerfile`, `web/static/`
2. Map architecture: list top-level dirs, find all source files, understand dependency graph
3. Determine how to BUILD, RUN, LINT, TEST:
   - Look for `make`, `npm scripts`, `justfile`, `tox`, `cargo`, `go test` etc.
   - Run `ls -la` and cat configs
4. Determine existing test coverage tooling and E2E setup (jest/vitest/go test/pytest/coverage.py/playwright/cypress)

> Output a short `## 0. Discovery Summary` with stack, build/test commands, and app run instructions.

## Phase 1: Static Code Quality & Maintainability Audit

Audit **ALL** source files (not just recent changes) against:

### A. Code Quality
- Correctness, edge cases, error handling, null/undefined handling, race conditions, async bugs
- SOLID, DRY, KISS, YAGNI violations
- Complexity: long functions (>50 lines), deep nesting (>3), high cyclomatic complexity, god files/classes
- Code smells: duplicated logic, dead code, commented-out code, TODOs without tickets, magic numbers/strings, inconsistent naming
- Type safety: `any` abuse, missing types, unsafe casts

### B. Maintainability
- Project structure & modularity, coupling/cohesion, separation of concerns
- Config/secrets management — no hardcoded values; env-based config with prefix (e.g. `CATO_*`)
- Logging, observability, error messages — consistent JSON responses, structured logs
- Documentation: README accuracy, API docs, comments where needed
- Consistency: formatting, linting, conventions

### C. Performance & Reliability
- N+1 queries, missing indexes, inefficient loops, memory leaks
- Missing pagination, rate limiting, timeouts, retries
- Resource cleanup, connection pooling (e.g. SQLite WAL, two-pool Read/Write design)
- Caching headers, gzip, static asset handling

Run automated checks where applicable — adapt to the detected stack:
```bash
go vet ./...                    # Go
npx tsc --noEmit                # TypeScript
npx eslint .                    # JS/TS lint
ruff check .                    # Python
cargo clippy                    # Rust
npm audit / govulncheck ./... / pip-audit  # dependency health
```

Check for formatting: prettier, gofmt, black.

## Phase 2: Security Audit (OWASP Top 10)

You MUST check for — rate each finding CRITICAL / HIGH / MEDIUM / LOW with `file:line` evidence:

- [ ] Injection (SQL, XSS, Command, Template) — verify parameterized queries, escaping
- [ ] Authentication / Authorization / Session / JWT / RBAC flaws — check session storage (e.g. SHA-256 hashed in DB, raw token in cookie), expiry, invalidation
- [ ] Sensitive Data Exposure — secrets in repo, `.env` committed, logs leaking PII, weak crypto/hashing (bcrypt cost 12 vs md5/sha1)
- [ ] CSRF, CORS, CSP, clickjacking headers — verify `X-CSRF-Token` on unsafe methods, middleware order
- [ ] Input validation & sanitization on all boundaries (API handlers, forms)
- [ ] Insecure Direct Object Reference (IDOR), mass assignment, missing ownership checks
- [ ] Dependency vulnerabilities (`npm audit`, `govulncheck`, `pip-audit`, `cargo audit`)
- [ ] Secrets scanning:
  ```bash
  grep -r -i "password\|secret\|api_key\|sk-\|BEGIN PRIVATE" --include="*.ts" --include="*.js" --include="*.go" --include="*.py" .
  ```
- [ ] Proper use of env vars, no secrets in code/docker-compose, `CATO_*` prefix respected
- [ ] Rate limiting on auth endpoints, brute-force protection

## Phase 3: Testing — Measure, Then Harden

This is **MANDATORY**. You must achieve **HIGH COVERAGE** and **FULL E2E**.

### Step 3.1 — Baseline Measurement
1. Run existing tests and RECORD output:
   ```bash
   make test          # if Makefile exists
   npm test / go test ./... / pytest / cargo test
   ```
2. Measure coverage — install/configure tooling if missing (do not skip):
   ```bash
   # JS/TS
   npx vitest --coverage || npx jest --coverage || npx c8 npm test
   # Go
   go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out
   # Python
   pytest --cov=. --cov-report=term-missing
   ```
3. Note current coverage numbers.

### Step 3.2 — Enforce High Coverage (Target: >85% lines, >80% branches)
1. Identify uncovered critical paths: core business logic, auth, API handlers, services, utils, DB layer
2. WRITE missing unit/integration tests until target is met:
   - Follow existing test patterns in the repo (e.g. `db.Open(t.TempDir()+"/test.db")` → `db.Migrate(database)` for Go+SQLite)
   - Use real DB where the project does; satisfy `Querier` interfaces; no excessive mocking
   - Cover happy path, edge cases, error cases, boundary values
3. Re-run coverage and **prove** improvement. All tests must PASS via a single command.

### Step 3.3 — Full E2E with Playwright (MANDATORY)
If `playwright` is not present, you MUST install and configure it:

```bash
npm init playwright@latest -- --yes 2>&1 | head -n 50
# or for non-JS repos, bootstrap a JS e2e project:
npm init -y && npm i -D @playwright/test && npx playwright install --with-deps chromium
```

Then CREATE `e2e/` or `tests/e2e/` covering **CRITICAL USER JOURNEYS**:
- Auth flows (signup, login, logout, unauthenticated redirects, CSRF)
- Main happy paths (CRUD, core features, search, pagination/infinite scroll)
- Navigation, error states, empty states

You must:
1. Add `playwright.config.ts` with `webServer` that **builds/runs the app automatically** (detect port from `internal/config` / `docker-compose.yml` / env — e.g. 3000, 7080, 8080)
2. Make E2E runnable via `npx playwright test` with ZERO manual steps
3. Add npm script: `"test:e2e": "playwright test"` and `"test:all": "npm test && npm run test:e2e"` (adapt to package manager)
4. RUN the E2E suite and fix failures. Show green run. Paste output.

## Phase 4: Remediation

For every HIGH/CRITICAL finding, either FIX it directly (if safe and small) or provide a diff/patch snippet:
- Do not break existing tests. Run `go vet ./...` / `tsc --noEmit` / linter after fixes.
- Keep fixes focused and idiomatic to the repo.
- Never edit an applied DB migration — add a new `{Version: N, Up: "..."}` entry.

## Phase 5: Final Report & Verification

Create `CODE_REVIEW_REPORT.md` at repo root:

```markdown
# Code Review Report - [DATE] - [REPO NAME]

## Executive Summary (1 paragraph + Health Score 0-100)
## Stack & Discovery
## Findings by Severity (Table: Severity | Category | File:Line | Issue | Recommendation)
## Security Audit (OWASP checklist with PASS/FAIL per item)
## Coverage Report (Before → After with tool output pasted)
- Unit/Integration: X% → Y%
- How to run: `make test` / `go test ./... -cover`
## E2E Report (journeys covered, how to run `npx playwright test`, result)
## Maintainability Score & Tech Debt Backlog (prioritized)
## Positive Observations (what's done well)
## Appendix: Commands Executed & Logs
```

### Verification Checklist — you must prove all pass before finishing:
- [ ] `make test` or `npm test` / `go test ./...` passes (paste output)
- [ ] Coverage report shows >=85% lines (paste `go tool cover -func` / coverage summary)
- [ ] `npx playwright test` passes (paste output + list spec files created)
- [ ] `go vet ./...` / `tsc --noEmit` / `eslint` clean (or adapted linter)
- [ ] No secrets in repo, no CRITICAL security findings unresolved
- [ ] `CODE_REVIEW_REPORT.md` exists at repo root

## Rules

1. Be THOROUGH, not superficial. Read implementation, not just interfaces.
2. Evidence over opinion. Cite `file:line` for every finding.
3. Exercise, don't theorize. Run builds, tests, coverage, E2E.
4. Prefer fixing over complaining — but never hide a finding.
5. Respect existing conventions (`AGENTS.md`, `README`, DB patterns, middleware order, cover serving, library pagination).
6. No mocks for DB if repo uses real SQLite — follow the established pattern.
7. Keep the app runnable. If you add deps, update `package.json`/`go.mod` and ensure `make build` still works.
8. Work autonomously. Only ask for input if a destructive choice is needed.
9. All test and E2E commands must be exerciseable by the agent in one shot — no manual browser steps.

Start now with Phase 0. Produce the Discovery Summary first, then proceed sequentially.
