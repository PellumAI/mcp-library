package audit

import "testing"

func TestReadLock_NPM(t *testing.T) {
	deps, err := ReadLock("../../servers/context7/overlay/package-lock.json")
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if len(deps) != 107 {
		t.Fatalf("got %d deps, want 107", len(deps))
	}

	// Names aren't unique: nested node_modules can lock two versions of the
	// same package for different consumers (e.g. @opentelemetry/core).
	byName := make(map[string][]Dep, len(deps))
	for _, d := range deps {
		if d.Ecosystem != "npm" {
			t.Fatalf("dep %s: ecosystem = %q, want npm", d.Name, d.Ecosystem)
		}
		byName[d.Name] = append(byName[d.Name], d)
	}

	servers, ok := byName["@upstash/context7-mcp"]
	if !ok {
		t.Fatal("expected the server itself, @upstash/context7-mcp, among the deps")
	}
	if servers[0].Version != "4.1.1" {
		t.Fatalf("server version = %q, want 4.1.1", servers[0].Version)
	}

	nested, ok := byName["content-type"]
	if !ok {
		t.Fatal("expected a nested node_modules dep, content-type, resolved by its own name")
	}
	if nested[0].Version == "" {
		t.Fatal("nested dep content-type has no version")
	}
}

func TestReadLock_Requirements(t *testing.T) {
	deps, err := ReadLock("testdata/requirements.txt")
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	want := []Dep{
		{Ecosystem: "PyPI", Name: "certifi", Version: "2024.8.30"},
		{Ecosystem: "PyPI", Name: "requests", Version: "2.32.3"},
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps, want %d", len(deps), len(want))
	}
	for i, d := range deps {
		if d != want[i] {
			t.Fatalf("dep %d = %+v, want %+v", i, d, want[i])
		}
	}
}

func TestReadLock_Requirements_Unpinned(t *testing.T) {
	_, err := ReadLock("testdata/requirements-unpinned.txt")
	if err == nil {
		t.Fatal("expected an error for an unpinned requirement")
	}
}

func TestReadLock_GoSum(t *testing.T) {
	deps, err := ReadLock("testdata/go.sum")
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	want := []Dep{
		{Ecosystem: "Go", Name: "github.com/pkg/errors", Version: "v0.9.1"},
		{Ecosystem: "Go", Name: "golang.org/x/sync", Version: "v0.8.0"},
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps, want %d", len(deps), len(want))
	}
	for i, d := range deps {
		if d != want[i] {
			t.Fatalf("dep %d = %+v, want %+v", i, d, want[i])
		}
	}
}

func TestReadLock_Unknown(t *testing.T) {
	_, err := ReadLock("testdata/Gemfile.lock")
	if err == nil {
		t.Fatal("expected an error for an unknown lockfile")
	}
}
