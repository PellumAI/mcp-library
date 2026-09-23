// Package target is EXECUTOR_TARGET.yaml: the one MCPGW build this library
// builds against, the package schema that build publishes, and the runtime
// window its executor image carries.
//
// The contract lives in MCPGW and is consumed here as files, never as a Go
// import: a public content repository cannot import a private product's
// internal packages, and must not try. The two files sit under contract/ and
// the digests in EXECUTOR_TARGET.yaml bind them; a file that does not hash to
// its recorded digest is refused, because it means the library would validate
// against a contract it did not record.
package target

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the target file at the repository root.
const FileName = "EXECUTOR_TARGET.yaml"

// ContractDir is where the two MCPGW contract files are committed.
const ContractDir = "contract"

// SchemaVersion is the only EXECUTOR_TARGET.yaml schema this tool reads. A
// higher one is refused rather than read, the rule MCPGW's bundle reader
// applies to its own format version.
const SchemaVersion = 1

// ErrDigest means a contract file does not hash to the digest recorded for
// it. It is never a warning.
var ErrDigest = errors.New("target: contract digest mismatch")

// ErrRuntime means a runtime line is outside the window. Its message names
// both sides, the way the gateway's runtime_unavailable refusal does, so an
// author sees in CI the sentence an operator would see at claim.
var ErrRuntime = errors.New("runtime_unavailable")

var (
	hex64RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex40RE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// buildImageRE is an image reference pinned by digest, and nothing else.
	buildImageRE = regexp.MustCompile(`^[a-z0-9./_-]+@sha256:[0-9a-f]{64}$`)
)

// Target is EXECUTOR_TARGET.yaml.
type Target struct {
	SchemaVersion int      `yaml:"schema_version"`
	MCPGWRelease  string   `yaml:"mcpgw_release"`
	MCPGWCommit   string   `yaml:"mcpgw_commit"`
	SchemaAsset   string   `yaml:"schema_asset"`
	SchemaSHA256  string   `yaml:"schema_sha256"`
	WindowAsset   string   `yaml:"window_asset"`
	WindowSHA256  string   `yaml:"window_sha256"`
	Runtimes      []string `yaml:"runtimes"`
	BuildImage    string   `yaml:"build_image"`

	dir string
}

// Load reads EXECUTOR_TARGET.yaml from dir and returns it. It does not fetch
// anything: a local validate must work offline, and the contract files are
// committed beside it.
func Load(dir string) (Target, error) {
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return Target{}, fmt.Errorf("target: %w", err)
	}
	t, err := Parse(b)
	if err != nil {
		return Target{}, err
	}
	t.dir = dir
	return t, nil
}

// Parse decodes and checks the target file's own shape.
func Parse(b []byte) (Target, error) {
	var t Target
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return Target{}, fmt.Errorf("target: %s: %w", FileName, err)
	}
	switch {
	case t.SchemaVersion > SchemaVersion:
		return Target{}, fmt.Errorf("target: %s: schema_version %d is newer than this tool reads, which is %d", FileName, t.SchemaVersion, SchemaVersion)
	case t.SchemaVersion != SchemaVersion:
		return Target{}, fmt.Errorf("target: %s: schema_version %d is not %d", FileName, t.SchemaVersion, SchemaVersion)
	case t.MCPGWRelease == "" && !hex40RE.MatchString(t.MCPGWCommit):
		return Target{}, fmt.Errorf("target: %s: name the MCPGW build as mcpgw_release, or as a full 40-character mcpgw_commit", FileName)
	case !hex64RE.MatchString(t.SchemaSHA256):
		return Target{}, fmt.Errorf("target: %s: schema_sha256 must be 64 hex characters", FileName)
	case !hex64RE.MatchString(t.WindowSHA256):
		return Target{}, fmt.Errorf("target: %s: window_sha256 must be 64 hex characters", FileName)
	case t.SchemaAsset == "" || t.WindowAsset == "":
		return Target{}, fmt.Errorf("target: %s: schema_asset and window_asset are required", FileName)
	case len(t.Runtimes) == 0:
		return Target{}, fmt.Errorf("target: %s: runtimes is empty", FileName)
	case t.BuildImage != "" && !buildImageRE.MatchString(t.BuildImage):
		return Target{}, fmt.Errorf("target: %s: build_image %q is not pinned by digest", FileName, t.BuildImage)
	}
	return t, nil
}

// Ref names the pinned MCPGW build for a human: the release tag when there is
// one, the commit otherwise.
func (t Target) Ref() string {
	if t.MCPGWRelease != "" {
		return "MCPGW " + t.MCPGWRelease
	}
	return "MCPGW commit " + t.MCPGWCommit[:12]
}

// AllowsRuntime reports whether line is inside this target's window.
func (t Target) AllowsRuntime(line string) bool { return slices.Contains(t.Runtimes, line) }

// RuntimeError is the refusal for a line outside the window. It names the
// line asked for, the pinned build and the whole window.
func (t Target) RuntimeError(line string) error {
	return fmt.Errorf("%w: runtime %q is outside the executor window for %s: %s",
		ErrRuntime, line, t.Ref(), strings.Join(t.Runtimes, ", "))
}

// CheckSchemaDigest compares the bytes of a schema file against the digest
// recorded in the target. A mismatch means the library is validating against
// a contract it did not record.
func (t Target) CheckSchemaDigest(schemaBytes []byte) error {
	return checkDigest(t.SchemaAsset, schemaBytes, t.SchemaSHA256)
}

// CheckWindow compares the window file against its recorded digest and
// against the runtimes restated in the target, so the two cannot drift.
func (t Target) CheckWindow(windowBytes []byte) error {
	if err := checkDigest(t.WindowAsset, windowBytes, t.WindowSHA256); err != nil {
		return err
	}
	var doc struct {
		SchemaVersion int      `json:"schema_version"`
		Runtimes      []string `json:"runtimes"`
	}
	if err := json.Unmarshal(windowBytes, &doc); err != nil {
		return fmt.Errorf("target: %s: %w", t.WindowAsset, err)
	}
	if doc.SchemaVersion != 1 {
		return fmt.Errorf("target: %s: schema_version %d is not 1", t.WindowAsset, doc.SchemaVersion)
	}
	if !slices.Equal(doc.Runtimes, t.Runtimes) {
		return fmt.Errorf("target: %s lists %v and %s restates %v; they must be identical", t.WindowAsset, doc.Runtimes, FileName, t.Runtimes)
	}
	return nil
}

// Schema reads the committed schema file and checks its digest.
func (t Target) Schema() ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(t.dir, ContractDir, t.SchemaAsset))
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if err := t.CheckSchemaDigest(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Window reads the committed window file and checks it.
func (t Target) Window() ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(t.dir, ContractDir, t.WindowAsset))
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if err := t.CheckWindow(b); err != nil {
		return nil, err
	}
	return b, nil
}

func checkDigest(name string, b []byte, want string) error {
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%w: %s hashes to %s and %s records %s", ErrDigest, name, got, FileName, want)
	}
	return nil
}
