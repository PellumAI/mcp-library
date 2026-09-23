package site_test

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/index"
	"github.com/pellumai/mcp-library/internal/site"
)

func fixture() index.Index {
	m := func(runtime string) json.RawMessage {
		return json.RawMessage(`{"runtime":"` + runtime + `","transport":"stdio","params":[{"name":"url","type":"string","env":"URL"},{"name":"token","type":"string","env":"TOKEN","secret":true}],"egress":[{"host":"${url.host}","reason":"the operator's own instance"}],"resources":{"memory_max":"128Mi","pids_max":64}}`)
	}
	return index.Index{SchemaVersion: 1, GeneratedAt: "2026-09-23T00:00:00Z", Library: "pellumai/mcp-library", SigningKeys: []string{"library-v1"},
		Packages: []index.Package{
			{Name: "grafana", Title: "Grafana", Description: "Query <script>alert(1)</script> dashboards", Categories: []string{"observability"}, Versions: []index.Version{
				{Version: "1.5.1", PublishedAt: "2026-09-23T00:00:00Z", Manifest: m("native"), Blobs: []index.Blob{{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("a", 64), Size: 1 << 20}}},
			}},
			{Name: "context7", Title: "Context7", Description: "Docs", Versions: []index.Version{
				{Version: "4.1.1", PublishedAt: "2026-09-23T00:00:00Z", Manifest: m("node@22"), Blobs: []index.Blob{
					{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("b", 64), Size: 2 << 20},
					{OS: "linux", Arch: "arm64", SHA256: strings.Repeat("b", 64), Size: 2 << 20},
				}},
			}},
		}}
}

func generate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := site.Generate(fixture(), dir); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return dir
}

// page reads a generated page as its text with HTML entities decoded, so an
// assertion about what a reader sees is not an assertion about markup.
func page(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return html.UnescapeString(string(b))
}

func TestGenerate_EveryPackageHasAPageNamingEveryDigest(t *testing.T) {
	dir := generate(t)
	for _, p := range fixture().Packages {
		body := page(t, filepath.Join(dir, "servers", p.Name, "index.html"))
		for _, v := range p.Versions {
			for _, b := range v.Blobs {
				if !strings.Contains(body, b.SHA256) {
					t.Errorf("%s's page does not name %s", p.Name, b.SHA256)
				}
			}
		}
		if !strings.Contains(body, "cosign verify-blob --insecure-ignore-tlog --key library-v1.pub") {
			t.Errorf("%s's page has no verification block", p.Name)
		}
	}
}

func TestGenerate_SearchHasOneRecordPerPackage(t *testing.T) {
	dir := generate(t)
	b, err := os.ReadFile(filepath.Join(dir, "search.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []site.SearchRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(fixture().Packages) {
		t.Fatalf("%d records for %d packages", len(recs), len(fixture().Packages))
	}
}

func TestGenerate_EscapesASubmittedDescription(t *testing.T) {
	dir := generate(t)
	for _, p := range []string{"index.html", filepath.Join("servers", "grafana", "index.html")} {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "<script>alert(1)</script>") {
			t.Fatalf("%s renders a submitted script tag unescaped", p)
		}
	}
}

func TestGenerate_WritesNothingOutsideDir(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "site")
	if err := site.Generate(fixture(), dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "site" {
		t.Fatalf("parent holds %v", entries)
	}
}

func TestGenerate_RefusesAPathLikeName(t *testing.T) {
	idx := fixture()
	idx.Packages[0].Name = "../escape"
	if err := site.Generate(idx, t.TempDir()); err == nil {
		t.Fatal("a package name with a path separator is written")
	}
}

func TestBudget_FailsOverLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob"), make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := site.CheckBudget(dir, 1); err == nil {
		t.Fatal("a 2 MiB site passes a 1 MiB budget")
	}
	if _, err := site.CheckBudget(dir, 3); err != nil {
		t.Fatalf("a 2 MiB site fails a 3 MiB budget: %v", err)
	}
}

// TestBudget_MessageNamesTheWayOut asserts the message says what to do,
// because a guard whose message does not is a guard that gets raised.
func TestBudget_MessageNamesTheWayOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob"), make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := site.CheckBudget(dir, 1)
	for _, s := range []string{"mcplib index --retire", "library_source"} {
		if err == nil || !strings.Contains(err.Error(), s) {
			t.Fatalf("message %v does not name %q", err, s)
		}
	}
}
