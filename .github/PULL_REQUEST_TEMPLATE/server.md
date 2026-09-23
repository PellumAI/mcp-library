## Server submission

**I am asking the maintainers to sign these bytes with the library key.**

- Server: `servers/<name>/`
- Upstream: <repository URL> at `<full 40-character commit>`, or `<npm package>@<version>` with its registry integrity, or `<pypi project>==<version>` with its sha256, or `<archive URL>` with its sha256

One answer per check in `docs/VETTING.md`, each with its evidence. Add the same answers as a row in the evaluated-candidates table there; CI refuses a new server with no row.

- [ ] 1. **The upstream is the vendor's own, maintained project.** Evidence:
- [ ] 2. **Pinned source with a lockfile we can vendor.** Which kind, and where the lockfile comes from:
- [ ] 3. **Runtime line inside the window**, and the upstream's own statement of its requirement:
- [ ] 4. **stdio or streamable HTTP on the executor's socket.** Evidence for the transport:
- [ ] 5. **Every parameter maps to an environment variable.** Params:
- [ ] 6. **The credential is one the operator holds**, environment or per request, with its primary source:
- [ ] 7. **A per-request credential fits one header, whole**, or there is none:
- [ ] 8. **Caller-token verification**, or null with the reason:
- [ ] 9. **Egress is declarable.** Hosts and one-sentence reasons:
- [ ] 10. **Runs without privilege.** Anything unusual it writes or opens:
- [ ] 11. **The licence permits redistributing the built artefact, dependencies included.** Licence:
- [ ] 12. **Reproducible.** Anything the recipe does to get there:

## Reviewer checklist

- [ ] The source pin is on a branch or tag the vendor publishes, and is not a fork
- [ ] The build steps and the installers' scripts do nothing surprising to the tree that gets signed
- [ ] The vendored dependency set has nothing in it that has no business there
- [ ] Every egress reason describes what the server does, not the hostname
- [ ] The vetting block's sources say what the recipe claims
