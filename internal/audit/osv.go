package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
)

// defaultOSVBaseURL is used when OSV.BaseURL is empty.
const defaultOSVBaseURL = "https://api.osv.dev"

// osvBatchSize is the largest query OSV's batch endpoint accepts.
const osvBatchSize = 1000

// Vuln is one vulnerability affecting one locked dependency.
type Vuln struct {
	ID string `json:"id"`
	// Aliases are the advisory's other ids, such as its CVE, as OSV lists
	// them. An audit waiver may name the advisory by any of them.
	Aliases  []string `json:"aliases,omitempty"`
	Severity string   `json:"severity"`
	Fixed    []string `json:"fixed"`
	Dep      Dep      `json:"dep"`
}

// OSV queries the OSV.dev vulnerability database. The zero value talks to
// the public API over http.DefaultClient.
type OSV struct {
	BaseURL string
	HTTP    *http.Client
}

func (o OSV) baseURL() string {
	if o.BaseURL != "" {
		return o.BaseURL
	}
	return defaultOSVBaseURL
}

func (o OSV) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return http.DefaultClient
}

// osvEcosystem maps this library's ecosystem name (as ReadLock produces it)
// to OSV's own ecosystem name.
func osvEcosystem(ecosystem string) string {
	switch ecosystem {
	case "npm":
		return "npm"
	case "PyPI":
		return "PyPI"
	case "Go":
		return "Go"
	default:
		return ecosystem
	}
}

type osvBatchRequest struct {
	Queries []osvBatchQuery `json:"queries"`
}

type osvBatchQuery struct {
	Package osvPackage `json:"package"`
	Version string     `json:"version,omitempty"`
}

type osvPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvBatchResponse struct {
	Results []osvBatchResult `json:"results"`
}

type osvBatchResult struct {
	Vulns []osvVulnRef `json:"vulns"`
}

type osvVulnRef struct {
	ID string `json:"id"`
}

type osvRecord struct {
	ID               string              `json:"id"`
	Aliases          []string            `json:"aliases"`
	Severity         []osvSeverity       `json:"severity"`
	Affected         []osvAffected       `json:"affected"`
	DatabaseSpecific osvDatabaseSpecific `json:"database_specific"`
}

type osvSeverity struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

type osvAffected struct {
	Package osvPackage `json:"package"`
	Ranges  []osvRange `json:"ranges"`
}

type osvRange struct {
	Type   string     `json:"type"`
	Events []osvEvent `json:"events"`
}

type osvEvent struct {
	Introduced   string `json:"introduced,omitempty"`
	Fixed        string `json:"fixed,omitempty"`
	LastAffected string `json:"last_affected,omitempty"`
	Limit        string `json:"limit,omitempty"`
}

type osvDatabaseSpecific struct {
	Severity string `json:"severity"`
}

// Query looks up every dep against OSV in batches of up to 1000, then
// fetches each distinct vulnerability id it gets back for severity and
// fixed versions. A dep with no hits contributes nothing to the result.
func (o OSV) Query(ctx context.Context, deps []Dep) ([]Vuln, error) {
	var vulns []Vuln
	records := map[string]*osvRecord{}

	for start := 0; start < len(deps); start += osvBatchSize {
		end := start + osvBatchSize
		if end > len(deps) {
			end = len(deps)
		}
		chunk := deps[start:end]

		req := osvBatchRequest{Queries: make([]osvBatchQuery, len(chunk))}
		for i, d := range chunk {
			req.Queries[i] = osvBatchQuery{
				Package: osvPackage{Name: d.Name, Ecosystem: osvEcosystem(d.Ecosystem)},
				Version: d.Version,
			}
		}

		var resp osvBatchResponse
		if err := o.post(ctx, "/v1/querybatch", req, &resp); err != nil {
			return nil, fmt.Errorf("audit: osv querybatch: %w", err)
		}
		if len(resp.Results) != len(chunk) {
			return nil, fmt.Errorf("audit: osv querybatch: got %d results for %d queries", len(resp.Results), len(chunk))
		}

		for i, result := range resp.Results {
			dep := chunk[i]
			for _, ref := range result.Vulns {
				record, ok := records[ref.ID]
				if !ok {
					// An OSV id never contains a slash; reject one that
					// does rather than let it steer the request path.
					if ref.ID == "" || strings.ContainsAny(ref.ID, "/\\") {
						return nil, fmt.Errorf("audit: osv querybatch: invalid vuln id %q", ref.ID)
					}
					var rec osvRecord
					if err := o.get(ctx, "/v1/vulns/"+url.PathEscape(ref.ID), &rec); err != nil {
						return nil, fmt.Errorf("audit: osv vuln %s: %w", ref.ID, err)
					}
					record = &rec
					records[ref.ID] = record
				}
				vulns = append(vulns, Vuln{
					ID:       record.ID,
					Aliases:  record.Aliases,
					Severity: severityOf(*record),
					Fixed:    fixedVersions(*record, dep),
					Dep:      dep,
				})
			}
		}
	}
	return vulns, nil
}

