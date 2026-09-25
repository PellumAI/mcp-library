# Server request pipeline A: audit and smoke Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-24-server-request-pipeline-design.md`, sections Components 2, 3 and 6, Error handling, Testing.
**Goal:** `mcplib audit` and `mcplib smoke` exist, run on every server PR, and branch protection documented in `docs/CURATION.md` makes them block the merge.
**Constraints:** the spec's `## Constraints` section applies. Additionally:
- The repo has no coder brief; the suite is `make check` plus `go test -tags=integration ./internal/smoke/...` where Docker is available.
- New packages follow `cmd/mcplib`'s command pattern: `register` in `init`, exit 1 for a detected failure, `errUsage` for 2.
**Verification:** `make check`; per-task test targets are listed under each task.

## File structure

| File | Responsibility |
|---|---|
| `internal/recipe/recipe.go` | `Smoke` block on `Recipe` |
| `internal/recipe/validate.go` | validate the `smoke` block |
| `internal/manifest/runtime.go` | decode the manifest fields smoke needs |
| `internal/audit/lock.go` | read locked dependencies from npm, python and Go lockfiles |
| `internal/audit/osv.go` | OSV batch query client |
| `internal/audit/license.go` | licence classification |
| `internal/audit/audit.go` | `Run`, the report, blocking rules |
| `cmd/mcplib/audit.go` | `mcplib audit` |
| `internal/smoke/proxy.go` | CONNECT allow-list proxy with a JSON-lines log |
| `cmd/mcplib/egressproxy.go` | hidden `mcplib egress-proxy` |
| `internal/smoke/container.go` | docker network, proxy and package container lifecycle |
| `internal/smoke/probe.go` | MCP client over stdio or unix socket |
| `internal/smoke/smoke.go` | `Run`, pass rules, snapshot |
| `cmd/mcplib/smoke.go` | `mcplib smoke` |
| `internal/fixture/servers/fixture-dialer/` | fixture that dials an undeclared host |
| `internal/fixture/servers/fixture-crash/` | fixture that exits during `initialize` |
| `servers/*/tools.snapshot.json` | committed snapshot per existing server |
| `.github/workflows/ci.yml` | `audit`, `smoke`, snapshot-drift jobs |
| `docs/CURATION.md` | Requests section, branch protection steps |

### Task 1: Recipe `smoke` block and manifest runtime view

**Files:**
- Modify: `internal/recipe/recipe.go:30-40`, `internal/recipe/validate.go:114-175`
- Create: `internal/manifest/runtime.go`
- Test: `internal/recipe/validate_test.go`, `internal/manifest/runtime_test.go`

**Interfaces:**
- Consumes: `manifest.Parse(raw []byte) (Doc, error)` at `internal/manifest/manifest.go:48`.
- Produces:

```go
type Smoke struct {
	Mode   string `yaml:"mode"`   // "" (full) or "initialize-only"
	Reason string `yaml:"reason"`
}
// Recipe gains: Smoke Smoke `yaml:"smoke"`
type Runtime struct {
	Params    []Param // Name, Env, Secret, Required
	Egress    []Egress // Host, Reason
	Resources struct{ CPUMax, MemoryMax string; PidsMax int }
	InitializeTimeout time.Duration
}
func ParseRuntime(raw []byte) (Runtime, error)
```

**Behavior:**
- `mode: initialize-only` with a non-empty reason → valid; an empty reason → error naming `smoke.reason`; any other mode → error.
- An absent `smoke` block → full mode.
- `ParseRuntime` on `servers/context7/manifest.json` → one secret param `CONTEXT7_API_KEY`, egress `context7.com`, timeout 20s.

**Tests:**
- `TestValidate_SmokeBlock`: the three cases above.
- `TestParseRuntime_Context7`: the values above.
- `TestParseRuntime_BadTimeout`: `"health":{"initialize_timeout":"soon"}` → error.

**Gotchas:**
- `recipe.Load` decodes strictly; the new field must exist before any recipe uses it.

### Task 2: Lockfile readers

**Files:**
- Create: `internal/audit/lock.go`
- Test: `internal/audit/lock_test.go`, `internal/audit/testdata/`

**Interfaces:**
- Produces: `type Dep struct{ Ecosystem, Name, Version string; InstallScript bool }`, `ReadLock(path string) ([]Dep, error)`.

**Behavior:**
- `package-lock.json` v3 → one `Dep` per `packages` entry except the root, ecosystem `npm`, `InstallScript` from `hasInstallScript`.
- `requirements.txt` with `--hash` lines → ecosystem `PyPI`, `name==version` pairs; an unpinned line → error.
- `go.sum` → ecosystem `Go`, one `Dep` per module version, `/go.mod` lines skipped.
- An unknown file name → error naming it.

**Tests:**
- `TestReadLock_NPM`: `servers/context7/overlay/package-lock.json` yields 107 deps, the server itself among them; the root entry `""` is skipped.
- `TestReadLock_Requirements`, `TestReadLock_GoSum`, `TestReadLock_Unknown`.

