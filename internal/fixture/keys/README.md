# These keys sign nothing anybody trusts, and no release ever carries them.

`library-fixture` and `library-fixture-v2` are throwaway cosign key pairs,
both halves committed on purpose, with an empty password. They sign only the
miniature library `mcplib fixture` generates for MCPGW's contract test, and
the key-rotation rehearsal `mcplib fixture --rotation` generates beside it.

Committing a private key is normally a defect. Committing these is what makes
the fixture regenerable by any contributor and keeps the real library key,
`keys/library-v1.pub`, out of every test in both repositories. MCPGW's
`library.Keys` reads only its own `internal/mcpcatalog/library/keys/` and will
never see these; its contract test passes the fixture's public key explicitly.
