#!/usr/bin/env bash
# Fetch the previously published index so mcplib index can merge into it and
# enforce digest immutability. "Not found" means this is the first publish,
# which is the state of the world exactly once and is not an error; any other
# failure is, because merging into an empty index by accident would drop every
# package the library has ever published.
set -euo pipefail
out="$1"
repo="${LIBRARY_OCI:-ghcr.io/pellumai/mcp-library}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if ! oras manifest fetch "$repo/index:latest" >/dev/null 2>"$tmp/err"; then
  if grep -qiE 'not found|404|name unknown|manifest unknown' "$tmp/err"; then
    printf '{"schema_version":1,"generated_at":"1970-01-01T00:00:00Z","library":"pellumai/mcp-library","signing_keys":[],"packages":[]}\n' > "$out"
    echo "no previous index at $repo/index:latest; this is the first publish"
    exit 0
  fi
  cat "$tmp/err" >&2
  echo "could not read $repo/index:latest, and it is not a 404; refusing to merge into an empty index" >&2
  exit 1
fi
oras pull "$repo/index:latest" --output "$tmp/pulled"
cp "$tmp/pulled/index.json" "$out"
echo "fetched the previous index: $(wc -c < "$out") bytes"
