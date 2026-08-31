#!/bin/sh
# Syncs action metadata defaults and documentation refs to a release version.
# Usage: scripts/sync-action-versions.sh <version>
set -eu

usage() {
  echo "usage: $0 <version>" >&2
  exit 1
}

[ "$#" -eq 1 ] || usage
new_version="$1"
printf '%s' "$new_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || usage

setup_file="actions/setup/action.yml"
[ -f "$setup_file" ] || { echo "error: missing $setup_file" >&2; exit 1; }

old_version=$(awk '
  match($0, /default: "[0-9]+\.[0-9]+\.[0-9]+"/) {
    print substr($0, RSTART + length("default: \""), RLENGTH - length("default: \"\""));
    exit
  }
' "$setup_file")
[ -n "$old_version" ] || { echo "error: could not read current version default from $setup_file" >&2; exit 1; }

if [ "$old_version" = "$new_version" ]; then
  echo "already at ${new_version}"
  exit 0
fi

rewrite() {
  file=$1
  tmp=$(mktemp) || exit 1
  awk -v new="$new_version" '
    /^  version:$/ { in_version = 1; print; next }
    in_version && /^  [a-z-]+:/ { in_version = 0 }
    in_version && /^    default:/ {
      printf "    default: \"%s\"\n", new;
      in_version = 0;
      next
    }
    { print }
  ' "$file" | sed -e "s/version: ${old_version}/version: ${new_version}/g" \
                  -e "s/@v${old_version}/@v${new_version}/g" > "$tmp"
  mv "$tmp" "$file"
}

for meta in actions/*/action.y*ml; do
  [ -f "$meta" ] || continue
  rewrite "$meta"
done

for doc in README.md actions/README.md docs/github-app.md; do
  [ -f "$doc" ] || continue
  rewrite "$doc"
done

echo "synced ${old_version} -> ${new_version}"
