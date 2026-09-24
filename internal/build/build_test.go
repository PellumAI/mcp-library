package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/pack"
	"github.com/pellumai/mcp-library/internal/recipe"
)

const image = "ghcr.io/pellumai/mcp-library/build@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"

// recorder is a Runner that records each step and runs a function standing in
// for what the step would have done in the container.
type recorder struct {
	specs []StepSpec
	do    func(spec StepSpec) error
}

func (r *recorder) Run(_ context.Context, s StepSpec) error {
	r.specs = append(r.specs, s)
	if r.do != nil {
		return r.do(s)
	}
	return nil
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fakeFetch(files map[string]string) func(context.Context, recipe.Source, string) error {
	return func(_ context.Context, _ recipe.Source, dir string) error {
		for name, body := range files {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

const nativeManifest = `{"schema_version":1,"name":"echo","version":"1.0.0","title":"Echo","runtime":"native","os":"linux",
"arch":["amd64","arm64"],"entrypoint":["bin/echo"],"transport":"stdio","path":"/mcp","resources":{"memory_max":"64Mi","pids_max":16}}`

func nativeOptions(t *testing.T, rec *recorder) Options {
	t.Helper()
	m, err := manifest.Parse([]byte(nativeManifest))
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		ServerDir: t.TempDir(),
		Recipe: recipe.Recipe{
			SchemaVersion: 1, Name: "echo", Version: "1.0.0", Runtime: "native", Arch: []string{"amd64", "arm64"},
			Source: recipe.Source{Kind: "git", Repo: "https://example.com/echo", Commit: strings.Repeat("a", 40)},
			Build: recipe.Build{
				Lockfile: "go.sum",
				Steps:    [][]string{{"go", "mod", "download"}, {"go", "build", "-o", "out/echo", "."}},
				Stage: []recipe.StageRule{
					{From: "out/echo", To: "bin/echo"},
					{From: "LICENSE", To: "LICENSE"},
					{From: "NOTICE", To: "NOTICE", Optional: true},
				},
			},
		},
		Manifest:   m,
		Arch:       "amd64",
		Out:        t.TempDir(),
		BuildImage: image,
		Runner:     rec,
		Fetch:      fakeFetch(map[string]string{"main.go": "package main", "go.sum": "", "LICENSE": "MIT", "extra.txt": "not staged"}),
		WorkDir:    t.TempDir(),
	}
}

func tarEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

// TestRun_Native drives the whole pipeline with a recorder in place of docker.
func TestRun_Native(t *testing.T) {
	rec := &recorder{do: func(s StepSpec) error {
		if s.Argv[1] == "build" {
			return os.WriteFile(filepath.Join(s.Work, "src", "out", "echo"), []byte("#!not-an-elf"), 0o755)
		}
		return os.MkdirAll(filepath.Join(s.Work, "src", "out"), 0o755)
	}}
	o := nativeOptions(t, rec)
	meta, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Run("the network is attached only to go mod download", func(t *testing.T) {
		if !rec.specs[0].Network || rec.specs[1].Network {
			t.Fatalf("network flags %v, %v", rec.specs[0].Network, rec.specs[1].Network)
		}
	})

	t.Run("the staged tree is exactly the stage rules plus the manifest", func(t *testing.T) {
		got := tarEntries(t, filepath.Join(o.Out, "echo-amd64.tar.gz"))
		var names []string
		for n := range got {
			names = append(names, n)
		}
		slices.Sort(names)
		want := []string{"LICENSE", "bin/", "bin/echo", "mcpgw-package.json"}
		if !slices.Equal(names, want) {
			t.Fatalf("entries %v, want %v", names, want)
		}
	})

	t.Run("a native build narrows arch to the one it built", func(t *testing.T) {
		if !slices.Equal(meta.Arch, []string{"amd64"}) {
			t.Fatalf("meta arch %v", meta.Arch)
		}
		got := tarEntries(t, filepath.Join(o.Out, "echo-amd64.tar.gz"))
		var m struct {
			Arch []string `json:"arch"`
		}
		if err := json.Unmarshal(got["mcpgw-package.json"], &m); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(m.Arch, []string{"amd64"}) {
			t.Fatalf("packaged manifest arch %v", m.Arch)
		}
		if !bytes.Equal(got["mcpgw-package.json"], meta.Manifest) {
			t.Fatal("the sidecar's manifest is not the packaged bytes")
		}
	})

	t.Run("the sidecar digest is the digest of the file on disk", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(o.Out, "echo-amd64.tar.gz"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if meta.SHA256 != hex.EncodeToString(sum[:]) || meta.Size != int64(len(b)) {
			t.Fatalf("meta %s/%d does not describe the file", meta.SHA256, meta.Size)
		}
		var onDisk Meta
		raw, err := os.ReadFile(filepath.Join(o.Out, "echo-amd64.meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &onDisk); err != nil || onDisk.SHA256 != meta.SHA256 || onDisk.BuiltAt != BuiltAt {
			t.Fatalf("meta.json %+v, %v", onDisk, err)
		}
	})
}

func TestRun_IsReproducible(t *testing.T) {
	do := func(s StepSpec) error {
		_ = os.MkdirAll(filepath.Join(s.Work, "src", "out"), 0o755)
		return os.WriteFile(filepath.Join(s.Work, "src", "out", "echo"), []byte("binary"), 0o755)
	}
	a := nativeOptions(t, &recorder{do: do})
	b := nativeOptions(t, &recorder{do: do})
	ma, err := Run(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := Run(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if ma.SHA256 != mb.SHA256 {
		t.Fatalf("two builds differ: %s vs %s", ma.SHA256, mb.SHA256)
	}
}

func TestRun_RefusesAnUndeclaredArch(t *testing.T) {
	o := nativeOptions(t, &recorder{})
	o.Arch = "riscv64"
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("an undeclared arch builds")
	}
}

func TestRun_RefusesAMissingLockfile(t *testing.T) {
	o := nativeOptions(t, &recorder{})
	o.Fetch = fakeFetch(map[string]string{"main.go": "package main"})
	if _, err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "lockfile") {
		t.Fatalf("got %v, want a missing-lockfile refusal", err)
	}
}

func TestRun_OverlayReplacesSourceFiles(t *testing.T) {
	var seen string
	rec := &recorder{do: func(s StepSpec) error {
		b, _ := os.ReadFile(filepath.Join(s.Work, "src", "go.sum"))
		seen = string(b)
		_ = os.MkdirAll(filepath.Join(s.Work, "src", "out"), 0o755)
		return os.WriteFile(filepath.Join(s.Work, "src", "out", "echo"), []byte("x"), 0o755)
	}}
	o := nativeOptions(t, rec)
	writeFiles(t, filepath.Join(o.ServerDir, "overlay"), map[string]string{"go.sum": "from the overlay"})
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if seen != "from the overlay" {
		t.Fatalf("the steps saw go.sum %q", seen)
	}
}

// TestDockerArgs asserts the argv field by field, because every flag on it is
// load-bearing.
func TestDockerArgs(t *testing.T) {
	args := DockerArgs(StepSpec{
		Image: image, Work: "/tmp/w", Argv: []string{"go", "build", "./..."},
		Arch: "arm64", Runtime: "node@22",
	}, 1001, 1002)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm --network none",
		"--user 1001:1002",
		"--workdir /build/src",
		"--mount type=bind,src=/tmp/w,dst=/build",
		"--env SOURCE_DATE_EPOCH=0",
		"--env TZ=UTC",
		"--env GOARCH=arm64",
		"--env CGO_ENABLED=0",
		"--env PATH=/opt/mcpgw/runtimes/node@22/bin:",
		image + " go build ./...",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q:\n%s", want, joined)
		}
	}
	native := strings.Join(DockerArgs(StepSpec{Image: image, Work: "/w", Argv: []string{"npm", "ci", "--prefix", "ui"}, Arch: "amd64", Runtime: "native", Toolchains: []string{"node@22"}, Network: true}, 1, 1), " ")
	if !strings.Contains(native, "--env PATH=/opt/mcpgw/runtimes/node@22/bin:/usr/local/go/bin:") {
		t.Errorf("a native build's toolchain is not on PATH:\n%s", native)
	}
	net := DockerArgs(StepSpec{Image: image, Work: "/w", Argv: []string{"npm", "ci"}, Arch: "amd64", Runtime: "node@22", Network: true}, 1, 1)
	if slices.Contains(net, "none") {
		t.Fatal("npm ci runs without the network")
	}
}

// packageTar gzips a tar of the given headers, each regular file carrying
// body as its content.
func packageTar(t *testing.T, hdrs []*tar.Header, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnpack(t *testing.T) {
	good := packageTar(t, []*tar.Header{
		{Name: "bin", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "bin/server", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: manifest.PackageManifestName, Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "bin/alias", Typeflag: tar.TypeSymlink, Linkname: "server"},
	}, "x")
	dir := t.TempDir()
	if err := Unpack(good, dir); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "bin", "server"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("bin/server: %v, mode %v", err, fi)
	}
	if link, err := os.Readlink(filepath.Join(dir, "bin", "alias")); err != nil || link != "server" {
		t.Errorf("bin/alias -> %q, %v", link, err)
	}

	escape := packageTar(t, []*tar.Header{
		{Name: "evil", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
	}, "")
	if err := Unpack(escape, t.TempDir()); !errors.Is(err, pack.ErrRefused) {
		t.Errorf("Unpack of a symlink escape = %v, want pack.ErrRefused", err)
	}
}
