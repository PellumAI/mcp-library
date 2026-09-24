package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/recipe"
)

// defaultNPMRegistry and defaultPyPIRegistry are used when Input leaves the
// registry fields empty.
const (
	defaultNPMRegistry  = "https://registry.npmjs.org"
	defaultPyPIRegistry = "https://pypi.org"
)

// resolveLockfileNames is the order --resolve mode searches the fetched
// tree in, since there is no recipe.Build.Lockfile to name one directly.
var resolveLockfileNames = []string{
	"package-lock.json",
	"npm-shrinkwrap.json",
	"requirements.txt",
	"go.sum",
}

// Report is `mcplib audit`'s deterministic result for one server, whether
// loaded from servers/<name> or resolved ahead of a request being accepted:
// its pin, its licence, its locked dependencies' vulnerabilities and
// licences, and the findings that block approval.
type Report struct {
	Source recipe.Source `json:"source"`
	// Lockfile is the path, relative to the fetched tree, of the lockfile
	// this report's dependency evidence came from, or "" when none was
	// found. No lockfile is not itself blocking -- evaluation decides
	// whether that needs an overlay.
	Lockfile string `json:"lockfile"`
	// License is the server's own licence: recipe.Vetting.License for
	// --server, or the fetched tree's own declared licence for --resolve.
	License  string `json:"license"`
	DepCount int    `json:"dep_count"`
	Vulns    []Vuln `json:"vulns"`
	// Licenses groups dependency names by licence class (permits, review,
	// refuses). A dependency whose lockfile entry carries no licence field
	// is left out entirely, never defaulted into "review".
	Licenses map[string][]string `json:"licenses"`
	// Scripts lists "<name>@<version>" for every dependency whose lockfile
	// entry marks an install or lifecycle script.
	Scripts []string `json:"install_scripts"`
	// Binaries lists paths, relative to the fetched tree, of every ELF or
	// Mach-O file and every .node addon.
	Binaries []string `json:"binaries"`
	// Blocking is one line per finding that must stop approval: an OSV
	// CRITICAL, an OSV HIGH with a fixed version available, or a refusing
	// licence (the server's own, or any dependency's). A non-empty
	// Blocking is `mcplib audit`'s exit-1 condition.
	Blocking []string `json:"blocking"`
}

// Input is everything Run needs to gather evidence, and nothing it fetches
// on its own initiative. Exactly one of Server or Resolve selects the
// source. Fetch and the registry URLs are overridable so tests never touch
// the network.
type Input struct {
	// Root is the repository root; Server names a servers/<name> under it.
	Root string
	// Server is the name under servers/ to audit, from its committed
	// recipe. Mutually exclusive with Resolve.
	Server string
	// Resolve is a "<kind>:<coordinate>" source spec -- npm:<name>@<exact
	// version>, pypi:<name>==<exact version>, or git:<https repo
	// url>@<40-hex commit> -- audited before servers/<name> exists.
	Resolve string

	// Fetch places the pinned source into a directory. Nil means
	// build.FetchSource.
	Fetch func(ctx context.Context, src recipe.Source, dir string) error
	// NPMRegistry and PyPIRegistry are the registries a Resolve coordinate
	// is looked up against, as fields so tests point them at an httptest
	// server. Empty means the public registry.
	NPMRegistry  string
	PyPIRegistry string
	// OSV queries the vulnerability database. The zero value talks to the
	// public API.
	OSV OSV
	// WorkDir is the parent of the scratch fetch tree. Empty means a
	// directory under os.TempDir.
	WorkDir string
}

