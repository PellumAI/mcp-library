package index_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/index"
)

func one(name, version, published, sha string) index.Index {
	return index.Index{SchemaVersion: 1, GeneratedAt: published, Library: "l", SigningKeys: []string{"library-v1"},
		Packages: []index.Package{{Name: name, Title: name, Versions: []index.Version{
			{Version: version, PublishedAt: published, Blobs: []index.Blob{{OS: "linux", Arch: "amd64", SHA256: sha, Size: 1}}, Manifest: json.RawMessage(`{}`)},
		}}}}
}

func TestMerge_RefusesAChangedDigest(t *testing.T) {
	prev := one("grafana", "1.5.1", "2026-09-01T00:00:00Z", "aaaa")
	next := one("grafana", "1.5.1", "2026-09-23T00:00:00Z", "bbbb")
	_, err := index.Merge(prev, next)
	if err == nil {
		t.Fatal("a changed digest merges")
	}
	for _, s := range []string{"sha256:aaaa", "sha256:bbbb", "immutable"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q does not name %s", err, s)
		}
	}
}

func TestMerge_KeepsTheOriginalPublicationOfAnUnchangedRebuild(t *testing.T) {
	prev := one("grafana", "1.5.1", "2026-09-01T00:00:00Z", "aaaa")
	next := one("grafana", "1.5.1", "2026-09-23T00:00:00Z", "aaaa")
	out, err := index.Merge(prev, next)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Packages[0].Versions[0].PublishedAt; got != "2026-09-01T00:00:00Z" {
		t.Fatalf("published_at moved to %s", got)
	}
}

func TestMerge_CarriesForwardUntouchedPackages(t *testing.T) {
	prev := one("terraform", "1.3.0", "2026-09-01T00:00:00Z", "tttt")
	next := one("grafana", "1.5.1", "2026-09-23T00:00:00Z", "gggg")
	out, err := index.Merge(prev, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Packages) != 2 || out.Packages[0].Name != "grafana" || out.Packages[1].Name != "terraform" {
		t.Fatalf("packages %v", out.Packages)
	}
	if out.GeneratedAt != "2026-09-23T00:00:00Z" {
		t.Fatalf("generated_at %s is not next's", out.GeneratedAt)
	}
}

func TestMerge_AddsANewVersionWithoutDisturbingTheOld(t *testing.T) {
	prev := one("grafana", "1.5.0", "2026-09-01T00:00:00Z", "old")
	next := one("grafana", "1.5.1", "2026-09-23T00:00:00Z", "new")
	out, err := index.Merge(prev, next)
	if err != nil {
		t.Fatal(err)
	}
	vs := out.Packages[0].Versions
	if len(vs) != 2 || vs[0].Version != "1.5.1" || vs[1].Version != "1.5.0" || vs[1].Blobs[0].SHA256 != "old" {
		t.Fatalf("versions %+v", vs)
	}
}

func TestMerge_RefusesAHigherSchemaVersion(t *testing.T) {
	prev := one("a", "1", "2026-09-01T00:00:00Z", "x")
	prev.SchemaVersion = 2
	if _, err := index.Merge(prev, one("a", "1", "2026-09-01T00:00:00Z", "x")); err == nil {
		t.Fatal("a newer previous index merges")
	}
}

func TestMerge_IntoTheEmptyIndex(t *testing.T) {
	out, err := index.Merge(index.Empty("l"), one("a", "1", "2026-09-23T00:00:00Z", "x"))
	if err != nil || len(out.Packages) != 1 {
		t.Fatalf("%v, %v", out.Packages, err)
	}
}
