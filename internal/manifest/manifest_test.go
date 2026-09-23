package manifest_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/manifest"
)

const good = `{
  "schema_version": 1,
  "name": "xx",
  "version": "1.0.0",
  "title": "X",
  "runtime": "native",
  "os": "linux",
  "arch": ["amd64", "arm64"],
  "entrypoint": ["bin/x"],
  "transport": "stdio",
  "path": "/mcp",
  "resources": {"memory_max": "128Mi", "pids_max": 64},
  "env": {"A": "1"}
}`

func schema(t *testing.T) *manifest.Schema {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contract", "mcpgw-package.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := manifest.CompileSchema(b)
	if err != nil {
		t.Fatalf("the committed contract schema does not compile: %v", err)
	}
	return s
}

func TestValidate_AcceptsAGoodManifest(t *testing.T) {
	if err := schema(t).Validate([]byte(good)); err != nil {
		t.Fatalf("refused: %v", err)
	}
}

func TestValidate_RefusesAnUnknownField(t *testing.T) {
	bad := strings.Replace(good, `"title": "X",`, `"title": "X", "image": "ghcr.io/x",`, 1)
	if err := schema(t).Validate([]byte(bad)); !errors.Is(err, manifest.ErrSchema) {
		t.Fatalf("got %v, want ErrSchema", err)
	}
}

// TestRender_IsCanonical asserts the bytes that go into a tar are a function
// of the content only: the same template rendered twice, and a template with
// its keys in another order, produce identical bytes.
func TestRender_IsCanonical(t *testing.T) {
	a, err := manifest.Render([]byte(good), []string{"amd64"}, map[string]string{"B": "2"})
	if err != nil {
		t.Fatal(err)
	}
	reordered := `{"env":{"A":"1"},"path":"/mcp","schema_version":1,"name":"xx","version":"1.0.0","title":"X","runtime":"native","os":"linux","arch":["amd64","arm64"],"entrypoint":["bin/x"],"transport":"stdio","resources":{"pids_max":64,"memory_max":"128Mi"}}`
	b, err := manifest.Render([]byte(reordered), []string{"amd64"}, map[string]string{"B": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("renders differ:\n%s\n%s", a, b)
	}
	if !strings.Contains(string(a), `"arch":["amd64"]`) {
		t.Fatalf("arch not narrowed: %s", a)
	}
	if err := schema(t).Validate(a); err != nil {
		t.Fatalf("rendered manifest refused: %v", err)
	}
}

func TestRender_RefusesAnEnvTheTemplateAlreadySets(t *testing.T) {
	if _, err := manifest.Render([]byte(good), nil, map[string]string{"A": "2"}); err == nil {
		t.Fatal("a builder env colliding with the template is accepted")
	}
}