### Task 3: OSV client and licence classifier

**Files:**
- Create: `internal/audit/osv.go`, `internal/audit/license.go`
- Test: `internal/audit/osv_test.go`, `internal/audit/license_test.go`

**Interfaces:**
- Produces: `type Vuln struct{ ID, Severity string; Fixed []string; Dep Dep }`, `type OSV struct{ BaseURL string; HTTP *http.Client }`, `(OSV) Query(ctx context.Context, deps []Dep) ([]Vuln, error)`, `ClassifyLicense(spdx string) LicenseClass` with `LicensePermits|LicenseReview|LicenseRefuses`.

**Behavior:**
- Query uses `POST /v1/querybatch` in chunks of 1000, then `GET /v1/vulns/{id}` for severity; severity is the CVSS v3 band from `severity[]`, or `database_specific.severity` if absent, or `UNKNOWN`.
- MIT, Apache-2.0, BSD-2/3-Clause, ISC, MPL-2.0 → permits; GPL, AGPL, SSPL, BUSL, FSL, `UNLICENSED` → refuses; empty or anything else → review.

**Tests:**
- `TestOSVQuery_Recorded`: an `httptest` server replaying `testdata/osv/*.json` returns one HIGH with a fix.
- `TestClassifyLicense`: a table covering each class, including `Apache-2.0 OR MIT`, which permits.

### Task 4: `mcplib audit`

**Files:**
- Create: `internal/audit/audit.go`, `cmd/mcplib/audit.go`
- Test: `internal/audit/audit_test.go`, `cmd/mcplib/audit_test.go`

**Interfaces:**
- Consumes: `build.FetchSource(ctx, src recipe.Source, dir string) error` at `internal/build/fetch.go:45`, `recipe.Load` at `internal/recipe/recipe.go:113`, Tasks 2 and 3.
- Produces: `Run(ctx context.Context, in Input) (Report, error)`; `mcplib audit --server <name>` or `--resolve <kind>:<coordinate>`, `--json`.

```go
type Report struct {
	Source    recipe.Source `json:"source"`
	License   string        `json:"license"`
	DepCount  int           `json:"dep_count"`
	Vulns     []Vuln        `json:"vulns"`
	Licenses  map[string][]string `json:"licenses"` // class → dep names
	Scripts   []string      `json:"install_scripts"`
	Binaries  []string      `json:"binaries"`
	Blocking  []string      `json:"blocking"`
}
```

**Behavior:**
- `Blocking` gets one line per CRITICAL, per HIGH with a `Fixed` version, and per refusing licence; a non-empty `Blocking` → exit 1.
- `Binaries` lists ELF or Mach-O files and `.node` addons in the fetched tree.
- `--resolve npm:@x/y@1.2.3` fetches without a `servers/` dir; `--resolve` and `--server` together → exit 2.

**Tests:**
- `TestRun_BlockingRules`: fixture vulns and licences produce the expected `Blocking` lines.
- `TestCmdAudit_Exclusive`: both flags → exit 2.

### Task 5: Egress proxy

**Files:**
- Create: `internal/smoke/proxy.go`, `cmd/mcplib/egressproxy.go`
- Test: `internal/smoke/proxy_test.go`

**Interfaces:**
- Produces: `type Attempt struct{ Host string; Port int; Allowed bool; Time time.Time }`, `NewProxy(allow []string, log io.Writer) http.Handler`; hidden command `mcplib egress-proxy --listen :3128 --allow a,b --log /log/proxy.jsonl`.

**Behavior:**
- `CONNECT context7.com:443` with `context7.com` allowed → 200 and a tunnel; the log gains `{"host":"context7.com","allowed":true}`.
- A disallowed host → 403, logged `allowed:false`.
- A plain-HTTP absolute-URI request is subject to the same rule.
- Matching is exact and case-insensitive. No wildcards.

**Tests:**
- `TestProxy_AllowsDeclared`, `TestProxy_DeniesUndeclared`, `TestProxy_PlainHTTP`: each asserts the status and the log line.

**Gotchas:**
- `egress-proxy` is left out of `usage`; `register` lists everything, so add a hidden flag to `command` at `cmd/mcplib/main.go:21`.
- Build `mcplib` with `CGO_ENABLED=0` so it runs in the build image.

### Task 6: Container lifecycle and MCP probe

**Files:**
- Create: `internal/smoke/container.go`, `internal/smoke/probe.go`
- Test: `internal/smoke/container_test.go`, `internal/smoke/probe_test.go`

**Interfaces:**
- Consumes: `target.Target.BuildImage` at `internal/target/target.go:66`, `manifest.ParseRuntime` from Task 1.
- Produces: `ContainerArgs(spec Spec) []string`, `type Session interface{ Call(ctx context.Context, method string, params any) (json.RawMessage, error); Close() error }`, `DialStdio(r io.Reader, w io.Writer) Session`, `DialSocket(path string) (Session, error)`.

