#!/usr/bin/env bash
# Starts a disposable cato server for Playwright E2E runs: fresh temp DB,
# fresh covers dir, HTTPS on port 7180 (configurable via E2E_ADDR).
#
# TLS: Cato is HTTPS-only. The certificate comes from `cato dev-cert` in
# E2E_TLS_DIR (e2e/playwright.config.ts generates it first and pins its SPKI
# in Chromium); standalone runs get a throwaway one under the temp dir. No
# trust store is touched.
#
# E2E-specific settings, and why:
# - CATO_AUTH_RATE_LIMIT=1000 / CATO_SEARCH_RATE_LIMIT=1000: every Playwright
#   client shares one IP (127.0.0.1), so the production per-IP buckets
#   (10 login / 5 signup / 120 searches per minute) would 429 legitimate
#   traffic mid-suite. 429 semantics themselves are covered deterministically
#   in Go (auth.TestRateLimiterMiddleware, http.TestSearchIsRateLimitedPerIP).
# - CATO_SIGNUP=open: each test creates its own account (production defaults
#   to closed after the first account).
# - CATO_DEV=1: serves the component gallery (#dev/ui) for a11y and visual specs.
# - Seed catalog (games 1-2, 100-164, 200-215): the fresh DB has an empty catalog and IGDB is
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
E2E_TLS_DIR="${E2E_TLS_DIR:-$E2E_TMP/tls}"
"$E2E_TMP/cato-bin" dev-cert --out "$E2E_TLS_DIR" >/dev/null

# Bind loopback only: this disposable server has relaxed auth rate limits and
# seeded data; it must not be reachable from the LAN. Playwright talks to
# 127.0.0.1 only (e2e/playwright.config.ts).
E2E_ADDR="${E2E_ADDR:-127.0.0.1:7180}"
E2E_PORT="${E2E_ADDR##*:}"

