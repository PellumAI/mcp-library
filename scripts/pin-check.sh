#!/usr/bin/env bash
# pin-check.sh — fail if anything the build resolves is still floating.
#
# A reduced copy of MCPGW's scripts/pin-check.sh, carrying only the rule
# families this repository can break: action pins, `version: latest`, literal
# node-version, Dockerfile FROM and `# syntax=`, and `@latest` anywhere in the
# Makefile, scripts/ or .github/. Two rules are this repository's own: the
# build image in EXECUTOR_TARGET.yaml must be pinned by digest, and no recipe
# may resolve a dependency at build time.
#
# Escape hatch: put `pin-check: allow` plus a reason in a comment on the line,
# or on the line directly above it. There is deliberately no allowlist FILE.
set -uo pipefail

repo=${PIN_CHECK_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}
cd "$repo" || exit 1

failed=0
checks=0

fail() { printf 'pin-check: %s\n' "$1" >&2; failed=1; }
ok()   { checks=$((checks + 1)); }

exempt() { # file line [text]
  local prev
  case "${3:-}" in *"pin-check: allow"*) return 0 ;; esac
  [ "$2" -gt 1 ] 2>/dev/null || return 1
  prev=$(sed -n "$(( $2 - 1 ))p" "$1" 2>/dev/null)
  case "$prev" in *"pin-check: allow"*) return 0 ;; *) return 1 ;; esac
}

workflow_files=$(git ls-files '.github/workflows/*.yml')
docker_files=$(git ls-files | grep -E '(^|/)Dockerfile$' || true)

# ---------------------------------------------------------------- actions
# A tag ref is mutable: the same `uses:` line can run different code tomorrow.
# The 40-hex commit is the pin; the trailing `# vX.Y.Z` is what makes the line
# readable.
if [ -n "$workflow_files" ]; then
while IFS=: read -r file line rest; do
  exempt "$file" "$line" "$rest" && continue
  ref=$(printf '%s' "$rest" | sed -E 's/.*uses:[[:space:]]*//; s/[[:space:]]*(#.*)?$//')
  case "$ref" in ./*|docker://*) continue ;; esac
  ok
  sha=${ref##*@}
  if [ "$sha" = "$ref" ]; then
    fail "$file:$line: uses: $ref has no @ref at all"
  elif ! [[ "$sha" =~ ^[0-9a-f]{40}$ ]]; then
    fail "$file:$line: uses: $ref is pinned to \"$sha\", not a 40-hex commit SHA"
  elif ! printf '%s' "$rest" | grep -qE '#[[:space:]]*v?[0-9]+\.[0-9]+\.[0-9]+'; then
    fail "$file:$line: uses: ${ref%@*} has no trailing # vX.Y.Z comment"
  fi
done < <(grep -nHE '^[[:space:]]*(-[[:space:]]+)?uses:' $workflow_files)

# `version: latest` re-floats a tool the action SHA has already pinned.
while IFS=: read -r file line rest; do
  exempt "$file" "$line" "$rest" && continue
  ok
  fail "$file:$line: 'version: latest' — name the exact version"
done < <(grep -nHE '^[[:space:]]*version:[[:space:]]*.?latest.?[[:space:]]*$' $workflow_files)

# A literal node-version restates a runtime the build image already pins.
while IFS=: read -r file line rest; do
  exempt "$file" "$line" "$rest" && continue
  ok
  fail "$file:$line: literal node-version — the build image carries the node lines"
done < <(grep -nHE '^[[:space:]]*node-version:' $workflow_files)
fi

# ---------------------------------------------------------------- images
if [ -n "$docker_files" ]; then
while IFS=: read -r file line rest; do
  exempt "$file" "$line" "$rest" && continue
  ref=$(printf '%s' "$rest" | sed -E 's/^[[:space:]]*FROM[[:space:]]+//; s/--[a-z-]+=[^[:space:]]+[[:space:]]+//g; s/[[:space:]]+AS[[:space:]]+.*$//I; s/[[:space:]]*(#.*)?$//')
  case "$ref" in *:*|*/*) ;; *) continue ;; esac
  ok
  case "$ref" in *@sha256:*) ;; *) fail "$file:$line: FROM $ref has no @sha256 digest" ;; esac
done < <(grep -nHE '^[[:space:]]*FROM[[:space:]]' $docker_files)

while IFS=: read -r file line rest; do
  exempt "$file" "$line" "$rest" && continue
  ref=$(printf '%s' "$rest" | sed -E 's/^[[:space:]]*#[[:space:]]*syntax=//; s/[[:space:]]*$//')
  ok
  case "$ref" in *@sha256:*) ;; *) fail "$file:$line: # syntax=$ref has no @sha256 digest" ;; esac
done < <(grep -nHE '^[[:space:]]*#[[:space:]]*syntax=' $docker_files)
fi

# ---------------------------------------------------------------- @latest
while IFS=: read -r file line rest; do
  exempt "$file" "$line" "$rest" && continue
  ok
  fail "$file:$line: resolves @latest at run time — pin the version"
done < <(grep -nHE '[A-Za-z0-9_./-]@latest' Makefile $(git ls-files scripts .github) | grep -v '^scripts/pin-check.sh:')

# ---------------------------------------------------------------- recipes
# A recipe may not resolve a version at build time. mcplib validate enforces
# the same refusals in Go; they are enforced again here so a reviewer sees the
# failure in CI output rather than only in a Go test name.
recipes=$(git ls-files 'servers/*/package.yaml')
if [ -n "$recipes" ]; then
  ok
  if grep -nHE '"(npm)", *"(install|i|update|add)"|"go", *"(get|install)"|"(curl|wget|npx)"|"apt-get", *"install"|"apk", *"add"' $recipes; then
    fail "a recipe resolves a dependency at build time; see docs/CURATION.md"
  fi
  while IFS=: read -r file line rest; do
    exempt "$file" "$line" "$rest" && continue
    case "$rest" in *--require-hashes*) ;; *) fail "$file:$line: pip install without --require-hashes" ;; esac
  done < <(grep -nHE '"pip3?", *"install"|"-m", *"pip", *"install"' $recipes)
fi

if [ "$failed" -eq 0 ]; then
  echo "pin-check: $checks pins verified, everything is exact"
else
  echo "pin-check: FAILED — see the lines above" >&2
fi
exit "$failed"
