# Curating the library

## The runtime-window rule

**A package must pin a runtime line that the executor window carries.**

`EXECUTOR_TARGET.yaml` names one MCPGW build and the runtime lines that
build's executor image ships. A submission whose `runtime` is outside that
list is refused by `mcplib validate`, in CI, with a message naming both the
line asked for and the whole window — the same sentence an operator would see
at claim time as `runtime_unavailable`, deliberately, so that the failure is
recognisable in both places.

The window is two lines per runtime: the current and the previous LTS. That is
what makes this rule a constraint on a submission rather than a coupling of
this library's cadence to the executor's. A line does not vanish the day it
stops being current; it leaves one executor release after it stops being
*previous* LTS, so every package on it has a full executor release in which to
move.

### Moving the executor target

Bumping `EXECUTOR_TARGET.yaml` is a pull request of its own, never part of a
server change — CI refuses the two together — and it carries this checklist:

1. Copy the new build's `mcpgw-package.schema.json` and `runtime-window.json`
   into `contract/`, from the MCPGW release's assets once one carries them,
   and record the release in `mcpgw_release` (or the commit in
   `mcpgw_commit` until then). Update `schema_sha256` and `window_sha256` to
   the files' digests, which must equal the release's `.sha256` sidecars.
2. Diff the new window against the current `runtimes:` list and restate it.
3. For every line that *left* the window, list the packages still pinning it:
   `mcplib validate ./servers/...` after the bump prints exactly that list.
4. Every one of those packages is rebuilt against a line that is still in the
   window, as a new version. Never as a replacement of an existing digest.
5. If the schema changed, run `make check` and fix whatever the schema now
   rejects before merging.
6. Merge the bump, then the rebuilds, so `main` is never in a state where a
   published package is outside its own declared window.

A bump touches every server even though its diff touches none, so
`scripts/touched-servers.sh` then selects all of them and `packages`,
`audit` and `smoke` run the whole library against the new target. The same
holds for any change under `build/`, `internal/build/` or `internal/smoke/`:
what every package is built or smoked with has moved, and every package is
re-proved against it.

When the `MCPGW_CONTRACT_TOKEN` secret is set, CI also proves the committed
contract files are byte-identical to the ones the pinned MCPGW build
publishes; MCPGW is private, so without it CI checks the recorded digests only.

### Moving the build image

A build image bump changes the toolchain, and the toolchain is an input to
every digest. See `docs/REPRODUCIBILITY.md`. `build-image.yml` pushes the new
image and prints its digest; pasting it into `build_image` is a pull request of
its own. The procedure is the same shape as above: rebuild everything, publish
new versions, never replace a digest. The publish job enforces the last part,
because `mcplib index` refuses a changed digest for a published version.

## Reviewing a server submission

The reviewer's job is the part CI cannot do. CI proves the package builds
twice to the same digest, validates against the pinned schema, and refuses a
build step that resolves a version. The reviewer decides whether these bytes
should be signed with this library's key at all.

1. **Follow the source pin.** Open `source.commit` on the upstream repository.
   Confirm it is on a branch or tag the upstream publishes, that the repository
   is the software vendor's own, and that the commit is not a fork.
2. **Read the build steps.** They run inside the build container, but `npm ci`
   and `pip install` execute upstream install scripts, and those three
   installer steps are the ones that get the network. Whatever those scripts
   do, they do to the tree that gets signed.
3. **Read the vendored dependency set.** For node, skim the committed lockfile's
   direct dependencies for anything that has no business being there. For
   python, confirm the requirements file was compiled with `--generate-hashes`.
4. **Read the declared egress.** Every rule needs a `reason` and every reason
   has to be a sentence about what the server does, not a restatement of the
   hostname. A server that dials whatever a tool argument tells it to cannot
   have its egress declared and does not belong here.
5. **Check the credential shape.** Environment credential or per-request
   credential, one or the other, never both; the manifest validator rejects a
   manifest that sets `gateway_auth` and `upstream_auth` together. Both travel
   as request headers, so a `stdio` package leaves both null.
6. **Check the vetting block.** `vetted_on`, `vetted_by` and at least one
   primary-source URL, and those URLs actually say what the recipe claims.