func (o OSV) post(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL()+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return o.do(req, out)
}

func (o OSV) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL()+path, nil)
	if err != nil {
		return err
	}
	return o.do(req, out)
}

func (o OSV) do(req *http.Request, out any) error {
	// req's host is always o.baseURL(), fixed by the caller; the only path
	// segment built from a network response is a vuln id, validated
	// slash-free and path-escaped in Query before it ever reaches here, so
	// this can't be steered to a third-party host.
	resp, err := o.client().Do(req) //nolint:gosec // G704: see comment above
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d: %s", req.Method, req.URL, resp.StatusCode, bytes.TrimSpace(body))
	}
	return json.Unmarshal(body, out)
}

// severityOf resolves a vulnerability record to a severity band: the CVSS
// v3 band computed from its severity[] vector, or its database's own band
// if that's absent, or UNKNOWN.
func severityOf(record osvRecord) string {
	for _, s := range record.Severity {
		if s.Type == "CVSS_V3" {
			if band, ok := cvssV3Band(s.Score); ok {
				return band
			}
		}
	}
	if record.DatabaseSpecific.Severity != "" {
		return strings.ToUpper(record.DatabaseSpecific.Severity)
	}
	return "UNKNOWN"
}

// fixedVersions collects the fixes that apply to dep's installed version:
// the "fixed" event closing each affected interval that contains it, in
// first-seen order, without duplicates. A fix for another release branch
// is not one — grpc-go's advisories fix 1.82.x in 1.82.2 and 1.83.x in
// 1.83.2, and a dep on 1.83.1 is not fixed by moving to 1.82.2 — so
// counting every fixed event would block on a fix the dep cannot take.
//
// A range whose versions cannot be ordered (an unparseable version, or an
// ecosystem with no ordering here) contributes every fix it names, since
// the audit then cannot show the dep is outside it. GIT ranges name
// commits, not versions, and contribute nothing.
func fixedVersions(record osvRecord, dep Dep) []string {
	var fixed []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			fixed = append(fixed, v)
		}
	}
	for _, aff := range record.Affected {
		if aff.Package.Name != "" && aff.Package.Name != dep.Name {
			continue
		}
		for _, rng := range aff.Ranges {
			if rng.Type == "GIT" {
				continue
			}
			for _, v := range rangeFixes(dep.Ecosystem, dep.Version, rng.Events) {
				add(v)
			}
		}
	}
	return fixed
}