**Behavior:**
- `ContainerArgs` → `--read-only`, `--user 65534:65534`, `--cap-drop ALL`, `--security-opt no-new-privileges`, tmpfs `/state` and `/tmp`, the unpacked tar bound read-only at `/srv`, workdir `/srv`, `--memory` from `memory_max`, `--cpus` as quota over period of `cpu_max`, `--pids-limit`, the internal network, and `HTTPS_PROXY`/`HTTP_PROXY` naming the proxy container.
- `PATH` names `/opt/mcpgw/runtimes/<runtime>/bin` first, as `build.DockerArgs` does.
- Secret params get `smoke-dummy-<name>`, non-secret required params `smoke`.
- The network is `docker network create --internal mcplib-smoke-<rand>`; the proxy joins it and the default bridge; all three are removed on every exit path.
- The probe frames newline-delimited JSON-RPC for stdio and streamable HTTP POSTs for the socket.

**Tests:**
- `TestContainerArgs_Context7`: each flag above, asserted field by field.
- `TestDialStdio_RoundTrip`: an in-process fake server answers `initialize` and `tools/list`.

**Gotchas:**
- Send `initialize` with `protocolVersion` equal to MCPGW's executor bridge value, `2025-06-18` at MCPGW `internal/mcpexecutor/bridge/bridge.go:45`. Define `SupportedProtocolVersions` in `internal/smoke` to mirror MCPGW `mcp.SupportedProtocolVersions()`, and bump it with `EXECUTOR_TARGET.yaml`.

### Task 7: `mcplib smoke`, fixtures and snapshots

**Files:**
- Create: `internal/smoke/smoke.go`, `cmd/mcplib/smoke.go`, `internal/fixture/servers/fixture-dialer/`, `internal/fixture/servers/fixture-crash/`, `servers/*/tools.snapshot.json`
- Test: `internal/smoke/smoke_integration_test.go` (`//go:build integration`)

**Interfaces:**
- Produces: `Run(ctx context.Context, in Input) (Report, error)`; `mcplib smoke --server <name> --tar <path> [--write-snapshot] [--json]`.

```go
type Report struct {
	Verdict     string        `json:"verdict"` // pass | fail
	Failures    []string      `json:"failures"`
	Protocol    string        `json:"protocol_version"`
	InitMillis  int64         `json:"initialize_ms"`
	ToolCount   int           `json:"tool_count"`
	Mode        string        `json:"mode"`
	Egress      []Attempt     `json:"egress"`
}
```

**Behavior:**
- A failure line is added for each of: a timeout past `initialize_timeout`, a protocol outside `SupportedProtocolVersions`, empty `tools/list`, an invalid input schema, a denied egress attempt, and exit before shutdown or non-zero after it.
- `initialize-only` skips the list checks and the snapshot.
- Without `--write-snapshot`, a snapshot differing from the committed `servers/<name>/tools.snapshot.json` → failure `snapshot drift`; a missing snapshot in full mode → failure.
- The snapshot is `tools` sorted by name, with `name`, `description` and `inputSchema`, indented two spaces, with a trailing newline.

**Tests:**
- `TestSmoke_FixtureEcho`: pass, one tool.
- `TestSmoke_FixtureCount`: pass.
- `TestSmoke_Dialer`: fail with a denied `example.com` attempt.
- `TestSmoke_Crash`: fail on exit during initialize.
- `TestSmoke_SnapshotDrift`: an edited snapshot fails.

**Gotchas:**
- Fixtures are not in `servers/`, so the curation and publish jobs ignore them; `mcplib fixture` must skip the two new ones or MCPGW's contract fixture changes.
- Generate the five existing servers' snapshots with `--write-snapshot`; `github` or `grafana` may need `initialize-only`, and each such reason goes in that recipe.

### Task 8: CI jobs and branch protection docs

**Files:**
- Modify: `.github/workflows/ci.yml:105-160`, `docs/CURATION.md`, `Makefile`
- Test: none; the PR's own CI run proves it.

**Behavior:**
- The `packages` job uploads `dist/a` with `actions/upload-artifact`.
- A new `audit` job runs `mcplib audit --server <name> --json` per touched server; a new `smoke` job, `needs: packages`, downloads the tars and runs `mcplib smoke --server <name> --tar dist/a/<name>-<arch>.tar.gz --json`. Each uploads its JSON report and is its own required check.
- A PR touching no server → both jobs succeed with "no servers touched".
- `make smoke SERVER=<name>` builds and smokes locally.
- `docs/CURATION.md` gains the branch-protection settings from spec section 6, as the steps the owner follows once.

**Gotchas:**
- Required checks never report on a PR whose path filter skips the workflow; keep the jobs unfiltered, returning early instead.
- Pin every new action by SHA; `make pin-check` fails otherwise.

## Coverage

Spec sections: Constraints, Components 2, 3, 6; Error handling; Testing (audit, smoke).
