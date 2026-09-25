// Package build turns one servers/<name> recipe into one package tar per
// architecture: fetch, overlay, steps, backend post-processing, stage,
// manifest, pack.
//
// Every step runs inside the pinned build image. Only the three
// lockfile-enforcing installers recipe.StepNeedsNetwork names get the
// network; every other step runs with --network none, so a step that needs to
// reach out fails loudly rather than making a digest depend on the internet.
package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/pack"
	"github.com/pellumai/mcp-library/internal/recipe"
)

// BuiltAt is the only built_at a sidecar carries. Under SOURCE_DATE_EPOCH=0 a
// build has no time of its own; the publication timestamp lives in the index,
// where it is allowed to be real.
const BuiltAt = "1970-01-01T00:00:00Z"

// Options is everything a build needs and nothing it does not.
type Options struct {
	// ServerDir is servers/<name>.
	ServerDir string
	Recipe    recipe.Recipe
	Manifest  manifest.Doc
	// Arch is the one architecture this build produces.
	Arch string
	// Out is where the tar, its .sha256 and its .meta.json land.
	Out string
	// BuildImage is the pinned image every step runs in.
	BuildImage string
	// Runner runs one step inside the build image. It is a field so tests
	// can substitute a recorder.
	Runner Runner
	// Fetch places the pinned source into a directory. Nil means FetchSource;
	// tests and the fixture generator pass their own.
	Fetch func(ctx context.Context, src recipe.Source, dir string) error
	// WorkDir is the parent of the scratch tree. Empty means a directory
	// under os.TempDir.
	WorkDir string
	// KeepWork leaves the scratch tree behind for debugging a failed step.
	KeepWork bool
	// Log receives one line per phase.
	Log func(format string, a ...any)
}

// Meta is the .meta.json sidecar, so the index generator never re-opens a
// tar. Manifest is the exact bytes written into the tar.
type Meta struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	OS         string          `json:"os"`
	Arch       []string        `json:"arch"`
	BuiltFor   string          `json:"built_for"`
	SHA256     string          `json:"sha256"`
	Size       int64           `json:"size"`
	Runtime    string          `json:"runtime"`
	BuiltAt    string          `json:"built_at"`
	BuildImage string          `json:"build_image"`
	Manifest   json.RawMessage `json:"manifest"`
}

// FileBase is <name>-<arch>, the stem of every artefact one build writes.
func FileBase(name, arch string) string { return name + "-" + arch }

// backend is the per-runtime post-processing between the steps and the stage.
type backend interface {
	// check runs before any step: refusals that need only the fetched tree.
	check(work string, r recipe.Recipe) error
	// post runs after the steps. It returns the arch set the built tree
	// serves and any env the manifest must carry.
	post(work string, r recipe.Recipe, arch string) (archs []string, env map[string]string, err error)
}

func backendFor(runtime string) (backend, error) {
	switch {
	case runtime == "native":
		return nativeBackend{}, nil
	case strings.HasPrefix(runtime, "node@"):
		return nodeBackend{}, nil
	case strings.HasPrefix(runtime, "python@"):
		return pythonBackend{line: runtime}, nil
	}
	return nil, fmt.Errorf("build: no backend for runtime %q", runtime)
}

