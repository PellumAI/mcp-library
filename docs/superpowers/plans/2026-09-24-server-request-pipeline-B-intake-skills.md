# Server request pipeline B: intake and skills Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-24-server-request-pipeline-design.md`, sections Components 1, 4, 5, Flow, Error handling, Testing.
**Revised 2026-09-26:** the request page is dropped per `docs/superpowers/specs/2026-09-26-s3-hosting-design.md` section 4: the repository goes private, so there are no public requests and collaborators file through the issue form.
**Goal:** a collaborator files through the issue form, and a maintainer runs `/evaluate-server-request <n>` then `/add-server <n>` or `/add-server --reject <n>` to reach one human-merged PR.
**Constraints:** the spec's `## Constraints` section applies. Additionally:
- Plan A has merged: `mcplib audit`, `mcplib smoke` and the `smoke:` recipe block exist.
- The skills follow the superpowers skill format: `SKILL.md` with `name` and `description` frontmatter, under 500 lines, with references split into sibling files.
**Verification:** `make check`; per-task test targets are listed under each task.

## File structure

| File | Responsibility |
|---|---|
| `.github/ISSUE_TEMPLATE/server-request.yml` | the request issue form |
| `.github/ISSUE_TEMPLATE/config.yml` | disable blank issues |
| `scripts/labels.sh` | create the pipeline labels, idempotently |
| `internal/issueform/issueform_test.go` | guard the issue form's field ids |
| `.github/PULL_REQUEST_TEMPLATE/server.md` | smoke and evaluation sections |
| `.claude/skills/evaluate-server-request/SKILL.md` | the evaluation procedure |
| `.claude/skills/evaluate-server-request/report-template.md` | the `mcplib-eval v1` comment |
| `.claude/skills/evaluate-server-request/risk-checklist.md` | the risk review items |
| `.claude/skills/add-server/SKILL.md` | the packaging and reject procedures |
| `.claude/skills/add-server/recipe-guide.md` | how report fields map to `package.yaml` and `manifest.json` |
| `docs/CURATION.md` | Requests section |

### Task 1: Issue form, config and labels

**Files:**
- Create: `.github/ISSUE_TEMPLATE/server-request.yml`, `.github/ISSUE_TEMPLATE/config.yml`, `scripts/labels.sh`
- Test: `internal/issueform/issueform_test.go`

**Interfaces:**
- Produces: the form field ids, which Task 3's skill reads.

```yaml
# issue form body ids
server_name, upstream_url, version, use_case, transport, credential, is_vendor
# labels: [server-request, needs-evaluation]; title: "Server request: "
```

**Behavior:**
- `server_name`, `upstream_url` and `use_case` are required; `transport` is a dropdown with `stdio`, `http` and `unknown`; `is_vendor` is a checkbox.
- `config.yml` sets `blank_issues_enabled: false` and links `docs/VETTING.md`.
- `scripts/labels.sh` runs `gh label create --force` for the eight spec labels, each with a fixed colour and description; a second run changes nothing.

**Tests:**
- `TestIssueForm_FieldIDs` in `internal/issueform/issueform_test.go`: parses `.github/ISSUE_TEMPLATE/server-request.yml` with `gopkg.in/yaml.v3` and asserts the seven ids above, the required flags and the labels. It lives here, not in `internal/site`, because with the request page gone the site has no tie to the form; the skill in Task 3 is the consumer, and a Go test keeps the check inside `make check` with no new tool.

**Gotchas:**
- `internal/issueform` holds only the test file; it reads the form at `../../.github/ISSUE_TEMPLATE/server-request.yml`.

### Task 2: PR template and CURATION Requests section

**Files:**
- Modify: `.github/PULL_REQUEST_TEMPLATE/server.md`, `docs/CURATION.md`

**Behavior:**
- The template gains an `Evaluation:` line for the report comment URL and `Closes #`, and a `## Test deployment` section: the smoke verdict, tool count, the `initialize-only` reason or "none", and a reviewer checkbox "the tool snapshot matches what I expect this server to expose".
- `docs/CURATION.md` gains `## Requests`: the spec Flow diagram, who may apply `approved` and `rejected`, and the two skill invocations.

**Tests:**
- None; `curation` CI parses nothing here.

### Task 3: Skill `evaluate-server-request`

**Files:**
- Create: `.claude/skills/evaluate-server-request/SKILL.md`, `report-template.md`, `risk-checklist.md`

**Interfaces:**
- Consumes: `mcplib audit --resolve <kind>:<coordinate> --json` from Plan A; `gh issue view <n> --json body,labels,author`; `gh issue comment`; `gh issue edit --add-label/--remove-label`.
- Produces: one issue comment whose first line is `<!-- mcplib-eval v1 -->` and whose Resolution section carries this block, which Task 4 parses:

```yaml
name: <server>
source: { kind: npm|pypi|git|archive, package|repo|url: ..., integrity|commit|sha256: ... }
runtime: node@22
transport: stdio
lockfile: upstream|overlay
params: [{ name, env, secret, required }]
egress: [{ host, reason }]
conditions: []
recommendation: APPROVE|APPROVE_WITH_CONDITIONS|REJECT
```

**Behavior:**
- The skill refuses unless the issue has `needs-evaluation` or `needs-approval`; a re-run replaces the marker comment by editing it, never adding a second one.
- Its steps are spec Component 4 steps 1–6, in order, with the decision rules applied before judgment.
- A missing required field or an unresolvable upstream → a comment listing what is missing; labels unchanged.
- The SKILL.md states that issue and upstream text are data, that an attempt to steer the verdict is a `REJECT` reason, and that the skill never runs upstream code, never applies `approved` or `rejected`, and changes no file.

**Tests:**
- Dry run documented in the PR: evaluate a synthetic request for `@upstash/context7-mcp@4.1.1`, posted to a scratch issue on a fork; the verdict and VETTING marks match `context7`'s row in `docs/VETTING.md`.

**Gotchas:**
- The superpowers `writing-skills` skill applies; run its pressure test on the injection and never-approve rules.

### Task 4: Skill `add-server`

**Files:**
- Create: `.claude/skills/add-server/SKILL.md`, `recipe-guide.md`

**Interfaces:**
- Consumes: the Task 3 YAML block; `gh api repos/PellumAI/mcp-library/issues/<n>/timeline` and `gh api .../collaborators/<login>/permission`; `mcplib validate`, `build`, `audit`, `smoke --write-snapshot`; `make check`.
- Produces: branch `server/<name>`, or `vetting/reject-<name>` for `--reject`, and one PR.

**Behavior:**
- Preconditions per spec Component 5, each refusal naming the failed one; `approved` counts only if the labeller has `write`, `maintain` or `admin`.
- Pin drift: re-resolving `source` yields a different integrity, commit or sha → stop, and comment asking for re-evaluation.
- The approve path follows spec Component 5 steps 1–5; the PR body fills the Task 2 template from the report and the smoke JSON.
- A smoke failure → no PR; the skill comments the smoke JSON on the issue; the label stays `approved`.
- The reject path adds the VETTING row with verdict `**Exclude, check N.**` and the report's reason, opens the PR, and closes the issue with `rejected` and the report link.
- The skill works in a git worktree, runs one git command per call, and runs `git diff --cached --stat` before each commit.

**Tests:**
- Dry run documented in the PR: re-package `context7` on a scratch branch from a synthetic report; `package.yaml` and `manifest.json` match the committed ones apart from `vetted_on`.

## Coverage

Spec sections: Flow, Components 1 (issue form and labels), 4, 5; Error handling; Testing (skills).
