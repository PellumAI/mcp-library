#!/usr/bin/env bash
# touched-servers.sh — print the servers/<name> directories a pull request
# touches, one per line, sorted and de-duplicated.
#
# Shared by the packages, audit and smoke CI jobs so all three agree on what
# "touched" means; a name printed here may since have been removed, which is
# each job's own concern, not this script's.
#
# Usage: BASE_REF=main scripts/touched-servers.sh
set -euo pipefail

: "${BASE_REF:?BASE_REF is required}"

git diff --name-only "origin/${BASE_REF}...HEAD" \
  | sed -nE 's#^servers/([^/]+)/.*#\1#p' | sort -u
