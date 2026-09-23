# Rotating the library key

Two facts set the shape of this. The gateway verifies against a public key that
shipped inside its own release, so a gateway only learns about a new key when
it upgrades. And a gateway may sit on an old release for a long time, which is
a feature: it is what makes verification work with no network.

So rotation is slow on purpose, and it overlaps.

## The overlap

| Phase | The library | A gateway release |
|---|---|---|
| Before | signs with `library-vN` | ships `library-vN` |
| Overlap opens | signs with **both** `library-vN+1` and `library-vN`; `signing_keys` is `["library-vN+1", "library-vN"]` | the next release ships **both** public keys |
| Overlap holds | unchanged | at least one full release, so every supported gateway has had an upgrade in which to acquire the new key |
| Overlap closes | signs with `library-vN+1` only; `signing_keys` is `["library-vN+1"]` | the release after that drops `library-vN` |

A gateway on the old release keeps verifying throughout the overlap, because
`<file>.sig.library-vN` is still published beside every object. A gateway on
the new release prefers the new key, because `signing_keys` is newest first and
the verifier tries in that order.

## Doing it

1. `cosign generate-key-pair`, on a maintainer's machine. Put the private half
   and its password into the Actions secrets as `COSIGN_LIBRARY_KEY_VN1` and
   `COSIGN_LIBRARY_PASSWORD_VN1`, leaving the old pair in place, and into the
   maintainers' password manager.
2. Commit `keys/library-vN+1.pub` here and add its row to `keys/README.md`:
   fingerprint, created-on, state `active`; move the old row to `overlap`.
3. Open a pull request on MCPGW adding the same file at
   `internal/mcpcatalog/library/keys/library-vN+1.pub`, with its row in that
   directory's README. That is the whole of the gateway-side change:
   `library.Keys` globs the directory, so there is no list to update and
   nothing to forget.
4. Change `publish.yml` to sign with both keys -- one `mcplib sign` per key,
   the newest first -- and set `SIGNING_KEYS` to `library-vN+1,library-vN`.
   `mcplib sign` refuses to sign an index whose `signing_keys` does not name
   the key it is signing with, so a half-done edit fails in CI.
5. Republish the index. Blobs are not re-signed: a published digest is
   immutable and so is the signature over it, and the old blobs keep their old
   `.sig.library-vN` files. Only objects published from now on carry both.

   **This is the one place the overlap has a sharp edge, and it is stated here
   rather than discovered.** A gateway that upgrades past the overlap's close
   and then fetches an *old* blob will find only `library-vN` signatures for
   it. So the close in step 7 drops the old key from the *release* but the
   library keeps publishing `.sig.library-vN` siblings for objects that already
   had them, forever. Dropping a key from a release is a statement about what
   is signed next, never a retraction of what was signed before.
6. Wait at least one full gateway release.
7. Close the overlap: sign with the new key only, set `signing_keys` to one
   entry, and move the old key's row to `retired` -- in `keys/README.md`, not by
   deleting the file. The next MCPGW release drops `library-vN.pub`.

## The rehearsal

`mcplib fixture --rotation` writes the overlap in miniature with two throwaway
keys, `library-fixture` and `library-fixture-v2`: the index signed by both
under `signing_keys ["library-fixture-v2", "library-fixture"]`, one blob
signed by both, and one blob signed by the old key only -- an object published
before the overlap opened. MCPGW commits that tree and runs three tests over it
on every `go test`: a gateway holding both keys verifies under the new one; a
gateway holding only the old key still verifies; and a gateway holding both
falls through to the old key for the old blob instead of failing because the
newest key had nothing to check. The next person to rotate the real key is
repeating something the repositories already do every run.

## If a key is compromised

The overlap does not apply. Rotate immediately, close the window in the same
change, and publish an advisory naming every digest signed with the compromised
key. Then ship a gateway release that drops the key, and tell operators to
upgrade, because until they do their gateway trusts a key an attacker holds.
There is no revocation mechanism in the contract and adding one would mean a
network call at verification time, which is the thing the contract is shaped to
avoid. That trade is stated in MCPGW's `docs/security-considerations.md`.
