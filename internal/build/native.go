package build

import (
	"debug/elf"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/pellumai/mcp-library/internal/recipe"
)

// nativeBackend packages statically linked binaries: a Go build with
// -trimpath -mod=readonly -ldflags "-s -w -buildid=" and CGO_ENABLED=0, or a
// prebuilt static binary from a pinned release archive. Either way a native
// package serves exactly the one arch it was built for.
type nativeBackend struct{}

func (nativeBackend) check(string, recipe.Recipe) error { return nil }

func (nativeBackend) post(work string, r recipe.Recipe, arch string) ([]string, map[string]string, error) {
	// Every ELF the stage rules will package must be static: a dynamically
	// linked binary will not find its loader inside the sandbox's binds,
	// and the failure would be a silent exec error.
	for _, s := range r.Build.Stage {
		from := filepath.Join(work, filepath.FromSlash(s.From))
		if err := refuseDynamic(from); err != nil {
			return nil, nil, fmt.Errorf("build: %s: %w", r.Name, err)
		}
	}
	return []string{arch}, nil, nil
}

// ErrDynamic marks an ELF with a PT_INTERP program header.
var ErrDynamic = errors.New("is dynamically linked; a native package must be static")

// refuseDynamic walks p, a file or a tree, and refuses any ELF carrying a
// PT_INTERP program header.
func refuseDynamic(p string) error {
	if _, err := os.Lstat(p); err != nil {
		return nil // an absent optional rule; stage reports a required one
	}
	return filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		return checkStatic(f)
	})
}

func checkStatic(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		// Not an ELF at all: a LICENSE, a script. Nothing to check.
		var fe *elf.FormatError
		if errors.As(err, &fe) {
			return nil
		}
		return nil
	}
	defer func() { _ = f.Close() }()
	if slices.ContainsFunc(f.Progs, func(p *elf.Prog) bool { return p.Type == elf.PT_INTERP }) {
		return fmt.Errorf("%s %w", filepath.Base(path), ErrDynamic)
	}
	return nil
}
