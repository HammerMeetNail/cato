#!/usr/bin/env bash
# Called only by the tag deployment job. Does not provision the host or its secrets.
set -euo pipefail
: "${DEPLOY_HOST:?}" "${DEPLOY_USER:?}" "${DEPLOY_PATH:?}"
: "${RELEASE_TAG:?}" "${RELEASE_COMMIT:?}"
: "${SSH_PRIVATE_KEY:?}" "${SSH_KNOWN_HOSTS:?}" "${IMAGE_DIGEST:?}"
IMAGE_REPOSITORY=${IMAGE_REPOSITORY:-quay.io/nabu/cato}
DEPLOY_PORT=${DEPLOY_PORT:-22}
# These values enter SSH configuration / the remote shell; reject shell metacharacters.
[[ "$DEPLOY_HOST" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]]
[[ "$DEPLOY_USER" =~ ^[a-z_][a-z0-9_-]*$ ]]
[[ "$DEPLOY_PORT" =~ ^[0-9]+$ ]] && (( DEPLOY_PORT > 0 && DEPLOY_PORT <= 65535 ))
[[ "$DEPLOY_PATH" =~ ^/[A-Za-z0-9_/-]+$ && "$DEPLOY_PATH" != / ]]
[[ "$IMAGE_REPOSITORY" =~ ^quay\.io/[a-z0-9_-]+/[a-z0-9_.-]+$ ]]
[[ "$IMAGE_DIGEST" =~ ^sha256:[a-f0-9]{64}$ ]]
[[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]
[[ "$RELEASE_COMMIT" =~ ^[a-f0-9]{40}$ ]]
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
  : "${TUNNEL_SERVICE_TOKEN_ID:?}" "${TUNNEL_SERVICE_TOKEN_SECRET:?}"
  command -v cloudflared >/dev/null
  printf '  ProxyCommand cloudflared access ssh --hostname %%h\n' >> "$ssh_dir/config"
fi
# Only validated non-secret arguments cross the shell boundary. Host .env is never rewritten.
compose_base64=$(base64 < "$repo_root/compose.server.yaml" | tr -d '\n')
database_base64=$(base64 < "$repo_root/scripts/database.py" | tr -d '\n')
ssh -F "$ssh_dir/config" cato-deploy \
  "bash -s -- '$DEPLOY_PATH' '$IMAGE_REPOSITORY@$IMAGE_DIGEST' '$compose_base64' '$database_base64' '$RELEASE_TAG' '$RELEASE_COMMIT'" <<'REMOTE'
set -euo pipefail
umask 077
cd "$1"
image=$2
compose_base64=$3
database_base64=$4
release_tag=$5
release_commit=$6
for executable in docker python3 curl flock base64; do command -v "$executable" >/dev/null; done
docker compose version >/dev/null
# Serialize both workflow and manual invocations on this host.
exec 9>.deploy.lock
flock -n 9 || { echo 'Another deployment is running.' >&2; exit 1; }
[[ -f .env && -d data && -d data/covers ]]
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
# Provision data ownership as UID/GID 10001 on the host before the first deployment.
# Pull while the old process is still serving; private registries need host-managed login.
docker pull "$image"
docker run --rm --network none --mount "type=bind,src=$PWD/data,dst=/app/data" \
  --entrypoint sh "$image" -ec '
    test -w /app/data && test -w /app/data/covers
    for file in /app/data/cato.db /app/data/cato.db-wal /app/data/cato.db-shm; do
      test ! -e "$file" || test -w "$file"
    done
  '
printf '%s' "$compose_base64" | base64 -d > "$stage/compose.yaml"
printf 'CATO_IMAGE=%s\n' "$image" > "$stage/release.env"
docker compose --project-directory "$PWD" --env-file "$stage/release.env" -f "$stage/compose.yaml" config --quiet
mkdir -p backups
backup_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
if [[ -f data/cato.db ]]; then
  curl --fail --silent --show-error --max-time 15 http://127.0.0.1:7080/healthz >/dev/null || \
    echo 'Existing service is unhealthy; continuing with verified backup and recovery rollout.' >&2
  # Reuse the verified online backup: WAL-safe, FK checks, standalone journal and fsync.
  printf '%s' "$database_base64" | base64 -d > "$stage/database.py"
  backup=$(python3 "$stage/database.py" backup --db data/cato.db --backup-dir backups)
  echo "Verified pre-deployment backup: $PWD/$backup"
fi
# Retain the old manifest/digest for an operator; migrations may preclude automatic rollback.
[[ ! -f compose.yaml ]] || cp compose.yaml "backups/compose-$backup_id.yaml"
[[ ! -f release.env ]] || cp release.env "backups/release-$backup_id.env"
state attempted
mv "$stage/compose.yaml" compose.yaml
mv "$stage/release.env" release.env
# No 'down', no volume deletion, and no mutation of host-managed application credentials.
docker compose --env-file release.env -f compose.yaml up -d --wait --wait-timeout 120
curl --fail --silent --show-error --max-time 15 http://127.0.0.1:7080/healthz >/dev/null
state succeeded
printf 'Healthy deployment: %s\n' "$image"
REMOTE
