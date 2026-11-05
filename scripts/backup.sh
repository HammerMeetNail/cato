#!/usr/bin/env bash
# Nightly off-host backup of the database (cato-backup.timer on the production
# host): a verified snapshot (scripts/database.py), gzip, gpg AES-256 with
# BACKUP_ENCRYPTION_KEY, uploaded to R2. Covers and avatars aren't backed up.
# Settings come from backup.env, which each deploy writes from GitHub secrets.
# A failure emails BACKUP_NOTIFY_EMAILS. Retention is the bucket's lifecycle rule.
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
mkdir -p backups
exec 9>backups/.backup.lock
flock -n 9 || exit 0
# On disk: /tmp is RAM on the host, and smaller than a growing database needs.
# A fixed name, under the lock, so the next run clears what a killed one left.
scratch=$PWD/backups/.tmp-backup
step=prepare
cleanup() {
  local result=$?
  rm -rf -- "$scratch"
  if (( result != 0 )); then
    echo "backup failed at: $step" >&2
    notify_email "Cato backup failed" "The nightly Cato database backup failed at: $step. Check: journalctl -u cato-backup.service" || true
  fi
  exit "$result"
}
trap cleanup EXIT
rm -rf -- "$scratch"
mkdir -m 700 "$scratch"

step=snapshot
# In podman's user namespace, where the container's files (UID 10001) are ours.
snapshot=$(podman unshare python3 scripts/database.py backup --db data/cato.db --backup-dir "$scratch")
step=encrypt
name="cato_$(date -u +%Y%m%d_%H%M%S).db.gz.gpg"
mkdir -m 700 "$scratch/gnupg"
gzip -9 < "$snapshot" | gpg --no-options --homedir "$scratch/gnupg" --batch --no-symkey-cache --pinentry-mode loopback \
  --symmetric --cipher-algo AES256 --passphrase-fd 3 --output "$scratch/$name" 3<<<"$BACKUP_ENCRYPTION_KEY"
step=upload
rclone copyto "$scratch/$name" "$remote/$name" --contimeout=10s --timeout=60s --retries=3
step=prune
# Pre-deploy snapshots and config copies on this disk: the newest three stay.
python3 scripts/prune.py files backups
echo "Backed up: $remote/$name"
