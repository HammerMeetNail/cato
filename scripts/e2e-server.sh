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

E2E_TMP=$(mktemp -d "${TMPDIR:-/tmp}/cato-e2e.XXXXXXXX")
SRV_PID=
cleanup() {
  trap - EXIT INT TERM
  if [ -n "$SRV_PID" ]; then
    kill "$SRV_PID" 2>/dev/null || true
    wait "$SRV_PID" 2>/dev/null || true
  fi
  rm -rf "$E2E_TMP"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$E2E_TMP/covers"
cp -R web/static "$E2E_TMP/static"
# Only this disposable server can serve this invocation's marker. A healthy
# unrelated listener on E2E_PORT must never authorize seeding or browser tests.
E2E_NONCE="${E2E_TMP##*/}"
E2E_MARKER="e2e-ready-${E2E_NONCE}.txt"
printf '%s' "$E2E_NONCE" > "$E2E_TMP/static/$E2E_MARKER"

go build -o "$E2E_TMP/cato-bin" ./cmd/cato

E2E_ADDR="${E2E_ADDR:-:7180}"
E2E_PORT="${E2E_ADDR##*:}"

IGDB_CLIENT_ID= IGDB_CLIENT_SECRET= TWITCH_OAUTH_ID= TWITCH_OAUTH_SECRET= \
GOOGLE_KEY= GOOGLE_SECRET= CATO_BASE_URL= CATO_SECURE_COOKIES=false \
CATO_AUTH_RATE_LIMIT=1000 \
CATO_DB_PATH="$E2E_TMP/cato.db" \
CATO_COVER_DIR="$E2E_TMP/covers" \
CATO_STATIC_DIR="$E2E_TMP/static" \
CATO_LISTEN_ADDR="$E2E_ADDR" \
"$E2E_TMP/cato-bin" &
SRV_PID=$!

# Wait for the server to finish migrating, then seed the catalog.
READY=0
for _ in $(seq 1 100); do
  if ! kill -0 "$SRV_PID" 2>/dev/null; then
    wait "$SRV_PID"
    exit 1
  fi
  if curl -sf --max-time 1 "http://127.0.0.1:${E2E_PORT}/healthz" >/dev/null 2>&1 &&
     [ "$(curl -sf --max-time 1 "http://127.0.0.1:${E2E_PORT}/$E2E_MARKER" 2>/dev/null)" = "$E2E_NONCE" ] &&
     kill -0 "$SRV_PID" 2>/dev/null; then
    READY=1
    break
  fi
  sleep 0.2
done
[ "$READY" = 1 ] || { echo "E2E server failed to establish owned readiness" >&2; exit 1; }
sqlite3 "$E2E_TMP/cato.db" \
  "INSERT OR IGNORE INTO games (id, name, slug, safe_name, normalized_name, summary) VALUES
   (1, 'Test Game', 'test-game', 'Test Game', 'test game', 'A test game for E2E'),
   (2, 'Game Two', 'game-two', 'Game Two', 'game two', 'Second E2E seed game');
   UPDATE games SET platforms_json = '[\"PC (Microsoft Windows)\",\"Nintendo Switch\"]' WHERE id = 1;
   WITH RECURSIVE ids(id) AS (SELECT 100 UNION ALL SELECT id + 1 FROM ids WHERE id < 164)
   INSERT OR IGNORE INTO games (id, name, slug, safe_name, normalized_name)
   SELECT id, printf('Pagination Game %03d', id), printf('pagination-game-%d', id),
     printf('Pagination Game %03d', id), printf('pagination game %03d', id) FROM ids;"

# Readiness includes seeding, which /healthz alone cannot guarantee.
kill -0 "$SRV_PID" 2>/dev/null || { wait "$SRV_PID"; exit 1; }
echo CATO_E2E_READY
wait $SRV_PID
