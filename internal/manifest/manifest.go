// Package manifest reads servers/<name>/manifest.json, the template of the
// mcpgw-package.json that ships inside a package tar.
//
// This repository does not own that schema: MCPGW does, and publishes it as
// contract/mcpgw-package.schema.json. So this package validates a manifest
// against those bytes and reads only the handful of fields the builder and
// the recipe rules need. Everything else is carried through as JSON the
// library never interprets, which is what lets it transport a field it has
// never heard of.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// FileName is the template's name under servers/<name>/.
const FileName = "manifest.json"

// PackageManifestName is the file at the root of every package tar.
const PackageManifestName = "mcpgw-package.json"

// Doc is the part of a manifest the library itself reads.
type Doc struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Homepage    string            `json:"homepage"`
	License     string            `json:"license"`
	Runtime     string            `json:"runtime"`
	OS          string            `json:"os"`
	Arch        []string          `json:"arch"`
	Entrypoint  []string          `json:"entrypoint"`
	Transport   string            `json:"transport"`
	Env         map[string]string `json:"env"`

	// Raw is the template exactly as committed.
	Raw []byte `json:"-"`
}

// Parse decodes a manifest template, keeping its raw bytes.
func Parse(raw []byte) (Doc, error) {
	var d Doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return Doc{}, fmt.Errorf("manifest: %w", err)
	}
	d.Raw = bytes.Clone(raw)
	return d, nil
}

// Schema is a compiled package schema.
type Schema struct{ s *jsonschema.Schema }

// CompileSchema compiles the contract's schema bytes.
func CompileSchema(schemaBytes []byte) (*Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("manifest: the package schema does not parse: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mcpgw-package.schema.json", doc); err != nil {
		return nil, fmt.Errorf("manifest: the package schema: %w", err)
	}
	s, err := c.Compile("mcpgw-package.schema.json")
	if err != nil {
		return nil, fmt.Errorf("manifest: the package schema does not compile: %w", err)
	}
	return &Schema{s: s}, nil
}

// ErrSchema wraps a manifest the pinned schema refuses.
var ErrSchema = errors.New("manifest: refused by the pinned package schema")

// Validate checks raw manifest bytes against the schema.
func (s *Schema) Validate(raw []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: not JSON: %v", ErrSchema, err)
	}
	if err := s.s.Validate(v); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("%w: %s", ErrSchema, flatten(ve))
		}
		return fmt.Errorf("%w: %v", ErrSchema, err)
	}
	return nil
}

// flatten renders a validation error tree on one line, so CI prints every
// problem in a manifest together.
func flatten(ve *jsonschema.ValidationError) string {
	lines := strings.Split(strings.TrimSpace(ve.Error()), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, "; ")
}

// Render produces the mcpgw-package.json bytes that go into a tar: the
// template with arch narrowed to what one blob serves and env extended by the
// backend, serialised in one canonical form. Go's encoder sorts object keys,
// so the bytes are a function of the content and nothing else.
func Render(template []byte, arch []string, extraEnv map[string]string) ([]byte, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(template))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: render: %w", err)
	}
	if arch != nil {
		a := make([]any, len(arch))
		for i, s := range arch {
			a[i] = s
		}
		m["arch"] = a
	}
	if len(extraEnv) > 0 {
		env, _ := m["env"].(map[string]any)
		if env == nil {
			env = map[string]any{}
		}
		for k, v := range extraEnv {
			if _, set := env[k]; set {
				return nil, fmt.Errorf("manifest: render: env %s is set by the template and by the builder; remove it from manifest.json", k)
			}
			env[k] = v
		}
		m["env"] = env
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("manifest: render: %w", err)
	}
	return buf.Bytes(), nil
}
