package smoke

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/manifest"
)

func TestEgressHosts(t *testing.T) {
	rt := manifest.Runtime{
		Params: []manifest.Param{{Name: "grafana_url", Env: "GRAFANA_URL", Required: true}},
		Egress: []manifest.Egress{{Host: "api.example.com"}, {Host: "${grafana_url.host}"}},
	}
	allow, values, err := egressHosts(rt)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api.example.com", "grafana_url.smoke.invalid"}; !slices.Equal(allow, want) {
		t.Errorf("allow = %q, want %q", allow, want)
	}
	if values["grafana_url"] != "https://grafana_url.smoke.invalid" || len(values) != 1 {
		t.Errorf("values = %v", values)
	}

	for _, host := range []string{"${nope.host}", "${grafana_url.port}", "x.${grafana_url.host}"} {
		rt.Egress = []manifest.Egress{{Host: host}}
		if _, _, err := egressHosts(rt); err == nil {
			t.Errorf("egressHosts accepted %q", host)
		}
	}

	allow, _, err = egressHosts(manifest.Runtime{})
	if err != nil || allow == nil || len(allow) != 0 {
		t.Errorf("no egress: allow = %#v, %v; want empty and non-nil", allow, err)
	}
}

func TestCheckInputSchema(t *testing.T) {
	for _, c := range []struct {
		schema string
		want   string // "" is valid
	}{
		{`{"type":"object","properties":{"text":{"type":"string"}}}`, ""},
		{`{"type":"object","$schema":"http://json-schema.org/draft-07/schema#"}`, ""},
		{`{"type":"object","properties":{"n":{"type":"integer","minimum":"zero"}}}`, "not a valid JSON Schema"},
		{`{"type":"string"}`, `not "object"`},
		{`{"properties":{}}`, `not "object"`},
		{`[]`, "not a JSON object"},
		{``, "no inputSchema"},
		{`null`, "no inputSchema"},
		{`{"type":"object","$ref":"https://example.com/s.json"}`, "not a valid JSON Schema"},
		{`{"type":"object","$ref":"file:///etc/passwd"}`, "not a valid JSON Schema"},
	} {
		err := checkInputSchema(json.RawMessage(c.schema))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.schema, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want %q", c.schema, err, c.want)
		}
	}
}

func TestSnapshot(t *testing.T) {
	got, err := Snapshot([]Tool{
		{Name: "zeta", Description: "Z <b>", InputSchema: json.RawMessage(`{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"integer","maximum":9007199254740993}}}`)},
		{Name: "alpha", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `[
  {
    "name": "alpha",
    "description": "",
    "inputSchema": {
      "type": "object"
    }
  },
  {
    "name": "zeta",
    "description": "Z <b>",
    "inputSchema": {
      "properties": {
        "a": {
          "maximum": 9007199254740993,
          "type": "integer"
        },
        "b": {
          "type": "string"
        }
      },
      "type": "object"
    }
  }
]
`
	if string(got) != want {
		t.Errorf("snapshot:\n%s\nwant:\n%s", got, want)
	}
}

func TestCheckSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SnapshotName)
	snap := []byte("[]\n")

	var rep Report
	checkSnapshot(Input{Snapshot: path}, snap, &rep, t.Logf)
	if len(rep.Failures) != 1 || !strings.HasPrefix(rep.Failures[0], "snapshot missing") {
		t.Errorf("missing snapshot: %q", rep.Failures)
	}

	rep = Report{Failures: []string{"earlier failure"}}
	checkSnapshot(Input{Snapshot: path, WriteSnapshot: true}, snap, &rep, t.Logf)
	if _, err := os.Stat(path); err == nil {
		t.Error("wrote a snapshot from a failed run")
	}

	rep = Report{}
	checkSnapshot(Input{Snapshot: path, WriteSnapshot: true}, snap, &rep, t.Logf)
	if b, err := os.ReadFile(path); err != nil || string(b) != string(snap) || len(rep.Failures) != 0 {
		t.Fatalf("write: %q, %v, %q", b, err, rep.Failures)
	}

	checkSnapshot(Input{Snapshot: path}, snap, &rep, t.Logf)
	if len(rep.Failures) != 0 {
		t.Errorf("matching snapshot failed: %q", rep.Failures)
	}
	checkSnapshot(Input{Snapshot: path}, []byte("[1]\n"), &rep, t.Logf)
	if len(rep.Failures) != 1 || !strings.HasPrefix(rep.Failures[0], "snapshot drift") {
		t.Errorf("drift: %q", rep.Failures)
	}
}

func TestReadProxyLog(t *testing.T) {
	dir := t.TempDir()
	got, err := readProxyLog(filepath.Join(dir, "absent.jsonl"))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("absent log = %#v, %v", got, err)
	}
	path := filepath.Join(dir, "proxy.jsonl")
	body := `{"host":"a.com","port":443,"allowed":true,"time":"2026-09-24T00:00:00Z"}` + "\n" +
		`{"host":"example.com","port":443,"allowed":false,"time":"2026-09-24T00:00:01Z"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = readProxyLog(path)
	if err != nil || len(got) != 2 || got[1].Host != "example.com" || got[1].Allowed {
		t.Fatalf("log = %+v, %v", got, err)
	}
}
