package audit

import (
	"context"
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
