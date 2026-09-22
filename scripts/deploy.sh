#!/usr/bin/env bash
# Cut a main-only release tag; the tag triggers the image release pipeline.
# DRY_RUN=1 prints the release without creating or pushing a tag.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "error: $*" >&2; exit 1; }
[[ "$(git branch --show-current)" == main ]] || fail "release tags must be cut from main"
[[ -z "$(git status --porcelain --untracked-files=no)" ]] || fail "commit or stash tracked changes first"
git fetch --quiet origin main --tags || fail "cannot verify origin/main; fetch failed"
[[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] || fail "local main must exactly match origin/main"

latest=
while IFS= read -r candidate; do
  if [[ "$candidate" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    latest=$candidate
    break
  fi
done < <(git tag --merged HEAD --sort=-version:refname)

if [[ -n "$latest" ]]; then
  [[ -n "$(git rev-list -n 1 "$latest..HEAD")" ]] || fail "nothing new to release since $latest"
  version=${latest#v}
  prefix=${version%.*}
  patch=${version##*.}
  next="v${prefix}.$((patch + 1))"
  range="$latest..HEAD"
else
  next=v0.1.0
  range=HEAD
fi
git show-ref --verify --quiet "refs/tags/$next" && fail "tag $next already exists on another history"

echo "Release $next from $(git rev-parse --short HEAD):"
git log --oneline "$range"
echo "Pushing this tag starts CI image publication and the configured production deployment."
if [[ "${DRY_RUN:-0}" == 1 ]]; then
  echo "Dry run: would create annotated tag $next and push only that tag."
  exit 0
fi
read -r -p "Create and push $next to trigger production deployment? [y/N] " answer || fail "release cancelled"
[[ "$answer" == y || "$answer" == Y ]] || fail "release cancelled"
git tag -a "$next" -m "Release $next"
if ! git push origin "refs/tags/$next"; then
  fail "push failed; local tag $next remains for inspection (nothing was force-pushed)"
fi
echo "Pushed $next. Watch the CI workflow and verify production using docs/production-runbook.md."
