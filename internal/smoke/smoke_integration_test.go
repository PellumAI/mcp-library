//go:build integration

package smoke

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/recipe"
	"github.com/pellumai/mcp-library/internal/target"
)

// These tests run real containers: each fixture is built by the real builder
// in the build image, then smoke-tested by Run against a real proxy sidecar.
// They skip when docker is unreachable. MCPLIB_SMOKE_IMAGE overrides the
// pinned build image, for a machine that cannot pull it.

var update = flag.Bool("update", false, "rewrite testdata/*.tools.snapshot.json from this run")

const repoRoot = "../.."

var env struct {
	once   sync.Once
	skip   string
	err    error
	image  string
	mcplib string
	dir    string

	mu   sync.Mutex
	tars map[string]string
}

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if env.dir != "" {
		_ = os.RemoveAll(env.dir)
	}
	os.Exit(code)
}

// setup finds docker and the image, and builds a static mcplib for the proxy
// sidecar, once for the whole package.
func setup(t *testing.T) {
	t.Helper()
	env.once.Do(func() {
		if err := exec.Command("docker", "info").Run(); err != nil {
			env.skip = fmt.Sprintf("docker info: %v", err)
			return
		}
		env.image = os.Getenv("MCPLIB_SMOKE_IMAGE")
		if env.image == "" {
			tg, err := target.Load(repoRoot)
			if err != nil {
				env.err = err
				return
			}
			env.image = tg.BuildImage
		}
		dir, err := os.MkdirTemp("", "mcplib-smoke-test-")
		if err != nil {
			env.err = err
			return
		}
		env.dir = dir
		env.mcplib = filepath.Join(dir, "mcplib")
		cmd := exec.Command("go", "build", "-trimpath", "-o", env.mcplib, "./cmd/mcplib")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			env.err = fmt.Errorf("build mcplib: %v: %s", err, out)
		}
		env.tars = map[string]string{}
	})
	if env.skip != "" {
		t.Skip(env.skip)
	}
	if env.err != nil {
		t.Fatal(env.err)
	}
}

