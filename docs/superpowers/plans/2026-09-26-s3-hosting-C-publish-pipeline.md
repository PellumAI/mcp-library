# S3 hosting C: publish pipeline Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-26-s3-hosting-design.md`, sections Decisions, Components 2, 3, 4, Cutover order, Error handling, Testing.
**Goal:** `publish.yml` publishes blobs, the signed index, release indexes and the site to the bucket behind `https://library.pellum.ai` through the OIDC publish role, a one-shot script migrates GHCR's blobs there, and Pages, ORAS and keyless attestation are gone.
**Constraints:** the spec's `## Constraints` section applies. Additionally:
- This repository holds no Terraform; bucket, distribution and role come from `PellumAI/Infra` through the Actions variables `AWS_PUBLISH_ROLE_ARN`, `LIBRARY_BUCKET`, `LIBRARY_DISTRIBUTION_ID`.
- The scripts call the `aws` CLI v2, at least 2.22, the first with `put-object --if-none-match`, and honour `AWS_ENDPOINT_URL` so the same scripts run against MinIO.
- Every new `uses:` is pinned to a 40-hex SHA with a `# vX.Y.Z` comment, and the MinIO image by `@sha256:` digest.
**Verification:** `make check`, plus `make test-s3`, the MinIO integration suite, for Tasks 2, 3, 4 and 6; per-task test targets are listed under each task.

## File structure

| File | Responsibility |
|---|---|
| `cmd/mcplib/fetch.go` | download a served library into a local tree for `verify --base` |
| `cmd/mcplib/fetch_test.go` | `--base` over an httptest TLS server |
| `cmd/mcplib/sign.go` | `verify` gains `--base` |
| `scripts/lib/s3.sh` | `put_immutable`, `put_mutable`, content type and cache rules per key |
| `scripts/publish-blobs.sh` | upload new blobs and signatures, never overwriting |
| `scripts/fetch-previous-index.sh` | read the previous index from the bucket |
| `scripts/assemble-site.sh` | the site tree without blobs |
| `scripts/publish-site.sh` | upload site, index, release paths, then invalidate |
| `scripts/migrate-ghcr-to-s3.sh` | one-shot copy of GHCR blobs and index into the bucket |
| `internal/s3publish/doc.go` | package clause for the integration suite |
| `internal/s3publish/publish_integration_test.go` | the scripts against MinIO in docker |
| `Makefile` | `test-s3` target |
| `.github/workflows/publish.yml` | OIDC role, S3 steps, no ORAS, GHCR push or keyless |
| `.github/workflows/pages.yml` | deleted |
| `.github/workflows/ci.yml` | run the MinIO suite |
| `internal/site/site.go` | `BaseURL` is `https://library.pellum.ai` |
| `internal/site/budget.go` | deleted |
| `internal/site/site_test.go` | new-host and no-keyless tests, budget tests removed |
| `internal/site/templates/server.html.tmpl` | drop the keyless verification block |
| `cmd/mcplib/site.go` | drop `--budget-mib` |
| `internal/sign/cosign.go` | comment no longer names a keyless attestation |
| `internal/fixture/fixture.go` | comment no longer names Pages |
| `docs/PUBLISHING.md` | S3, CloudFront, credentials, release paths, migration |
| `docs/INDEX-FORMAT.md` | release paths |
| `README.md` | new base URL, maintainer settings |
| `docs/CURATION.md` | drop the keyless bullet |

### Task 1: `mcplib verify --base`

**Files:**
- Create: `cmd/mcplib/fetch.go`
- Modify: `cmd/mcplib/sign.go:119-197`
- Test: `cmd/mcplib/fetch_test.go`

**Interfaces:**
- Consumes: `sign.FileSigs(path string) func(string) ([]byte, bool)` at `internal/sign/keys.go:97`, `index.Parse` as used at `cmd/mcplib/sign.go:138`.
- Produces:

```go
// httpClient is what --base fetches with; tests swap in an httptest TLS client.
var httpClient = &http.Client{Timeout: 10 * time.Minute}

// fetchLibrary writes dir/index.json, dir/index.json.sig*, and
// dir/blobs/sha256/<hex> with <hex>.sig* for every blob the index lists.
func fetchLibrary(ctx context.Context, hc *http.Client, base, dir string) error
```

