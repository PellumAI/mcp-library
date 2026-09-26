# Library hosting on S3 and a private repository — design

Date: 2026-09-26. Status: approved in discussion, written for review.

## Goal

The library source repository becomes private. The catalogue site and every
published artefact move from GitHub Pages and GHCR to one public S3 bucket
behind CloudFront at `https://library.pellum.ai`. Gateways keep fetching the
same unauthenticated static paths; only the base URL changes.

## Decisions

| Question | Decision |
|---|---|
| Who can fetch the index and packages | Anyone. Integrity comes from the library signatures a gateway verifies offline, not from access control |
| Origin | One private S3 bucket, reachable only through CloudFront with origin access control |
| Hostname | `library.pellum.ai`, DNS hosted outside AWS: the ACM validation record and the CNAME to CloudFront are added by hand once |
| Provisioning | The `PellumAI/Infra` repository, stack `library` in the `prod` account, applied by its manual `apply` workflow; see Infra's `docs/superpowers/specs/2026-09-26-infra-repo-design.md` |
| CI credentials | GitHub Actions OIDC into one narrow publish role; no long-lived AWS keys anywhere |
| GHCR | Stops being a library store. It keeps only the pinned build image, private, pulled in CI with `GITHUB_TOKEN` |
| Keyless attestations | Dropped. The gateway never checks Rekor, and every entry permanently publishes the repository and workflow name |
| Public server requests | Dropped. The request page goes; collaborators request through the private repository's issue form |
| Transition | None: no customers yet, so the old Pages URL is not kept alive |

## Constraints

- The published path layout in `docs/INDEX-FORMAT.md` does not change, so a
  gateway needs a base-URL change and nothing else.
- A published blob is immutable. Nothing — CI, the publish role, a re-run —
  can overwrite or delete an object under `blobs/`.
- Only the `publish.yml` workflow on `refs/heads/main`, or on a release, can
  assume the publish role. Pull-request workflows get no AWS access.
- The publish role can put objects in this bucket and create invalidations on
  this distribution; nothing else. It cannot delete, change bucket policy, or
  touch any other resource.
- Every object is served over HTTPS only, TLS 1.2 minimum.
- Library signing is unchanged: the same ECDSA library key, `.sig` and
  `.sig.<key id>` files, verified by the gateway against embedded keys.
- The library signing key never leaves this repository's secrets; the Infra
  repository never holds it.

## Components

### 1. Infrastructure, provided by `PellumAI/Infra`

This repository holds no Terraform. Infra's `stacks/library` provides the
resources below, and Infra's `stacks/github` writes their coordinates into
this repository's Actions variables `AWS_PUBLISH_ROLE_ARN`, `LIBRARY_BUCKET`
and `LIBRARY_DISTRIBUTION_ID`. The requirements this repository places on
that stack:

  - **Bucket** `pellum-mcp-library` (name is a variable): versioning on, all
    public access blocked, default SSE-S3 encryption, object ownership
    enforced. A bucket policy grants `s3:GetObject` to the CloudFront
    distribution only, through OAC, and denies non-TLS requests.
  - **Immutability**: a bucket policy statement denies `s3:DeleteObject` and
    `s3:DeleteObjectVersion` under `blobs/*` to every principal except the ARNs in the
    `break_glass_principals` variable, which defaults to empty. Object overwrite is prevented at write time by
    `If-None-Match: *`.
  - **Certificate**: ACM in `us-east-1` for `library.pellum.ai`, DNS
    validated. Terraform outputs the validation record for the owner to add.
  - **Distribution**: alias `library.pellum.ai`, default root object
    `index.html`, HTTP redirected to HTTPS, TLS 1.2 minimum, compression on,
    price class 100. A CloudFront function maps `/<dir>/` to
    `/<dir>/index.html` so server pages resolve.
  - **Cache policies**:

    | Path | TTL |
    |---|---|
    | `blobs/*`, `keys/*` | 1 year, immutable |
    | `index.json*`, `releases/*` | 60 s, and invalidated on publish |
    | everything else (HTML, `search.json`, CSS) | 5 min, and invalidated on publish |

  - **Publish role**: a role whose trust policy requires `aud=sts.amazonaws.com` and
    `sub` matching `repo:PellumAI/mcp-library:ref:refs/heads/main` or the
    release environment. Its policy allows `s3:PutObject`, `s3:GetObject` and
    `s3:ListBucket` on the bucket, and `cloudfront:CreateInvalidation` on the
    distribution.
  - **Outputs**: the distribution domain for the CNAME, the ACM validation
    record, the role ARN, the bucket name and the distribution id.

### 2. Publish pipeline

- `publish.yml` gains `id-token: write` for AWS only, assumes the role with
  `aws-actions/configure-aws-credentials`, and loses the ORAS, GHCR-push,
  GHCR-public and keyless `cosign sign-blob --bundle` steps. Library-key
  signing with cosign stays.
- `scripts/publish-blobs.sh` uploads each new blob and its `.sig*` files with
  `aws s3api put-object --if-none-match '*'`. A `412 Precondition Failed` on
  an object whose existing bytes match the local sha256 is success, since a
  re-run is allowed; any other existing object is a failure.
