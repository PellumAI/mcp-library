package build

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pellumai/mcp-library/internal/recipe"
)

// pythonBackend vendors a python package into .venv without producing a
// virtual environment, which is a distinction worth stating plainly.
//
// A venv's bin/python is a copy of, or a symlink to, an interpreter, and a
// package must not carry an interpreter -- the sandbox binds exactly one,
// read-only, at /opt/mcpgw/runtimes/<line>. What the tree needs instead is
// for that bound interpreter to find the vendored packages, which is
// PYTHONPATH's job, and for any console script to start with a shebang naming
// the bound interpreter rather than a build-time path.
//
// The directory is still called .venv because the spec's Layout section names
// it and an operator reading a package tree should find what the document
// told them to expect. docs/PACKAGE-FORMAT.md says what is really in it.
//
// The vendoring itself is the recipe's step:
//
//	python3 -m pip install --require-hashes --no-compile --no-deps
//	  --target .venv/lib/python<X.Y>/site-packages -r <lockfile>
type pythonBackend struct{ line string }

// srvDir is where the executor binds the unpacked tree.
const srvDir = "/srv"

var hashLineRE = regexp.MustCompile(`--hash=sha256:[0-9a-f]{64}`)

func (b pythonBackend) version() string { return strings.TrimPrefix(b.line, "python@") }

// SitePackages is the vendored tree's path, relative to the package root.
func (b pythonBackend) SitePackages() string {
	return ".venv/lib/python" + b.version() + "/site-packages"
}

func (b pythonBackend) check(work string, r recipe.Recipe) error {
	lock, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(r.Build.Lockfile)))
	if err != nil {
		return err
	}
	if !hashLineRE.Match(lock) {
		return fmt.Errorf("build: %s: the requirements file carries no hashes; compile it with pip-compile --generate-hashes", r.Name)
	}
	// Every requirement line must be pinned with ==, because a hash pins the
	// file only once pip has chosen which file to fetch.
	sc := bufio.NewScanner(bytes.NewReader(lock))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "--hash") || strings.HasPrefix(line, "\\") {
			continue
		}
		req := strings.TrimSpace(strings.TrimSuffix(strings.Fields(line)[0], "\\"))
		if req == "" || strings.HasPrefix(req, "-") {
			continue
		}
		if !strings.Contains(req, "==") {
			return fmt.Errorf("build: %s: requirement %q is not pinned with ==", r.Name, req)
		}
	}
	return sc.Err()
}

func (b pythonBackend) post(work string, r recipe.Recipe, _ string) ([]string, map[string]string, error) {
	venv := filepath.Join(work, ".venv")
	site := filepath.Join(work, filepath.FromSlash(b.SitePackages()))
	if _, err := os.Stat(site); err != nil {
		return nil, nil, fmt.Errorf("build: %s: no %s after the steps; the pip step must install with --target %s", r.Name, b.SitePackages(), b.SitePackages())
	}
	if err := cleanPython(venv); err != nil {
		return nil, nil, fmt.Errorf("build: %s: %w", r.Name, err)
	}
	interp := runtimeRoot + "/" + b.line + "/bin/python3"
	if err := rewriteShebangs(venv, interp); err != nil {
		return nil, nil, fmt.Errorf("build: %s: %w", r.Name, err)
	}
	env := map[string]string{"PYTHONPATH": srvDir + "/" + b.SitePackages()}
	return append([]string(nil), r.Arch...), env, nil
}

// cleanPython deletes the four kinds of file that carry a timestamp or a
// build-time path: __pycache__, *.pyc, *.dist-info/RECORD and
// *.dist-info/direct_url.json.
func cleanPython(venv string) error {
	var doomed []string
	err := filepath.WalkDir(venv, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		parent := filepath.Base(filepath.Dir(p))
		switch {
		case d.IsDir() && name == "__pycache__":
			doomed = append(doomed, p)
			return fs.SkipDir
		case !d.IsDir() && strings.HasSuffix(name, ".pyc"):
			doomed = append(doomed, p)
		case !d.IsDir() && strings.HasSuffix(parent, ".dist-info") && (name == "RECORD" || name == "direct_url.json"):
			doomed = append(doomed, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range doomed {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// rewriteShebangs points every script under .venv whose shebang names a
// python at the interpreter the sandbox binds.
func rewriteShebangs(venv, interp string) error {
	return filepath.WalkDir(venv, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		if filepath.Base(filepath.Dir(p)) != "bin" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(b, []byte("#!")) {
			return nil
		}
		first, rest, _ := bytes.Cut(b, []byte("\n"))
		if !bytes.Contains(first, []byte("python")) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out := append([]byte("#!"+interp+"\n"), rest...)
		return os.WriteFile(p, out, fi.Mode().Perm())
	})
}