// Run resolves one server's source, fetches it, reads its lockfile, and
// composes OSV and licence evidence into a Report. It never mutates
// servers/<name>: everything it reads about the source comes from a scratch
// fetch, the same one `mcplib build` would make.
func Run(ctx context.Context, in Input) (Report, error) {
	if (in.Server == "") == (in.Resolve == "") {
		return Report{}, fmt.Errorf("audit: exactly one of --server or --resolve is required")
	}

	var (
		src            recipe.Source
		serverLicense  string
		overlayDir     string
		recipeLockfile string
	)
	if in.Server != "" {
		dir := filepath.Join(in.Root, "servers", in.Server)
		r, err := recipe.Load(filepath.Join(dir, recipe.FileName))
		if err != nil {
			return Report{}, err
		}
		src = r.Source
		serverLicense = r.Vetting.License
		overlayDir = filepath.Join(dir, recipe.OverlayDir)
		recipeLockfile = r.Build.Lockfile
	} else {
		resolved, err := resolveSource(ctx, in.Resolve, in)
		if err != nil {
			return Report{}, err
		}
		src = resolved
	}

	fetch := in.Fetch
	if fetch == nil {
		fetch = build.FetchSource
	}
	scratch, err := os.MkdirTemp(in.WorkDir, "mcplib-audit-")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	if err := fetch(ctx, src, scratch); err != nil {
		return Report{}, fmt.Errorf("audit: fetch: %w", err)
	}
	if overlayDir != "" {
		if err := applyOverlay(overlayDir, scratch); err != nil {
			return Report{}, fmt.Errorf("audit: overlay: %w", err)
		}
	}

	lockPath, lockRel, err := findLockfile(scratch, recipeLockfile)
	if err != nil {
		return Report{}, err
	}
	var deps []Dep
	if lockPath != "" {
		if deps, err = ReadLock(lockPath); err != nil {
			return Report{}, err
		}
	}

	vulns, err := in.OSV.Query(ctx, deps)
	if err != nil {
		return Report{}, fmt.Errorf("audit: %w", err)
	}

	license := serverLicense
	if in.Server == "" {
		license = sourceLicense(scratch)
	}

	var blocking []string
	for _, v := range vulns {
		switch v.Severity {
		case "CRITICAL":
			blocking = append(blocking, fmt.Sprintf("%s: CRITICAL vulnerability in %s@%s", v.ID, v.Dep.Name, v.Dep.Version))
		case "HIGH":
			if len(v.Fixed) > 0 {
				blocking = append(blocking, fmt.Sprintf("%s: HIGH vulnerability in %s@%s, fixed in %s", v.ID, v.Dep.Name, v.Dep.Version, strings.Join(v.Fixed, ", ")))
			}
		}
	}
	if ClassifyLicense(license) == LicenseRefuses {
		blocking = append(blocking, fmt.Sprintf("server licence %s refuses redistribution", license))
	}

	licenses := map[string][]string{}
	var scripts []string
	for _, d := range deps {
		if d.InstallScript {
			scripts = append(scripts, d.Name+"@"+d.Version)
		}
		if d.License == "" {
			continue
		}
		class := ClassifyLicense(d.License)
		licenses[string(class)] = append(licenses[string(class)], d.Name)
		if class == LicenseRefuses {
			blocking = append(blocking, fmt.Sprintf("%s licence %s refuses redistribution", d.Name, d.License))
		}
	}
	for _, names := range licenses {
		sort.Strings(names)
	}

	binaries, err := findBinaries(scratch)
	if err != nil {
		return Report{}, err
	}

	return Report{
		Source:   src,
		Lockfile: lockRel,
		License:  license,
		DepCount: len(deps),
		Vulns:    vulns,
		Licenses: licenses,
		Scripts:  scripts,
		Binaries: binaries,
		Blocking: blocking,
	}, nil
}

// applyOverlay copies overlayDir's tree over dst, overwriting any file it
// also names. This mirrors internal/build's overlay semantics
// (copyTree(overlay, src, merge=true)) so audit reads exactly the tree a
// build would produce; an absent overlayDir is not an error, since overlay/
// is optional.
func applyOverlay(overlayDir, dst string) error {
	fi, err := os.Lstat(overlayDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("overlay %s is not a directory", overlayDir)
	}
	return filepath.WalkDir(overlayDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(overlayDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		_ = os.Remove(target)
		return copyOverlayFile(p, target, d)
	})
}