**Behavior:**
- `verify --keys keys --base https://library.pellum.ai` fetches into a temp dir, then runs the existing index check and the `--blobs` check over `<tmp>/blobs/sha256`.
- `--base http://…` → usage error `--base must be an https URL`; `--base` with `--index`, `--dist` or `--blobs` → usage error naming the conflict.
- Signatures: `.sig` plus `.sig.<id>` per `signing_keys` entry; a 404 on a `.sig.<id>` is skipped, a 404 on `.sig` or a blob → error naming the URL.
- A non-200, non-404 status → error naming URL and status.

**Tests:**
- `TestVerifyBase_VerifiesAServedLibrary`: a throwaway P-256 key signs an index and one blob served over `httptest.NewTLSServer`; `cmdVerify` exits nil and prints both `verified` lines.
- `TestVerifyBase_MissingSiblingIsTolerated`: no `.sig.library-v1` served; passes on `.sig`.
- `TestVerifyBase_MissingBlobFails`: error names the blob URL.
- `TestVerifyBase_RefusesHTTP`: `http://` base is a usage error.

**Gotchas:**
- cosign's format is base64 of the ASN.1 DER signature over SHA-256; sign in the test with `crypto/ecdsa`, as `internal/sign/verify_test.go:38` does.

### Task 2: S3 helpers and the MinIO harness

**Files:**
- Create: `scripts/lib/s3.sh`, `internal/s3publish/doc.go`, `internal/s3publish/publish_integration_test.go`
- Modify: `Makefile:1,26-28`

**Interfaces:**
- Produces, sourced by Tasks 3, 4 and 6:

```bash
# put_immutable <file> <key>: PutObject --if-none-match '*'. 412 on identical
# bytes → 0; 412 on different bytes → 1, message names the key.
put_immutable() { … }
# put_mutable <file> <key>: plain PutObject.
put_mutable() { … }
# object_meta <key>: prints "<content-type>\t<cache-control>" for the key.
object_meta() { … }
```

- Makefile: `test-s3: go test -count=1 -tags=integration ./internal/s3publish/...`
- Test helper: `startMinIO(t *testing.T) (env []string)`, the `AWS_*` and `LIBRARY_BUCKET` environment the scripts run with.

**Behavior:**
- Cache: `blobs/*`, `keys/*` → `public, max-age=31536000, immutable`; `index.json*`, `releases/*` → `public, max-age=60`; else `public, max-age=300`.
- Type: blob `application/octet-stream`; `*.sig*`, `*.pub`, `releases/latest/tag` `text/plain`; `*.json` `application/json`; `*.html` `text/html; charset=utf-8`; `*.css` `text/css; charset=utf-8`.
- Both put functions set the `object_meta` headers; `LIBRARY_BUCKET` unset → exit 2 naming it.

**Tests:**
- `TestPutImmutable_IdenticalBytesSucceed`: two puts of one file exit 0.
- `TestPutImmutable_DifferentBytesFail`: second put of other bytes exits 1 and names the key.
- `TestObjectMeta_PerPath`: `head-object` after `put_mutable` on each path class matches the rules above.

**Gotchas:**
- Harness: `docker run -d -p 127.0.0.1::9000` the pinned MinIO image, `docker port` for the port, poll `/minio/health/ready`, `aws s3api create-bucket`. Skip when docker or `aws` is absent unless `MCPLIB_S3_IT_REQUIRED=1`, then fail.
- Use a MinIO release that honours `If-None-Match: *` on PutObject; `TestPutImmutable_DifferentBytesFail` proves it.
- Never set `Content-Encoding` on a blob: a client that decodes it breaks the digest.

### Task 3: Blob upload and the previous index

**Files:**
- Modify: `scripts/publish-blobs.sh:1-59`, `scripts/fetch-previous-index.sh:1-25`
- Test: `internal/s3publish/publish_integration_test.go`

**Interfaces:**
- Consumes: `put_immutable` from Task 2; `mcplib verify-blob --key k.pub --signature f.sig f` at `cmd/mcplib/sign.go:199`.
- Produces: `publish-blobs.sh <dist>`, `fetch-previous-index.sh <out>`.

**Behavior:**
- `publish-blobs.sh`: per `*.tar.gz`, `put_immutable` each `.sig*` to `blobs/sha256/<hex>.sig*`, then the tar to `blobs/sha256/<hex>`; `.cosign.bundle` is never uploaded; the index is not touched.
- A `.sig*` 412 with different bytes succeeds only when the stored signature verifies over the local tar under `keys/<id>.pub`, the unsuffixed `.sig` under the first `SIGNING_KEYS` id.
- `fetch-previous-index.sh`: `get-object index.json`; `NoSuchKey` → the empty first-publish index at `fetch-previous-index.sh:15`; any other failure → exit 1 with the CLI's message.

