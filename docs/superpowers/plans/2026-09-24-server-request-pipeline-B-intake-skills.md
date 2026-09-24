# Server request pipeline B: intake and skills Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-24-server-request-pipeline-design.md`, sections Components 1, 4, 5, Flow, Error handling, Testing.
**Goal:** a requester files through the Pages form or the issue form, and a maintainer runs `/evaluate-server-request <n>` then `/add-server <n>` or `/add-server --reject <n>` to reach one human-merged PR.
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
| `internal/site/templates/request.html.tmpl` | the request page |
| `internal/site/templates/layout.html.tmpl` | nav link to the request page |
| `internal/site/site.go` | render `request.html` |
| `internal/site/static/request.js` | build the prefilled issue URL |
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

**Interfaces:**
- Produces: the form field ids, which Task 2's URL builder and Task 4's skill read.

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
- `TestIssueForm_FieldIDs` in `internal/site/site_test.go`: parses the YAML and asserts the seven ids above, so Task 2 cannot drift.

**Gotchas:**
- GitHub prefills issue-form fields only from query keys equal to the field `id`.

### Task 2: Request page

**Files:**
- Create: `internal/site/templates/request.html.tmpl`, `internal/site/static/request.js`
- Modify: `internal/site/site.go:25-30,112-164`, `internal/site/templates/layout.html.tmpl`
- Test: `internal/site/site_test.go`

**Interfaces:**
- Consumes: `render(path, name string, data pageData) error` at `internal/site/site.go:166`, the Task 1 field ids.
- Produces: `request.html` and `request.js` in the site root; `const IssueRepo = "PellumAI/mcp-library"`.

**Behavior:**
- The page lists what the library accepts, in three bullets linking `docs/VETTING.md`, then a form with the Task 1 fields.
- Submit opens `https://github.com/PellumAI/mcp-library/issues/new?template=server-request.yml&title=Server+request%3A+<name>&<id>=<value>…`, with every value `encodeURIComponent`-encoded, in the same tab.
- An empty required field blocks submit using native `required` validation; the script makes no network call.
- Every page's nav gains "Request a server".

**Tests:**
- `TestGenerate_RequestPage`: `request.html` exists, links `request.js` and contains an input for each field id.
- `TestGenerate_NavLink`: `index.html` and a server page link `request.html` with the right `Root` prefix.

**Gotchas:**
- The site is embedded through `//go:embed` at `site.go:25-28`; add `static/request.js` to the embed pattern, or `Generate` writes nothing.
- Keep the page script-light like `index.html.tmpl`'s inline search; no framework.

### Task 3: PR template and CURATION Requests section

**Files:**
- Modify: `.github/PULL_REQUEST_TEMPLATE/server.md`, `docs/CURATION.md`

**Behavior:**
- The template gains an `Evaluation:` line for the report comment URL and `Closes #`, and a `## Test deployment` section: the smoke verdict, tool count, the `initialize-only` reason or "none", and a reviewer checkbox "the tool snapshot matches what I expect this server to expose".
- `docs/CURATION.md` gains `## Requests`: the spec Flow diagram, who may apply `approved` and `rejected`, and the two skill invocations.

**Tests:**
- None; `curation` CI parses nothing here.

### Task 4: Skill `evaluate-server-request`

**Files:**
- Create: `.claude/skills/evaluate-server-request/SKILL.md`, `report-template.md`, `risk-checklist.md`

**Interfaces:**
- Consumes: `mcplib audit --resolve <kind>:<coordinate> --json` from Plan A; `gh issue view <n> --json body,labels,author`; `gh issue comment`; `gh issue edit --add-label/--remove-label`.
- Produces: one issue comment whose first line is `<!-- mcplib-eval v1 -->` and whose Resolution section carries this block, which Task 5 parses:

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

### Task 5: Skill `add-server`

**Files:**
- Create: `.claude/skills/add-server/SKILL.md`, `recipe-guide.md`

**Interfaces:**
- Consumes: the Task 4 YAML block; `gh api repos/PellumAI/mcp-library/issues/<n>/timeline` and `gh api .../collaborators/<login>/permission`; `mcplib validate`, `build`, `audit`, `smoke --write-snapshot`; `make check`.
- Produces: branch `server/<name>`, or `vetting/reject-<name>` for `--reject`, and one PR.

**Behavior:**
- Preconditions per spec Component 5, each refusal naming the failed one; `approved` counts only if the labeller has `write`, `maintain` or `admin`.
- Pin drift: re-resolving `source` yields a different integrity, commit or sha → stop, and comment asking for re-evaluation.
- The approve path follows spec Component 5 steps 1–5; the PR body fills the Task 3 template from the report and the smoke JSON.
- A smoke failure → no PR; the skill comments the smoke JSON on the issue; the label stays `approved`.
- The reject path adds the VETTING row with verdict `**Exclude, check N.**` and the report's reason, opens the PR, and closes the issue with `rejected` and the report link.
- The skill works in a git worktree, runs one git command per call, and runs `git diff --cached --stat` before each commit.

**Tests:**
- Dry run documented in the PR: re-package `context7` on a scratch branch from a synthetic report; `package.yaml` and `manifest.json` match the committed ones apart from `vetted_on`.

## Coverage

Spec sections: Flow, Components 1, 4, 5; Error handling; Testing (request page, skills).
