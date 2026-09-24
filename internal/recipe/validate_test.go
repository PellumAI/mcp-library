package recipe_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/recipe"
	"github.com/pellumai/mcp-library/internal/target"
)

func testTarget(t *testing.T) target.Target {
	t.Helper()
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	tg, err := target.Parse([]byte(`schema_version: 1
mcpgw_commit: "27fbc1d0621ac5a3f6dce7682f59450c5c578193"
schema_asset: mcpgw-package.schema.json
schema_sha256: "` + sum("s") + `"
window_asset: runtime-window.json
window_sha256: "` + sum("w") + `"
runtimes: [node@22, node@20, python@3.12, python@3.11, native]
`))
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func good() (recipe.Recipe, manifest.Doc) {
	r := recipe.Recipe{
		SchemaVersion: 1,
		Name:          "grafana",
		Version:       "1.5.1",
		Runtime:       "native",
		Arch:          []string{"amd64"},
		Source: recipe.Source{
			Kind:   "git",
			Repo:   "https://github.com/grafana/mcp-grafana",
			Commit: "0123456789abcdef0123456789abcdef01234567",
		},
		Build: recipe.Build{
			Lockfile: "go.sum",
			Steps: [][]string{
				{"go", "mod", "download"},
				{"go", "build", "-trimpath", "-o", "out/mcp-grafana", "./cmd/mcp-grafana"},
			},
			Stage: []recipe.StageRule{{From: "out/mcp-grafana", To: "bin/mcp-grafana"}},
		},
		Vetting: recipe.Vetting{
			VettedOn: "2026-09-23",
			VettedBy: "maintainer",
			Sources:  []string{"https://github.com/grafana/mcp-grafana"},
		},
	}
	m := manifest.Doc{Name: "grafana", Version: "1.5.1", Runtime: "native", Arch: []string{"amd64", "arm64"}}
	return r, m
}

func TestValidate_AcceptsAGoodRecipe(t *testing.T) {
	r, m := good()
	if err := recipe.Validate(r, m, testTarget(t)); err != nil {
		t.Fatalf("good recipe refused: %v", err)
	}
}

// TestValidate_Refusals is one row per rule, each asserting its own message,
// because "it failed" is not a rule.
func TestValidate_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*recipe.Recipe, *manifest.Doc)
		want   string
	}{
		{"schema version", func(r *recipe.Recipe, _ *manifest.Doc) { r.SchemaVersion = 2 }, "schema_version 2 is not 1"},
		{"name", func(_ *recipe.Recipe, m *manifest.Doc) { m.Name = "other" }, `name does not match manifest name "other"`},
		{"version", func(_ *recipe.Recipe, m *manifest.Doc) { m.Version = "9" }, `version "1.5.1" does not match manifest version "9"`},
		{"runtime mismatch", func(_ *recipe.Recipe, m *manifest.Doc) { m.Runtime = "node@22" }, `runtime "native" does not match manifest runtime "node@22"`},
		{"runtime window", func(r *recipe.Recipe, m *manifest.Doc) { r.Runtime, m.Runtime = "node@18", "node@18" }, `runtime "node@18" is outside the executor window`},
		{"toolchain window", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Toolchains = []string{"node@18"} }, `build.toolchains: runtime_unavailable: runtime "node@18"`},
		{"arch empty", func(r *recipe.Recipe, _ *manifest.Doc) { r.Arch = nil }, "is not a subset of the manifest's"},
		{"arch superset", func(r *recipe.Recipe, m *manifest.Doc) { m.Arch = []string{"arm64"} }, "is not a subset of the manifest's"},
		{"source kind", func(r *recipe.Recipe, _ *manifest.Doc) { r.Source.Kind = "latest" }, `source.kind "latest" is not git, npm, pypi or archive`},
		{"git tag", func(r *recipe.Recipe, _ *manifest.Doc) { r.Source.Commit = "v1.5.1" }, "source.commit must be a full 40-character commit"},
		{"npm integrity", func(r *recipe.Recipe, _ *manifest.Doc) {
			r.Source = recipe.Source{Kind: "npm", Package: "@upstash/context7-mcp@4.1.1"}
		}, "source.integrity is required so the fetch is pinned"},
		{"npm range", func(r *recipe.Recipe, _ *manifest.Doc) {
			r.Source = recipe.Source{Kind: "npm", Package: "@upstash/context7-mcp@^4"}
		}, "must be <name>@<exact version>"},
		{"pypi integrity", func(r *recipe.Recipe, _ *manifest.Doc) {
			r.Source = recipe.Source{Kind: "pypi", Package: "six==1.17.0"}
		}, "source.integrity is required so the fetch is pinned"},
		{"archive sha", func(r *recipe.Recipe, _ *manifest.Doc) {
			r.Source = recipe.Source{Kind: "archive", URL: "https://example.com/x.tar.gz", SHA256: "abc"}
		}, "source.sha256 must be 64 hex characters"},
		{"lockfile", func(r *recipe.Recipe, m *manifest.Doc) {
			r.Runtime, m.Runtime, r.Build.Lockfile = "node@22", "node@22", ""
		}, `build.lockfile is required for runtime "node@22"`},
		{"npm install", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Steps = [][]string{{"npm", "install"}} }, "build.steps[0] resolves a dependency at build time"},
		{"pip without hashes", func(r *recipe.Recipe, _ *manifest.Doc) {
			r.Build.Steps = [][]string{{"python3", "-m", "pip", "install", "-r", "requirements.txt"}}
		}, "build.steps[0] resolves a dependency at build time"},
		{"go get", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Steps = [][]string{{"go", "mod", "download"}, {"go", "get", "x"}} }, "build.steps[1] resolves a dependency at build time"},
		{"curl pipe", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Steps = [][]string{{"curl", "https://x|sh"}} }, "build.steps[0] resolves a dependency at build time"},
		{"apt-get", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Steps = [][]string{{"apt-get", "install", "-y", "jq"}} }, "build.steps[0] resolves a dependency at build time"},
		{"shell string", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Steps = [][]string{{"sh", "-c", "make"}} }, "build.steps[0] resolves a dependency at build time"},
		{"stage escapes", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Stage[0].To = "../bin/x" }, `stage[0].to "../bin/x" must be a relative path inside the package`},
		{"stage absolute", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Stage[0].To = "/bin/x" }, "must be a relative path inside the package"},
		{"stage over manifest", func(r *recipe.Recipe, _ *manifest.Doc) { r.Build.Stage[0].To = "mcpgw-package.json" }, "must be a relative path inside the package"},
		{"vetted on", func(r *recipe.Recipe, _ *manifest.Doc) { r.Vetting.VettedOn = "yesterday" }, "vetting.vetted_on"},
		{"vetting sources", func(r *recipe.Recipe, _ *manifest.Doc) { r.Vetting.Sources = nil }, "vetting.sources must cite at least one primary source"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, m := good()
			c.mutate(&r, &m)
			err := recipe.Validate(r, m, testTarget(t))
			if err == nil {
				t.Fatalf("accepted; want %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

// TestValidate_SmokeBlock covers the recipe's optional smoke escape hatch:
// initialize-only requires a reason, any other mode is refused, and an
// absent block (the zero value, as good() leaves it) is full mode and needs
// no case here because every other test in this file already exercises it.
func TestValidate_SmokeBlock(t *testing.T) {
	cases := []struct {
		name  string
		smoke recipe.Smoke
		want  string // "" means the recipe is accepted
	}{
		{"initialize-only with a reason", recipe.Smoke{Mode: "initialize-only", Reason: "refuses tools/list without a live credential"}, ""},
		{"initialize-only without a reason", recipe.Smoke{Mode: "initialize-only"}, "smoke.reason"},
		{"unknown mode", recipe.Smoke{Mode: "skip"}, `smoke.mode "skip"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, m := good()
			r.Smoke = c.smoke
			err := recipe.Validate(r, m, testTarget(t))
			if c.want == "" {
				if err != nil {
					t.Fatalf("good smoke block refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted; want error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

func TestStepNeedsNetwork_OnlyTheThreeLockfileInstallers(t *testing.T) {
	for _, s := range [][]string{
		{"npm", "ci", "--omit=dev"},
		{"go", "mod", "download"},
		{"/opt/mcpgw/runtimes/python@3.12/bin/python3", "-m", "pip", "install", "--require-hashes", "-r", "r.txt"},
	} {
		if !recipe.StepNeedsNetwork(s) {
			t.Errorf("%v does not get the network", s)
		}
	}
	for _, s := range [][]string{
		{"go", "build", "./..."},
		{"npm", "run", "build"},
		{"python3", "-m", "pip", "install", "-r", "r.txt"},
	} {
		if recipe.StepNeedsNetwork(s) {
			t.Errorf("%v gets the network", s)
		}
	}
}

func TestParse_RefusesAnUnknownKey(t *testing.T) {
	if _, err := recipe.Parse([]byte("schema_version: 1\nname: x\nunknown_key: 1\n")); err == nil {
		t.Fatal("a misspelled key is accepted")
	}
}
