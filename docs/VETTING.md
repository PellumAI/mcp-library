# Vetting a library package

Shipping a package is a statement that this library will build these bytes,
sign them with its key, and let a gateway run them in a sandbox with the
operator's credentials in the environment. The bar is about evidence, not
popularity. Record every answer in the table at the bottom, including the
rejections: a rejected candidate nobody wrote down gets proposed again in six
months.

The 2026-09-18 checklist was written for containers, and three of its checks
assumed an image: "the image is published by the software's own vendor", "the
container serves streamable HTTP natively" and "a digest exists to pin". What
replaces them is narrower and admits far more of the ecosystem.

## The checklist

1. **The upstream is the software's own project.** The vendor's GitHub
   organisation, the vendor's npm scope, the vendor's PyPI project. A community
   fork does not qualify, however good it is: the package outlives the person
   maintaining it. **A project its vendor has deprecated does not qualify
   either**, because nobody will ship its next security fix.
2. **It installs from a pinned source with a lockfile we can vendor.** One of:
   a git commit plus a committed lockfile; an npm package plus its registry
   integrity string; a PyPI project plus a `--generate-hashes` requirements
   file; or a release archive plus its sha256 and a statically linked binary
   inside. Anything else is not a v1 candidate. **This replaces the old "an
   official image exists" check and is the reason most of the ecosystem is now
   reachable.**
3. **It pins a runtime line inside the window.** See `docs/CURATION.md`. Record
   which line and the upstream's own statement of its requirement — a node
   `engines` field, a python `requires-python`, a Go `go` directive.
4. **It speaks stdio or streamable HTTP.** Both are first class. stdio is the
   expected majority and needs no evidence beyond the entrypoint: if the
   server reads MCP framing on stdin, the executor's bridge gives it a correct
   streamable-HTTP front end. **This replaces the old "serves streamable HTTP
   natively" check, which excluded the majority of the ecosystem.** For
   `transport: http`, cite the primary source for the flag or variable that
   makes the server listen on the unix socket in `MCPGW_LISTEN_SOCKET`; a
   server that can only listen on a TCP port does not qualify for `http`.
5. **Every parameter maps to an environment variable.** A package gets a
   writable `/state`, `HOME` and `/tmp`, so a server that writes a cache or a
   session file is fine; a server that can only be configured from a file the
   operator must author is not.
6. **The credential the server needs is one the operator holds**, and it
   arrives either in the environment or on every request. An environment
   credential is an ordinary `secret: true` param. A per-request credential is
   `upstream_auth` naming a `secret` and `required` param whose `env` is empty;
   the gateway resolves it when it upserts the managed upstream, seals it on
   that row and presents it on every proxied call, so it never reaches the
   executor and never reaches the process environment. **A per-request
   credential is a request header, so it needs `transport: http`**: the stdio
   bridge carries no header into the process. Record which shape and the
   primary source that says so. Getting it backwards produces a server that
   401s every call with no obvious cause.
7. **The per-request credential fits one header, whole.** No OAuth exchange, no
   refresh, no per-request signature, and no second field that must travel in
   its own header on every call. A second field that can be fixed at deploy
   time is an ordinary param and is fine.
8. **The server can verify a caller token, or it cannot and you say so.** Fill
   in `gateway_auth` if it exposes a server-auth token variable **and it serves
   `transport: http`**; leave it null otherwise, and the instance page tells the
   operator that the sandbox and the executor listener are then the only
   controls on who may call it. `gateway_auth` and `upstream_auth` are mutually
   exclusive.
9. **Egress is declarable.** List the hostnames the server dials in normal
   operation, each with a `reason`, using `${param.host}` for anything that is
   the operator's own. A server that dials whatever the user's data tells it to
   — a generic fetch tool, a browser — cannot have its egress declared and is
   excluded. Be honest in the record about layer two's limit: the proxy
   constrains a client that honours `HTTPS_PROXY` and does not constrain one
   that opens a raw socket.
10. **It runs without privilege.** No device, no mount, no browser sandbox, no
    setuid. The sandbox gives it a user namespace, a pid namespace, a cgroup
    sub-slice with limits, a read-only `/srv`, a writable `/state`, `/tmp` and
    one read-only runtime tree. A server that needs more than that is excluded.
11. **The licence permits redistribution of the built artefact.** We ship
    bytes, not a reference, so unlike the container-era checklist this one is
    not a formality. Record the licence and confirm it permits distributing a
    build that includes the vendored dependency set.
12. **It is reproducible.** CI proves it, but note anything the recipe had to
    do to get there — a stripped timestamp, a rewritten shebang — so the next
    person bumping the version knows what to expect.

## Audit waivers

