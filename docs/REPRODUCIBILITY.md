# What "reproducible" means here

A package's identity is the sha256 of its compressed tar. Two builds of the
same recipe must produce the same 64 characters, and CI proves it on every
pull request by building each touched package twice and comparing.

That property is real, and it is scoped. It holds **for a given toolchain**:
the pinned build image, which `EXECUTOR_TARGET.yaml` names by digest and
`scripts/pin-check.sh` refuses unpinned, and the Go release that compiles
`mcplib`, which `go.mod` pins and the tar writer's gzip output depends on.

Three things break byte-stability, and only the first is under our control:

1. **The recipe.** A build step that resolves a version, writes a timestamp
   into a file, or emits a map in Go's randomised iteration order will differ
   between runs. `mcplib validate` refuses the first; the double-build gate
   catches the other two.
2. **The Go toolchain.** `compress/gzip`'s output is a function of the Go
   release that produced it. A Go upgrade in `go.mod`, or in the build image
   for a native package, can change every digest in the library without
   changing a single recipe.
3. **The runtime toolchains.** `npm` and `pip` write absolute paths and
   timestamps into some artefacts. The node and python backends strip the ones
   we know about; a new one shows up as a double-build failure.

Because of (2), bumping the build image or the Go toolchain is a library
release, not a chore. The procedure is in `docs/CURATION.md` under "Moving the
build image", and its first line is that every existing package is rebuilt and
its new digest published as a new version, never as a silent replacement of an
old digest. **A published digest is immutable.** Nothing in this repository
ever republishes different bytes under a digest that already existed, because a
gateway that pinned it would have no way to notice.

## The tar writer's five choices

`internal/pack` is where determinism is decided. Entries are sorted by path in
byte order; every header carries the Unix epoch as its mtime and no owner;
modes are normalised to 0755 or 0644; the tar format is PAX with no records
beyond a long path; and gzip runs at a fixed level with an empty name and a
zero mtime. `TestWrite_ByteStable` builds one tree twice, with different
mtimes and in a different creation order, and asserts identical bytes; CI runs
it five times so a map-iteration dependency cannot pass by luck.

## When the gate fires

The double-build failure names both digests and suggests
`diffoscope dist/a/<name>-<arch>.tar.gz dist/b/<name>-<arch>.tar.gz`, which
shows the first differing entry. The usual culprits, in order: a build step
that stamps the date or the build path into an artefact, a dependency whose
install script does the same, and a map serialised in iteration order.
