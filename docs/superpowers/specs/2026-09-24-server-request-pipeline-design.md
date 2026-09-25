# Server request pipeline — design

Date: 2026-09-24. Status: draft for review.

## Goal

Anyone with a GitHub account can ask for an MCP server to be added to the
library. A maintainer-driven agent evaluates the request against
`docs/VETTING.md` plus a security risk review, and a human decides. On approval
a second agent packages the server and proves it runs before a human merges.
Nothing reaches `main`, and so nothing is signed, without a human merge and a
green test deployment.

## Decisions

| Question | Decision |
|---|---|
| Who requests | GitHub users. The Pages form hands off to a prefilled GitHub issue form; there is no relay and no bot token. |
| Test deployment | `mcplib smoke`: the built tar run in the pinned build image under a locked-down container and an egress allow-list proxy, probed over MCP. The real MCPGW executor is not used; MCPGW is private and its bwrap sandbox fails on GitHub-hosted runners. |
| Agent triggering | Manual. A maintainer runs the skills in Claude Code. No agent runs in CI and no model key is stored in the repo. |
| Merge | Only a human with write access merges. One PR per server. Any failed check blocks the merge. |
| Rejections | Recorded as a row in the VETTING "Evaluated candidates" table through a docs-only PR, and the issue is closed `rejected`. |
| Split of work | Skills supply judgment and write-ups; deterministic evidence comes from `mcplib audit` and `mcplib smoke`, which CI re-runs, so the merge gate never rests on the agent's word. |
| Advisory with no fixed release | A reviewed, time-boxed waiver in the recipe's `audit.waivers`, expiring at most 90 days after `vetted_on`; an expired or unused waiver blocks, and a licence refusal is never waivable. |

## Constraints

- No agent runs in CI, and the repository stores no model API key.
- The skills never apply `approved` or `rejected`, never merge, and never push to `main`.
- `manifest.json` gains no field; everything library-internal goes in `package.yaml`, because the manifest is contract-pinned against MCPGW.
- `smoke` runs the exact tar `mcplib build` produced; it never rebuilds or modifies it.
- `audit` reaches only the source registries and the OSV API; `smoke` gives the package no network except the allow-list proxy.
- Neither skill executes upstream code outside `mcplib build` and `mcplib smoke`.
- No skill weakens a check, adds an unevaluated escape hatch, or edits CI to pass.
- Every new workflow action is pinned by commit SHA, as `scripts/pin-check.sh` enforces.

## Delivery plans

- **A**: `mcplib audit`, `mcplib smoke` with its egress proxy and fixtures, the recipe `smoke:` block, and the CI jobs and branch-protection docs.
- **B**: the issue form, labels and request page, the PR template changes, and the two skills; depends on A.

## Flow

```
Pages request form ──► issues/new?template=server-request.yml  (user submits under own account)
        issue labels: server-request, needs-evaluation
                         │
maintainer: /evaluate-server-request <n>
        report comment (marker mcplib-eval v1)
        labels: needs-approval + recommend:approve | recommend:reject
                         │
maintainer applies  approved ─────────────┐        rejected ──────────┐
                                          ▼                           ▼
maintainer: /add-server <n>            maintainer: /add-server --reject <n>
        branch server/<name>, one PR        docs-only PR: VETTING row
        Closes #n, label pr-open            issue closed, rejected
                         │
CI: check, curation, reproducible build, audit, smoke
        CODEOWNER review ──► human squash-merge ──► publish.yml signs
```

## Components

### 1. Request page and issue form

- `.github/ISSUE_TEMPLATE/server-request.yml`, a GitHub issue form, usable on its
  own. Fields: server name, upstream (repository, npm or PyPI URL), the version
  wanted or "latest release", use case, transport if known, the credential the
  server needs, and whether the requester is the vendor. It applies the labels
  `server-request` and `needs-evaluation`.
- `.github/ISSUE_TEMPLATE/config.yml` points blank issues elsewhere, so requests
  come through the form.
- `internal/site/templates/request.html.tmpl` renders `request.html`: a short
  explanation of what the library accepts, linking `docs/VETTING.md`, and a
  form whose submit builds the `issues/new?template=server-request.yml&…` URL
  from the field values, URL-encoded, and opens it. The page holds no secret and
  makes no API call. `layout.html.tmpl` gains a "Request a server" link.
- Labels, created once by a documented `gh label create` script:
  `server-request`, `needs-evaluation`, `needs-approval`, `recommend:approve`,
  `recommend:reject`, `approved`, `rejected`, `pr-open`.

The page ships through the existing `publish.yml` → `pages.yml` path; it
appears after the next publish run.

