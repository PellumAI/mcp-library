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

// TestParseRuntime_EgressPortAndCIDR asserts both optional egress_rule
// fields the schema defines are read, so smoke can enforce a port and
// refuse a cidr rule instead of silently widening or dropping either.
func TestParseRuntime_EgressPortAndCIDR(t *testing.T) {
	raw := []byte(`{"egress":[{"host":"api.example.com","port":443,"reason":"api"},{"cidr":"10.0.0.0/8","port":5432}]}`)
	rt, err := manifest.ParseRuntime(raw)
	if err != nil {
		t.Fatalf("ParseRuntime: %v", err)
	}
	want := []manifest.Egress{
		{Host: "api.example.com", Port: 443, Reason: "api"},
		{CIDR: "10.0.0.0/8", Port: 5432},
	}
	if len(rt.Egress) != len(want) {
		t.Fatalf("egress = %+v, want %+v", rt.Egress, want)
	}
	for i := range want {
		if rt.Egress[i] != want[i] {
			t.Errorf("egress[%d] = %+v, want %+v", i, rt.Egress[i], want[i])
		}
	}
}

// TestParseRuntime_ParamTypeDefaultEnum asserts the param fields smoke's
// DummyValue chooses by are read.
func TestParseRuntime_ParamTypeDefaultEnum(t *testing.T) {
	raw := []byte(`{"params":[{"name":"region","type":"enum","env":"REGION","required":true,"enum":["eu","us"],"default":"us"}]}`)
	rt, err := manifest.ParseRuntime(raw)
	if err != nil {
		t.Fatalf("ParseRuntime: %v", err)
	}
	p := rt.Params[0]
	if p.Type != "enum" || p.Default != "us" || len(p.Enum) != 2 || p.Enum[0] != "eu" || !p.Required || p.Env != "REGION" {
		t.Errorf("param = %+v", p)
	}
}
