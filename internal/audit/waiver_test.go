package audit

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pellumai/mcp-library/internal/recipe"
)

// waiverLock has one dependency per signal a waiver test needs. ReadLock
// sorts deps by name, and waiverBatch answers in that same order.
const waiverLock = `{
  "packages": {
    "": {},
    "node_modules/gpl-dep": {"version": "6.0.0", "license": "GPL-3.0-only"},
    "node_modules/vuln-high-fixed": {"version": "2.0.0", "license": "MIT"},
    "node_modules/vuln-high-nofix": {"version": "3.0.0", "license": "MIT"}
  }
}`

const waiverBatch = `{"results": [
    {},
    {"vulns": [{"id": "HIGH-FIXED"}]},
    {"vulns": [{"id": "HIGH-NOFIX"}]}
  ]}`

// waiverVulns gives HIGH-FIXED the alias CVE-2026-0001, so a waiver can name
// the advisory by either spelling.
var waiverVulns = map[string]string{
	"HIGH-FIXED": `{
  "id": "HIGH-FIXED",
  "aliases": ["CVE-2026-0001"],
  "affected": [{"ranges": [{"type": "SEMVER", "events": [{"introduced":"0"}, {"fixed":"2.0.1"}]}]}],
  "database_specific": {"severity": "HIGH"}
}`,
	"HIGH-NOFIX": vulnRecord("HIGH-NOFIX", "HIGH", ""),
}

const highFixedLine = "HIGH-FIXED: HIGH vulnerability in vuln-high-fixed@2.0.0, fixed in 2.0.1"
const gplLine = "gpl-dep licence GPL-3.0-only refuses redistribution"

