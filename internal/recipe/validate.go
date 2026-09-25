package recipe

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/target"
)

var (
	hex40RE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// npmPkgRE is <name>@<exact version>, scoped or not. A range, a tag or
	// "latest" does not match.
	npmPkgRE = regexp.MustCompile(`^(@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*@[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
	// pypiPkgRE is <name>==<exact version>.
	pypiPkgRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*==[0-9][0-9A-Za-z.+!-]*$`)
	npmIntegrity  = regexp.MustCompile(`^sha512-[A-Za-z0-9+/]{86}==$`)
	pypiIntegrity = regexp.MustCompile(`^sha256=[0-9a-f]{64}$`)
)

// shellChars are the characters that only mean something to a shell. An argv
// carrying one is a shell string somebody wrote where an argv belongs, and
// there is no shell in the build container to interpret it.
var shellChars = []string{"|", ";", "&&", "||", ">", "<", "`", "$("}

// StepNeedsNetwork reports whether a build step is one of the three
// lockfile-enforcing installers that run with the network attached. Every
// other step runs with --network none. The three are installers that refuse
// to install anything their lockfile does not name by digest, which is why
// they, and nothing else, may reach a registry during a build.
func StepNeedsNetwork(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	cmd := path.Base(argv[0])
	switch {
	case len(argv) >= 2 && cmd == "npm" && argv[1] == "ci":
		return true
	case len(argv) >= 3 && cmd == "go" && argv[1] == "mod" && argv[2] == "download":
		return true
	case isPipInstall(argv) && slices.Contains(argv, "--require-hashes"):
		return true
	}
	return false
}

func isPipInstall(argv []string) bool {
	for i, a := range argv {
		if (a == "pip" || a == "pip3" || strings.HasSuffix(a, "/pip") || strings.HasSuffix(a, "/pip3")) && i+1 < len(argv) && argv[i+1] == "install" {
			return true
		}
		if a == "-m" && i+2 < len(argv) && argv[i+1] == "pip" && argv[i+2] == "install" {
			return true
		}
	}
	return false
}

// ResolvesAtBuildTime reports why a step would resolve a dependency version
// at build time, or "" when it would not. It is a denylist over argv, not a
// guess at intent.
func ResolvesAtBuildTime(argv []string) string {
	if len(argv) == 0 {
		return "the step is empty"
	}
	for _, a := range argv {
		for _, c := range shellChars {
			if strings.Contains(a, c) {
				return fmt.Sprintf("argument %q carries the shell operator %q; a step is an argv, not a shell string", a, c)
			}
		}
	}
	cmd := path.Base(argv[0])
	sub := ""
	if len(argv) > 1 {
		sub = argv[1]
	}
	switch {
	case (cmd == "sh" || cmd == "bash" || cmd == "dash") && slices.Contains(argv, "-c"):
		return "a shell -c step is a shell string in disguise"
	case cmd == "npm" && slices.Contains([]string{"install", "i", "add", "update", "up", "upgrade", "exec"}, sub):
		return "npm " + sub + " resolves versions; use npm ci against the committed lockfile"
	case cmd == "npx":
		return "npx resolves and downloads a package at run time"
	case cmd == "yarn" && slices.Contains([]string{"add", "up", "upgrade", "dlx"}, sub):
		return "yarn " + sub + " resolves versions"
	case cmd == "pnpm" && slices.Contains([]string{"add", "update", "up", "dlx"}, sub):
		return "pnpm " + sub + " resolves versions"
	case isPipInstall(argv) && !slices.Contains(argv, "--require-hashes"):
		return "pip install without --require-hashes resolves versions"
	case cmd == "go" && (sub == "get" || sub == "install"):
		return "go " + sub + " resolves module versions; build against the committed go.sum"
	case cmd == "cargo" && sub == "install":
		return "cargo install resolves crate versions"
	case (cmd == "apt-get" || cmd == "apt") && sub == "install":
		return "apt-get install is unpinned; the build image carries the toolchain"
	case cmd == "apk" && sub == "add":
		return "apk add is unpinned; the build image carries the toolchain"
	case cmd == "curl" || cmd == "wget":
		return cmd + " fetches an unpinned URL; declare it as the source instead"
	}
	return ""
}

