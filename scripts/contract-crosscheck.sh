#!/usr/bin/env bash
# Prove the committed contract/ files are byte-identical to the ones the
# pinned MCPGW build publishes. Needs GH_TOKEN with read access to
# PellumAI/MCPGW, which is private; ci.yml only calls this when one is set.
#
# With mcpgw_release set, the files come from that release's assets, which is
# the plan of record. Until the first MCPGW release that attaches them exists,
# mcpgw_commit is the pin and the files come from that commit's tree.
set -euo pipefail

field() { sed -nE "s/^$1:[[:space:]]*\"?([^\"#[:space:]]*)\"?.*/\1/p" EXECUTOR_TARGET.yaml; }

release="$(field mcpgw_release)"
commit="$(field mcpgw_commit)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [ -n "$release" ]; then
  gh release download "$release" --repo PellumAI/MCPGW \
    --pattern mcpgw-package.schema.json --pattern runtime-window.json \
    --dir "$tmp" --clobber
else
  test -n "$commit"
  for f in package.schema.json:mcpgw-package.schema.json runtime-window.json:runtime-window.json; do
    src="${f%%:*}" dst="${f##*:}"
    gh api -H 'Accept: application/vnd.github.raw' \
      "repos/PellumAI/MCPGW/contents/internal/mcpcatalog/schema/$src?ref=$commit" > "$tmp/$dst"
  done
fi

for f in mcpgw-package.schema.json runtime-window.json; do
  if ! cmp -s "$tmp/$f" "contract/$f"; then
    echo "contract/$f differs from the file the pinned MCPGW build publishes" >&2
    exit 1
  fi
  echo "contract/$f matches the pinned MCPGW build"
done
