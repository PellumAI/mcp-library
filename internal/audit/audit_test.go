package audit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/recipe"
)

// writeFixtureFile writes content at dir/rel, creating parent directories.
func writeFixtureFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeELF returns a minimal file starting with the ELF magic number, enough
// for hasBinaryMagic to recognise without being a valid, loadable ELF.
func fakeELF() string {
	return "\x7fELF" + "not a real binary, just the magic header for audit's detector"
}

// blockingPackageLock is the package-lock.json v3 fixture TestRun_BlockingRules
// and its siblings share: six dependencies, each isolating one signal.
const blockingPackageLock = `{
  "packages": {
    "": {},
    "node_modules/vuln-critical": {"version": "1.0.0", "license": "MIT"},
    "node_modules/vuln-high-fixed": {"version": "2.0.0", "license": "MIT"},
    "node_modules/vuln-high-nofix": {"version": "3.0.0", "license": "MIT"},
    "node_modules/gpl-dep": {"version": "6.0.0", "license": "GPL-3.0-only"},
    "node_modules/scripty": {"version": "5.0.0", "hasInstallScript": true}
  }
}`

// osvHandler serves a fixed querybatch response (by request order) and named
// vuln records, so a test can control severities precisely without touching
// the network.
func osvHandler(t *testing.T, batch string, vulns map[string]string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/querybatch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, batch)
	})
	mux.HandleFunc("/v1/vulns/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/v1/vulns/"):]
		rec, ok := vulns[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, rec)
	})
	return mux
}

func vulnRecord(id, databaseSeverity string, fixed string) string {
	f := ""
	if fixed != "" {
		f = fmt.Sprintf(`,{"fixed":%q}`, fixed)
	}
	return fmt.Sprintf(`{
  "id": %q,
  "affected": [{"ranges": [{"type": "SEMVER", "events": [{"introduced":"0"}%s]}]}],
  "database_specific": {"severity": %q}
}`, id, f, databaseSeverity)
}