// rangeFixes walks one range's events in version order, as the OSV schema
// defines them: each "introduced" opens an affected interval, the next
// "fixed", "last_affected" or "limit" closes it. It returns the fixed
// version of every interval containing installed.
func rangeFixes(ecosystem, installed string, events []osvEvent) []string {
	type point struct {
		kind, version string
	}
	var points []point
	for _, ev := range events {
		switch {
		case ev.Introduced != "":
			points = append(points, point{"introduced", ev.Introduced})
		case ev.Fixed != "":
			points = append(points, point{"fixed", ev.Fixed})
		case ev.LastAffected != "":
			points = append(points, point{"last_affected", ev.LastAffected})
		case ev.Limit != "":
			points = append(points, point{"limit", ev.Limit})
		}
	}

	// cmp orders two versions, "0" (the OSV spelling of "since the
	// beginning") below every other; ok false means the range is unordered.
	cmp := func(a, b string) (int, bool) {
		switch {
		case a == "0" && b == "0":
			return 0, true
		case a == "0":
			return -1, true
		case b == "0":
			return 1, true
		}
		return compareVersion(ecosystem, a, b)
	}

	ordered := true
	if _, ok := cmp(installed, installed); !ok || installed == "0" {
		ordered = false
	}
	for _, p := range points {
		if _, ok := cmp(p.version, p.version); !ok {
			ordered = false
		}
	}
	if !ordered {
		var all []string
		for _, p := range points {
			if p.kind == "fixed" {
				all = append(all, p.version)
			}
		}
		return all
	}

	// Insertion sort: stable, and the event lists are a handful long.
	for i := 1; i < len(points); i++ {
		for j := i; j > 0; j-- {
			if c, _ := cmp(points[j-1].version, points[j].version); c <= 0 {
				break
			}
			points[j-1], points[j] = points[j], points[j-1]
		}
	}

	var fixes []string
	open, lower := false, ""
	for _, p := range points {
		if p.kind == "introduced" {
			if !open {
				open, lower = true, p.version
			}
			continue
		}
		if !open {
			continue
		}
		open = false
		if p.kind != "fixed" {
			continue // last_affected and limit close the interval with no fix
		}
		atOrAbove, _ := cmp(installed, lower)
		below, _ := cmp(installed, p.version)
		if atOrAbove >= 0 && below < 0 {
			fixes = append(fixes, p.version)
		}
	}
	return fixes
}

// CVSS v3.1 base metric weights (https://www.first.org/cvss/v3-1/specification-document#Base-Metrics).
var (
	cvssAV          = map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2}
	cvssAC          = map[string]float64{"L": 0.77, "H": 0.44}
	cvssPRUnchanged = map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}
	cvssPRChanged   = map[string]float64{"N": 0.85, "L": 0.68, "H": 0.50}
	cvssUI          = map[string]float64{"N": 0.85, "R": 0.62}
	cvssCIA         = map[string]float64{"N": 0.0, "L": 0.22, "H": 0.56}
)

// cvssV3Band computes the base score of a CVSS v3(.1) vector string, such
// as "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", and returns its band.
// ok is false if the vector is missing a metric this needs.
func cvssV3Band(vector string) (band string, ok bool) {
	metrics := map[string]string{}
	for _, part := range strings.Split(vector, "/") {
		key, value, found := strings.Cut(part, ":")
		if found {
			metrics[key] = value
		}
	}

	av, ok1 := cvssAV[metrics["AV"]]
	ac, ok2 := cvssAC[metrics["AC"]]
	ui, ok3 := cvssUI[metrics["UI"]]
	c, ok4 := cvssCIA[metrics["C"]]
	i, ok5 := cvssCIA[metrics["I"]]
	a, ok6 := cvssCIA[metrics["A"]]
	scope := metrics["S"]
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || (scope != "U" && scope != "C") {
		return "", false
	}

	prTable := cvssPRUnchanged
	if scope == "C" {
		prTable = cvssPRChanged
	}
	pr, ok7 := prTable[metrics["PR"]]
	if !ok7 {
		return "", false
	}

	iss := 1 - (1-c)*(1-i)*(1-a)
	var impact float64
	if scope == "U" {
		impact = 6.42 * iss
	} else {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	}
	if impact <= 0 {
		return "NONE", true
	}

	exploitability := 8.22 * av * ac * pr * ui
	var base float64
	if scope == "U" {
		base = cvssRoundup(math.Min(impact+exploitability, 10))
	} else {
		base = cvssRoundup(math.Min(1.08*(impact+exploitability), 10))
	}
	return cvssBand(base), true
}

// cvssRoundup is CVSS's specified rounding: up to the nearest 0.1, computed
// on integer cents to avoid float error at the boundary.
func cvssRoundup(x float64) float64 {
	cents := math.Round(x * 100000)
	if math.Mod(cents, 10000) == 0 {
		return cents / 100000
	}
	return (math.Floor(cents/10000) + 1) / 10
}

func cvssBand(score float64) string {
	switch {
	case score == 0:
		return "NONE"
	case score < 4.0:
		return "LOW"
	case score < 7.0:
		return "MEDIUM"
	case score < 9.0:
		return "HIGH"
	default:
		return "CRITICAL"
	}
}