// fixtureTar builds internal/fixture/servers/<name> the way mcplib fixture
// does, from its src/ tree, and returns the tar's path.
func fixtureTar(t *testing.T, name string) string {
	t.Helper()
	env.mu.Lock()
	defer env.mu.Unlock()
	if p, ok := env.tars[name]; ok {
		return p
	}
	tg, err := target.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	schemaBytes, err := tg.Schema()
	if err != nil {
		t.Fatal(err)
	}
	schema, err := manifest.CompileSchema(schemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repoRoot, "internal", "fixture", "servers", name)
	r, err := recipe.Load(filepath.Join(dir, recipe.FileName))
	if err != nil {
		t.Fatal(err)
	}
	r.Source = recipe.Source{Kind: "git", Repo: "https://github.com/PellumAI/mcp-library", Commit: strings.Repeat("0", 40)}
	raw, err := os.ReadFile(filepath.Join(dir, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(raw); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := recipe.Validate(r, m, tg); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out := filepath.Join(env.dir, "dist")
	src, err := filepath.Abs(filepath.Join(dir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = build.Run(ctx, build.Options{
		ServerDir:  dir,
		Recipe:     r,
		Manifest:   m,
		Arch:       "amd64",
		Out:        out,
		BuildImage: env.image,
		Runner:     build.Docker{Stdout: &log, Stderr: &log},
		Fetch: func(_ context.Context, _ recipe.Source, into string) error {
			return os.CopyFS(into, os.DirFS(src))
		},
	})
	if err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, log.Bytes())
	}
	p := filepath.Join(out, build.FileBase(name, "amd64")+".tar.gz")
	env.tars[name] = p
	return p
}

// run smoke-tests a fixture against snapshot.
func run(t *testing.T, name, snapshot string, write bool) Report {
	t.Helper()
	setup(t)
	tar := fixtureTar(t, name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rep, err := Run(ctx, Input{
		Tar:           tar,
		Name:          name,
		Snapshot:      snapshot,
		WriteSnapshot: write,
		Image:         env.image,
		MCPLib:        env.mcplib,
		Log:           t.Logf,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	logged := rep
	logged.Stderr = nil
	t.Logf("report: %+v\nstderr: %s", logged, rep.Stderr)
	return rep
}

func testdataSnapshot(name string) string {
	return filepath.Join("testdata", name+".tools.snapshot.json")
}

func assertPass(t *testing.T, rep Report) {
	t.Helper()
	if rep.Verdict != VerdictPass || len(rep.Failures) != 0 {
		t.Fatalf("verdict %s, failures %q", rep.Verdict, rep.Failures)
	}
	if !slices.Contains(SupportedProtocolVersions, rep.Protocol) {
		t.Errorf("protocol %q", rep.Protocol)
	}
	if len(rep.Egress) != 0 {
		t.Errorf("egress attempts from a server that dials nothing: %+v", rep.Egress)
	}
}

func hasFailure(rep Report, sub string) bool {
	return slices.ContainsFunc(rep.Failures, func(f string) bool { return strings.Contains(f, sub) })
}

func TestSmoke_FixtureEcho(t *testing.T) {
	rep := run(t, "fixture-echo", testdataSnapshot("fixture-echo"), *update)
	assertPass(t, rep)
	if rep.ToolCount != 1 || rep.Mode != ModeFull {
		t.Errorf("tool count %d, mode %s; want 1 tool in full mode", rep.ToolCount, rep.Mode)
	}
}

func TestSmoke_FixtureCount(t *testing.T) {
	rep := run(t, "fixture-count", testdataSnapshot("fixture-count"), *update)
	assertPass(t, rep)
	if rep.ToolCount < 1 {
		t.Errorf("tool count %d", rep.ToolCount)
	}
}

func TestSmoke_Dialer(t *testing.T) {
	rep := run(t, "fixture-dialer", filepath.Join(t.TempDir(), SnapshotName), true)
	if rep.Verdict != VerdictFail {
		t.Fatalf("verdict %s, want fail", rep.Verdict)
	}
	if !slices.ContainsFunc(rep.Egress, func(a Attempt) bool { return a.Host == "example.com" && !a.Allowed }) {
		t.Errorf("no denied example.com attempt in %+v", rep.Egress)
	}
	if !hasFailure(rep, "egress: denied example.com:443") {
		t.Errorf("failures %q lack the denied attempt", rep.Failures)
	}
}

func TestSmoke_Crash(t *testing.T) {
	rep := run(t, "fixture-crash", filepath.Join(t.TempDir(), SnapshotName), false)
	if rep.Verdict != VerdictFail {
		t.Fatalf("verdict %s, want fail", rep.Verdict)
	}
	if !hasFailure(rep, "initialize: the package closed the session") {
		t.Errorf("failures %q lack the unanswered initialize", rep.Failures)
	}
	if !hasFailure(rep, "exited before shutdown, with exit code 3") {
		t.Errorf("failures %q lack the exit before shutdown", rep.Failures)
	}
	if !strings.Contains(string(rep.Stderr), "exiting during initialize") {
		t.Errorf("stderr tail %q", rep.Stderr)
	}
}

func TestSmoke_SnapshotDrift(t *testing.T) {
	b, err := os.ReadFile(testdataSnapshot("fixture-echo"))
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(b, []byte("Return the text it is given."), []byte("Return the text, edited."), 1)
	if bytes.Equal(edited, b) {
		t.Fatal("the committed snapshot no longer carries the description this test edits")
	}
	path := filepath.Join(t.TempDir(), SnapshotName)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	rep := run(t, "fixture-echo", path, false)
	if rep.Verdict != VerdictFail || !hasFailure(rep, "snapshot drift") || len(rep.Failures) != 1 {
		t.Fatalf("verdict %s, failures %q; want only snapshot drift", rep.Verdict, rep.Failures)
	}
}
