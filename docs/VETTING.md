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
