#!/usr/bin/env bash
# Starts a disposable cato server for Playwright E2E runs: fresh temp DB,
# fresh covers dir, port 7180 (configurable via E2E_ADDR).
#
# Two E2E-specific settings, and why:
# - CATO_AUTH_RATE_LIMIT=1000: every Playwright client shares one IP
#   (127.0.0.1), so the production login/signup buckets (10/5 per minute)
#   would 429 legitimate signups mid-suite. 429 semantics themselves are
#   covered deterministically in Go (auth.TestRateLimiterMiddleware).
# - Seed catalog (games 1-2): the fresh DB has an empty catalog and IGDB is
#   unconfigured in E2E, so library/search specs insert against these rows.
#   The games_fts triggers keep full-text search consistent on plain INSERTs.
set -euo pipefail
cd "$(dirname "$0")/.."

E2E_TMP="${TMPDIR:-/tmp}/cato-e2e-run"
rm -rf "$E2E_TMP"
mkdir -p "$E2E_TMP/covers"

go build -o "$E2E_TMP/cato-bin" ./cmd/cato

E2E_ADDR="${E2E_ADDR:-:7180}"
E2E_PORT="${E2E_ADDR##*:}"

CATO_AUTH_RATE_LIMIT=1000 \
CATO_DB_PATH="$E2E_TMP/cato.db" \
CATO_COVER_DIR="$E2E_TMP/covers" \
CATO_STATIC_DIR=web/static \
CATO_LISTEN_ADDR="$E2E_ADDR" \
"$E2E_TMP/cato-bin" &
SRV_PID=$!
trap 'kill $SRV_PID 2>/dev/null' EXIT

# Wait for the server to finish migrating, then seed the catalog.
for _ in $(seq 1 100); do
  if curl -sf "http://127.0.0.1:${E2E_PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
sqlite3 "$E2E_TMP/cato.db" \
  "INSERT OR IGNORE INTO games (id, name, slug, safe_name, normalized_name, summary) VALUES
   (1, 'Test Game', 'test-game', 'Test Game', 'test game', 'A test game for E2E'),
   (2, 'Game Two', 'game-two', 'Game Two', 'game two', 'Second E2E seed game');"

wait $SRV_PID