Only then does the merge happen, and the merge is what signs it.

### Test deployment

`smoke` is the CI job that runs the exact tar `packages` just built — the one
publishing would sign — inside the locked-down container, and probes it over
MCP. A pass proves the package starts, negotiates a protocol version inside
the contract's supported set within `health.initialize_timeout`, lists at
least one tool with a schema that parses as JSON Schema, dials no host
outside `manifest.egress`, and exits cleanly on shutdown.

Some servers refuse `tools/list` without a real credential. `package.yaml`
may then carry `smoke: { mode: initialize-only, reason: "<why>" }`, and
`smoke` requires only `initialize` and a clean exit. This also skips the
snapshot, so the PR template asks the reviewer to confirm the stated reason
is real — it is the one place a reviewer would otherwise see the tool
surface directly.

**Snapshot drift.** `smoke` writes `servers/<name>/tools.snapshot.json` —
tool names, descriptions and schemas, sorted — and it is committed. CI fails
when a fresh smoke run produces a different file than the one in the diff, so
approving the PR always means approving the exact tool surface the snapshot
shows, never whatever a stale run happened to record.

**A package that ignores `HTTPS_PROXY`/`HTTP_PROXY` has no route out at
all** — the container's only reachable peer is the proxy sidecar, so there is
nothing else for its connections to reach. Its attempts are therefore not
egress violations the proxy log records; the proxy never sees them. `smoke`
reports the result as the process failing or timing out, not as a named
denied host, so a server that dials around the proxy fails smoke but reads
like a broken server rather than a caught escape.

## Branch protection

The merge gate itself is not something CI can enforce on `main`; a repository
owner sets it once, by hand, in **Settings → Branches → Branch protection
rules → `main`** (or with the equivalent `gh api` call below), and it applies
to everyone, including the owner.

1. **Require status checks to pass before merging**, with branches required
   to be up to date. Required checks: `check`, `curation`, `packages`
   (the reproducible-build job), `audit`, `smoke`.
2. **Require a pull request before merging**, with at least one approval from
   a CODEOWNER, and dismiss stale approvals on new commits.
3. **Do not allow bypassing the above settings**, for anyone — including
   administrators. No one merges around a red check.
4. **Squash merge only** (Settings → General → Pull Requests): disable merge
   commits and rebase merging, so `main` carries one commit per server.

The equivalent as `gh api` calls, run once by the owner:

    gh api --method PUT -H "Accept: application/vnd.github+json" \
      repos/PellumAI/mcp-library/branches/main/protection --input - <<'JSON'
    {
      "required_status_checks": {
        "strict": true,
        "contexts": ["check", "curation", "packages", "audit", "smoke"]
      },
      "enforce_admins": true,
      "required_pull_request_reviews": {
        "dismiss_stale_reviews": true,
        "require_code_owner_reviews": true,
        "required_approving_review_count": 1
      },
      "restrictions": null
    }
    JSON

    gh api --method PATCH repos/PellumAI/mcp-library \
      -F allow_squash_merge=true \
      -F allow_merge_commit=false \
      -F allow_rebase_merge=false

Because a required check that never reports blocks the merge forever, `audit`
and `smoke` stay unfiltered by path rather than gated behind a `paths:`
filter that would skip the workflow — and the check itself — on a PR that
happens not to touch `servers/`. Such a PR still runs both jobs; they print
"no servers touched" and succeed.

## Retiring a version

`mcplib index --retire <name>@<version>` removes the version from the index and
leaves the blob in place. A gateway that already pinned that digest keeps
working; a gateway browsing the catalogue stops being offered it. There is no
mechanism to unpublish bytes, deliberately: a pinned digest that stopped
resolving is an outage with no diagnosis.

## What this library never does

- Republish different bytes under a digest that already existed.
- Fetch anything at build time that is not pinned by a digest or a full commit.
- Bake a customer hostname into a manifest. `egress` interpolates
  `${param.host}` and that is the whole of the mechanism.
- Depend on the keyless attestation for verification. It is evidence for
  people. The key signature is the path for machines, because an air-gapped
  gateway has no Fulcio and no Rekor.
