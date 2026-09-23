// Uploads files to an existing GitHub Release through the REST API.
// Issue #1203.
//
// Both release-assets upload steps used to shell out to
// `gh release upload "$TAG" ... --clobber`. Every job in that workflow runs
// on the self-hosted pool, whose host tool inventory is make, docker, the
// Playwright chromium packages and an apparmor_parser sudoers rule — no `gh`.
// Run 35449200222 died at `gh: command not found`, exit 127, after goreleaser
// had already written every archive into dist/, so the release carried no
// assets at all.
//
// actions/github-script runs this file on the runner's bundled node and talks
// to api.github.com directly, so the workflow depends on no undeclared host
// tool. github-script is the action this repo already pins, in
// commit-lint.yml, which is why it was preferred over softprops.
//
// --clobber parity: an asset whose name already exists on the release is
// deleted before the new one is uploaded, so re-running a job overwrites
// rather than failing the upload with `already_exists`.
//
// Inputs arrive through process.env and are never interpolated into this
// file or into a shell:
//   TAG          the release tag, e.g. v2.0.0-rc.1
//   ASSET_GLOBS  newline-separated @actions/glob patterns, relative to the
//                workspace

module.exports = async ({ github, context, core, glob }) => {
  const fs = require('fs');
  const path = require('path');

  const tag = process.env.TAG;
  if (!tag) {
    throw new Error('TAG is empty');
  }

  const patterns = (process.env.ASSET_GLOBS || '')
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
  if (patterns.length === 0) {
    throw new Error('ASSET_GLOBS is empty');
  }

  const owner = context.repo.owner;
  const repo = context.repo.repo;

  // Resolve by tag rather than trusting context.payload.release.id, so a
  // manual re-run against the same tag lands on the same release.
  const release = await github.rest.repos.getReleaseByTag({ owner, repo, tag });
  const releaseId = release.data.id;

  // One pattern at a time, and a throw the moment any single one is empty.
  // `gh release upload dist/*.tar.gz dist/*.zip dist/SHA256SUMS` failed under
  // set -e when ANY of the three matched nothing, because bash handed the
  // unexpanded literal to gh and gh could not open it. Globbing the patterns
  // together would lose that: a vanished windows zip or a renamed SHA256SUMS
  // would publish a quietly partial release for as long as one other pattern
  // still matched. Restoring the per-pattern check keeps a missing artifact a
  // release failure rather than a release nobody looks at twice.
  const seen = new Set();
  const files = [];
  for (const pattern of patterns) {
    const globber = await glob.create(pattern);
    const matched = await globber.glob();
    const hits = matched.filter((file) => fs.statSync(file).isFile()).sort();
    if (hits.length === 0) {
      throw new Error(`no files matched ${pattern}`);
    }
    for (const hit of hits) {
      if (!seen.has(hit)) {
        seen.add(hit);
        files.push(hit);
      }
    }
  }

  let existing = await github.paginate(github.rest.repos.listReleaseAssets, {
    owner,
    repo,
    release_id: releaseId,
    per_page: 100,
  });

  for (const file of files) {
    const name = path.basename(file);

    for (const asset of existing.filter((a) => a.name === name)) {
      core.info(`clobber: deleting existing asset ${name}, id ${asset.id}`);
      await github.rest.repos.deleteReleaseAsset({
        owner,
        repo,
        asset_id: asset.id,
      });
    }
    existing = existing.filter((a) => a.name !== name);

    const data = fs.readFileSync(file);
    core.info(`uploading ${name}, ${data.length} bytes`);
    await github.rest.repos.uploadReleaseAsset({
      owner,
      repo,
      release_id: releaseId,
      name,
      data,
      headers: {
        'content-type': 'application/octet-stream',
        'content-length': data.length,
      },
    });
  }

  core.info(`uploaded ${files.length} asset(s) to ${tag}`);
};
