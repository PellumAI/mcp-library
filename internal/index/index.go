// Package index is the signed document that is the library's entire contract
// with a gateway. Every field below appears in the 2026-09-22 spec's "Remote
// master library / The contract" section, and the json tags are that
// document's field names. Renaming one is a breaking change for every gateway
// that pinned this library, so the tags are pinned by a test the way
// internal/mcpwire's are in MCPGW, and MCPGW's internal/mcpcatalog/library
// mirrors these types field for field.
package index

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/recipe"
)

// SchemaVersion is the only value this generator writes and the only value a
// reader of this package accepts. A gateway refuses a higher version rather
// than reading past fields it does not know.
const SchemaVersion = 1

// Index is the whole document.
type Index struct {
	SchemaVersion int    `json:"schema_version"`
	GeneratedAt   string `json:"generated_at"`
	Library       string `json:"library"`
	// SigningKeys names every key id whose detached signature is published
	// beside this index and beside every blob it lists, newest first. During
	// a rotation overlap it has two entries; otherwise one. A gateway picks
	// the first id it holds a public key for.
	SigningKeys []string  `json:"signing_keys"`
	Packages    []Package `json:"packages"`
}

// Package is one server, with every version the library still publishes.
type Package struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Homepage    string    `json:"homepage,omitempty"`
	License     string    `json:"license,omitempty"`
	Categories  []string  `json:"categories,omitempty"`
	Versions    []Version `json:"versions"`
}

// Version is one release of one server.
type Version struct {
	Version     string `json:"version"`
	PublishedAt string `json:"published_at"`
	Blobs       []Blob `json:"blobs"`
	// Manifest is the exact bytes of mcpgw-package.json from inside the tar.
	// It is json.RawMessage rather than a typed struct on purpose: this
	// repository does not own the manifest schema -- MCPGW does -- and a
	// generator that re-marshalled it through local types would quietly
	// strip every field MCPGW adds from every index it publishes.
	Manifest json.RawMessage `json:"manifest"`
}

// Blob is one tar, addressed by its own digest.
//
// An arch-independent package emits one blob per declared arch, all carrying
// the same digest. That looks redundant and is not: a gateway looks a blob up
// by the pair it runs on, and a lookup that had to know "sometimes arch means
// every arch" would have a special case in it.
type Blob struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Empty is the index of a library that has published nothing yet.
func Empty(library string) Index {
	return Index{SchemaVersion: SchemaVersion, GeneratedAt: "1970-01-01T00:00:00Z", Library: library, SigningKeys: []string{}, Packages: []Package{}}
}

// Parse decodes an index and refuses a schema_version this reader does not
// know, the rule every gateway applies too.
func Parse(b []byte) (Index, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return Index{}, fmt.Errorf("index: %w", err)
	}
	if idx.SchemaVersion > SchemaVersion {
		return Index{}, fmt.Errorf("index: schema_version %d is newer than this tool writes, which is %d", idx.SchemaVersion, SchemaVersion)
	}
	if idx.SchemaVersion != SchemaVersion {
		return Index{}, fmt.Errorf("index: schema_version %d is not %d", idx.SchemaVersion, SchemaVersion)
	}
	return idx, nil
}

