#!/usr/bin/env bash
# Re-vendors the frontend libraries from npm (docs/cato_smooth.md §4.2):
#   scripts/vendor-update.sh [LIT_HTML_VERSION] [LUCIDE_VERSION]
# Copies lit-html's production modules and Lucide's icon data into
# web/static/js/vendor (and the generated ui/icons-data.js), rewrites
# js/vendor/CHECKSUMS, and prints the diff for review. Nothing runs at
# serve time; commit the result after reading the diff.
set -euo pipefail
LIT="${1:-3.3.3}"
LUCIDE="${2:-0.544.0}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VENDOR="$ROOT/web/static/js/vendor"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

(cd "$TMP" && npm pack --silent "lit-html@$LIT" "lucide-static@$LUCIDE" >/dev/null)
mkdir -p "$TMP/lit" "$TMP/lucide"
tar xzf "$TMP"/lit-html-*.tgz -C "$TMP/lit"
tar xzf "$TMP"/lucide-static-*.tgz -C "$TMP/lucide"

rm -rf "$VENDOR/lit-html"
mkdir -p "$VENDOR/lit-html/directives"
for f in lit-html.js directive.js directive-helpers.js async-directive.js LICENSE; do
  cp "$TMP/lit/package/$f" "$VENDOR/lit-html/"
done
for d in repeat live class-map style-map ref keyed if-defined; do
  cp "$TMP/lit/package/directives/$d.js" "$VENDOR/lit-html/directives/"
done
cp "$TMP/lucide/package/LICENSE" "$VENDOR/LUCIDE-LICENSE"
node "$ROOT/scripts/vendor-icons.cjs" "$TMP/lucide/package/icon-nodes.json" "$ROOT/web/static/js/ui/icons-data.js"
printf 'lit-html %s\nlucide-static %s\n' "$LIT" "$LUCIDE" > "$VENDOR/VERSIONS"
(cd "$VENDOR" && find . -type f ! -name CHECKSUMS | LC_ALL=C sort | xargs shasum -a 256 > CHECKSUMS)
git -C "$ROOT" --no-pager diff --stat -- web/static/js/vendor web/static/js/ui/icons-data.js
echo "Review the diff (git diff web/static/js/vendor), bump nothing else: asset versions are automatic."