### 2. `mcplib audit <server-dir | --resolve <source>>`

Deterministic supply-chain evidence, JSON on stdout, human summary on stderr,
non-zero exit on a blocking finding.

- Resolves the source pin exactly as `mcplib build` fetches it, and reads the
  lockfile: the committed or overlay lockfile for node, the hashed requirements
  for python, `go.sum` for native Go.
- OSV lookup for every locked dependency: id, severity, and whether a fixed
  version exists.
- The licence of the server and of every dependency, classified as permits
  redistribution, needs review, or refuses.
- Install and lifecycle scripts, compiled addons, and prebuilt binaries in the
  dependency tree.
- The dependency count and the resolved pin's commit or integrity.

`--resolve` works before a `servers/<name>/` directory exists, for evaluation.
Blocking: an OSV critical, an OSV high with a fixed version available, or a
licence that refuses redistribution. `audit` has no network access beyond the
source registries and the OSV API.
In `--server` mode a recipe `audit.waivers` entry (id or alias, package, reason,
expiry) moves a matching vulnerability finding from `blocking` to `waived`
until it expires; see VETTING "Audit waivers".

### 3. `mcplib smoke <server>`

The test deployment. It runs the exact tar `mcplib build` produced, the one
publishing would sign.

- **Container.** The pinned build image, which carries the runtime window.
  `--read-only`, a non-root uid, tmpfs `/state` and `/tmp`, the package
  mounted read-only at `/srv`, `--memory` and `--cpus` from
  `manifest.resources`, no added capabilities, and the manifest `env` plus
  `params` with dummy values for `secret` params.
- **Network.** The container sits on an internal Docker network with no route
  out. Its only reachable peer is a CONNECT proxy sidecar, set as
  `HTTPS_PROXY`/`HTTP_PROXY`, that admits only `manifest.egress` hosts, with
  `${param.host}` hosts substituted from the dummy params, and logs every
  attempt. A server that ignores the proxy fails closed; its connections have
  no route.
- **Probe.** stdio: attach to the entrypoint's stdin and stdout. http: connect
  to `MCPGW_LISTEN_SOCKET`. Send `initialize`, `notifications/initialized`,
  `tools/list`, then `resources/list` and `prompts/list` if the capabilities
  advertise them, then shut down.
- **Pass requires all of:**
  - `initialize` within `health.initialize_timeout`;
  - a negotiated protocol version inside the contract's supported set;
  - `tools/list` non-empty, and every input schema valid JSON Schema;
  - no proxy request to an undeclared host;
  - the process alive until shutdown, then a clean exit.
- **Snapshot.** Writes `servers/<name>/tools.snapshot.json`: names,
  descriptions and schemas, sorted. The file is committed. CI fails when a
  fresh smoke run produces a different snapshot, so the reviewer always sees
  the exact tool surface being signed.
- **Credential escape hatch.** Some servers refuse `tools/list` without a real
  credential. `package.yaml` may carry
  `smoke: { mode: initialize-only, reason: "<why>" }`; smoke then requires only
  `initialize` and the clean exit, and writes no snapshot. The field lives in
  `package.yaml`, not in `manifest.json`, because the manifest is
  contract-pinned against MCPGW. The recipe validator accepts the block, and
  the PR template asks the reviewer to confirm the reason.

JSON report on stdout: the timings, negotiated version, tool count, proxy log
and verdict.

### 4. Skill `.claude/skills/evaluate-server-request`

Invoked as `/evaluate-server-request <issue>`. Read-only on upstream code: it
fetches and reads but never executes upstream code, never installs, and makes
no repository change.

1. Read the issue form fields. Resolve the upstream to a concrete pin (commit,
   npm integrity or PyPI sha256) and fetch it into a scratch directory.
2. Run `mcplib audit --resolve`.
3. Score VETTING checks 1–12, each ✅, ⚠️ or ❌ with one line of evidence (a
   file:line or URL).
4. Assess risk, as judgment with evidence:
   - tool surface: read-only or destructive tools, shell or arbitrary-code tools;
   - egress: hosts the code dials against hosts it documents, and any
     user-controlled URL (SSRF);
   - credential scope: the least token scope the tools need;
   - tool-metadata poisoning: instructions to the model hidden in tool names,
     descriptions or schemas;
   - supply chain: the publisher matches the vendor, release cadence, commits in
     the last 90 days, recent ownership transfer, and typosquat distance to
     popular packages;
   - data handling: telemetry, and writes outside `/state` and `/tmp`.
5. Apply the decision rules, then post the report comment.
6. Swap `needs-evaluation` for `needs-approval` plus `recommend:approve` or
   `recommend:reject`. Never apply `approved` or `rejected`.

