#!/usr/bin/env bash
# Assemble the GitHub Pages tree, which is the gateway's library_source. The
# layout is the contract, literally:
#
#   site/index.json, index.json.sig, index.json.sig.<key id>
#   site/blobs/sha256/<hex>, <hex>.sig, <hex>.sig.<key id>, <hex>.cosign.bundle
#   site/keys/<key id>.pub
#
# A Pages deployment replaces the whole site, so every blob the index lists is
# placed here: from this run's build when it built it, otherwise re-downloaded
# from GHCR by digest. A partial site would break every gateway pinned to a
# digest that was not rebuilt this run.
#
# Usage: assemble-site.sh <dist> <site>
set -euo pipefail
dist="$1"
site="$2"
repo="${LIBRARY_OCI:-ghcr.io/pellumai/mcp-library}"

rm -rf "$site"
mkdir -p "$site/blobs/sha256" "$site/keys"
cp "$dist"/index.json "$dist"/index.json.sig* "$site/"
cp keys/*.pub "$site/keys/"

place() { # <tar> <hex>
  local tar="$1" hex="$2" f
  cp "$tar" "$site/blobs/sha256/$hex"
  for f in "$tar".sig "$tar".sig.* "$tar".cosign.bundle; do
    [ -f "$f" ] || continue
    cp "$f" "$site/blobs/sha256/$hex${f#"$tar"}"
  done
}

shopt -s nullglob
declare -A built=()
for tar in "$dist"/*.tar.gz; do
  hex="$(cut -d' ' -f1 < "$tar.sha256")"
  place "$tar" "$hex"
  built[$hex]=1
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for hex in $(jq -r '[.packages[].versions[].blobs[].sha256] | unique | .[]' "$dist/index.json"); do
  [ -n "${built[$hex]:-}" ] && continue
  echo "carrying over $hex from GHCR"
  rm -rf "$tmp/pull" && mkdir -p "$tmp/pull"
  oras pull "$repo/blobs:sha256-$hex" --output "$tmp/pull"
  tars=("$tmp"/pull/*.tar.gz)
  if [ "${#tars[@]}" -ne 1 ]; then
    echo "$repo/blobs:sha256-$hex does not hold exactly one tar" >&2
    exit 1
  fi
  got="$(sha256sum "${tars[0]}" | cut -d' ' -f1)"
  if [ "$got" != "$hex" ]; then
    echo "$repo/blobs:sha256-$hex holds bytes hashing to $got" >&2
    exit 1
  fi
  place "${tars[0]}" "$hex"
done

# Pages serves a directory index for / only when index.html exists; the
# contract paths need no index, and a .nojekyll file stops Pages rewriting
# anything under a leading underscore.
touch "$site/.nojekyll"
echo "assembled $(find "$site/blobs/sha256" -maxdepth 1 -type f ! -name '*.*' | wc -l) blobs into $site"
