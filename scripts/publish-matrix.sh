#!/usr/bin/env bash
# Print the publish build matrix as JSON: {"include":[{"name":..,"arch":..}]}.
#
# A push to main builds the servers it touched. A change to the build image or
# the executor target touches every package, because both are inputs to every
# digest. A release or a manual run builds everything, so the index and the
# site are complete rather than incremental.
#
# Usage: publish-matrix.sh <event> [<before sha> <after sha>]
set -euo pipefail
event="$1"
before="${2:-}"
after="${3:-}"

all() { find servers -mindepth 2 -maxdepth 2 -name package.yaml -printf '%h\n' 2>/dev/null | xargs -r -n1 basename | sort -u; }

names=""
if [ "$event" = "push" ] && [ -n "$before" ] && ! [[ "$before" =~ ^0+$ ]]; then
  changed="$(git diff --name-only "$before" "$after")"
  if grep -qE '^(build/|EXECUTOR_TARGET\.yaml$|contract/)' <<<"$changed"; then
    names="$(all)"
  else
    names="$(sed -nE 's#^servers/([^/]+)/.*#\1#p' <<<"$changed" | sort -u)"
  fi
else
  names="$(all)"
fi

rows=()
for n in $names; do
  [ -f "servers/$n/package.yaml" ] || continue
  for a in $(./bin/mcplib arches --server "$n"); do
    rows+=("{\"name\":\"$n\",\"arch\":\"$a\"}")
  done
done
if [ "${#rows[@]}" -eq 0 ]; then
  echo '{"include":[]}'
else
  (IFS=,; echo "{\"include\":[${rows[*]}]}")
fi
