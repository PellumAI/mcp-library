package build

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pellumai/mcp-library/internal/recipe"
)

// nodeBackend vendors a node package: npm ci --omit=dev against the committed
// lockfile, then the clean-up that makes the tree reproducible and packable.
type nodeBackend struct{}

func (nodeBackend) check(work string, r recipe.Recipe) error {
	if filepath.Base(r.Build.Lockfile) != "package-lock.json" && filepath.Base(r.Build.Lockfile) != "npm-shrinkwrap.json" {
		return fmt.Errorf("build: %s: a node package's lockfile is package-lock.json, not %s", r.Name, r.Build.Lockfile)
	}
	// npm ci is the only npm verb that refuses to update the lockfile. A
	// recipe using npm install was refused by validate already; this is the
	// second check, at the layer that knows what it is looking at.
	if !slices.ContainsFunc(r.Build.Steps, func(s []string) bool { return len(s) >= 2 && s[0] == "npm" && s[1] == "ci" }) {
		return fmt.Errorf("build: %s: a node package is vendored by npm ci, and no step runs it", r.Name)
	}
	return nil
}

func (nodeBackend) post(work string, r recipe.Recipe, arch string) ([]string, map[string]string, error) {
	nm := filepath.Join(work, "node_modules")
	if _, err := os.Stat(nm); err != nil {
		return nil, nil, fmt.Errorf("build: %s: npm ci produced no node_modules", r.Name)
	}
	if err := cleanNodeModules(nm); err != nil {
		return nil, nil, fmt.Errorf("build: %s: %w", r.Name, err)
	}
	if err := relinkBin(work, filepath.Join(nm, ".bin")); err != nil {
		return nil, nil, fmt.Errorf("build: %s: %w", r.Name, err)
	}
	addons, err := nativeAddons(nm)
	if err != nil {
		return nil, nil, err
	}
	if len(addons) == 0 {
		return slices.Clone(r.Arch), nil, nil
	}
	// A compiled addon makes the tree arch-specific. npm ci fetched or built
	// it for the build container's own arch, which is amd64, so a build for
	// any other arch would ship the wrong binary.
	if arch != "amd64" {
		return nil, nil, fmt.Errorf("build: %s: native addons %v were built for amd64 and this build is for %s", r.Name, addons, arch)
	}
	fmt.Fprintf(os.Stderr, "build: %s: native addons %v make this package arch-specific; arch narrowed to %s\n", r.Name, addons, arch)
	return []string{arch}, nil, nil
}

// cleanNodeModules deletes the files npm and dependencies leave behind that
// carry a timestamp, a host path or a credential, and are never needed to run.
func cleanNodeModules(nm string) error {
	if err := os.Remove(filepath.Join(nm, ".package-lock.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	var doomed []string
	err := filepath.WalkDir(nm, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && d.Name() == ".cache":
			doomed = append(doomed, p)
			return fs.SkipDir
		case !d.IsDir() && (d.Name() == ".DS_Store" || d.Name() == ".npmrc"):
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

// relinkBin rewrites every node_modules/.bin symlink that points at an
// absolute path inside the work tree into a relative one, and refuses one
// that points anywhere else. pack.Refuse would reject the absolute form
// anyway; doing it here names the dependency rather than a path.
func relinkBin(work, bin string) error {
	entries, err := os.ReadDir(bin)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(bin, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		link, err := os.Readlink(p)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(link) {
			continue
		}
		host := link
		if strings.HasPrefix(link, containerSrc+"/") {
			host = filepath.Join(work, strings.TrimPrefix(link, containerSrc+"/"))
		}
		if !strings.HasPrefix(filepath.Clean(host)+"/", filepath.Clean(work)+"/") {
			return fmt.Errorf("node_modules/.bin/%s links to %s, outside the package, and cannot be made relative", e.Name(), link)
		}
		rel, err := filepath.Rel(bin, host)
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		if err := os.Symlink(rel, p); err != nil {
			return err
		}
	}
	return nil
}

// nativeAddons lists every compiled .node file under node_modules.
func nativeAddons(nm string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(nm, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".node") {
			rel, _ := filepath.Rel(nm, p)
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}