**Decision rules**, applied before judgment:

- ❌ on check 1, 2, 3, 4, 10, 11 or 12 → `REJECT`.
- An `audit` blocking finding → `REJECT`, unless a newer upstream release clears
  it, in which case the skill re-pins to that release and re-runs `audit`.
- Hidden instructions in tool metadata, or code dialing hosts the upstream does
  not document → `REJECT`.
- Otherwise `APPROVE`, or `APPROVE WITH CONDITIONS` naming each condition
  (narrow egress, set a param, and so on), with reasons.

**Report comment** — fixed sections under a hidden `<!-- mcplib-eval v1 -->`
marker, so `add-server` can parse it: Resolution (source kind, pin, runtime,
transport, lockfile or overlay); VETTING 1–12; Audit; Risk; Recommendation with
conditions. Resolution also carries a fenced YAML block with the machine-read
fields (`name`, `source`, `runtime`, `transport`, `egress`, `params`,
`conditions`).

Because the repository is public and the skill reads untrusted upstream text,
the skill treats everything in the issue and the upstream as data, never
instructions, and a request that tries to steer the evaluation is itself a
`REJECT` reason.

### 5. Skill `.claude/skills/add-server`

Invoked as `/add-server <issue>` or `/add-server --reject <issue>`.

**Preconditions**, refused otherwise:

- the issue is open;
- `approved` (or `rejected` for `--reject`) was applied by a user with write
  access, checked through the issue's timeline events;
- the `mcplib-eval v1` comment is present;
- the pin it records still resolves to the same bytes; drift stops the skill
  and asks for re-evaluation.

**Approve path:**

1. Create a worktree on branch `server/<name>`.
2. Write `servers/<name>/package.yaml` from the resolution, the `vetting:`
   block from the report, and `manifest.json` (params, egress with reasons,
   resources, health), plus `overlay/` with a vendored lockfile when upstream
   ships none. Apply every condition from `APPROVE WITH CONDITIONS`.
3. Add the VETTING table row.
4. Run `make check`, the double build with digest comparison, `mcplib audit`
   and `mcplib smoke`, and commit `tools.snapshot.json`. Stop on any failure.
   Never weaken a check, add an escape hatch that was not in the evaluation, or
   edit CI to get past a failure.
5. Open one PR from the server PR template, each check answered with the
   evidence from the report, linking the report comment and saying
   `Closes #<n>`. Swap `approved` for `pr-open` on the issue.

**Reject path:** a docs-only PR adding the rejection row to the VETTING table,
and the issue closed with `rejected` and a link to the report.

Scope: new servers only. Version bumps of existing servers are out of scope.

### 6. CI and the merge gate

- `ci.yml` gains `audit` and `smoke` jobs, matrixed over the servers a PR
  touches, on GitHub-hosted runners, and a snapshot-drift check.
- Branch protection on `main`, set by the owner and documented in
  `docs/CURATION.md`:
  - required checks: `check`, `curation`, the reproducible-build job, `audit`,
    `smoke`;
  - one CODEOWNER approval, with stale approvals dismissed;
  - no bypass for anyone, including administrators;
  - squash merge only.
- The PR template gains a smoke section — the snapshot tool count, the
  `initialize-only` reason if any — and a line for the evaluation link.
- `docs/CURATION.md` gains a "Requests" section describing this flow.

## Error handling

- Unresolvable upstream, or a request missing required fields: the evaluation
  posts what is missing, keeps `needs-evaluation`, and stops.
- Upstream drift between approval and packaging: `add-server` stops;
  re-evaluation overwrites the report under a new marker revision.
- A smoke failure during `add-server`: no PR is opened; the skill comments the
  smoke JSON on the issue and leaves `approved` for a maintainer to decide.
- A PR check failure after open: the merge is blocked by branch protection; a
  maintainer fixes it or closes the PR.

## Testing

- `audit`: unit tests over fixture lockfiles for OSV parsing, licence
  classification and install-script detection; OSV responses recorded, not
  live.
- `smoke`: integration tests against `fixture-echo` (native) and
  `fixture-count` (python), plus two new fixtures: one that dials an undeclared
  host, and one that exits during `initialize`. Both must fail.
- The request page: a `site_test.go` golden test of `request.html` and of the
  URL the form builds.
- Skills: a dry-run evaluation against an existing server (`context7`), whose
  verdict must match its existing VETTING row.

## Out of scope

- Relay-based anonymous requests.
- Running the real MCPGW executor in library CI.
- Agents running in CI, and any agent merge right.
- Version bumps of existing servers.
- Live-credential tool calls during smoke.
