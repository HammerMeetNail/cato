#!/usr/bin/env bash
# Called only by the tag deployment job. Does not provision the host: the
# directory, data ownership, cato.service and the tunnel route are one-time
# operator steps (docs/agents/deployment.md).
set -euo pipefail
: "${DEPLOY_HOST:?}" "${DEPLOY_USER:?}" "${DEPLOY_PATH:?}"
: "${RELEASE_TAG:?}" "${RELEASE_COMMIT:?}"
: "${SSH_PRIVATE_KEY:?}" "${SSH_KNOWN_HOSTS:?}" "${IMAGE_DIGEST:?}"
: "${CATO_BASE_URL:?}"
IMAGE_REPOSITORY=${IMAGE_REPOSITORY:-quay.io/localbyte/cato}
DEPLOY_PORT=${DEPLOY_PORT:-22}
# These values enter SSH configuration / the remote shell; reject shell metacharacters.
# Each check must fail explicitly: bash 3.2 (macOS /bin/bash) does NOT abort on a
# failed [[ ]] under set -e, so a bare [[ ]] gate would silently let invalid
# values through to ssh on that platform.
reject() { echo "error: $1 failed validation" >&2; exit 1; }
[[ "$DEPLOY_HOST" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || reject DEPLOY_HOST
[[ "$DEPLOY_USER" =~ ^[a-z_][a-z0-9_-]*$ ]] || reject DEPLOY_USER
[[ "$DEPLOY_PORT" =~ ^[0-9]+$ ]] || reject DEPLOY_PORT
(( DEPLOY_PORT > 0 && DEPLOY_PORT <= 65535 )) || reject DEPLOY_PORT
[[ "$DEPLOY_PATH" =~ ^/[A-Za-z0-9_/-]+$ ]] || reject DEPLOY_PATH
[[ "$DEPLOY_PATH" != / ]] || reject DEPLOY_PATH
[[ "$IMAGE_REPOSITORY" =~ ^quay\.io/[a-z0-9_-]+/[a-z0-9_.-]+$ ]] || reject IMAGE_REPOSITORY
[[ "$IMAGE_DIGEST" =~ ^sha256:[a-f0-9]{64}$ ]] || reject IMAGE_DIGEST
[[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || reject RELEASE_TAG
[[ "$RELEASE_COMMIT" =~ ^[a-f0-9]{40}$ ]] || reject RELEASE_COMMIT
[[ "$CATO_BASE_URL" =~ ^https://[A-Za-z0-9.-]+$ ]] || reject CATO_BASE_URL
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
umask 077
ssh_dir=$(mktemp -d)
trap 'rm -rf "$ssh_dir"' EXIT
printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/key"
printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/known_hosts"
cat > "$ssh_dir/config" <<CONFIG
Host cato-deploy
  HostName $DEPLOY_HOST
  User $DEPLOY_USER
  Port $DEPLOY_PORT
  IdentityFile $ssh_dir/key
  UserKnownHostsFile $ssh_dir/known_hosts
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  IdentitiesOnly yes
  BatchMode yes
  ConnectTimeout 30
CONFIG
if [[ ${DEPLOY_CLOUDFLARE_ACCESS:-false} == true ]]; then
  # A service token is needed only when an Access policy guards the SSH host.
  command -v cloudflared >/dev/null
  printf '  ProxyCommand cloudflared access ssh --hostname %%h\n' >> "$ssh_dir/config"
fi

# The host's .env is rendered here from the job's environment (GitHub secrets
# and variables) and streamed over SSH as data: secrets never enter a command
# line or the remote script. Single quotes keep values literal for
# podman-compose's dotenv parser, so a value may not contain one.
ENV_KEYS=(CATO_BASE_URL CATO_TRUSTED_PROXIES CATO_SIGNUP CATO_LOG_FORMAT CATO_METRICS_TOKEN
  CATO_WIKIDATA_LINKS CATO_PUSH_CONTACT GOOGLE_KEY GOOGLE_SECRET TWITCH_OAUTH_ID TWITCH_OAUTH_SECRET
  STEAM_WEB_API_KEY ITAD_API_KEY UPCITEMDB_KEY
  CATO_SMTP_HOST CATO_SMTP_PORT CATO_SMTP_USER CATO_SMTP_PASSWORD CATO_SMTP_FROM)
# The nightly backup's settings go to their own backup.env, read only by
# scripts/backup.sh and verify-backup.sh: the app container never sees them.
BACKUP_KEYS=(BACKUP_ENCRYPTION_KEY R2_ACCOUNT_ID R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY R2_BUCKET
  RESEND_API_KEY EMAIL_FROM_ADDRESS BACKUP_NOTIFY_EMAILS)
render() {
  local key value
  for key in "$@"; do
    value=${!key-}
    [[ -n "$value" ]] || continue
    [[ "$value" != *$'\n'* && "$value" != *$'\r'* && "$value" != *"'"* ]] || reject "$key"
    printf "%s='%s'\n" "$key" "$value"
  done
}
render "${ENV_KEYS[@]}" > "$ssh_dir/app.env"
render "${BACKUP_KEYS[@]}" > "$ssh_dir/backup.env"
ssh -F "$ssh_dir/config" cato-deploy "umask 077 && cat > '$DEPLOY_PATH/.env.next'" < "$ssh_dir/app.env"
ssh -F "$ssh_dir/config" cato-deploy "umask 077 && cat > '$DEPLOY_PATH/backup.env.next'" < "$ssh_dir/backup.env"

# Only validated non-secret arguments cross the shell boundary.
compose_base64=$(base64 < "$repo_root/compose.server.yaml" | tr -d '\n')
database_base64=$(base64 < "$repo_root/scripts/database.py" | tr -d '\n')
# The backup timers' scripts (scripts/ on the host).
scripts_base64=$(tar -C "$repo_root/scripts" -cf - backup.sh verify-backup.sh notify-email.sh database.py | base64 | tr -d '\n')
ssh -F "$ssh_dir/config" cato-deploy \
  "bash -s -- '$DEPLOY_PATH' '$IMAGE_REPOSITORY@$IMAGE_DIGEST' '$compose_base64' '$database_base64' '$RELEASE_TAG' '$RELEASE_COMMIT' '$scripts_base64'" <<'REMOTE'
set -euo pipefail
umask 077
cd "$1"
image=$2
compose_base64=$3
database_base64=$4
release_tag=$5
release_commit=$6
scripts_base64=$7
health=http://127.0.0.1:8083/healthz
for executable in podman podman-compose python3 curl flock base64; do command -v "$executable" >/dev/null; done
# Serialize both workflow and manual invocations on this host.
exec 9>.deploy.lock
flock -n 9 || { echo 'Another deployment is running.' >&2; exit 1; }
[[ -f .env.next && -d data && -d data/covers ]]
stage=$(mktemp -d .deploy.XXXXXX)
trap 'rm -rf "$stage"' EXIT
# Persist attempted identity before restart: even a failed health check can follow a migration.
cat > "$stage/release-state.py" <<'PY_STATE'
import json, os, pathlib, re, sys, tempfile
assert sys.version_info >= (3, 10), "Python 3.10+ is required"
operation, tag, commit, digest = sys.argv[1:]
state_path = pathlib.Path(".release-state.json")
def version(value):
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", value):
        raise ValueError("invalid release marker version")
    return tuple(map(int, value[1:].split(".")))
if state_path.exists():
    previous = json.loads(state_path.read_text())
    if version(tag) < version(previous["tag"]):
        raise SystemExit("Refusing an older release; deliberate operator recovery is required")
    if version(tag) == version(previous["tag"]) and commit != previous["commit"]:
        raise SystemExit("Refusing to replace a release with a different commit")
if operation != "check":
    state = dict(tag=tag, commit=commit, digest=digest, status=operation)
    fd, name = tempfile.mkstemp(prefix=".release-state-", dir=".")
    try:
        with os.fdopen(fd, "w") as file:
            json.dump(state, file)
            file.write("\n")
            file.flush()
            os.fsync(file.fileno())
        os.replace(name, state_path)
        directory = os.open(".", os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        pathlib.Path(name).unlink(missing_ok=True)
PY_STATE
state() { python3 "$stage/release-state.py" "$1" "$release_tag" "$release_commit" "$image"; }
state check
# Rootless podman: the container's UID 10001 maps to a subordinate host UID,
# so data/ is provisioned once with `podman unshare chown -R 10001:10001 data`.
# Pull while the old process is still serving; private registries need host-managed login.
podman pull "$image"
podman run --rm --network none -v "$PWD/data:/app/data:Z" --entrypoint sh "$image" -ec '
    test -w /app/data && test -w /app/data/covers
    for file in /app/data/cato.db /app/data/cato.db-wal /app/data/cato.db-shm; do
      test ! -e "$file" || test -w "$file"
    done
  '
# The digest goes into the compose file itself, so a plain `podman-compose up -d`
# (cato.service at boot) runs this release.
printf '%s' "$compose_base64" | base64 -d | IMAGE="$image" python3 -c '
import os, sys
text = sys.stdin.read()
line = "image: ${CATO_IMAGE:?Set CATO_IMAGE to a repository@sha256 digest}"
assert text.count(line) == 1, "compose.server.yaml image line changed"
sys.stdout.write(text.replace(line, "image: " + os.environ["IMAGE"]))
' > "$stage/compose.yaml"
printf 'CATO_IMAGE=%s\n' "$image" > "$stage/release.env"
mkdir -p backups
backup_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
if [[ -f data/cato.db ]]; then
  curl --fail --silent --show-error --max-time 15 "$health" >/dev/null || \
    echo 'Existing service is unhealthy; continuing with verified backup and recovery rollout.' >&2
  # Reuse the verified online backup: WAL-safe, FK checks, standalone journal and fsync.
  # In podman's user namespace, where the container's files (UID 10001) are ours.
  printf '%s' "$database_base64" | base64 -d > "$stage/database.py"
  backup=$(podman unshare python3 "$stage/database.py" backup --db data/cato.db --backup-dir backups)
  echo "Verified pre-deployment backup: $PWD/$backup"
fi
# Retain the old manifest, digest and settings for an operator; migrations may
# preclude automatic rollback.
[[ ! -f compose.yaml ]] || cp compose.yaml "backups/compose-$backup_id.yaml"
[[ ! -f release.env ]] || cp release.env "backups/release-$backup_id.env"
[[ ! -f .env ]] || cp .env "backups/env-$backup_id"
state attempted
mv "$stage/compose.yaml" compose.yaml
mv "$stage/release.env" release.env
mv .env.next .env
mv backup.env.next backup.env
mkdir -p scripts
printf '%s' "$scripts_base64" | base64 -d | tar -C scripts -xf -
chmod 0700 scripts/backup.sh scripts/verify-backup.sh
# No 'down', no volume deletion. 9>&-: conmon and rootlessport outlive this
# script, and holding the lock's descriptor they'd block every later deploy.
podman-compose -f compose.yaml up -d --force-recreate 9>&-
for _ in $(seq 1 60); do
  curl --fail --silent --max-time 5 "$health" >/dev/null && break
  sleep 2
done
curl --fail --silent --show-error --max-time 15 "$health" >/dev/null
state succeeded
printf 'Healthy deployment: %s\n' "$image"
REMOTE