`mcplib audit --server` blocks on an OSV critical, an OSV high with a fix on
the installed release branch, and a refusing licence. The fix is to re-pin to
an upstream release that clears the finding. A waiver is acceptable only when
no such release exists: the fix is merged upstream but not tagged, or not
merged at all. It is never a way around a release that exists but is
inconvenient to take.

A waiver lives in the recipe, next to the vetting it rests on:

```yaml
audit:
  waivers:
    - id: GHSA-2v4p-qf9q-27wj        # the OSV id or any alias, as audit reports it
      package: google.golang.org/grpc # the dependency name, as audit reports it
      reason: "No release carries grpc v1.83.2; upstream main 3c90b87e does."
      expires: "2026-10-25"
```

- `reason` names the missing release and the upstream commit or PR that
  carries the fix, so the reviewer can check it and the next person knows what
  to watch for.
- `expires` is at most 90 days after `vetting.vetted_on`; `recipe.Validate`
  refuses anything later, a missing field, and a second waiver for the same
  advisory and package.
- A waived finding moves from `blocking` to `waived` in the report and the
  summary prints it with its expiry date.
- On the day after `expires` (UTC) the waiver waives nothing: its finding
  blocks again and the waiver adds a blocking line of its own. Re-pin, or
  re-vet and renew the waiver in a reviewed PR.
- A waiver that matches no blocking finding, because the re-pin cleared it or
  the id was mistyped, blocks until it is removed.
- A refusing licence can never be waived, and `--resolve` takes no waivers.

## Why every v1 package is amd64 only

The MCPGW gateway and executor images are built `linux/amd64` only today, per
MCPGW's `release-assets.yml` `platforms: linux/amd64` and goreleaser's
`goarch: [amd64]`. Publishing arm64 blobs nothing can run would be publishing
artefacts nobody has verified. The builder supports `GOARCH=arm64` today; the
recipes gain the arch in the release where the executor image goes
multi-arch, which is one line per recipe and a rebuild.

## Evaluated candidates

