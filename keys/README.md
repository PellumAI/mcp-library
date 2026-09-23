# Library signing keys

Every `*.pub` here is a public key this library has signed with. The key id is
the file name without `.pub`; it is the id `signing_keys` in `index.json` and
the `.sig.<key id>` signature siblings use. The same files, byte for byte, are
embedded in MCPGW at `internal/mcpcatalog/library/keys/`, which is how a
gateway release learns which keys it trusts.

A row here is never deleted, only moved to `retired`: a gateway pinned to an
old release still verifies against the key that release shipped.

| Key id | SHA-256 of the DER public key | Created | State | First library release signed |
|---|---|---|---|---|
| `library-v1` | `3838613f0f3482f6a355bc39fe52585c58b4e6ee047f577590558aa920502aaf` | 2026-09-23 | active | the first publish |

Compute a fingerprint the same way every time:

```bash
openssl pkey -pubin -in keys/library-v1.pub -outform DER | sha256sum
```

## Custody

The ECDSA P-256 key was generated with `cosign generate-key-pair` on a
maintainer's machine, never in CI. Its private half and password are the
repository-level Actions secrets `COSIGN_LIBRARY_KEY` and
`COSIGN_LIBRARY_PASSWORD`, scoped to this repository so no other organisation
repository can read them, with an offline backup in the maintainers' password
manager. The private half exists nowhere else and is never committed.

Only the publish workflow on `main` signs. Maintainers do not sign by hand
except in the key-rotation rehearsal, which uses throwaway fixture keys; see
`docs/KEY-ROTATION.md`.
