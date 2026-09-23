# Publishing

`publish.yml` builds, signs and publishes on a push to `main` that touches a
server, the build image, the executor target or the keys, on a published
release, and on a manual run. It has two destinations, and they have two
different jobs.

## GitHub Pages is the gateway's `library_source`

A gateway's contract with a library is static files over plain HTTPS:

```
<base>/index.json
<base>/index.json.sig
<base>/blobs/sha256/<hex>
<base>/blobs/sha256/<hex>.sig
```

GHCR does not serve those paths: it serves the OCI distribution API, and even
an anonymous pull needs a token exchange first, so a gateway pointed at GHCR
would need an OCI client. Pages serves them as plain files, at
`https://pellumai.github.io/mcp-library`, which is the default
`mcp_catalog.library_source` MCPGW pins. `.sig.<key id>` siblings, the
`.cosign.bundle` keyless attestations and `keys/*.pub` sit beside them.

## GHCR through ORAS is the durable, content-addressed mirror

Every blob is an OCI artifact at `ghcr.io/pellumai/mcp-library/blobs`, tagged
`sha256-<hex>` because an OCI tag may not contain a colon, carrying the tar,
its signatures and its bundle. The index is `ghcr.io/pellumai/mcp-library/index`
at `:latest` and, on a release, at `:<tag>`. GHCR is where a blob becomes
immutable under its digest: a tag that exists is never pushed over. It has no
size ceiling worth worrying about, and it is what the Pages carry-over reads
old blobs back from, since a Pages deployment replaces the whole site.

## The size budget

Pages caps a published site at one gigabyte. `mcplib site` fails the publish
at 900 MiB with a message naming both ways out: retire old versions with
`mcplib index --retire`, or move `library_source` to a host with no ceiling.
The blobs are already on GHCR by digest and the contract is four static paths,
so any object store or CDN in front of them serves a gateway with no gateway
change.

## Immutability, from the consumer's side

A digest that resolved once resolves to the same bytes forever. `mcplib index`
refuses a publish that would change the digest of an existing
`name@version` and arch, and `publish-blobs.sh` never pushes over an existing
blob tag.

## Tip or release

`library_source` names a base URL, and the Pages site always serves the tip.
A gateway that wants an exact library release instead reads
`index:<tag>` from GHCR through `gatewayctl library export` on a connected
machine, or the `index.json` release asset.

## Verifying by hand

The key signature, the one a gateway checks:

```bash
go run ./cmd/mcplib verify-blob --key keys/library-v1.pub \
  --signature <hex>.sig <hex>
```

or with the cosign CLI:

```bash
cosign verify-blob --insecure-ignore-tlog --key keys/library-v1.pub \
  --signature <hex>.sig <hex>
```

The keyless attestation, which says which workflow in which repository at
which commit produced the bytes:

```bash
cosign verify-blob --bundle <hex>.cosign.bundle \
  --certificate-identity-regexp '^https://github\.com/PellumAI/mcp-library/\.github/workflows/publish\.yml@refs/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  <hex>
```

**The gateway does not use the keyless attestation and must never be made
to.** Depending on it would mean depending on Fulcio and Rekor at install
time, and an air-gapped install has neither. It is evidence for people; the
key signature is the path for machines.

## The one manual setting

The first ORAS push creates `ghcr.io/pellumai/mcp-library/index`,
`ghcr.io/pellumai/mcp-library/blobs` and `ghcr.io/pellumai/mcp-library/build`
private. An organisation owner flips each to public once, under the package's
settings, then sets the repository variable `GHCR_PUBLIC` to `true`. From then
on the publish job's anonymous-pull check fails the run on a regression;
before it, the check warns.