- `scripts/fetch-previous-index.sh` reads `index.json` and its signatures
  from the bucket, and treats a missing index as the first publish.
- `scripts/assemble-site.sh` stops re-pulling old blobs. It builds only the
  site pages, `search.json`, `style.css`, `keys/`, and the signed index.
- A new `scripts/publish-site.sh` uploads those files with the right
  `Content-Type` and `Cache-Control`, then invalidates `/index.json*`,
  `/search.json`, `/index.html` and `/servers/*`.
- On a release, the signed index is also written to
  `releases/<tag>/index.json` and its signatures, and to
  `releases/latest/index.json`. The GitHub release keeps its index asset for
  maintainers.
- `pages.yml` is deleted. `internal/site`: `BaseURL` becomes
  `https://library.pellum.ai`, and `budget.go` and its flag go.
- `docs/PUBLISHING.md` and `docs/INDEX-FORMAT.md` describe S3, CloudFront and
  the release paths. `keys/README.md` drops the Rekor paragraph.

### 3. Migration

- `scripts/migrate-ghcr-to-s3.sh`, run once by the owner with ORAS logged in
  to GHCR and AWS credentials for the bucket. It copies every blob the
  current index lists, with its `.sig*` files, from
  `ghcr.io/pellumai/mcp-library/blobs:sha256-<hex>` to `blobs/sha256/<hex>*`,
  checking each sha256 before upload. It then uploads the current signed
  index. It is idempotent.
- After migration: one `workflow_dispatch` of `publish.yml`, then
  `mcplib verify` over the index and every blob it lists, fetched from
  `https://library.pellum.ai`. `verify` gains a `--base <url>` mode if it
  does not already read over HTTPS.

### 4. Repository changes that follow privacy

- `.github/ISSUE_TEMPLATE/` keeps the server-request form (Plan B), now for
  collaborators. Plan B's request-page task is dropped, and its plan is
  revised to match.
- `docs/CURATION.md` and `README.md` drop links to the public Pages site and
  point to `library.pellum.ai`.
- `fixture.yml` is unchanged: it builds in CI and uploads an artefact, which
  maintainers with repository access download.

### 5. Gateway, PellumStation

- `snapshot/pinned.json` `source` becomes `https://library.pellum.ai`.
- `scripts/library-snapshot-refresh.sh` reads
  `https://library.pellum.ai/releases/latest/index.json` and its signatures,
  or `releases/<tag>/`, instead of the GitHub releases API.
- Comments corrected: `internal/mcplibrary/settingskeys.go` on
  `PinnedSource`, and `internal/mcpcatalog/library/library.go` on why the
  verifier is copied.
- Check that the client accepts a `Content-Encoding: gzip` or `br` response
  for `index.json` under `maxIndexBytes`. If it does not, the distribution
  excludes `index.json*` from compression.

### Cutover order

1. Merge the mcp-library changes with the publish job disabled by a missing
   role variable; the job fails fast and names the variable.
2. Infra's order of first use steps 1–4: bootstrap, the library stack with the
   two DNS records, and the github stack, which writes this repository's
   variables.
3. The owner runs `migrate-ghcr-to-s3.sh`.
4. `workflow_dispatch` `publish.yml`, then verify from the new URL.
5. Merge the PellumStation change, and refresh its snapshot from
   `releases/latest`.
6. An Infra PR sets `mcp_library_visibility = private` and is applied. Unpublish the Pages site, and delete the GHCR
   `blobs` and `index` packages. The `build` image package stays.

## Error handling

- A missing role variable or a failed role assumption stops the publish job
  before it builds anything, with the variable name in the message.
- A blob upload conflict with different bytes fails the job and names the
  digest. This should never happen, and it means something wrote to `blobs/`
  outside the pipeline.
- A failed invalidation fails the job after the upload. The objects are
  correct, and the next publish or a re-run invalidates again.
- A verify failure after migration stops the cutover before the gateway
  change.

## Testing

- Infrastructure is tested in `PellumAI/Infra`.
- `publish-blobs.sh` and `publish-site.sh` are tested against a local S3
  (MinIO in a container) in an integration test:
  - first upload;
  - an identical re-upload succeeds;
  - a different-bytes conflict fails;
  - `Content-Type` and `Cache-Control` are set per path.
- `internal/site` golden tests move to the new base URL.
- The end-to-end check is the post-migration `mcplib verify` over the live
  URL.
- In PellumStation, a `library-snapshot-refresh.sh` test against a fixture
  HTTP server serving the `releases/latest/` layout.

## Out of scope

- Authenticated or customer-only access to packages.
- A second region, or failover.
- Moving the build image to ECR.
- Redirecting the old Pages URL.

## Delivery plans

- **C**: mcp-library — the S3 publish pipeline, the migration
  script, the site and docs changes, and the removal of Pages, ORAS and
  keyless attestation.
- **D**: PellumStation — `pinned.json`, the snapshot refresh script, and the
  comment fixes; depends on C's cutover step 4.
- The AWS and GitHub resources are Infra's Plans A and B, which precede C's
  cutover.
