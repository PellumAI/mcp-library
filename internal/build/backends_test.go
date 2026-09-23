package build

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/recipe"
)

func TestNode_CleansAndRelinks(t *testing.T) {
	work := t.TempDir()
	nm := filepath.Join(work, "node_modules")
	writeFiles(t, work, map[string]string{
		"node_modules/.package-lock.json":  "{}",
		"node_modules/dep/.cache/x":        "cache",
		"node_modules/dep/.npmrc":          "//registry:_authToken=secret",
		"node_modules/dep/.DS_Store":       "x",
		"node_modules/dep/index.js":        "module.exports = 1",
		"node_modules/dep/bin/cli.js":      "#!/usr/bin/env node",
		"node_modules/other/lib/keep.json": "{}",
	})
	if err := os.MkdirAll(filepath.Join(nm, ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// npm ci inside the container writes absolute links under /build/src.
	if err := os.Symlink("/build/src/node_modules/dep/bin/cli.js", filepath.Join(nm, ".bin", "dep")); err != nil {
		t.Fatal(err)
	}
	r := recipe.Recipe{Name: "n", Arch: []string{"amd64", "arm64"}}
	archs, _, err := nodeBackend{}.post(work, r, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{".package-lock.json", "dep/.cache", "dep/.npmrc", "dep/.DS_Store"} {
		if _, err := os.Lstat(filepath.Join(nm, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived", gone)
		}
	}
	link, err := os.Readlink(filepath.Join(nm, ".bin", "dep"))
	if err != nil || link != "../dep/bin/cli.js" {
		t.Fatalf(".bin/dep links to %q, %v; want ../dep/bin/cli.js", link, err)
	}
	if !slices.Equal(archs, []string{"amd64", "arm64"}) {
		t.Fatalf("a pure-JS tree narrowed arch to %v", archs)
	}
}

func TestNode_NativeAddonNarrowsArch(t *testing.T) {
	work := t.TempDir()
	writeFiles(t, work, map[string]string{"node_modules/x/build/Release/x.node": "\x7fELF"})
	r := recipe.Recipe{Name: "n", Arch: []string{"amd64", "arm64"}}
	archs, _, err := nodeBackend{}.post(work, r, "amd64")
	if err != nil || !slices.Equal(archs, []string{"amd64"}) {
		t.Fatalf("got %v, %v; want [amd64]", archs, err)
	}
	if _, _, err := (nodeBackend{}).post(work, r, "arm64"); err == nil {
		t.Fatal("an amd64 addon is shipped in an arm64 build")
	}
}

func TestNode_RequiresNpmCi(t *testing.T) {
	r := recipe.Recipe{Name: "n", Build: recipe.Build{Lockfile: "package-lock.json", Steps: [][]string{{"node", "build.js"}}}}
	if err := (nodeBackend{}).check(t.TempDir(), r); err == nil {
		t.Fatal("a node recipe with no npm ci is accepted")
	}
}

func TestPython_ShebangsCachesAndPath(t *testing.T) {
	work := t.TempDir()
	b := pythonBackend{line: "python@3.12"}
	site := b.SitePackages()
	writeFiles(t, work, map[string]string{
		site + "/pkg/__init__.py":                   "",
		site + "/pkg/__pycache__/x.cpython-312.pyc": "x",
		site + "/pkg/stray.pyc":                     "x",
		site + "/pkg-1.0.dist-info/RECORD":          "paths",
		site + "/pkg-1.0.dist-info/direct_url.json": "{}",
		site + "/pkg-1.0.dist-info/METADATA":        "keep",
		site + "/bin/pkg-cli":                       "#!/build/src/.venv/bin/python3\nimport pkg\n",
		site + "/bin/plain":                         "#!/bin/sh\necho hi\n",
	})
	_, env, err := b.post(work, recipe.Recipe{Name: "p", Arch: []string{"amd64"}}, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"pkg/__pycache__", "pkg/stray.pyc", "pkg-1.0.dist-info/RECORD", "pkg-1.0.dist-info/direct_url.json"} {
		if _, err := os.Lstat(filepath.Join(work, site, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(work, site, "pkg-1.0.dist-info", "METADATA")); err != nil {
		t.Error("METADATA was removed")
	}
	cli, _ := os.ReadFile(filepath.Join(work, site, "bin", "pkg-cli"))
	if !strings.HasPrefix(string(cli), "#!/opt/mcpgw/runtimes/python@3.12/bin/python3\n") {
		t.Fatalf("shebang not rewritten: %q", cli)
	}
	plain, _ := os.ReadFile(filepath.Join(work, site, "bin", "plain"))
	if !strings.HasPrefix(string(plain), "#!/bin/sh") {
		t.Fatalf("a non-python shebang was rewritten: %q", plain)
	}
	if env["PYTHONPATH"] != "/srv/.venv/lib/python3.12/site-packages" {
		t.Fatalf("PYTHONPATH is %q", env["PYTHONPATH"])
	}
}

func TestPython_RefusesAHashFreeRequirementsFile(t *testing.T) {
	work := t.TempDir()
	writeFiles(t, work, map[string]string{"requirements.txt": "six==1.17.0\n"})
	err := pythonBackend{line: "python@3.12"}.check(work, recipe.Recipe{Name: "p", Build: recipe.Build{Lockfile: "requirements.txt"}})
	if err == nil || !strings.Contains(err.Error(), "pip-compile --generate-hashes") {
		t.Fatalf("got %v", err)
	}
}

func TestPython_RefusesAnUnpinnedRequirement(t *testing.T) {
	work := t.TempDir()
	writeFiles(t, work, map[string]string{"requirements.txt": "six>=1 \\\n    --hash=sha256:" + strings.Repeat("a", 64) + "\n"})
	err := pythonBackend{line: "python@3.12"}.check(work, recipe.Recipe{Name: "p", Build: recipe.Build{Lockfile: "requirements.txt"}})
	if err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("got %v", err)
	}
}

// minimalELF is a 64-bit little-endian ELF header plus one program header, of
// type PT_INTERP when dynamic is set and PT_LOAD otherwise. It exists so the
// refusal is testable without shipping a real binary.
func minimalELF(dynamic bool) []byte {
	var b bytes.Buffer
	ident := []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}
	b.Write(ident)
	b.Write(make([]byte, 8))
	le := binary.LittleEndian
	w16 := func(v uint16) { _ = binary.Write(&b, le, v) }
	w32 := func(v uint32) { _ = binary.Write(&b, le, v) }
	w64 := func(v uint64) { _ = binary.Write(&b, le, v) }
	w16(2)    // ET_EXEC
	w16(62)   // EM_X86_64
	w32(1)    // EV_CURRENT
	w64(0)    // entry
	w64(64)   // phoff
	w64(0)    // shoff
	w32(0)    // flags
	w16(64)   // ehsize
	w16(56)   // phentsize
	w16(1)    // phnum
	w16(64)   // shentsize
	w16(0)    // shnum
	w16(0)    // shstrndx
	typ := uint32(1) // PT_LOAD
	if dynamic {
		typ = 3 // PT_INTERP
	}
	w32(typ)
	w32(4)    // flags
	w64(0)    // offset
	w64(0)    // vaddr
	w64(0)    // paddr
	w64(0)    // filesz
	w64(0)    // memsz
	w64(1)    // align
	return b.Bytes()
}

func TestNative_RefusesADynamicBinary(t *testing.T) {
	work := t.TempDir()
	writeFiles(t, work, map[string]string{"out/static": string(minimalELF(false)), "out/dynamic": string(minimalELF(true)), "LICENSE": "MIT"})
	r := recipe.Recipe{Name: "x", Build: recipe.Build{Stage: []recipe.StageRule{{From: "out/static", To: "bin/static"}, {From: "LICENSE", To: "LICENSE"}}}}
	if _, _, err := (nativeBackend{}).post(work, r, "amd64"); err != nil {
		t.Fatalf("a static binary is refused: %v", err)
	}
	r.Build.Stage = append(r.Build.Stage, recipe.StageRule{From: "out/dynamic", To: "bin/dynamic"})
	if _, _, err := (nativeBackend{}).post(work, r, "amd64"); !errors.Is(err, ErrDynamic) {
		t.Fatalf("got %v, want ErrDynamic", err)
	}
}