// Validate checks a recipe against its manifest template and the executor
// target. The rules run in a fixed order so a recipe with two problems always
// reports the same one first.
func Validate(r Recipe, m manifest.Doc, t target.Target) error {
	n := r.Name
	if n == "" {
		n = "<unnamed>"
	}
	if r.SchemaVersion != 1 {
		return fmt.Errorf("recipe: %s: schema_version %d is not 1", n, r.SchemaVersion)
	}
	if r.Name != m.Name {
		return fmt.Errorf("recipe: %s: name does not match manifest name %q", n, m.Name)
	}
	if r.Version != m.Version {
		return fmt.Errorf("recipe: %s: version %q does not match manifest version %q", n, r.Version, m.Version)
	}
	if r.Runtime != m.Runtime {
		return fmt.Errorf("recipe: %s: runtime %q does not match manifest runtime %q", n, r.Runtime, m.Runtime)
	}
	if !t.AllowsRuntime(r.Runtime) {
		return fmt.Errorf("recipe: %s: %w", n, t.RuntimeError(r.Runtime))
	}
	if len(r.Arch) == 0 || !subset(r.Arch, m.Arch) {
		return fmt.Errorf("recipe: %s: arch %v is not a subset of the manifest's %v", n, r.Arch, m.Arch)
	}
	if err := validateSource(n, r.Source); err != nil {
		return err
	}
	for _, tc := range r.Build.Toolchains {
		if tc == "native" || !t.AllowsRuntime(tc) {
			return fmt.Errorf("recipe: %s: build.toolchains: %w", n, t.RuntimeError(tc))
		}
	}
	if (strings.HasPrefix(r.Runtime, "node@") || strings.HasPrefix(r.Runtime, "python@")) && r.Build.Lockfile == "" {
		return fmt.Errorf("recipe: %s: build.lockfile is required for runtime %q", n, r.Runtime)
	}
	for i, s := range r.Build.Steps {
		if why := ResolvesAtBuildTime(s); why != "" {
			return fmt.Errorf("recipe: %s: build.steps[%d] resolves a dependency at build time: %q: %s", n, i, strings.Join(s, " "), why)
		}
	}
	if len(r.Build.Stage) == 0 {
		return fmt.Errorf("recipe: %s: build.stage is empty, so the package would carry only its manifest", n)
	}
	for i, s := range r.Build.Stage {
		if s.From == "" || !insidePath(s.From) {
			return fmt.Errorf("recipe: %s: stage[%d].from %q must be a relative path inside the source tree", n, i, s.From)
		}
		if s.To == "" || !insidePath(s.To) || s.To == manifest.PackageManifestName {
			return fmt.Errorf("recipe: %s: stage[%d].to %q must be a relative path inside the package", n, i, s.To)
		}
	}
	vettedOn, err := time.Parse(time.DateOnly, r.Vetting.VettedOn)
	if err != nil {
		return fmt.Errorf("recipe: %s: vetting.vetted_on %q must be a YYYY-MM-DD date", n, r.Vetting.VettedOn)
	}
	if strings.TrimSpace(r.Vetting.VettedBy) == "" {
		return fmt.Errorf("recipe: %s: vetting.vetted_by must name the maintainer who vetted it", n)
	}
	if len(r.Vetting.Sources) == 0 {
		return fmt.Errorf("recipe: %s: vetting.sources must cite at least one primary source", n)
	}
	if err := validateSmoke(n, r.Smoke); err != nil {
		return err
	}
	if err := validateAudit(n, r.Audit, vettedOn); err != nil {
		return err
	}
	return nil
}

// MaxWaiverDays caps how far past vetting.vetted_on a waiver may expire, so
// an exception is re-reviewed at least as often as the vetting it rests on
// would go stale.
const MaxWaiverDays = 90

// ValidateAudit runs only Validate's audit-waiver rules. `mcplib audit`
// calls it on the recipe it loads, so a waiver past its cap or without a
// reason is refused by the audit job itself, not only by validate-all.
// A recipe with no waivers passes whatever its vetting says.
func ValidateAudit(r Recipe) error {
	if len(r.Audit.Waivers) == 0 {
		return nil
	}
	n := r.Name
	if n == "" {
		n = "<unnamed>"
	}
	vettedOn, err := time.Parse(time.DateOnly, r.Vetting.VettedOn)
	if err != nil {
		return fmt.Errorf("recipe: %s: vetting.vetted_on %q must be a YYYY-MM-DD date", n, r.Vetting.VettedOn)
	}
	return validateAudit(n, r.Audit, vettedOn)
}