**Tests:**
- `TestPublishBlobs_FirstUpload`: tar and sigs land with Task 2's metadata.
- `TestPublishBlobs_IdenticalRerunSucceeds`: second run exits 0.
- `TestPublishBlobs_DifferentBytesFails`: pre-seeded different tar bytes → non-zero, output names the digest.
- `TestPublishBlobs_ResignedRerunSucceeds`: a fresh valid `.sig` over the same tar → exit 0.
- `TestFetchPreviousIndex_EmptyBucketIsFirstPublish`: output equals the first-publish document.

**Gotchas:**
- Sigs go up before the tar, so a present tar implies its sigs; a partial earlier run leaves only sigs.

### Task 4: Site assembly and `publish-site.sh`

**Files:**
- Create: `scripts/publish-site.sh`
- Modify: `scripts/assemble-site.sh:1-66`
- Test: `internal/s3publish/publish_integration_test.go`

**Interfaces:**
- Consumes: `put_immutable`, `put_mutable`, `object_meta` from Task 2.
- Produces: `publish-site.sh [--no-invalidate] <site> [release-tag]`.

**Behavior:**
- `assemble-site.sh <dist> <site>` copies `index.json`, `index.json.sig*` and `keys/*.pub`, nothing under `blobs/`, no `.nojekyll`, no ORAS call.
- Upload order: `keys/*` by `put_immutable`, then HTML, `search.json`, `style.css`, then `index.json.sig*`, then `index.json` last, all `put_mutable` with `object_meta` headers.
- With a release tag: `releases/<tag>/index.json` and sigs, then `releases/latest/index.json`, sigs, and `releases/latest/tag` holding the tag and a newline.
- Then `create-invalidation` on `LIBRARY_DISTRIBUTION_ID` for `/`, `/index.html`, `/index.json*`, `/search.json`, `/style.css`, `/servers/*`, `/releases/latest/*`; a failure exits 1 after the uploads. `--no-invalidate` skips it; only the test passes it.
- Tag not matching `^v[0-9]` → exit 2.

**Tests:**
- `TestPublishSite_SetsTypeAndCachePerPath`: `head-object` on `index.html`, `servers/grafana/index.html`, `index.json`, `keys/library-v1.pub`, `style.css` matches the Task 2 rules.
- `TestPublishSite_ReleaseWritesTagAndLatest`: tag `v0.2.0` → both release trees and `releases/latest/tag` = `v0.2.0\n`.
- `TestAssembleSite_HasNoBlobs`: no `blobs/` in the tree.

**Gotchas:**
- CloudFront caches `/` apart from `/index.html`; invalidate both.

### Task 5: `internal/site` and the keyless removal in code

**Files:**
- Modify: `internal/site/site.go:31-33`, `internal/site/templates/server.html.tmpl:71-76`, `cmd/mcplib/site.go:13-41`, `internal/sign/cosign.go:28-31`, `internal/fixture/fixture.go:1-4`
- Delete: `internal/site/budget.go`
- Test: `internal/site/site_test.go:121-147`

**Interfaces:**
- Produces: `const BaseURL = "https://library.pellum.ai"`; `mcplib site --index <f> --out <dir>` with no `--budget-mib`.

**Behavior:**
- Every server page's curl lines start `curl -fsSLO https://library.pellum.ai/`.
- The server page carries the library-key block only; no `.cosign.bundle`, `--certificate-identity-regexp` or Fulcio text.
- `mcplib site` prints `site: <out> generated for <n> packages`; `--budget-mib` is an unknown flag.
- `cosign.go` comment says the gateway verifies against a key it shipped, with no transparency log; `fixture.go` says "the published library".

**Tests:**
- `TestGenerate_UsesTheLibraryHost`: the grafana page contains `https://library.pellum.ai/blobs/sha256/`.
- `TestGenerate_HasNoKeylessBlock`: no page contains `cosign.bundle`.
- `TestBudget_*` removed with `budget.go`.

**Gotchas:**
- There are no golden files; the Testing section's "golden tests" are the substring tests in `site_test.go`.

### Task 6: Migration script

**Files:**
- Create: `scripts/migrate-ghcr-to-s3.sh`
- Test: `internal/s3publish/publish_integration_test.go`

**Interfaces:**
- Consumes: `put_immutable`, `put_mutable` from Task 2; `mcplib verify --keys keys --index <f>` at `cmd/mcplib/sign.go:121`.
- Produces: `migrate-ghcr-to-s3.sh`, env `LIBRARY_BUCKET`, `LIBRARY_OCI` defaulting to `ghcr.io/pellumai/mcp-library`.

