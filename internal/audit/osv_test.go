package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestOSVQuery_Recorded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/querybatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("querybatch: method = %s, want POST", r.Method)
		}
		serveTestdataJSON(t, w, "testdata/osv/querybatch.json")
	})
	mux.HandleFunc("/v1/vulns/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("vulns: method = %s, want GET", r.Method)
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/vulns/")
		serveTestdataJSON(t, w, "testdata/osv/vulns/"+id+".json")
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	osv := OSV{BaseURL: server.URL, HTTP: server.Client()}
	deps := []Dep{
		{Ecosystem: "npm", Name: "lodash", Version: "4.17.15"},
		{Ecosystem: "PyPI", Name: "requests", Version: "2.32.3"},
	}

	vulns, err := osv.Query(context.Background(), deps)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(vulns) != 1 {
		t.Fatalf("got %d vulns, want 1", len(vulns))
	}

	v := vulns[0]
	if v.ID != "GHSA-h4x0-1234-abcd" {
		t.Errorf("ID = %q, want GHSA-h4x0-1234-abcd", v.ID)
	}
	if v.Severity != "HIGH" {
		t.Errorf("Severity = %q, want HIGH", v.Severity)
	}
	if len(v.Fixed) != 1 || v.Fixed[0] != "4.17.19" {
		t.Errorf("Fixed = %v, want [4.17.19]", v.Fixed)
	}
	if v.Dep != deps[0] {
		t.Errorf("Dep = %+v, want %+v", v.Dep, deps[0])
	}
}

func serveTestdataJSON(t *testing.T, w http.ResponseWriter, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(data); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestCVSSV3Band(t *testing.T) {
	cases := []struct {
		vector string
		want   string
	}{
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", "CRITICAL"},
		{"CVSS:3.1/AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:H/A:N", "HIGH"},
		{"CVSS:3.1/AV:L/AC:L/PR:L/UI:R/S:U/C:L/I:N/A:N", "LOW"},
	}
	for _, tc := range cases {
		got, ok := cvssV3Band(tc.vector)
		if !ok {
			t.Errorf("cvssV3Band(%q): not ok", tc.vector)
			continue
		}
		if got != tc.want {
			t.Errorf("cvssV3Band(%q) = %q, want %q", tc.vector, got, tc.want)
		}
	}
}

// grpcRecord is the shape of a real grpc-go advisory: one range with two
// branches, the older one fixed in 1.82.2 and the development line that
// forked at 1.83.0-dev fixed in 1.83.2.
const grpcRecord = `{
  "id": "GHSA-grpc",
  "affected": [{
    "package": {"ecosystem": "Go", "name": "google.golang.org/grpc"},
    "ranges": [{"type": "SEMVER", "events": [
      {"introduced": "0"}, {"fixed": "1.82.2"},
      {"introduced": "1.83.0-dev"}, {"fixed": "1.83.2"}
    ]}]
  }]
}`

func TestFixedVersions_OnlyFixesForTheInstalledBranch(t *testing.T) {
	var rec osvRecord
	if err := json.Unmarshal([]byte(grpcRecord), &rec); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		version string
		want    []string
	}{
		{"v1.79.0", []string{"1.82.2"}},
		{"v1.82.1", []string{"1.82.2"}},
		{"v1.82.2", nil}, // fixed: between the branches, affected by neither
		{"v1.82.9", nil},
		{"v1.83.0", []string{"1.83.2"}},
		{"v1.83.1", []string{"1.83.2"}},
		{"v1.83.2", nil},
		{"v1.90.0", nil},
	}
	for _, c := range cases {
		dep := Dep{Ecosystem: "Go", Name: "google.golang.org/grpc", Version: c.version}
		got := fixedVersions(rec, dep)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: fixed = %v, want %v", c.version, got, c.want)
		}
	}
}

func TestFixedVersions_RangeShapes(t *testing.T) {
	dep := Dep{Ecosystem: "npm", Name: "pkg", Version: "2.5.0"}
	cases := []struct {
		name, affected string
		want           []string
	}{
		{"branches as separate ranges", `[{"ranges": [
			{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "1.9.0"}]},
			{"type": "SEMVER", "events": [{"introduced": "2.0.0"}, {"fixed": "2.6.0"}]}]}]`, []string{"2.6.0"}},
		{"last_affected carries no fix", `[{"ranges": [
			{"type": "SEMVER", "events": [{"introduced": "0"}, {"last_affected": "3.0.0"}]}]}]`, nil},
		{"another package's range is ignored", `[{"package": {"ecosystem": "npm", "name": "other"}, "ranges": [
			{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "9.0.0"}]}]}]`, nil},
		{"git ranges are not versions", `[{"ranges": [
			{"type": "GIT", "events": [{"introduced": "0"}, {"fixed": "abc123"}]}]}]`, nil},
		{"unsorted events are ordered first", `[{"ranges": [
			{"type": "ECOSYSTEM", "events": [{"fixed": "2.6.0"}, {"introduced": "2.0.0"}, {"introduced": "0"}, {"fixed": "1.0.0"}]}]}]`, []string{"2.6.0"}},
		{"an unparseable fix is kept, conservatively", `[{"ranges": [
			{"type": "ECOSYSTEM", "events": [{"introduced": "0"}, {"fixed": "next"}]}]}]`, []string{"next"}},
	}
	for _, c := range cases {
		var rec osvRecord
		if err := json.Unmarshal([]byte(`{"affected": `+c.affected+`}`), &rec); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := fixedVersions(rec, dep)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: fixed = %v, want %v", c.name, got, c.want)
		}
	}
}
