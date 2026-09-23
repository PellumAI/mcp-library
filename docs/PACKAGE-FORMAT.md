# What a package is

A package is one gzip-compressed tar with `mcpgw-package.json` at its root. It
is named by the sha256 of its compressed bytes and by nothing else: that digest
is what a gateway instance pins, what the executor fetches by, and what the
library's signature covers. Version strings are metadata for people.

This page is for someone adding a server. The field-by-field contract is the
JSON Schema at `contract/mcpgw-package.schema.json`, which MCPGW authors and
this repository pins in `EXECUTOR_TARGET.yaml`. This page links it rather than
restating it, because prose that copies a pinned schema drifts from it.

## Layout

```
mcpgw-package.json          required, at the tar root
bin/                        native entrypoints, if runtime is native
lib/                        server source
node_modules/               vendored, if runtime is node
.venv/                      vendored, if runtime is python
LICENSE, NOTICE             carried through from the upstream project
```

The unpacked tree is complete. The first thing the executor does after
unpacking is run the entrypoint, and nothing between those two steps opens a
socket. That is what makes an air-gapped install work, and it is also what
makes the supply chain auditable: the bytes that run are the bytes that were
signed.

## What the unpacker refuses

The gateway-side unpacker refuses the whole archive, rather than skipping the
entry, on any of these, and `mcplib pack` refuses the same entries at build
time so a bad package fails a contributor's CI instead of an operator's
deploy:

1. an absolute path;
2. a path that climbs out of the package root with `..`;
3. a symlink whose target is absolute or lands outside the package root;
4. a device, fifo or socket entry, or any setuid, setgid or sticky bit.

A relative symlink that stays inside the tree is allowed on purpose:
`node_modules/.bin/` is a directory of them.

## Two files per server, and why

```
servers/<name>/
  package.yaml        how to build it: upstream pin, runtime line, build steps
  manifest.json       what it is: the mcpgw-package.json template
  overlay/            files laid over the fetched source; rare and reviewed
  patches/            upstream patches; rare and reviewed
```

`manifest.json` ships inside the tar and is what the gateway validates.
`package.yaml` is build instructions the gateway never sees. The two overlap in
four fields, `name`, `version`, `runtime` and `arch`, and `mcplib validate`
refuses a pair that disagrees on any of them.

The builder renders `manifest.json` into the tar with two changes and no
others: `arch` is narrowed to the architectures that one blob serves, and a
backend may add an `env` entry it owns, such as `PYTHONPATH` for a python
package. The rendered bytes are a canonical serialisation, so they are a
function of the content alone.

## One example per runtime line

A native package names a path under `bin/`:

```json
{ "runtime": "native", "entrypoint": ["bin/mcp-grafana"], "transport": "stdio" }
```

A node package names the interpreter from the runtime line the sandbox binds
at `/opt/mcpgw/runtimes/<line>`, which is on `PATH` inside the sandbox, and a
file in the vendored tree:

```json
{ "runtime": "node@22", "entrypoint": ["node", "lib/index.js"], "env": { "NODE_ENV": "production" } }
```

A python package does the same with `python3`; the builder sets `PYTHONPATH`
to the vendored `site-packages` under `.venv/`:

```json
{ "runtime": "python@3.12", "entrypoint": ["python3", "-m", "fixture_count"] }
```

`.venv/` is not a real virtual environment. A package must not carry an
interpreter, because the sandbox binds exactly one read-only; `.venv/` holds
the vendored packages and any console scripts, with shebangs rewritten to the
bound interpreter.

## Egress hosts

`egress` lists the hosts a server dials in normal operation, each with a
`reason`. A host may be a literal hostname or exactly `${<param>.host}`, the
host component of a non-secret param the operator fills in, which the gateway
resolves at claim time. That interpolation is the whole mechanism: this
library never learns a customer's hostnames, and a recipe that hardcodes one is
a curation failure.

## Credentials

A server takes its credential in one of two shapes, never both. An
environment credential is an ordinary `secret: true` param with an `env`. A
per-request credential is `upstream_auth` naming a `secret`, `required` param
whose `env` is empty; the gateway presents it on every proxied call and the
process never sees it.

`gateway_auth` and `upstream_auth` both travel as HTTP headers between the
gateway and the package, so both need `transport: http`. Under `stdio` the
executor's bridge is the process's whole interface and carries no request
header into it; a stdio package therefore leaves both null, and the sandbox and
the executor listener are the controls on who may call it.