// Run builds one package.
func Run(ctx context.Context, o Options) (Meta, error) {
	r := o.Recipe
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if !slices.Contains(r.Arch, o.Arch) {
		return Meta{}, fmt.Errorf("build: %s does not declare arch %s; it declares %v", r.Name, o.Arch, r.Arch)
	}
	if o.Runner == nil {
		return Meta{}, errors.New("build: no runner")
	}
	be, err := backendFor(r.Runtime)
	if err != nil {
		return Meta{}, err
	}
	if _, err := os.Stat(filepath.Join(o.ServerDir, "patches")); err == nil {
		return Meta{}, fmt.Errorf("build: %s carries patches/, which this builder does not apply yet; use overlay/ for whole-file replacements", r.Name)
	}

	scratch, err := os.MkdirTemp(o.WorkDir, "mcplib-"+r.Name+"-")
	if err != nil {
		return Meta{}, err
	}
	if !o.KeepWork {
		defer func() { _ = removeAll(scratch) }()
	} else {
		logf("keeping the work tree at %s", scratch)
	}
	src := filepath.Join(scratch, "src")
	for _, d := range []string{src, filepath.Join(scratch, "home"), filepath.Join(scratch, "cache")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Meta{}, err
		}
	}

	fetch := o.Fetch
	if fetch == nil {
		fetch = FetchSource
	}
	logf("fetch %s", DescribeSource(r.Source))
	if err := fetch(ctx, r.Source, src); err != nil {
		return Meta{}, fmt.Errorf("build: %s: fetch: %w", r.Name, err)
	}
	if err := ApplyOverlay(o.ServerDir, src); err != nil {
		return Meta{}, fmt.Errorf("build: %s: overlay: %w", r.Name, err)
	}
	if err := nonEmpty(src); err != nil {
		return Meta{}, fmt.Errorf("build: %s: %w", r.Name, err)
	}
	if r.Build.Lockfile != "" {
		if _, err := os.Stat(filepath.Join(src, filepath.FromSlash(r.Build.Lockfile))); err != nil {
			return Meta{}, fmt.Errorf("build: %s: the declared lockfile %s is not in the source tree", r.Name, r.Build.Lockfile)
		}
	}
	if err := be.check(src, r); err != nil {
		return Meta{}, err
	}

	for i, argv := range r.Build.Steps {
		spec := StepSpec{
			Image:   o.BuildImage,
			Work:    scratch,
			Argv:    argv,
			Arch:    o.Arch,
			Runtime:    r.Runtime,
			Toolchains: r.Build.Toolchains,
			Network:    recipe.StepNeedsNetwork(argv),
		}
		logf("step %d: %s (network %v)", i, strings.Join(argv, " "), spec.Network)
		if err := o.Runner.Run(ctx, spec); err != nil {
			return Meta{}, fmt.Errorf("build: %s: step %d %q: %w", r.Name, i, strings.Join(argv, " "), err)
		}
	}

	archs, env, err := be.post(src, r, o.Arch)
	if err != nil {
		return Meta{}, err
	}

	staged := filepath.Join(scratch, "staged")
	if err := stage(src, staged, r.Build.Stage); err != nil {
		return Meta{}, fmt.Errorf("build: %s: %w", r.Name, err)
	}
	rendered, err := manifest.Render(o.Manifest.Raw, archs, env)
	if err != nil {
		return Meta{}, err
	}
	if err := os.WriteFile(filepath.Join(staged, manifest.PackageManifestName), rendered, 0o644); err != nil {
		return Meta{}, err
	}

	base := FileBase(r.Name, o.Arch)
	res, err := pack.WriteFile(staged, filepath.Join(o.Out, base+".tar.gz"))
	if err != nil {
		return Meta{}, err
	}
	meta := Meta{
		Name:       r.Name,
		Version:    r.Version,
		OS:         "linux",
		Arch:       archs,
		BuiltFor:   o.Arch,
		SHA256:     res.SHA256,
		Size:       res.Size,
		Runtime:    r.Runtime,
		BuiltAt:    BuiltAt,
		BuildImage: o.BuildImage,
		Manifest:   json.RawMessage(rendered),
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Meta{}, err
	}
	if err := os.WriteFile(filepath.Join(o.Out, base+".meta.json"), append(b, '\n'), 0o644); err != nil {
		return Meta{}, err
	}
	logf("packed %s: sha256:%s, %d bytes, %d entries", base, res.SHA256, res.Size, res.Entries)
	return meta, nil
}

// DescribeSource is a one-line rendering of a pin, for a log or summary
// line. Exported so audit's report summary uses exactly the same rendering
// as a build's own log line, rather than a second copy that can drift.
func DescribeSource(s recipe.Source) string {
	switch s.Kind {
	case "git":
		return s.Repo + "@" + s.Commit
	case "npm", "pypi":
		return s.Kind + ":" + s.Package
	case "archive":
		return s.URL
	}
	return s.Kind
}

// ApplyOverlay copies serverDir/overlay's tree over dst, overwriting any
// file it also names; an absent overlay/ is not an error, since it's
// optional. Exported so audit reads exactly the tree a build would
// produce, through the one copyTree merge implementation, rather than a
// second copy that can drift from it.
func ApplyOverlay(serverDir, dst string) error {
	return copyTree(filepath.Join(serverDir, recipe.OverlayDir), dst, true)
}

func nonEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("the fetched source tree is empty")
	}
	return nil
}

// removeAll deletes a scratch tree even where a toolchain left read-only
// directories behind.
func removeAll(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755) //nolint:gosec // scratch tree, deleted next
		}
		return nil
	})
	return os.RemoveAll(dir)
}
