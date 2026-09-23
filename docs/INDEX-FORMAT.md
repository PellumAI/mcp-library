# index.json

The index is the only document a gateway needs to browse this library. It
carries every package's manifest in full, so a gateway that holds no blobs at
all still has a complete catalogue; what it lacks is the bytes, which it
fetches by digest when an operator installs something.

## The published paths

```
<base>/index.json                         the index
<base>/index.json.sig                     its signature under the newest key
<base>/index.json.sig.<key id>            one per key in signing_keys
<base>/blobs/sha256/<hex>                 one package tar, named by its digest
<base>/blobs/sha256/<hex>.sig             its signature under the newest key
<base>/blobs/sha256/<hex>.sig.<key id>    one per key in signing_keys
```

The unsuffixed `.sig` is what a single-key gateway reads. The `.sig.<key id>`
siblings are additive and exist so a key rotation overlaps instead of being a
flag day; see `docs/KEY-ROTATION.md`.

## Fields

| Field | Meaning |
|---|---|
| `schema_version` | `1`. A gateway refuses a higher value rather than reading past fields it does not know |
| `generated_at` | RFC 3339, UTC, when this index was generated |
| `library` | `pellumai/mcp-library` |
| `signing_keys` | Key ids whose signatures are published beside this index and its blobs, **newest first**. A gateway tries the ids it holds a public key for, in this order |
| `packages[].name` | The package name and the catalogue entry id |
| `packages[].title`, `description`, `homepage`, `license` | From the newest version's manifest |
| `packages[].categories` | From the recipe |
| `packages[].versions[]` | Newest first by `published_at` |
| `versions[].version` | The upstream version, metadata for people |
| `versions[].published_at` | When this version was first published. A rebuild to the same digest keeps it |
| `versions[].blobs[]` | One row per `os`/`arch`, with that tar's `sha256` and `size`. An arch-independent package lists one row per arch, all carrying the same digest |
| `versions[].manifest` | The exact bytes of `mcpgw-package.json` from inside the tar |

The file is compact JSON with a trailing newline, because `encoding/json`
re-indents embedded raw JSON and a manifest's bytes here must be exactly the
bytes inside its tar. Read it with `jq`.

## Immutability

A digest that resolved once resolves to the same bytes forever. `mcplib index`
merges every publish into the previous index and refuses a build whose digest
differs from the one already published for the same `name@version` and
`os`/`arch`; the answer is a new version. Retiring a version,
`mcplib index --retire <name>@<version>`, removes it from the index and leaves
its blob and signatures published, so a gateway that already pinned it keeps
working.
