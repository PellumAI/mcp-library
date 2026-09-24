// Package recipe is the schema of servers/<name>/package.yaml: everything the
// builder needs to turn one upstream project into one reproducible tar, and
// nothing about how it is published.
//
// The recipe is NOT the manifest. servers/<name>/manifest.json holds the
// mcpgw-package.json that ships inside the tar and that the gateway validates;
// this file holds the build instructions, which the gateway never sees. The
// two overlap in exactly four fields -- name, version, runtime and arch -- and
// Validate asserts they agree rather than letting one drift behind the other.
package recipe

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// FileName is the recipe's name under servers/<name>/.
const FileName = "package.yaml"

// OverlayDir is the directory under servers/<name>/ whose files are copied
// over the fetched source tree before any step runs: the committed lockfile
// for an npm package that does not publish one, most often. Like patches/, it
// is rare and it is reviewed.
const OverlayDir = "overlay"

// Recipe is one server's build definition.
type Recipe struct {
	SchemaVersion int      `yaml:"schema_version"`
	Name          string   `yaml:"name"`
	Version       string   `yaml:"version"`
	Runtime       string   `yaml:"runtime"`
	Arch          []string `yaml:"arch"`
	Categories    []string `yaml:"categories"`
	Source        Source   `yaml:"source"`
	Build         Build    `yaml:"build"`
	Vetting       Vetting  `yaml:"vetting"`
	Smoke         Smoke    `yaml:"smoke"`
}

// Smoke is the credential escape hatch for servers whose tools require a
// live secret to enumerate: mode "initialize-only" tells `mcplib smoke` to
// require only a clean initialize and shutdown, and to write no
// tools.snapshot.json, so a package that would otherwise never pass a
// network-isolated smoke run can still ship. The zero value ("") is full
// mode: initialize, then tools/list and the other capability-advertised
// listings. Reason is not decoration; the PR template asks a reviewer to
// confirm it before a signed package skips the snapshot the reviewer would
// otherwise use to see the tool surface being signed.
type Smoke struct {
	Mode   string `yaml:"mode"`
	Reason string `yaml:"reason"`
}

// Source names exactly one upstream, pinned. There is no "latest" kind and
// there is no kind that resolves a range: every kind below carries either a
// digest or an immutable coordinate.
type Source struct {
	// Kind is git, npm, pypi or archive.
	Kind string `yaml:"kind"`
	// Repo is the git remote for kind git.
	Repo string `yaml:"repo"`
	// Commit is the full 40-hex commit for kind git. A tag is not accepted.
	Commit string `yaml:"commit"`
	// Package addresses a registry artefact for kind npm, as
	// <name>@<exact version>, or for kind pypi, as <name>==<exact version>.
	Package string `yaml:"package"`
	// Integrity is the registry's own digest for kind npm or pypi, in the
	// registry's native form: sha512-<base64> for npm, sha256=<hex> for pypi.
	Integrity string `yaml:"integrity"`
	// URL and SHA256 address a release archive for kind archive.
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
}

// Build is the backend-specific recipe. Steps run inside the pinned build
// image with the source tree as the working directory.
type Build struct {
	// Steps are argv lines, not shell strings, for the same reason the
	// manifest's entrypoint is an argv: there is no shell to interpret one
	// and a quoting bug should be a parse error rather than a surprise.
	//
	// A step runs with the network removed, with exactly three exceptions,
	// each of which is an installer that refuses to install anything its
	// lockfile does not name by digest: npm ci, pip install
	// --require-hashes, and go mod download. See docs/REPRODUCIBILITY.md.
	Steps [][]string `yaml:"steps"`
	// Stage maps a path in the built source tree to its path in the package
	// tar. Anything not named here is not packaged.
	Stage []StageRule `yaml:"stage"`
	// Lockfile is the path, relative to the source tree after the overlay,
	// of the file that makes the dependency set exact. Required for node and
	// python, and for a native Go build.
	Lockfile string `yaml:"lockfile"`
	// Toolchains are extra runtime lines put on PATH for the steps only, for
	// a native build that compiles front-end assets with node before it
	// compiles Go. Each must be inside the executor window, so the build
	// image carries it; none of them reaches the package.
	Toolchains []string `yaml:"toolchains"`
}

// StageRule copies From in the built tree to To in the package tar.
type StageRule struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	// Optional allows a rule to match nothing, for a LICENSE that some
	// upstreams do not ship at the path everyone else uses.
	Optional bool `yaml:"optional"`
}

// Vetting is the evidence a maintainer recorded, carried in the recipe rather
// than only in a document so that a stale entry is visible in the diff that
// bumps a version.
type Vetting struct {
	VettedOn  string   `yaml:"vetted_on"`
	VettedBy  string   `yaml:"vetted_by"`
	Sources   []string `yaml:"sources"`
	License   string   `yaml:"license"`
	EgressRat string   `yaml:"egress_rationale"`
	Notes     string   `yaml:"notes"`
}

// Load reads and strictly decodes a package.yaml. An unknown key is an error:
// a misspelled field that silently did nothing is exactly the kind of recipe
// bug that ends up signed.
func Load(path string) (Recipe, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Recipe{}, fmt.Errorf("recipe: %w", err)
	}
	return Parse(b)
}

// Parse strictly decodes recipe bytes.
func Parse(b []byte) (Recipe, error) {
	var r Recipe
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return Recipe{}, fmt.Errorf("recipe: %w", err)
	}
	return r, nil
}