// validateAudit checks the audit waivers: every field set, expires a date
// within MaxWaiverDays of vettedOn, and no (id, package) pair twice, since a
// second copy could only be a stale one with a different expiry.
func validateAudit(n string, a Audit, vettedOn time.Time) error {
	limit := vettedOn.AddDate(0, 0, MaxWaiverDays)
	seen := map[[2]string]bool{}
	for i, w := range a.Waivers {
		for _, f := range []struct{ name, value string }{
			{"id", w.ID}, {"package", w.Package}, {"reason", w.Reason}, {"expires", w.Expires},
		} {
			if strings.TrimSpace(f.value) == "" {
				return fmt.Errorf("recipe: %s: audit.waivers[%d].%s is required", n, i, f.name)
			}
		}
		expires, err := time.Parse(time.DateOnly, w.Expires)
		if err != nil {
			return fmt.Errorf("recipe: %s: audit.waivers[%d].expires %q must be a YYYY-MM-DD date", n, i, w.Expires)
		}
		if expires.After(limit) {
			return fmt.Errorf("recipe: %s: audit.waivers[%d].expires %s is more than %d days after vetting.vetted_on %s", n, i, w.Expires, MaxWaiverDays, vettedOn.Format(time.DateOnly))
		}
		key := [2]string{w.ID, w.Package}
		if seen[key] {
			return fmt.Errorf("recipe: %s: audit.waivers[%d] duplicates the waiver for %s/%s", n, i, w.ID, w.Package)
		}
		seen[key] = true
	}
	return nil
}

// validateSmoke checks the optional credential escape hatch. The zero value
// is full mode and needs no reason; "initialize-only" needs one, because it
// is the reviewer's only evidence that skipping the tool-surface snapshot
// was warranted rather than convenient; anything else is a typo the recipe
// should not silently treat as full mode.
func validateSmoke(n string, s Smoke) error {
	switch s.Mode {
	case "":
		return nil
	case "initialize-only":
		if strings.TrimSpace(s.Reason) == "" {
			return fmt.Errorf("recipe: %s: smoke.reason is required when smoke.mode is initialize-only", n)
		}
		return nil
	default:
		return fmt.Errorf("recipe: %s: smoke.mode %q is not \"\" or \"initialize-only\"", n, s.Mode)
	}
}

func validateSource(n string, s Source) error {
	switch s.Kind {
	case "git":
		if s.Repo == "" || !strings.HasPrefix(s.Repo, "https://") {
			return fmt.Errorf("recipe: %s: source.repo must be an https git remote", n)
		}
		if !hex40RE.MatchString(s.Commit) {
			return fmt.Errorf("recipe: %s: source.commit must be a full 40-character commit, not a tag or a branch", n)
		}
	case "npm":
		if !npmPkgRE.MatchString(s.Package) {
			return fmt.Errorf("recipe: %s: source.package %q must be <name>@<exact version>", n, s.Package)
		}
		if !npmIntegrity.MatchString(s.Integrity) {
			return fmt.Errorf("recipe: %s: source.integrity is required so the fetch is pinned", n)
		}
	case "pypi":
		if !pypiPkgRE.MatchString(s.Package) {
			return fmt.Errorf("recipe: %s: source.package %q must be <name>==<exact version>", n, s.Package)
		}
		if !pypiIntegrity.MatchString(s.Integrity) {
			return fmt.Errorf("recipe: %s: source.integrity is required so the fetch is pinned", n)
		}
	case "archive":
		if !strings.HasPrefix(s.URL, "https://") {
			return fmt.Errorf("recipe: %s: source.url must be https", n)
		}
		if !hex64RE.MatchString(s.SHA256) {
			return fmt.Errorf("recipe: %s: source.sha256 must be 64 hex characters", n)
		}
	default:
		return fmt.Errorf("recipe: %s: source.kind %q is not git, npm, pypi or archive", n, s.Kind)
	}
	return nil
}

// insidePath reports whether p is relative, clean enough and never climbs out.
func insidePath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	c := path.Clean(p)
	return c != "." && c != ".." && !strings.HasPrefix(c, "../")
}

func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}
