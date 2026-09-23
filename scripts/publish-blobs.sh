#!/usr/bin/env bash
# Push every built blob to GHCR as an OCI artifact addressed by its own
# digest, then push the index under the moving tag and, on a release, an exact
# one. A blob tag is sha256-<hex> because an OCI tag may not contain a colon;
# the digest inside the artifact and in the index is the real identity and the
# tag is only a way to find it.
#
# A blob tag that already exists is never pushed over. mcplib index has
# already refused a changed digest for a published name@version, so an
# existing tag holds these exact bytes, and skipping it is what keeps a
# published digest immutable on GHCR too.
#
# Usage: publish-blobs.sh <dist> [release-tag]
set -euo pipefail
dist="$1"
release="${2:-}"
repo="${LIBRARY_OCI:-ghcr.io/pellumai/mcp-library}"
media="application/vnd.mcpgw.package.v1+tar+gzip"
source_url="https://github.com/PellumAI/mcp-library"

cd "$dist"
shopt -s nullglob
for tar in *.tar.gz; do
  hex="$(cut -d' ' -f1 < "$tar.sha256")"
  ref="$repo/blobs:sha256-$hex"
  if oras manifest fetch "$ref" >/dev/null 2>&1; then
    echo "$tar is already published as $ref"
    continue
  fi
  files=("$tar:$media")
  for extra in "$tar".sig "$tar".sig.* "$tar".cosign.bundle; do
    [ -f "$extra" ] && files+=("$extra:text/plain")
  done
  echo "pushing $tar as $ref"
  oras push "$ref" \
    --artifact-type "$media" \
    --annotation "org.opencontainers.image.source=$source_url" \
    --annotation "org.opencontainers.image.revision=${GITHUB_SHA:-unknown}" \
    --annotation "vnd.mcpgw.package.sha256=$hex" \
    "${files[@]}"
done

index_files=("index.json:application/json")
for s in index.json.sig index.json.sig.*; do
  [ -f "$s" ] && index_files+=("$s:text/plain")
done
oras push "$repo/index:latest" \
  --artifact-type application/vnd.mcpgw.library-index.v1+json \
  --annotation "org.opencontainers.image.source=$source_url" \
  --annotation "org.opencontainers.image.revision=${GITHUB_SHA:-unknown}" \
  "${index_files[@]}"
echo "pushed $repo/index:latest"
if [ -n "$release" ]; then
  oras push "$repo/index:$release" \
    --artifact-type application/vnd.mcpgw.library-index.v1+json \
    --annotation "org.opencontainers.image.source=$source_url" \
    "${index_files[@]}"
  echo "pushed $repo/index:$release"
fi
