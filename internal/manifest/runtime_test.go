package manifest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pellumai/mcp-library/internal/manifest"
)

// TestParseRuntime_Context7 asserts ParseRuntime reads the fields smoke needs
// straight off a real, committed manifest, not a hand-built fixture, so a
// change to the field names here would break the same way smoke's real input
// would.
func TestParseRuntime_Context7(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "servers", "context7", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := manifest.ParseRuntime(raw)
	if err != nil {
		t.Fatalf("ParseRuntime: %v", err)
	}
	if len(rt.Params) != 1 || rt.Params[0].Env != "CONTEXT7_API_KEY" || !rt.Params[0].Secret {
		t.Fatalf("params = %+v, want one secret param with env CONTEXT7_API_KEY", rt.Params)
	}
	if len(rt.Egress) != 1 || rt.Egress[0].Host != "context7.com" {
		t.Fatalf("egress = %+v, want one entry for context7.com", rt.Egress)
	}
	if rt.InitializeTimeout != 20*time.Second {
		t.Fatalf("initialize timeout = %v, want 20s", rt.InitializeTimeout)
	}
}

// TestParseRuntime_BadTimeout asserts a health.initialize_timeout that
// time.ParseDuration cannot read is an error rather than a silently zero
// timeout, which would make smoke time out immediately.
func TestParseRuntime_BadTimeout(t *testing.T) {
	raw := []byte(`{"health":{"initialize_timeout":"soon"}}`)
	if _, err := manifest.ParseRuntime(raw); err == nil {
		t.Fatal("a health.initialize_timeout ParseDuration cannot read is accepted")
	}
}