func copyOverlayFile(src, dst string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := os.FileMode(0o644)
	if info.Mode().Perm()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// findLockfile locates the lockfile a report's dependency evidence should
// come from. In --server mode that is exactly recipeLockfile, and its
// absence is an error, matching build.Run's own check. In --resolve mode
// (recipeLockfile == "") it searches the fetched tree's root for the first
// of resolveLockfileNames present, and returns "" for both when none is.
func findLockfile(root, recipeLockfile string) (path, rel string, err error) {
	if recipeLockfile != "" {
		rel = filepath.ToSlash(recipeLockfile)
		p := filepath.Join(root, filepath.FromSlash(recipeLockfile))
		if _, err := os.Stat(p); err != nil {
			return "", "", fmt.Errorf("audit: the declared lockfile %s is not in the fetched tree", recipeLockfile)
		}
		return p, rel, nil
	}
	for _, name := range resolveLockfileNames {
		p := filepath.Join(root, name)
		if _, err := os.Stat(p); err == nil {
			return p, name, nil
		}
	}
	return "", "", nil
}

// findBinaries walks root for every ELF or Mach-O file and every .node
// addon, and returns their paths relative to root, sorted.
func findBinaries(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if strings.HasSuffix(p, ".node") || hasBinaryMagic(p) {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

var machOMagics = [][]byte{
	{0xfe, 0xed, 0xfa, 0xce}, // 32-bit big-endian
	{0xce, 0xfa, 0xed, 0xfe}, // 32-bit little-endian
	{0xfe, 0xed, 0xfa, 0xcf}, // 64-bit big-endian
	{0xcf, 0xfa, 0xed, 0xfe}, // 64-bit little-endian
}

var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

// hasBinaryMagic reports whether path opens and starts with an ELF or
// Mach-O magic number. It reads only the first 4 bytes and treats any
// error, including a too-short file, as "not a binary" -- a tree can
// legitimately hold tiny text files this never needs to reject.
func hasBinaryMagic(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var header [4]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return false
	}
	if string(header[:]) == string(elfMagic) {
		return true
	}
	for _, m := range machOMagics {
		if string(header[:]) == string(m) {
			return true
		}
	}
	return false
}

// resolveSource turns a "--resolve <kind>:<coordinate>" spec into the exact
// recipe.Source `mcplib build` would fetch, looking up whatever pin the
// registry itself publishes so the resolved Source is the same pin the
// evaluator would later commit to package.yaml.
func resolveSource(ctx context.Context, resolve string, in Input) (recipe.Source, error) {
	kind, coordinate, ok := strings.Cut(resolve, ":")
	if !ok || coordinate == "" {
		return recipe.Source{}, fmt.Errorf("audit: --resolve %q: expected <kind>:<coordinate>", resolve)
	}
	switch kind {
	case "npm":
		return resolveNPM(ctx, coordinate, registryOr(in.NPMRegistry, defaultNPMRegistry))
	case "pypi":
		return resolvePyPI(ctx, coordinate, registryOr(in.PyPIRegistry, defaultPyPIRegistry))
	case "git":
		return resolveGit(coordinate)
	default:
		return recipe.Source{}, fmt.Errorf("audit: --resolve %q: unknown source kind %q; expected npm, pypi or git", resolve, kind)
	}
}

func registryOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// resolveNPM looks up name@version's dist.integrity from the registry, the
// same field build.FetchSource verifies the download against.
func resolveNPM(ctx context.Context, coordinate, registry string) (recipe.Source, error) {
	at := strings.LastIndex(coordinate, "@")
	if at <= 0 || at == len(coordinate)-1 {
		return recipe.Source{}, fmt.Errorf("audit: npm coordinate %q: expected <name>@<exact version>", coordinate)
	}
	name, version := coordinate[:at], coordinate[at+1:]
	endpoint := registry + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1) + "/" + url.PathEscape(version)
	body, err := httpGetJSON(ctx, endpoint)
	if err != nil {
		return recipe.Source{}, fmt.Errorf("audit: npm %s: %w", coordinate, err)
	}
	var doc struct {
		Dist struct {
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return recipe.Source{}, fmt.Errorf("audit: npm %s: %w", coordinate, err)
	}
	if doc.Dist.Integrity == "" {
		return recipe.Source{}, fmt.Errorf("audit: npm %s: the registry publishes no dist.integrity", coordinate)
	}
	return recipe.Source{Kind: "npm", Package: coordinate, Integrity: doc.Dist.Integrity}, nil
}

// resolvePyPI looks up name==version's sdist sha256 from the registry, the
// same digest build.FetchSource verifies the download against.
func resolvePyPI(ctx context.Context, coordinate, registry string) (recipe.Source, error) {
	name, version, ok := strings.Cut(coordinate, "==")
	if !ok || name == "" || version == "" {
		return recipe.Source{}, fmt.Errorf("audit: pypi coordinate %q: expected <name>==<exact version>", coordinate)
	}
	endpoint := registry + "/pypi/" + url.PathEscape(name) + "/" + url.PathEscape(version) + "/json"
	body, err := httpGetJSON(ctx, endpoint)
	if err != nil {
		return recipe.Source{}, fmt.Errorf("audit: pypi %s: %w", coordinate, err)
	}
	var doc struct {
		URLs []struct {
			PackageType string            `json:"packagetype"`
			Digests     map[string]string `json:"digests"`
		} `json:"urls"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return recipe.Source{}, fmt.Errorf("audit: pypi %s: %w", coordinate, err)
	}
	for _, u := range doc.URLs {
		if u.PackageType != "sdist" {
			continue
		}
		if sha := u.Digests["sha256"]; sha != "" {
			return recipe.Source{Kind: "pypi", Package: coordinate, Integrity: "sha256=" + sha}, nil
		}
	}
	return recipe.Source{}, fmt.Errorf("audit: pypi %s: no sdist with a sha256 digest is published", coordinate)
}

var gitCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// resolveGit needs no registry lookup: the commit is the pin.
func resolveGit(coordinate string) (recipe.Source, error) {
	at := strings.LastIndex(coordinate, "@")
	if at <= 0 || at == len(coordinate)-1 {
		return recipe.Source{}, fmt.Errorf("audit: git coordinate %q: expected <https repo url>@<40-hex commit>", coordinate)
	}
	repo, commit := coordinate[:at], coordinate[at+1:]
	if !strings.HasPrefix(repo, "https://") {
		return recipe.Source{}, fmt.Errorf("audit: git coordinate %q: repo must be an https:// URL", coordinate)
	}
	if !gitCommitRE.MatchString(commit) {
		return recipe.Source{}, fmt.Errorf("audit: git coordinate %q: commit must be 40 lowercase hex characters", coordinate)
	}
	return recipe.Source{Kind: "git", Repo: repo, Commit: commit}, nil
}

// maxRegistryResponse caps a registry metadata fetch; these are JSON
// documents, never artefacts.
const maxRegistryResponse = 8 << 20

func httpGetJSON(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", endpoint, resp.Status)
	}
	return body, nil
}

// sourceLicense reads a --resolve fetched tree's own declared licence:
// package.json's license field, then pyproject.toml's, then PKG-INFO's
// License header -- the same precedence upstream tooling itself checks in.
// It deliberately isn't a full TOML or email-header parser, just enough to
// read one field, because a --resolve audit runs before any
// recipe.Vetting.License exists to fall back on.
func sourceLicense(root string) string {
	if l := npmPackageJSONLicense(filepath.Join(root, "package.json")); l != "" {
		return l
	}
	if l := pyprojectLicense(filepath.Join(root, "pyproject.toml")); l != "" {
		return l
	}
	if l := pkgInfoLicense(filepath.Join(root, "PKG-INFO")); l != "" {
		return l
	}
	return ""
}

func npmPackageJSONLicense(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		License json.RawMessage `json:"license"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || len(doc.License) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(doc.License, &s); err == nil {
		return s
	}
	// Legacy package.json form: {"license": {"type": "MIT", "url": "..."}}.
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(doc.License, &obj); err == nil {
		return obj.Type
	}
	return ""
}

// pyprojectLicenseRE matches PEP 621's `license = "MIT"` and the table form
// `license = { text = "MIT" }`, the two shapes in common use.
var pyprojectLicenseRE = regexp.MustCompile(`(?m)^\s*license\s*=\s*(?:\{\s*text\s*=\s*)?"([^"]*)"`)

func pyprojectLicense(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	m := pyprojectLicenseRE.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func pkgInfoLicense(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "License:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Summary renders the human-readable report `mcplib audit` prints to
// stderr, blocking findings last so they're what a scrolled terminal
// leaves on screen.
func (r Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "audit: %s\n", describeSource(r.Source))
	lockfile := r.Lockfile
	if lockfile == "" {
		lockfile = "(none)"
	}
	fmt.Fprintf(&b, "audit: %d dependencies, lockfile %s\n", r.DepCount, lockfile)
	if r.License != "" {
		fmt.Fprintf(&b, "audit: licence %s (%s)\n", r.License, ClassifyLicense(r.License))
	} else {
		fmt.Fprintf(&b, "audit: licence unknown\n")
	}
	for _, class := range []LicenseClass{LicensePermits, LicenseReview, LicenseRefuses} {
		if names := r.Licenses[string(class)]; len(names) > 0 {
			fmt.Fprintf(&b, "audit: dependency licences %s: %s\n", class, strings.Join(names, ", "))
		}
	}
	fmt.Fprintf(&b, "audit: %d vulnerabilities\n", len(r.Vulns))
	if len(r.Scripts) > 0 {
		fmt.Fprintf(&b, "audit: install scripts: %s\n", strings.Join(r.Scripts, ", "))
	}
	if len(r.Binaries) > 0 {
		fmt.Fprintf(&b, "audit: binaries: %s\n", strings.Join(r.Binaries, ", "))
	}
	if len(r.Blocking) == 0 {
		fmt.Fprintf(&b, "audit: no blocking findings\n")
		return b.String()
	}
	for _, line := range r.Blocking {
		fmt.Fprintf(&b, "audit: BLOCKING: %s\n", line)
	}
	return b.String()
}

// describeSource is build's own describeSource, unexported there too: a
// one-line rendering of a pin for a log or summary line.
func describeSource(s recipe.Source) string {
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