IGDB_CLIENT_ID= IGDB_CLIENT_SECRET= TWITCH_OAUTH_ID= TWITCH_OAUTH_SECRET= \
GOOGLE_KEY= GOOGLE_SECRET= CATO_BASE_URL= CATO_SECURE_COOKIES=false \
CATO_DEV_TLS= CATO_ALLOW_INSECURE= CATO_HTTP_REDIRECT_ADDR= CATO_TRUSTED_PROXIES= \
CATO_TLS_CERT_FILE="$E2E_TLS_DIR/cert.pem" CATO_TLS_KEY_FILE="$E2E_TLS_DIR/key.pem" \
CATO_AUTH_RATE_LIMIT=1000 CATO_SEARCH_RATE_LIMIT=1000 CATO_SIGNUP=open CATO_DEV=1 \
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
  # -k: the certificate is self-signed; the nonce marker (not the
  # certificate) proves this listener is the server started above.
  if curl -skf --max-time 1 "https://127.0.0.1:${E2E_PORT}/healthz" >/dev/null 2>&1 &&
     [ "$(curl -skf --max-time 1 "https://127.0.0.1:${E2E_PORT}/$E2E_MARKER" 2>/dev/null)" = "$E2E_NONCE" ] &&
     kill -0 "$SRV_PID" 2>/dev/null; then
    READY=1
    break
  fi
  sleep 0.2
done
[ "$READY" = 1 ] || { echo "E2E server failed to establish owned readiness" >&2; exit 1; }
# .timeout: the Go server holds the DB open (startup repairs + maintenance
# sweep); the sqlite3 CLI's default busy timeout is 0 and would abort the
# harness with "database is locked" under contention.
sqlite3 "$E2E_TMP/cato.db" \
  ".timeout 15000" \
  "INSERT OR IGNORE INTO games (id, name, slug, safe_name, normalized_name, summary) VALUES
   (1, 'Test Game', 'test-game', 'Test Game', 'test game', 'A test game for E2E'),
   (2, 'Game Two', 'game-two', 'Game Two', 'game two', 'Second E2E seed game');
   UPDATE games SET platforms_json = '[\"PC (Microsoft Windows)\",\"Nintendo Switch\"]' WHERE id = 1;
   WITH RECURSIVE ids(id) AS (SELECT 100 UNION ALL SELECT id + 1 FROM ids WHERE id < 164)
   INSERT OR IGNORE INTO games (id, name, slug, safe_name, normalized_name)
   SELECT id, printf('Pagination Game %03d', id), printf('pagination-game-%d', id),
     printf('Pagination Game %03d', id), printf('pagination game %03d', id) FROM ids;
   INSERT OR IGNORE INTO games (id, name, slug, safe_name, normalized_name, summary, platforms_json,
     first_release_date, aggregated_rating, aggregated_rating_count, popularity_score, local_cover_path) VALUES
   (200, 'Hades', 'hades', 'Hades', 'hades', 'Escape the underworld.', '[6,130]', 1600300800, 93, 40, 900, '/covers/200.jpg'),
   (201, 'Celeste', 'celeste', 'Celeste', 'celeste', 'Climb the mountain.', '[6,130]', 1516665600, 92, 35, 850, '/covers/201.jpg'),
   (202, 'Hollow Knight', 'hollow-knight', 'Hollow Knight', 'hollow knight', 'Descend into Hallownest.', '[6,130,48]', 1487894400, 87, 30, 800, '/covers/202.jpg'),
   (203, 'Balatro', 'balatro', 'Balatro', 'balatro', 'Poker roguelike.', '[6,130,167]', 1708041600, 90, 25, 780, '/covers/203.jpg'),
   (204, 'Outer Wilds', 'outer-wilds', 'Outer Wilds', 'outer wilds', 'A time loop in space.', '[6,48]', 1559001600, 85, 28, 760, '/covers/204.jpg'),
   (205, 'Stardew Valley', 'stardew-valley', 'Stardew Valley', 'stardew valley', 'Farm life.', '[6,130]', 1456444800, 89, 33, 740, '/covers/205.jpg'),
   (206, 'Disco Elysium', 'disco-elysium', 'Disco Elysium', 'disco elysium', 'A detective RPG.', '[6,167]', 1571702400, 91, 31, 720, '/covers/206.jpg'),
   (207, 'Slay the Spire', 'slay-the-spire', 'Slay the Spire', 'slay the spire', 'Deckbuilding roguelike.', '[6,130]', 1548115200, 89, 29, 700, '/covers/207.jpg'),
   (208, 'Tunic', 'tunic', 'Tunic', 'tunic', 'A little fox.', '[6,130]', 1647302400, 85, 22, 680, '/covers/208.jpg'),
   (209, 'Dead Cells', 'dead-cells', 'Dead Cells', 'dead cells', 'Roguevania.', '[6,130,48]', 1533600000, 89, 27, 660, '/covers/209.jpg'),
   (210, 'Zelda Breath of the Wild', 'zelda-botw', 'Zelda Breath of the Wild', 'zelda breath of the wild', 'Open air adventure.', '[130]', 1488499200, 97, 60, 990, ''),
   (211, 'Zelda Tears of the Kingdom', 'zelda-totk', 'Zelda Tears of the Kingdom', 'zelda tears of the kingdom', 'Sky islands.', '[130]', 1683849600, 96, 55, 980, ''),
   (212, 'Zelda Links Awakening', 'zelda-la', 'Zelda Links Awakening', 'zelda links awakening', 'Koholint island.', '[130]', 1569542400, 87, 40, 600, ''),
   (213, 'Pikmin 4', 'pikmin-4', 'Pikmin 4', 'pikmin 4', 'Tiny explorers.', '[130]', 1689897600, 87, 30, 640, ''),
   (214, 'Astro Bot', 'astro-bot', 'Astro Bot', 'astro bot', 'Platforming joy.', '[167]', 1725580800, 94, 50, 950, ''),
   (215, 'Metroid Dread', 'metroid-dread', 'Metroid Dread', 'metroid dread', 'Samus returns.', '[130]', 1633651200, 88, 40, 620, '');
   -- A fixed admin (password e2e-password-1, bcrypt cost 4) so admin specs
   -- don't depend on which test signs up first.
   INSERT OR IGNORE INTO users (id, email, password_hash, display_name, username, role, visibility) VALUES
   ('e2e-admin', 'admin@e2e.test', '\$2a\$04\$K4tCXdGH3if/71qNrmzGf.MlyEEvl/p23Qg30S4jNeOxdsL/miw3G', 'E2E Admin', 'e2e_admin', 'admin', 'instance');"

# Readiness includes seeding, which /healthz alone cannot guarantee.
kill -0 "$SRV_PID" 2>/dev/null || { wait "$SRV_PID"; exit 1; }
echo CATO_E2E_READY
wait $SRV_PID
