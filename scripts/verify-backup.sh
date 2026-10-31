#!/usr/bin/env bash
# Daily check that the newest off-host backup restores (cato-verify-backup.timer):
# it must be under 26 hours old, decrypt and decompress, pass SQLite's integrity
# and foreign key checks, and still hold accounts and library items. Anything
# else emails BACKUP_NOTIFY_EMAILS. Nothing on this host is touched.
set -euo pipefail
umask 077
cd "${CATO_DIR:-$(dirname "$0")/..}"
set -a
# shellcheck source=/dev/null
source ./backup.env
set +a
: "${BACKUP_ENCRYPTION_KEY:?}" "${R2_ACCOUNT_ID:?}" "${R2_ACCESS_KEY_ID:?}" "${R2_SECRET_ACCESS_KEY:?}"
# shellcheck source=scripts/notify-email.sh
source scripts/notify-email.sh
export RCLONE_CONFIG_R2_TYPE=s3 RCLONE_CONFIG_R2_PROVIDER=Cloudflare RCLONE_CONFIG_R2_NO_CHECK_BUCKET=true \
  RCLONE_CONFIG_R2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID" RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY" \
  RCLONE_CONFIG_R2_ENDPOINT="https://$R2_ACCOUNT_ID.r2.cloudflarestorage.com"
remote="r2:${R2_BUCKET:-cato-backups}"
scratch=$(mktemp -d)
step=list
cleanup() {
  local result=$?
  rm -rf -- "$scratch"
  if (( result != 0 )); then
    echo "backup verification failed at: $step" >&2
    notify_email "Cato backup verification failed" "The newest Cato backup didn't verify (stage: $step). Check: journalctl -u cato-verify-backup.service" || true
  fi
  exit "$result"
}
trap cleanup EXIT

# Names sort by their UTC timestamp.
newest=$(rclone lsf "$remote" --include 'cato_*.db.gz.gpg' --contimeout=10s --timeout=60s | sort | tail -n 1)
[[ -n "$newest" ]]
step=age
NAME="$newest" python3 -c '
import datetime as dt, os, re
stamp = re.fullmatch(r"cato_(\d{8}_\d{6})\.db\.gz\.gpg", os.environ["NAME"]).group(1)
age = dt.datetime.now(dt.timezone.utc) - dt.datetime.strptime(stamp, "%Y%m%d_%H%M%S").replace(tzinfo=dt.timezone.utc)
assert dt.timedelta(0) <= age < dt.timedelta(hours=26), f"newest backup is {age} old"
'
step=download
rclone copyto "$remote/$newest" "$scratch/$newest" --contimeout=10s --timeout=60s --retries=3
step=decrypt
mkdir -m 700 "$scratch/gnupg"
gpg --no-options --homedir "$scratch/gnupg" --batch --no-symkey-cache --pinentry-mode loopback --quiet \
  --decrypt --passphrase-fd 3 "$scratch/$newest" 3<<<"$BACKUP_ENCRYPTION_KEY" | gzip -d > "$scratch/cato.db"
step=check
python3 - "$scratch/cato.db" <<'PY'
import sqlite3, sys
db = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok", "integrity check failed"
assert not db.execute("PRAGMA foreign_key_check").fetchall(), "foreign key check failed"
users = db.execute("SELECT count(*) FROM users").fetchone()[0]
items = db.execute("SELECT count(*) FROM library_items").fetchone()[0]
assert users > 0 and items > 0, f"{users} users, {items} library items"
print(f"verified: {users} users, {items} library items")
PY
echo "Verified: $remote/$newest"
