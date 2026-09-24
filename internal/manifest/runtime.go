package manifest

import (
	"encoding/json"
	"fmt"
	"time"
)

// Param is one parameter `mcplib smoke` must supply to the container as an
// environment variable: the subset of a manifest.json params entry smoke
// needs to know which ones to fill with a dummy value versus a value it
// must have (Required), which to redact from its logs (Secret), and what a
// dummy must look like to parse (Type, one of the schema's string, int,
// bool or enum, and the Default and Enum values it may take).
type Param struct {
	Name     string   `json:"name"`
	Env      string   `json:"env"`
	Secret   bool     `json:"secret"`
	Required bool     `json:"required"`
	Type     string   `json:"type"`
	Default  string   `json:"default"`
	Enum     []string `json:"enum"`
}

// Egress is one rule of the manifest's egress allow-list, the same list
// smoke's proxy sidecar admits and nothing else reaches. The schema makes a
// rule name either a Host or a CIDR, optionally on one Port; Port 0 means
// any port.
type Egress struct {
	Host   string `json:"host"`
	CIDR   string `json:"cidr"`
	Port   int    `json:"port"`
	Reason string `json:"reason"`
}

// Runtime is the manifest fields `mcplib smoke` needs to run a package's
// container and probe it over MCP: params to fill in, hosts the egress proxy
// admits, the resource limits to hand the container runtime, and how long to
// wait for initialize before giving up.
//
// It is deliberately separate from Doc and ParseRuntime deliberately
// separate from Parse: Doc is the handful of fields the builder and the
// recipe rules need at build time, and adding smoke's fields there would
// make every build-time caller carry parsing it never uses. The manifest
// schema itself is unchanged; this is just a second, narrower read of the
// same bytes.
type Runtime struct {
	Params    []Param
	Egress    []Egress
	Resources struct {
		CPUMax    string
		MemoryMax string
		PidsMax   int
	}
	InitializeTimeout time.Duration
}

// runtimeWire is the JSON shape ParseRuntime decodes, matching
// manifest.json's field names before conversion: health.initialize_timeout
// is a duration string on disk, and Runtime carries it already parsed so
// every caller does not repeat that parse.
type runtimeWire struct {
	Params    []Param  `json:"params"`
	Egress    []Egress `json:"egress"`
	Resources struct {
		CPUMax    string `json:"cpu_max"`
		MemoryMax string `json:"memory_max"`
		PidsMax   int    `json:"pids_max"`
	} `json:"resources"`
	Health struct {
		InitializeTimeout string `json:"initialize_timeout"`
	} `json:"health"`
}

// ParseRuntime reads raw manifest bytes for the fields `mcplib smoke` needs.
// It does not validate against the pinned package schema; Schema.Validate
// already does that, and ParseRuntime only runs on bytes that passed it.
func ParseRuntime(raw []byte) (Runtime, error) {
	var w runtimeWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Runtime{}, fmt.Errorf("manifest: runtime: %w", err)
	}
	rt := Runtime{Params: w.Params, Egress: w.Egress}
	rt.Resources.CPUMax = w.Resources.CPUMax
	rt.Resources.MemoryMax = w.Resources.MemoryMax
	rt.Resources.PidsMax = w.Resources.PidsMax
	if w.Health.InitializeTimeout != "" {
		d, err := time.ParseDuration(w.Health.InitializeTimeout)
		if err != nil {
			return Runtime{}, fmt.Errorf("manifest: runtime: health.initialize_timeout %q: %w", w.Health.InitializeTimeout, err)
		}
		rt.InitializeTimeout = d
	}
	return rt, nil
}