func TestRun_BlockingRules(t *testing.T) {
	// ReadLock sorts deps by name: gpl-dep, scripty, vuln-critical,
	// vuln-high-fixed, vuln-high-nofix -- the batch response below is in
	// that same order.
	batch := `{"results": [
    {},
    {},
    {"vulns": [{"id": "CRIT-1"}]},
    {"vulns": [{"id": "HIGH-FIXED"}]},
    {"vulns": [{"id": "HIGH-NOFIX"}]}
  ]}`
	vulns := map[string]string{
		"CRIT-1":     vulnRecord("CRIT-1", "CRITICAL", ""),
		"HIGH-FIXED": vulnRecord("HIGH-FIXED", "HIGH", "2.0.1"),
		"HIGH-NOFIX": vulnRecord("HIGH-NOFIX", "HIGH", ""),
	}
	server := httptest.NewServer(osvHandler(t, batch, vulns))
	defer server.Close()

	fetch := func(_ context.Context, _ recipe.Source, dir string) error {
		writeFixtureFile(t, dir, "package-lock.json", blockingPackageLock)
		writeFixtureFile(t, dir, "package.json", `{"name": "fixture", "license": "MIT"}`)
		writeFixtureFile(t, dir, "bin/tool", fakeELF())
		writeFixtureFile(t, dir, "node_modules/foo/build/Release/foo.node", "not really a node addon, just a suffix")
		return nil
	}

	report, err := Run(context.Background(), Input{
		Resolve: "git:https://example.com/fixture.git@" + fortyHex,
		Fetch:   fetch,
		OSV:     OSV{BaseURL: server.URL, HTTP: server.Client()},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.DepCount != 5 {
		t.Errorf("DepCount = %d, want 5", report.DepCount)
	}
	if report.License != "MIT" {
		t.Errorf("License = %q, want MIT", report.License)
	}
	if len(report.Vulns) != 3 {
		t.Fatalf("got %d vulns, want 3", len(report.Vulns))
	}

	wantBlocking := []string{
		"CRIT-1: CRITICAL vulnerability in vuln-critical@1.0.0",
		"HIGH-FIXED: HIGH vulnerability in vuln-high-fixed@2.0.0, fixed in 2.0.1",
		"gpl-dep licence GPL-3.0-only refuses redistribution",
	}
	if len(report.Blocking) != len(wantBlocking) {
		t.Fatalf("Blocking = %v, want %v", report.Blocking, wantBlocking)
	}
	for i, line := range wantBlocking {
		if report.Blocking[i] != line {
			t.Errorf("Blocking[%d] = %q, want %q", i, report.Blocking[i], line)
		}
	}
	for _, unwanted := range []string{"HIGH-NOFIX"} {
		for _, line := range report.Blocking {
			if line != "" && line == unwanted {
				t.Errorf("HIGH with no fix must not block, got %q", line)
			}
		}
	}

	if got := report.Licenses[string(LicensePermits)]; len(got) != 3 {
		t.Errorf("permits licences = %v, want 3 entries (vuln-critical, vuln-high-fixed, vuln-high-nofix)", got)
	}
	if got := report.Licenses[string(LicenseRefuses)]; len(got) != 1 || got[0] != "gpl-dep" {
		t.Errorf("refuses licences = %v, want [gpl-dep]", got)
	}
	// scripty carries no licence field and must not be classified at all.
	for _, names := range report.Licenses {
		for _, n := range names {
			if n == "scripty" {
				t.Errorf("scripty has no licence field and must not appear in Licenses, got it under some class")
			}
		}
	}

	if len(report.Scripts) != 1 || report.Scripts[0] != "scripty@5.0.0" {
		t.Errorf("Scripts = %v, want [scripty@5.0.0]", report.Scripts)
	}

	wantBinaries := []string{"bin/tool", "node_modules/foo/build/Release/foo.node"}
	sort.Strings(wantBinaries)
	if len(report.Binaries) != len(wantBinaries) {
		t.Fatalf("Binaries = %v, want %v", report.Binaries, wantBinaries)
	}
	for i, b := range wantBinaries {
		if filepath.ToSlash(report.Binaries[i]) != b {
			t.Errorf("Binaries[%d] = %q, want %q", i, report.Binaries[i], b)
		}
	}

	if report.Lockfile != "package-lock.json" {
		t.Errorf("Lockfile = %q, want package-lock.json", report.Lockfile)
	}
}

const fortyHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRun_ServerMode_UsesRecipeAndOverlay(t *testing.T) {
	root := t.TempDir()
	serverDir := filepath.Join(root, "servers", "fixture-server")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipeYAML := `
schema_version: 1
name: fixture-server
version: 1.0.0
runtime: node@20
arch: [amd64]
source:
  kind: npm
  package: fixture-server@1.0.0
  integrity: sha512-fake
build:
  steps: [["npm", "ci"]]
  lockfile: package-lock.json
vetting:
  license: GPL-3.0-only
`
	if err := os.WriteFile(filepath.Join(serverDir, recipe.FileName), []byte(recipeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// The upstream fetch produces no lockfile of its own; overlay/ vendors
	// one, exactly as recipe.OverlayDir documents.
	writeFixtureFile(t, serverDir, "overlay/package-lock.json", `{
  "packages": {
    "": {},
    "node_modules/leaf": {"version": "1.0.0", "license": "MIT"}
  }
}`)

	fetched := false
	fetch := func(_ context.Context, src recipe.Source, dir string) error {
		fetched = true
		if src.Package != "fixture-server@1.0.0" {
			t.Errorf("fetch got source package %q, want fixture-server@1.0.0", src.Package)
		}
		return os.WriteFile(filepath.Join(dir, "README.md"), []byte("upstream ships no lockfile"), 0o644)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/querybatch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results": [{}]}`)
	})
	osvServer := httptest.NewServer(mux)
	defer osvServer.Close()

	report, err := Run(context.Background(), Input{
		Root:   root,
		Server: "fixture-server",
		Fetch:  fetch,
		OSV:    OSV{BaseURL: osvServer.URL, HTTP: osvServer.Client()},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !fetched {
		t.Fatal("Fetch was never called")
	}
	if report.Lockfile != "package-lock.json" {
		t.Errorf("Lockfile = %q, want package-lock.json", report.Lockfile)
	}
	if report.DepCount != 1 {
		t.Errorf("DepCount = %d, want 1 (the overlay lockfile's leaf dep)", report.DepCount)
	}
	if report.License != "GPL-3.0-only" {
		t.Errorf("License = %q, want GPL-3.0-only", report.License)
	}
	wantBlocking := []string{"server licence GPL-3.0-only refuses redistribution"}
	if len(report.Blocking) != 1 || report.Blocking[0] != wantBlocking[0] {
		t.Errorf("Blocking = %v, want %v", report.Blocking, wantBlocking)
	}
}

func TestRun_ServerMode_MissingDeclaredLockfileErrors(t *testing.T) {
	root := t.TempDir()
	serverDir := filepath.Join(root, "servers", "fixture-server")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipeYAML := `
schema_version: 1
name: fixture-server
version: 1.0.0
runtime: node@20
arch: [amd64]
source:
  kind: npm
  package: fixture-server@1.0.0
  integrity: sha512-fake
build:
  steps: [["npm", "ci"]]
  lockfile: package-lock.json
vetting:
  license: MIT
`
	if err := os.WriteFile(filepath.Join(serverDir, recipe.FileName), []byte(recipeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch := func(_ context.Context, _ recipe.Source, dir string) error { return nil }

	_, err := Run(context.Background(), Input{Root: root, Server: "fixture-server", Fetch: fetch})
	if err == nil {
		t.Fatal("expected an error for a declared lockfile absent from the fetched tree")
	}
}

func TestRun_ResolveNPM_LooksUpRegistryIntegrity(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/@scope%2Fpkg/1.2.3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"dist": {"integrity": "sha512-abc123", "tarball": "https://example.invalid/t.tgz"}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	fetch := func(_ context.Context, src recipe.Source, dir string) error {
		if src.Kind != "npm" || src.Package != "@scope/pkg@1.2.3" || src.Integrity != "sha512-abc123" {
			t.Errorf("fetch got unexpected source %+v", src)
		}
		return nil
	}

	report, err := Run(context.Background(), Input{
		Resolve:     "npm:@scope/pkg@1.2.3",
		NPMRegistry: server.URL,
		Fetch:       fetch,
		OSV:         OSV{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Source.Integrity != "sha512-abc123" {
		t.Errorf("Report.Source.Integrity = %q, want sha512-abc123", report.Source.Integrity)
	}
}

func TestRun_ResolvePyPI_LooksUpSdistSHA256(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/pypi/widget/2.0.0/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"urls": [
			{"packagetype": "bdist_wheel", "digests": {"sha256": "wheelsha"}},
			{"packagetype": "sdist", "digests": {"sha256": "sdistsha"}}
		]}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	fetch := func(_ context.Context, src recipe.Source, dir string) error {
		if src.Kind != "pypi" || src.Package != "widget==2.0.0" || src.Integrity != "sha256=sdistsha" {
			t.Errorf("fetch got unexpected source %+v", src)
		}
		return nil
	}

	report, err := Run(context.Background(), Input{
		Resolve:      "pypi:widget==2.0.0",
		PyPIRegistry: server.URL,
		Fetch:        fetch,
		OSV:          OSV{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Source.Integrity != "sha256=sdistsha" {
		t.Errorf("Report.Source.Integrity = %q, want sha256=sdistsha", report.Source.Integrity)
	}
}

func TestRun_ResolveGit_NoRegistryLookup(t *testing.T) {
	fetch := func(_ context.Context, src recipe.Source, dir string) error {
		if src.Kind != "git" || src.Repo != "https://example.com/repo.git" || src.Commit != fortyHex {
			t.Errorf("fetch got unexpected source %+v", src)
		}
		return nil
	}
	_, err := Run(context.Background(), Input{
		Resolve: "git:https://example.com/repo.git@" + fortyHex,
		Fetch:   fetch,
		OSV:     OSV{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRun_ResolveGit_RejectsShortCommit(t *testing.T) {
	_, err := Run(context.Background(), Input{
		Resolve: "git:https://example.com/repo.git@abc123",
		Fetch:   func(context.Context, recipe.Source, string) error { return nil },
	})
	if err == nil {
		t.Fatal("expected an error for a non-40-hex commit")
	}
}

func TestRun_NoLockfileIsNotBlocking(t *testing.T) {
	fetch := func(_ context.Context, _ recipe.Source, dir string) error {
		return os.WriteFile(filepath.Join(dir, "README.md"), []byte("no lockfile here"), 0o644)
	}
	report, err := Run(context.Background(), Input{
		Resolve: "git:https://example.com/repo.git@" + fortyHex,
		Fetch:   fetch,
		OSV:     OSV{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Lockfile != "" {
		t.Errorf("Lockfile = %q, want \"\"", report.Lockfile)
	}
	if report.DepCount != 0 {
		t.Errorf("DepCount = %d, want 0", report.DepCount)
	}
	if len(report.Blocking) != 0 {
		t.Errorf("Blocking = %v, want none: no lockfile is not itself blocking", report.Blocking)
	}
}

func TestRun_RequiresExactlyOneOfServerOrResolve(t *testing.T) {
	cases := []Input{
		{},
		{Server: "a", Resolve: "npm:a@1.0.0"},
	}
	for _, in := range cases {
		if _, err := Run(context.Background(), in); err == nil {
			t.Errorf("Run(%+v): expected an error", in)
		}
	}
}

func TestFindBinaries(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, dir, "bin/tool", fakeELF())
	writeFixtureFile(t, dir, "lib/thing.node", "addon body, no magic header needed")
	writeFixtureFile(t, dir, "README.md", "just text")
	writeFixtureFile(t, dir, "macho/a.out", "\xfe\xed\xfa\xcemacho body padding")

	got, err := findBinaries(dir)
	if err != nil {
		t.Fatalf("findBinaries: %v", err)
	}
	want := []string{"bin/tool", "lib/thing.node", "macho/a.out"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if filepath.ToSlash(got[i]) != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestSourceLicense_Precedence(t *testing.T) {
	// package.json wins when present.
	dir := t.TempDir()
	writeFixtureFile(t, dir, "package.json", `{"license": "MIT"}`)
	writeFixtureFile(t, dir, "pyproject.toml", "[project]\nlicense = \"Apache-2.0\"\n")
	if got := sourceLicense(dir); got != "MIT" {
		t.Errorf("sourceLicense = %q, want MIT", got)
	}

	// Without package.json, pyproject.toml wins over PKG-INFO.
	dir2 := t.TempDir()
	writeFixtureFile(t, dir2, "pyproject.toml", "[project]\nlicense = { text = \"Apache-2.0\" }\n")
	writeFixtureFile(t, dir2, "PKG-INFO", "Metadata-Version: 2.1\nLicense: MIT\n")
	if got := sourceLicense(dir2); got != "Apache-2.0" {
		t.Errorf("sourceLicense = %q, want Apache-2.0", got)
	}

	// PKG-INFO alone.
	dir3 := t.TempDir()
	writeFixtureFile(t, dir3, "PKG-INFO", "Metadata-Version: 2.1\nName: widget\nLicense: BSD-3-Clause\n")
	if got := sourceLicense(dir3); got != "BSD-3-Clause" {
		t.Errorf("sourceLicense = %q, want BSD-3-Clause", got)
	}

	// Nothing at all.
	dir4 := t.TempDir()
	if got := sourceLicense(dir4); got != "" {
		t.Errorf("sourceLicense = %q, want \"\"", got)
	}
}

func TestReportSummary_MentionsBlockingLast(t *testing.T) {
	r := Report{
		Source:   recipe.Source{Kind: "npm", Package: "x@1.0.0"},
		License:  "GPL-3.0-only",
		DepCount: 1,
		Blocking: []string{"server licence GPL-3.0-only refuses redistribution"},
	}
	s := r.Summary()
	depsIdx := strings.Index(s, "1 dependencies")
	blockingIdx := strings.Index(s, "BLOCKING: server licence GPL-3.0-only refuses redistribution")
	if depsIdx == -1 {
		t.Fatalf("Summary missing dependency count line: %s", s)
	}
	if blockingIdx == -1 {
		t.Fatalf("Summary missing blocking line: %s", s)
	}
	if blockingIdx < depsIdx {
		t.Errorf("Summary must print the blocking line last, got:\n%s", s)
	}

	clean := Report{DepCount: 0}
	if got := clean.Summary(); !strings.Contains(got, "no blocking findings") {
		t.Errorf("Summary with no findings should say so, got:\n%s", got)
	}
}
