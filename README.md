# mcp-library

Curated, signed MCP server packages for [MCPGW](https://github.com/PellumAI).

Each server in `servers/` is built reproducibly inside a pinned container into
one tar per architecture, named by the sha256 of its bytes, signed with this
library's long-lived key, and listed in a signed `index.json`. An MCPGW gateway
verifies both against a public key that shipped inside its own release, with
no network, no transparency log and no clock.

## Consuming the library

A gateway's `mcp_catalog.library_source` is a base URL serving four static
paths:

```
<base>/index.json            the signed index
<base>/index.json.sig        its detached signature
<base>/blobs/sha256/<hex>    one package tar, addressed by its own digest
<base>/blobs/sha256/<hex>.sig
```

The default base is `https://pellumai.github.io/mcp-library`. Both the index
and every blob are signed; the public key is `keys/library-v1.pub`, and its
fingerprint is recorded in `keys/README.md`.

## Submitting a server

Open one pull request adding one directory under `servers/`. CI builds it twice
and refuses it unless both builds produce the same digest, validates its
manifest against the schema the pinned MCPGW build publishes, and refuses it if
its runtime line is outside the executor window in `EXECUTOR_TARGET.yaml`. See
`docs/PACKAGE-FORMAT.md` for what a package is.

## Repository layout

| Path | What it is |
|---|---|
| `EXECUTOR_TARGET.yaml` | The one MCPGW build this library targets |
| `contract/` | The package schema and runtime window that build publishes, bound by digest |
| `servers/<name>/` | One recipe and one manifest template per server |
| `cmd/mcplib` | The single build tool: validate, build, pack, index, sign, site, fixture |
| `docs/` | The package format, reproducibility, curation and vetting rules |

## Checks

```
make check      # pin-check, vet, lint, test and validate every recipe
```

## Maintainer settings

These are organisation or repository settings no workflow token can change:

- **GHCR package visibility.** The first ORAS push creates the
  `ghcr.io/pellumai/mcp-library/*` packages private. An organisation owner
  must flip each one to public by hand once, after which the publish workflow
  proves anonymous pull on every run.
- **Branch protection on `main`** requires the `check` status.
