package index_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/index"
)

var at = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func writeMeta(t *testing.T, dist string, m build.Meta) {
	t.Helper()
	base := filepath.Join(dist, build.FileBase(m.Name, m.BuiltFor))
	if err := os.WriteFile(base+".tar.gz", bytes.Repeat([]byte("x"), int(m.Size)), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".meta.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sample() index.Index {
	return index.Index{
		SchemaVersion: 1, GeneratedAt: "2026-09-23T00:00:00Z", Library: "pellumai/mcp-library",
		SigningKeys: []string{"library-v1"},
		Packages: []index.Package{
			{Name: "zeta", Title: "Z", Versions: []index.Version{
				{Version: "1.0.0", PublishedAt: "2026-01-01T00:00:00Z", Blobs: []index.Blob{{OS: "linux", Arch: "arm64", SHA256: "b", Size: 1}, {OS: "linux", Arch: "amd64", SHA256: "a", Size: 1}}, Manifest: json.RawMessage(`{}`)},
				{Version: "2.0.0", PublishedAt: "2026-02-01T00:00:00Z", Blobs: []index.Blob{{OS: "linux", Arch: "amd64", SHA256: "c", Size: 1}}, Manifest: json.RawMessage(`{}`)},
			}},
			{Name: "alpha", Title: "A", Versions: []index.Version{
				{Version: "1.0.0", PublishedAt: "2026-01-01T00:00:00Z", Blobs: []index.Blob{{OS: "linux", Arch: "amd64", SHA256: "d", Size: 1}}, Manifest: json.RawMessage(`{}`)},
			}},
		},
	}
}

func reversed(idx index.Index) index.Index {
	out := idx
	out.Packages = slices.Clone(idx.Packages)
	slices.Reverse(out.Packages)
	for i := range out.Packages {
		vs := slices.Clone(out.Packages[i].Versions)
		slices.Reverse(vs)
		for j := range vs {
			bs := slices.Clone(vs[j].Blobs)
			slices.Reverse(bs)
			vs[j].Blobs = bs
		}
		out.Packages[i].Versions = vs
	}
	return out
}

func TestMarshal_IsStable(t *testing.T) {
	a, err := index.Marshal(sample())
	if err != nil {
		t.Fatal(err)
	}
	b, err := index.Marshal(reversed(sample()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("two orderings marshal differently:\n%s\n%s", a, b)
	}
	if !bytes.HasSuffix(a, []byte("}\n")) {
		t.Fatal("no trailing newline")
	}
}

// TestMarshal_FieldNames pins the exact key set at each level: renaming one is
// a breaking change for every gateway that pinned this library.
func TestMarshal_FieldNames(t *testing.T) {
	idx := sample()
	idx.Packages[1].Homepage = "https://example.com"
	idx.Packages[1].License = "MIT"
	idx.Packages[1].Categories = []string{"x"}
	b, err := index.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	if got := keys(doc); got != "generated_at,library,packages,schema_version,signing_keys" {
		t.Fatalf("top level: %s", got)
	}
	pkg := doc["packages"].([]any)[0].(map[string]any)
	if got := keys(pkg); got != "categories,description,homepage,license,name,title,versions" {
		t.Fatalf("package: %s", got)
	}
	ver := pkg["versions"].([]any)[0].(map[string]any)
	if got := keys(ver); got != "blobs,manifest,published_at,version" {
		t.Fatalf("version: %s", got)
	}
	blob := ver["blobs"].([]any)[0].(map[string]any)
	if got := keys(blob); got != "arch,os,sha256,size" {
		t.Fatalf("blob: %s", got)
	}
}

func TestCanonicalise_Ordering(t *testing.T) {
	idx := reversed(sample())
	index.Canonicalise(&idx)
	t.Run("packages by name", func(t *testing.T) {
		if idx.Packages[0].Name != "alpha" {
			t.Fatalf("first package %s", idx.Packages[0].Name)
		}
	})
	t.Run("versions newest first", func(t *testing.T) {
		if idx.Packages[1].Versions[0].Version != "2.0.0" {
			t.Fatalf("first version %s", idx.Packages[1].Versions[0].Version)
		}
	})
	t.Run("blobs by os then arch", func(t *testing.T) {
		bs := idx.Packages[1].Versions[1].Blobs
		if bs[0].Arch != "amd64" || bs[1].Arch != "arm64" {
			t.Fatalf("blob order %v", bs)
		}
	})
}

const nodeManifest = `{"schema_version":1,"name":"ctx","version":"4.1.1","title":"Context","description":"docs","runtime":"node@22","arch":["amd64","arm64"],"from_the_future":{"x":1}}`

func TestGenerate_ArchIndependentEmitsOneBlobPerArch(t *testing.T) {
	dist := t.TempDir()
	for _, a := range []string{"amd64", "arm64"} {
		writeMeta(t, dist, build.Meta{Name: "ctx", Version: "4.1.1", OS: "linux", Arch: []string{"amd64", "arm64"}, BuiltFor: a, SHA256: "same", Size: 3, Manifest: json.RawMessage(nodeManifest)})
	}
	idx, err := index.Generate(dist, "", "pellumai/mcp-library", at)
	if err != nil {
		t.Fatal(err)
	}
	bs := idx.Packages[0].Versions[0].Blobs
	if len(bs) != 2 || bs[0].SHA256 != "same" || bs[1].SHA256 != "same" || bs[0].Arch == bs[1].Arch {
		t.Fatalf("blobs %v, want two rows with one digest", bs)
	}
}

func TestGenerate_ManifestBytesAreCarriedThrough(t *testing.T) {
	dist := t.TempDir()
	writeMeta(t, dist, build.Meta{Name: "ctx", Version: "4.1.1", OS: "linux", Arch: []string{"amd64"}, BuiltFor: "amd64", SHA256: "d", Size: 3, Manifest: json.RawMessage(nodeManifest)})
	idx, err := index.Generate(dist, "", "pellumai/mcp-library", at)
	if err != nil {
		t.Fatal(err)
	}
	b, err := index.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"manifest":`+nodeManifest)) {
		t.Fatalf("the manifest bytes, unknown field and all, are not carried byte for byte:\n%s", b)
	}
	if got := idx.Packages[0]; got.Title != "Context" || got.Description != "docs" {
		t.Fatalf("package fields %+v", got)
	}
	if idx.Packages[0].Versions[0].PublishedAt != "2026-09-23T12:00:00Z" || idx.GeneratedAt != "2026-09-23T12:00:00Z" {
		t.Fatalf("times %s %s", idx.Packages[0].Versions[0].PublishedAt, idx.GeneratedAt)
	}
}

func TestGenerate_RefusesASizeDisagreement(t *testing.T) {
	dist := t.TempDir()
	writeMeta(t, dist, build.Meta{Name: "ctx", Version: "1", OS: "linux", Arch: []string{"amd64"}, BuiltFor: "amd64", SHA256: "d", Size: 3, Manifest: json.RawMessage(nodeManifest)})
	if err := os.WriteFile(filepath.Join(dist, "ctx-amd64.tar.gz"), []byte("xxxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Generate(dist, "", "l", at); err == nil {
		t.Fatal("a sidecar whose size disagrees with its tar is accepted")
	}
}

func TestParse_RefusesAHigherSchemaVersion(t *testing.T) {
	if _, err := index.Parse([]byte(`{"schema_version":2}`)); err == nil {
		t.Fatal("schema_version 2 is read")
	}
}

func TestRetire_RemovesTheVersionOnly(t *testing.T) {
	idx := sample()
	if err := index.Retire(&idx, "zeta@1.0.0"); err != nil {
		t.Fatal(err)
	}
	for _, p := range idx.Packages {
		if p.Name == "zeta" && (len(p.Versions) != 1 || p.Versions[0].Version != "2.0.0") {
			t.Fatalf("zeta versions %v", p.Versions)
		}
	}
	if err := index.Retire(&idx, "alpha@1.0.0"); err != nil || len(idx.Packages) != 1 {
		t.Fatalf("retiring the last version leaves %v, %v", idx.Packages, err)
	}
	if err := index.Retire(&idx, "nope@1"); err == nil {
		t.Fatal("retiring an absent version succeeds")
	}
}