**Behavior:**
- `oras pull $LIBRARY_OCI/index:latest`, then `mcplib verify` the pulled index against `keys/`; a failure → exit 1, nothing uploaded.
- Per unique listed `sha256`: `oras pull …/blobs:sha256-<hex>`; exactly one tar whose sha256 is `<hex>`, else exit 1 naming the digest; `put_immutable` its `.sig*` then the tar as `blobs/sha256/<hex>*`. `.cosign.bundle` is dropped.
- Last, the index and its sigs by `put_mutable` with the `index.json` metadata.
- A second run over a migrated bucket exits 0 and changes nothing.

**Tests:**
- `TestMigrate_CopiesAndIsIdempotent`: a stub `oras` on `PATH` serves a fixture registry dir; two runs exit 0; every blob and the index are present.
- `TestMigrate_RefusesAWrongDigest`: the stub serves bytes hashing elsewhere; exit non-zero naming the digest, that blob absent.

**Gotchas:**
- The owner runs it once from a laptop with `oras login ghcr.io` and bucket credentials; it is not wired into any workflow.

### Task 7: Workflows

**Files:**
- Modify: `.github/workflows/publish.yml:3-11,24-27,56-60,84-89,132-143,152-280,297-298`, `.github/workflows/ci.yml:51-57`
- Delete: `.github/workflows/pages.yml`

**Interfaces:**
- Consumes: `publish-blobs.sh <dist>`, `publish-site.sh <site> [tag]`, `assemble-site.sh`, `make test-s3`.

**Behavior:**
- `prepare`'s first step fails when any of `vars.AWS_PUBLISH_ROLE_ARN`, `LIBRARY_BUCKET`, `LIBRARY_DISTRIBUTION_ID` is empty, with `::error::<NAME> is not set; PellumAI/Infra stacks/github writes it`.
- `build`: no `id-token`, no attest step; `packages: read` stays for the build image.
- `publish`: permissions `contents: read`, `id-token: write`; `environment: release` only on a release event; `aws-actions/configure-aws-credentials` assumes `vars.AWS_PUBLISH_ROLE_ARN`, region `us-east-1`, before any download.
- `publish` steps: download, fetch previous, index and sign, verify `dist`, `publish-blobs.sh dist`, `assemble-site.sh dist site`, `mcplib site`, `publish-site.sh site "$RELEASE"`, then the `index` artefact. No ORAS, GHCR login, site artefact or anonymous-pull step.
- `release-asset` unchanged except its comment: blobs live in the bucket.
- `paths:` gains `scripts/lib/**`.
- `ci.yml` `check`: a step `MCPLIB_S3_IT_REQUIRED=1 make test-s3` after `make check`.

**Tests:**
- `make pin-check` passes on the new `uses:` lines.
- Proven at cutover step 4: one `workflow_dispatch`.

**Gotchas:**
- The role trusts `sub` `ref:refs/heads/main` or `environment:release`. Setting an environment on main pushes changes `sub` and breaks assumption, so it must be conditional.

### Task 8: Documentation

**Files:**
- Modify: `docs/PUBLISHING.md:1-97`, `docs/INDEX-FORMAT.md:9-21`, `README.md:13-24,70-80`, `docs/CURATION.md:182-184`

**Behavior:**
- `PUBLISHING.md` sections: the bucket behind CloudFront as `library_source` at `https://library.pellum.ai`; cache rules per path; immutability through `If-None-Match` and the Infra delete-deny; credentials as the OIDC role and three Actions variables; tip or release through `releases/<tag>/` and `releases/latest/`; verifying by hand with the library key only; migration and cutover as the spec's order, with `mcplib verify --keys keys --base https://library.pellum.ai` as the proof.
- `INDEX-FORMAT.md` lists `releases/<tag>/index.json`, its sigs, `releases/latest/index.json`, sigs and `tag`; the six contract paths are unchanged.
- `README.md`: default base `https://library.pellum.ai`; the GHCR-visibility bullet becomes "the three AWS variables come from PellumAI/Infra".
- `CURATION.md` drops the keyless bullet.

**Tests:**
- `grep -rn 'github.io\|cosign.bundle\|ORAS\|GHCR_PUBLIC' docs README.md internal cmd scripts .github` , run with `docs/*.md` in place of `docs`, returns only GHCR build-image lines.

## Coverage

Spec sections: Decisions; Components 2, 3, 4; Cutover order steps 1, 3, 4; Error handling; Testing except Infra and PellumStation.