// runWithWaivers writes servers/fixture-server, vetted 2026-09-23, with
// auditYAML as its audit block and runs a --server audit at now.
func runWithWaivers(t *testing.T, auditYAML, today string) Report {
	t.Helper()
	report, err := runServerAudit(t, "2026-09-23", auditYAML, fixedNow(t, today))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

func runServerAudit(t *testing.T, vettedOn, auditYAML string, now func() time.Time) (Report, error) {
	t.Helper()
	root := writeWaiverServer(t, vettedOn, auditYAML)
	server := httptest.NewServer(osvHandler(t, waiverBatch, waiverVulns))
	defer server.Close()
	return Run(context.Background(), Input{
		Root:   root,
		Server: "fixture-server",
		Fetch:  waiverFetch(t),
		OSV:    OSV{BaseURL: server.URL, HTTP: server.Client()},
		Now:    now,
	})
}

func writeWaiverServer(t *testing.T, vettedOn, auditYAML string) string {
	t.Helper()
	root := t.TempDir()
	recipeYAML := `schema_version: 1
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
  vetted_on: "` + vettedOn + `"
  license: MIT
` + auditYAML
	writeFixtureFile(t, filepath.Join(root, "servers", "fixture-server"), recipe.FileName, recipeYAML)
	return root
}

func waiverFetch(t *testing.T) func(context.Context, recipe.Source, string) error {
	return func(_ context.Context, _ recipe.Source, dir string) error {
		writeFixtureFile(t, dir, "package-lock.json", waiverLock)
		return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"license": "MIT"}`), 0o644)
	}
}

func fixedNow(t *testing.T, day string) func() time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatal(err)
	}
	// Late in the UTC day, so a comparison that used local midnight or the
	// wall clock rather than the UTC date would show up.
	return func() time.Time { return d.Add(23 * time.Hour) }
}

func waiverYAML(id, pkg, expires string) string {
	return `audit:
  waivers:
    - id: ` + id + `
      package: ` + pkg + `
      reason: no upstream release carries the fix yet
      expires: "` + expires + `"
`
}

func assertBlocking(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("Blocking = %q, want %q", got, want)
	}
}

func TestRun_WaiverMovesAMatchedFindingOutOfBlocking(t *testing.T) {
	// The expiry day itself still waives.
	report := runWithWaivers(t, waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2026-10-25"), "2026-10-25")
	assertBlocking(t, report.Blocking, []string{gplLine})
	if len(report.Waived) != 1 {
		t.Fatalf("Waived = %+v, want one finding", report.Waived)
	}
	w := report.Waived[0]
	if w.Vuln.ID != "HIGH-FIXED" || w.Vuln.Dep.Name != "vuln-high-fixed" {
		t.Errorf("Waived vuln = %+v, want HIGH-FIXED in vuln-high-fixed", w.Vuln)
	}
	if w.Waiver.Expires != "2026-10-25" || w.Waiver.Reason == "" {
		t.Errorf("Waived waiver = %+v, want the recipe's waiver", w.Waiver)
	}
}

func TestRun_WaiverMatchesAnAlias(t *testing.T) {
	report := runWithWaivers(t, waiverYAML("CVE-2026-0001", "vuln-high-fixed", "2026-10-25"), "2026-09-25")
	assertBlocking(t, report.Blocking, []string{gplLine})
	if len(report.Waived) != 1 || report.Waived[0].Vuln.ID != "HIGH-FIXED" {
		t.Errorf("Waived = %+v, want HIGH-FIXED waived through its alias", report.Waived)
	}
}

func TestRun_ExpiredWaiverWaivesNothing(t *testing.T) {
	report := runWithWaivers(t, waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2026-10-25"), "2026-10-26")
	assertBlocking(t, report.Blocking, []string{
		highFixedLine,
		"waiver for HIGH-FIXED/vuln-high-fixed expired 2026-10-25",
		gplLine,
	})
	if len(report.Waived) != 0 {
		t.Errorf("Waived = %+v, want none", report.Waived)
	}
}

func TestRun_UnusedWaiverBlocks(t *testing.T) {
	for _, c := range []struct{ name, id, pkg string }{
		{"no such advisory", "GHSA-none", "vuln-high-fixed"},
		{"right advisory, other package", "HIGH-FIXED", "vuln-high-nofix"},
		// HIGH-NOFIX is reported but does not block, so there is nothing
		// for a waiver of it to do.
		{"a finding that does not block", "HIGH-NOFIX", "vuln-high-nofix"},
	} {
		t.Run(c.name, func(t *testing.T) {
			report := runWithWaivers(t, waiverYAML(c.id, c.pkg, "2026-10-25"), "2026-09-25")
			assertBlocking(t, report.Blocking, []string{
				highFixedLine,
				"waiver for " + c.id + "/" + c.pkg + " matches no blocking finding; remove it",
				gplLine,
			})
			if len(report.Waived) != 0 {
				t.Errorf("Waived = %+v, want none", report.Waived)
			}
		})
	}
}

func TestRun_LicenceRefusalIsNotWaivable(t *testing.T) {
	report := runWithWaivers(t, waiverYAML("GPL-3.0-only", "gpl-dep", "2026-10-25"), "2026-09-25")
	assertBlocking(t, report.Blocking, []string{
		highFixedLine,
		"waiver for GPL-3.0-only/gpl-dep matches no blocking finding; remove it",
		gplLine,
	})
	if len(report.Waived) != 0 {
		t.Errorf("Waived = %+v, want none", report.Waived)
	}
}

func TestRun_ResolveModeTakesNoWaivers(t *testing.T) {
	// A committed recipe waiving the finding sits under Root, but --resolve
	// audits a coordinate, not servers/<name>, and never reads it.
	root := writeWaiverServer(t, "2026-09-23", waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2026-10-25"))
	server := httptest.NewServer(osvHandler(t, waiverBatch, waiverVulns))
	defer server.Close()
	report, err := Run(context.Background(), Input{
		Root:    root,
		Resolve: "git:https://example.com/fixture.git@" + fortyHex,
		Fetch:   waiverFetch(t),
		OSV:     OSV{BaseURL: server.URL, HTTP: server.Client()},
		Now:     fixedNow(t, "2026-09-25"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertBlocking(t, report.Blocking, []string{highFixedLine, gplLine})
	if len(report.Waived) != 0 {
		t.Errorf("Waived = %+v, want none in --resolve mode", report.Waived)
	}
}

func TestReportSummary_ListsWaivedFindingsWithExpiry(t *testing.T) {
	r := Report{
		Source: recipe.Source{Kind: "npm", Package: "x@1.0.0"},
		Waived: []WaivedFinding{{
			Vuln:   Vuln{ID: "HIGH-FIXED", Severity: "HIGH", Dep: Dep{Name: "vuln-high-fixed", Version: "2.0.0"}},
			Waiver: recipe.Waiver{ID: "CVE-2026-0001", Package: "vuln-high-fixed", Reason: "no release yet", Expires: "2026-10-25"},
		}},
	}
	s := r.Summary()
	want := "audit: WAIVED: HIGH-FIXED: HIGH vulnerability in vuln-high-fixed@2.0.0, waived by CVE-2026-0001 until 2026-10-25: no release yet"
	if !strings.Contains(s, want) {
		t.Errorf("Summary missing %q:\n%s", want, s)
	}
	if !strings.Contains(s, "no blocking findings") {
		t.Errorf("Summary with only waived findings should say nothing blocks:\n%s", s)
	}
}

func TestRun_ExpiryIsJudgedByTheUTCDate(t *testing.T) {
	// 20:00 on 2026-10-25 at UTC-5 is 01:00 on 2026-10-26 in UTC, the day
	// after the waiver expired.
	now := func() time.Time { return time.Date(2026, 10, 25, 20, 0, 0, 0, time.FixedZone("UTC-5", -5*3600)) }
	report, err := runServerAudit(t, "2026-09-23", waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2026-10-25"), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertBlocking(t, report.Blocking, []string{
		highFixedLine,
		"waiver for HIGH-FIXED/vuln-high-fixed expired 2026-10-25",
		gplLine,
	})
}

// TestRun_RefusesAnInvalidOrGamedWaiver pins that the audit job alone,
// without validate-all, refuses a waiver recipe.Validate would, and one
// that games the 90-day cap by dating vetted_on in the future.
func TestRun_RefusesAnInvalidOrGamedWaiver(t *testing.T) {
	for _, c := range []struct{ name, vettedOn, audit, want string }{
		{"past the vetted_on cap", "2026-09-23", waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2099-01-01"),
			"audit.waivers[0].expires 2099-01-01 is more than 90 days after vetting.vetted_on 2026-09-23"},
		{"blank reason", "2026-09-23", strings.Replace(waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2026-10-25"),
			"reason: no upstream release carries the fix yet", `reason: " "`, 1),
			"audit.waivers[0].reason is required"},
		{"past today's cap", "2026-12-01", waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2027-02-20"),
			"audit.waivers[0].expires 2027-02-20 is more than 90 days after today 2026-09-25"},
		{"vetted in the future", "2026-09-30", waiverYAML("HIGH-FIXED", "vuln-high-fixed", "2026-10-25"),
			"vetting.vetted_on 2026-09-30 is later than today 2026-09-25"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := runServerAudit(t, c.vettedOn, c.audit, fixedNow(t, "2026-09-25"))
			if err == nil {
				t.Fatalf("accepted; want error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
		})
	}
}