| Candidate | Upstream and pin | Runtime, and the upstream's requirement | Transport | Credential shape, with its source | Egress | Licence | Verdict |
|---|---|---|---|---|---|---|---|
| `grafana` | [grafana/mcp-grafana](https://github.com/grafana/mcp-grafana) v1.5.1 at `2a33c72f211560e4ffb39d6b99cad3c3dc2a3f6e` | `native`; `go 1.26.5` in go.mod | stdio, the `-t` default | Environment: `GRAFANA_SERVICE_ACCOUNT_TOKEN`, per the v1.5.1 README. `MCP_GRAFANA_SERVER_TOKEN` has no effect under stdio, per main.go, so `gateway_auth` is null | `${grafana_url.host}` | Apache-2.0 | **Package.** Re-vetted from the 2026-09-18 image entry; port, health path and endpoint path dropped as image properties |
| `terraform` | [hashicorp/terraform-mcp-server](https://github.com/hashicorp/terraform-mcp-server) v1.3.0 at `943a44eb28dc58432b34efdf08f7fc846adc446d` | `native`; `go 1.26.6` in go.mod | stdio, the `stdio` subcommand | Environment, optional: `TFE_TOKEN`, per pkg/client/tfe_client.go | `registry.terraform.io`, `app.terraform.io` | MPL-2.0 | **Package.** `TRANSPORT_*` variables dropped; a self-hosted TFE host is an instance egress override |
| `buildkite` | [buildkite/buildkite-mcp-server](https://github.com/buildkite/buildkite-mcp-server) v1.22.0 at `565af319ef25f4f53336cdba4469db784aaf67a3` | `native`; `go 1.25.8` in go.mod | stdio, the `stdio` subcommand | Environment: `BUILDKITE_API_TOKEN`, per cmd/buildkite-mcp-server/main.go. Passes only because the process-wide token is still supported beside a per-request mode | `api.buildkite.com` | MIT | **Package.** Re-check the process-wide token on every version bump |
| `github` | [github/github-mcp-server](https://github.com/github/github-mcp-server) v1.12.2 at `85598ba6e1256f7ebf4867b95d63b833c4549264` | `native`, with `node@22` as a build-time toolchain for the embedded UI; `go 1.25.12` in go.mod, UI `engines` `^20.19.0 \|\| >=22.12.0` | stdio, the `stdio` subcommand | Environment: `GITHUB_PERSONAL_ACCESS_TOKEN`, per cmd/github-mcp-server/main.go | `api.github.com`, `raw.githubusercontent.com` | MIT | **Package.** Takes the fifth slot SonarQube left. Job-log tools follow redirects to storage hosts that cannot be declared |
| `context7` | [@upstash/context7-mcp](https://www.npmjs.com/package/@upstash/context7-mcp/v/4.1.1) 4.1.1, integrity `sha512-fUARTIZG…l/C5pA==`, vendored through a committed lockfile | `node@22`; `engines` `>=20.18.1` | stdio, the `--transport` default | Environment, optional: `CONTEXT7_API_KEY`, per the package README | `context7.com` | MIT | **Package.** The first node package; takes the slot Elasticsearch left |
| elasticsearch | [elastic/mcp-server-elasticsearch](https://github.com/elastic/mcp-server-elasticsearch) 0.4.x | none: 0.4 is a Rust build shipped only as a container image | stdio or HTTP | Environment: `ES_API_KEY` | `${es_url.host}` | Apache-2.0 | **Exclude, check 1.** The README marks it deprecated in favour of the Elastic Agent Builder MCP endpoint, receiving critical security fixes only; and check 2, no installable artefact |
| sonarqube | [SonarSource/sonarqube-mcp-server](https://github.com/SonarSource/sonarqube-mcp-server) 1.27 | none: a JVM artefact, distributed as a jar and a container | stdio | Environment: `SONARQUBE_TOKEN` | `${sonarqube_url.host}` | not asserted by GitHub | **Exclude, check 3, no runtime line in the window.** A `jvm@<lts>` line would be an executor image change, not a library change |
| mongodb | [mongodb-js/mongodb-mcp-server](https://github.com/mongodb-js/mongodb-mcp-server) 3.0.4 | `node@22` | stdio | Environment: a connection string | cannot be declared: a `mongodb+srv` string resolves to hosts the manifest cannot name, and the driver opens raw sockets the proxy does not see | Apache-2.0 | **Container era: excluded on check 2, no primary source for the HTTP endpoint path. Package criteria: that check no longer applies; now excluded on check 9** |
| neo4j | the Neo4j Cypher MCP server | to establish | stdio | Environment | the operator's Neo4j host | to establish | **Container era: excluded on check 1, the image sat in the Docker `mcp/` namespace. Package criteria: re-evaluate against the upstream project's ownership.** Candidate for a later submission |
| sentry | [@sentry/mcp-server](https://www.npmjs.com/package/@sentry/mcp-server) 0.39.0 | `node@22`; `engines` `>=22.13` | stdio | Environment: `SENTRY_ACCESS_TOKEN` | `sentry.io` or the operator's host | FSL-1.1-ALv2 | **Container era: excluded on check 1, no vendor image. Package criteria: the vendor publishes an installable package, so check 1 passes; check 11 needs a licence review of FSL-1.1 before anyone submits it** |
| apify | the Apify MCP server | to establish | stdio | Environment | `api.apify.com` | to establish | **Container era: excluded on check 1. Package criteria: re-evaluate whether the vendor publishes an installable package.** Candidate for a later submission |
| playwright | [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp) | `node@22` plus a browser | stdio | none | the whole internet | Apache-2.0 | **Still excluded, and more firmly: checks 9 and 10.** A browser's egress is anywhere a page links, and it needs a writable profile and its own sandbox inside ours |
| circleci | [@circleci/mcp-server-circleci](https://www.npmjs.com/package/@circleci/mcp-server-circleci) 0.20.0 | `node@22` | stdio | Environment: `CIRCLECI_TOKEN` | `circleci.com` | Apache-2.0 | **Exclude, check 1.** The package deprecates itself in favour of CircleCI's hosted server, in every tool description |
| notion | [makenotion/notion-mcp-server](https://github.com/makenotion/notion-mcp-server) 2.5.2 | `node@22` | stdio | Environment: `NOTION_TOKEN` | `api.notion.com` | MIT | **Exclude, check 1.** The README says the local server is no longer actively maintained |
| stripe | [@stripe/mcp](https://www.npmjs.com/package/@stripe/mcp) 0.3.3 | `node@22` | stdio | Environment: `STRIPE_SECRET_KEY` | `mcp.stripe.com` | MIT | **Not packaged.** The local package is a stdio relay to Stripe's hosted server, so it adds a hop and nothing else; point the gateway at the hosted server instead |

### The per-request-credential reference entry is unfilled in v1

The plan made one of the five the `upstream_auth` reference, SonarQube or else GitHub, taking `Authorization: Bearer` on every request. Neither can be. SonarQube is excluded on check 3. GitHub's per-request token exists only in its `http` subcommand, which listens on a TCP `--port`; an `http` package must listen on the unix socket `MCPGW_LISTEN_SOCKET` names, and the executor's stdio bridge carries no request header into a stdio process. So `github` ships with its environment credential, and the library carries no `upstream_auth` package until an upstream serves streamable HTTP on a socket path it is given. That is recorded as a gap, issue 11, not papered over with a manifest that would 401 every call.

### The python backend has no curated server yet

None of the five is a python project. The python backend is exercised end to end by MCPGW's contract fixture, whose `fixture-count` package is python, on every fixture refresh, and a curated python server is tracked as issue 10 on this repository.
