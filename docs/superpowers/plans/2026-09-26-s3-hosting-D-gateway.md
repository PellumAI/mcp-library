# S3 hosting D: gateway Implementation Plan

**Target repository:** `PellumAI/PellumStation`. This plan lives beside its spec in mcp-library; every path below is in PellumStation.
**Spec:** `docs/superpowers/specs/2026-09-26-s3-hosting-design.md` in `PellumAI/mcp-library`, sections Component 5, Cutover order step 5, Testing.
**Goal:** a PellumStation release pins `https://library.pellum.ai`, refreshes its snapshot from the library's `releases/` paths, and its comments and docs describe the private library honestly.
**Constraints:** the spec's `## Constraints` section applies. Additionally:
- Merge only after Plan C's cutover step 4 has verified `https://library.pellum.ai`; before that the new source serves nothing.
- `releases/latest/tag` is Plan C's plain-text file holding the newest release tag.
**Verification:** the stack-free suite in `.claude/agents/coder.md`; per-task test targets are listed under each task.

## File structure

| File | Responsibility |
|---|---|
| `internal/mcpcatalog/library/snapshot/pinned.json` | `source` is `https://library.pellum.ai` |
| `internal/mcpcatalog/library/snapshot_test.go` | pin the new source |
| `internal/mcpcatalog/library/library.go` | package comment: why the verifier is copied, no keyless attestation |
| `internal/mcplibrary/settingskeys.go` | `PinnedSource` comment |
| `internal/mcplibrary/client.go` | `maxIndexBytes` comment on decoded size |
| `internal/mcplibrary/client_fetch_test.go` | gzip-encoded index tests |
| `scripts/library-snapshot-refresh.sh` | read `releases/` from the library host |
| `internal/mcpcatalog/library/refresh_script_test.go` | the script against a fixture HTTP server |
| `docs/operations/mcp-library.md` | new default source |
| `docs/upgrade-notes.md` | the source change for operators who set it |

### Task 1: The pinned source and the comments

**Files:**
- Modify: `internal/mcpcatalog/library/snapshot/pinned.json:3`, `internal/mcpcatalog/library/library.go:15-25`, `internal/mcplibrary/settingskeys.go:43-46`, `docs/operations/mcp-library.md:31`, `docs/upgrade-notes.md:20`, a new entry above that line
- Test: `internal/mcpcatalog/library/snapshot_test.go`

**Interfaces:**
- Consumes: `library.PinnedSource() (string, error)` at `internal/mcpcatalog/library/snapshot.go:80`, assigned to `mcplibrary.PinnedSource` at `cmd/station/main.go:2849-2852`.

**Behavior:**
- `pinned.json` `source` → `https://library.pellum.ai`; `pins`, `index_sha256` and `refreshed_at` unchanged, so the snapshot still verifies.
- `settingskeys.go`: `PinnedSource` is set at startup from `library.PinnedSource()`, and stays empty only when that fails.
- `library.go`: the verifier is copied because the library repository is private and releases on its own cadence, so the product cannot import it; the keyless-attestation sentences go, since the library no longer publishes one.
- `mcp-library.md`: the default is `https://library.pellum.ai`.
- `upgrade-notes.md`: a new Unreleased entry says the default moved, the old Pages URL stops serving, and an operator who set `library_source` to `https://pellumai.github.io/mcp-library` must change it or clear it.

**Tests:**
- `TestPinnedSource_IsTheLibraryHost`: `PinnedSource()` returns `https://library.pellum.ai`.
- `make library-snapshot-check` still passes.

**Gotchas:**
- The name matches `TestPinned`, so `library-snapshot-check` runs it.
- `ui/src/pages/mcp-catalog/LibraryPage.test.tsx:36` carries the old host as mock data; leave it, no UI change here.

### Task 2: Snapshot refresh from `releases/`

**Files:**
- Modify: `scripts/library-snapshot-refresh.sh:1-45`
- Test: `internal/mcpcatalog/library/refresh_script_test.go`

**Interfaces:**
- Consumes: `go run ./internal/mcpcatalog/library/cmd/verifysnapshot <dir>` at `scripts/library-snapshot-refresh.sh:56`.
- Produces: `scripts/library-snapshot-refresh.sh [release-tag]`, env `LIBRARY_BASE`, default `https://library.pellum.ai`, and `SNAPSHOT_DIR`, default `internal/mcpcatalog/library/snapshot`.

**Behavior:**
- No tag → GET `$LIBRARY_BASE/releases/latest/tag`, trimmed; the tag must match `v[0-9]*`, else exit 2.
- Fetch `$LIBRARY_BASE/releases/<tag>/index.json`, `.sig`, and `.sig.<id>` per `signing_keys`; a missing `.sig.<id>` prints the existing note.
- The GitHub releases API and `releases/download` URLs are gone.
- Verification, `pinned.json` rewrite and the summary are unchanged; `source` is still copied from the existing `pinned.json`.

**Tests:**
- `TestRefreshScript_WritesAVerifiedSnapshot`: `httptest.NewServer` serves the committed snapshot's index and sigs under `releases/v0.1.0/` and `v0.1.0` at `releases/latest/tag`; with `SNAPSHOT_DIR` a temp copy, the script exits 0 and `pinned.json` pins `v0.1.0` with the old `source`.
- `TestRefreshScript_ExplicitTag`: tag argument reads `releases/<tag>/` and never requests `releases/latest/tag`.
- `TestRefreshScript_RefusesATamperedIndex`: one flipped byte → non-zero, output contains `nothing written`, `SNAPSHOT_DIR` unchanged.

**Gotchas:**
- The test runs `bash` and needs `jq` and `curl`; `t.Skip` when `jq` is absent, naming it.
- The script's nested `go run` builds from the repository root, so the test sets `cmd.Dir` to it.

### Task 3: Compressed index responses

**Files:**
- Modify: `internal/mcplibrary/client.go:77-80`
- Test: `internal/mcplibrary/client_fetch_test.go`

**Interfaces:**
- Consumes: `(*Client).FetchIndex(ctx, base string) (Index, string, error)` at `internal/mcplibrary/client.go:258`, `newEnv(t)` at `internal/mcplibrary/mcplibrary_test.go:79`.

**Behavior:**
- Finding: the client never sets `Accept-Encoding` or `DisableCompression`, so Go's transport asks for gzip only and decodes it; `br` is never requested, and the `maxIndexBytes` cap at `client.go:245` applies to decoded bytes. No distribution change is needed.
- The `maxIndexBytes` comment says it bounds the decoded index.

**Tests:**
- `TestFetchIndex_AcceptsAGzipEncodedIndex`: a server answering `Accept-Encoding: gzip` with `Content-Encoding: gzip` bytes → the index verifies and parses.
- `TestFetchIndex_CapsTheDecodedIndex`: a gzip body decoding past `maxIndexBytes` → error `is over`.

**Gotchas:**
- A test that sets `Accept-Encoding` itself turns off transparent decoding; let the transport set it.

## Coverage

Spec sections: Component 5; Cutover order step 5; Testing, PellumStation.
