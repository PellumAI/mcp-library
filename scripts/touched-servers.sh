#!/usr/bin/env bash
# touched-servers.sh — print the servers/<name> directories a pull request
# touches, one per line, sorted and de-duplicated.
#
# Shared by the packages, audit and smoke CI jobs so all three agree on what
# "touched" means; a name printed here may since have been removed, which is
# each job's own concern, not this script's.
#
# A change to what every package is built or smoked against — the executor
# target, the build image recipe, the build or smoke harness — touches every
# server, so the script then prints all of them. Otherwise a harness change
# would merge having re-proved nothing it could have broken.
#
# Usage: BASE_REF=main scripts/touched-servers.sh
set -euo pipefail

: "${BASE_REF:?BASE_REF is required}"

files="$(git diff --name-only "origin/${BASE_REF}...HEAD")"

{
  sed -nE 's#^servers/([^/]+)/.*#\1#p' <<<"$files"
  if grep -qE '^(EXECUTOR_TARGET\.yaml$|build/|internal/build/|internal/smoke/)' <<<"$files"; then
    for manifest in servers/*/package.yaml; do
      [ -f "$manifest" ] || continue
      basename "$(dirname "$manifest")"
    done
  fi
} | sort -u