// Marshal writes idx in the one form this repository ever publishes: compact
// JSON, HTML escaping off, a trailing newline, and every slice already in
// canonical order. It is deterministic given the content, which is what lets
// CI assert that regenerating the index from unchanged inputs produces
// unchanged bytes.
//
// Compact rather than indented because encoding/json re-indents a
// json.RawMessage under SetIndent, and a manifest's bytes in the index must be
// exactly the bytes inside its tar. Pipe it through jq to read it.
func Marshal(idx Index) ([]byte, error) {
	Canonicalise(&idx)
	if idx.SigningKeys == nil {
		idx.SigningKeys = []string{}
	}
	if idx.Packages == nil {
		idx.Packages = []Package{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(idx); err != nil {
		return nil, fmt.Errorf("index: marshal: %w", err)
	}
	return buf.Bytes(), nil
}

// Canonicalise sorts every slice into the order Marshal publishes: packages by
// name; versions newest first by published_at and, on a tie, by version
// descending under a plain string comparison, which is well defined for the
// strings this library accepts and is not pretending to be a semver ordering;
// blobs by os then arch. signing_keys keeps its order, which is meaningful.
func Canonicalise(idx *Index) {
	sort.SliceStable(idx.Packages, func(i, j int) bool { return idx.Packages[i].Name < idx.Packages[j].Name })
	for pi := range idx.Packages {
		vs := idx.Packages[pi].Versions
		sort.SliceStable(vs, func(i, j int) bool {
			if vs[i].PublishedAt != vs[j].PublishedAt {
				return vs[i].PublishedAt > vs[j].PublishedAt
			}
			return vs[i].Version > vs[j].Version
		})
		for vi := range vs {
			bs := vs[vi].Blobs
			sort.SliceStable(bs, func(i, j int) bool {
				if bs[i].OS != bs[j].OS {
					return bs[i].OS < bs[j].OS
				}
				return bs[i].Arch < bs[j].Arch
			})
		}
	}
}

// GeneratedAt resolves the generation time: an explicit value, then
// SOURCE_DATE_EPOCH, then the wall clock, always as RFC 3339 in UTC.
func GeneratedAt(flag string) (time.Time, error) {
	if flag != "" {
		t, err := time.Parse(time.RFC3339, flag)
		if err != nil {
			return time.Time{}, fmt.Errorf("index: --generated-at %q is not RFC 3339", flag)
		}
		return t.UTC(), nil
	}
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		var n int64
		if _, err := fmt.Sscan(s, &n); err != nil {
			return time.Time{}, fmt.Errorf("index: SOURCE_DATE_EPOCH %q is not an integer", s)
		}
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Now().UTC().Truncate(time.Second), nil
}

// Generate builds an Index from the .meta.json sidecars under distDir and the
// human-facing fields in servers/<name>/package.yaml and manifest.json. Every
// version it lists is published at generatedAt.
func Generate(distDir, serversDir, library string, generatedAt time.Time) (Index, error) {
	metas, err := readMetas(distDir)
	if err != nil {
		return Index{}, err
	}
	at := generatedAt.UTC().Format(time.RFC3339)
	type key struct{ name, version string }
	byVersion := map[key][]build.Meta{}
	var order []key
	for _, m := range metas {
		k := key{m.Name, m.Version}
		if _, seen := byVersion[k]; !seen {
			order = append(order, k)
		}
		byVersion[k] = append(byVersion[k], m)
	}
	pkgs := map[string]*Package{}
	for _, k := range order {
		ms := byVersion[k]
		v, err := versionFrom(ms, at)
		if err != nil {
			return Index{}, err
		}
		p, ok := pkgs[k.name]
		if !ok {
			p, err = packageFrom(serversDir, k.name, ms[0].Manifest)
			if err != nil {
				return Index{}, err
			}
			pkgs[k.name] = p
		}
		p.Versions = append(p.Versions, v)
	}
	idx := Index{SchemaVersion: SchemaVersion, GeneratedAt: at, Library: library, SigningKeys: []string{}}
	for _, p := range pkgs {
		idx.Packages = append(idx.Packages, *p)
	}
	Canonicalise(&idx)
	return idx, nil
}

func readMetas(distDir string) ([]build.Meta, error) {
	paths, err := filepath.Glob(filepath.Join(distDir, "*.meta.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []build.Meta
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var m build.Meta
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("index: %s: %w", filepath.Base(p), err)
		}
		tar := strings.TrimSuffix(p, ".meta.json") + ".tar.gz"
		fi, err := os.Stat(tar)
		if err != nil {
			return nil, fmt.Errorf("index: %s has no tar beside it", filepath.Base(p))
		}
		if fi.Size() != m.Size {
			return nil, fmt.Errorf("index: %s says %d bytes and %s is %d", filepath.Base(p), m.Size, filepath.Base(tar), fi.Size())
		}
		out = append(out, m)
	}
	return out, nil
}

// versionFrom folds every build of one name@version into one Version. The
// manifest is carried byte for byte when every build packaged the same bytes,
// which is the arch-independent case; when per-arch builds narrowed arch
// differently, it is the canonical render of the first with arch set to the
// union, so the index says which arches the version serves.
func versionFrom(ms []build.Meta, at string) (Version, error) {
	v := Version{Version: ms[0].Version, PublishedAt: at}
	seen := map[string]Blob{}
	var archs []string
	same := true
	for _, m := range ms {
		if !bytes.Equal(m.Manifest, ms[0].Manifest) {
			same = false
		}
		for _, a := range m.Arch {
			b := Blob{OS: m.OS, Arch: a, SHA256: m.SHA256, Size: m.Size}
			if prev, dup := seen[m.OS+"/"+a]; dup {
				if prev.SHA256 != b.SHA256 {
					return Version{}, fmt.Errorf("index: %s@%s %s/%s was built twice to different digests, %s and %s", m.Name, m.Version, m.OS, a, prev.SHA256, b.SHA256)
				}
				continue
			}
			seen[m.OS+"/"+a] = b
			v.Blobs = append(v.Blobs, b)
			archs = append(archs, a)
		}
	}
	if same {
		v.Manifest = slices.Clone(ms[0].Manifest)
		return v, nil
	}
	slices.Sort(archs)
	rendered, err := manifest.Render(ms[0].Manifest, archs, nil)
	if err != nil {
		return Version{}, err
	}
	v.Manifest = rendered
	return v, nil
}

func packageFrom(serversDir, name string, raw json.RawMessage) (*Package, error) {
	m, err := manifest.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("index: %s: %w", name, err)
	}
	p := &Package{Name: name, Title: m.Title, Description: m.Description, Homepage: m.Homepage, License: m.License}
	if serversDir != "" {
		r, err := recipe.Load(filepath.Join(serversDir, name, recipe.FileName))
		switch {
		case err == nil:
			p.Categories = slices.Clone(r.Categories)
		case !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file"):
			return nil, err
		}
	}
	return p, nil
}

// Retire removes name@version from idx. The blob stays wherever it is
// published: a gateway that already pinned the digest keeps working, and one
// browsing the catalogue stops being offered it.
func Retire(idx *Index, nameAtVersion string) error {
	name, version, ok := strings.Cut(nameAtVersion, "@")
	if !ok {
		return fmt.Errorf("index: --retire %q is not <name>@<version>", nameAtVersion)
	}
	for pi := range idx.Packages {
		p := &idx.Packages[pi]
		if p.Name != name {
			continue
		}
		i := slices.IndexFunc(p.Versions, func(v Version) bool { return v.Version == version })
		if i < 0 {
			break
		}
		p.Versions = slices.Delete(p.Versions, i, i+1)
		if len(p.Versions) == 0 {
			idx.Packages = slices.Delete(idx.Packages, pi, pi+1)
		}
		return nil
	}
	return fmt.Errorf("index: %s is not in the index", nameAtVersion)
}
