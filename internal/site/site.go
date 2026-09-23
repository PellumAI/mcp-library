// Package site generates the static library site from the signed index and
// from nothing else. There is no second source of truth about what the
// library publishes, so a page can never describe a package the index does not
// list, a package the index lists always has a page, and regenerating the site
// is never a merge.
//
// Templates are html/template, which escapes by default. That matters: title,
// description and homepage come from a submitted recipe, and a contributor is
// a semi-trusted input.
package site

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"github.com/pellumai/mcp-library/internal/index"
)

//go:embed templates/*.html.tmpl
var templateFS embed.FS

//go:embed static/style.css
var styleCSS []byte

// BaseURL is where Pages serves the site, and so the base a gateway's
// library_source names.
const BaseURL = "https://pellumai.github.io/mcp-library"

var tmpl = template.Must(template.New("site").Funcs(template.FuncMap{
	"mib": func(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) },
	"short": func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		return s
	},
	"join": strings.Join,
}).ParseFS(templateFS, "templates/*.html.tmpl"))

// manifestView is the part of a manifest a server page shows.
type manifestView struct {
	Runtime   string   `json:"runtime"`
	Transport string   `json:"transport"`
	Arch      []string `json:"arch"`
	Params    []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
		Env         string `json:"env"`
		Required    bool   `json:"required"`
		Secret      bool   `json:"secret"`
		Default     string `json:"default"`
	} `json:"params"`
	Egress []struct {
		Host   string `json:"host"`
		CIDR   string `json:"cidr"`
		Port   int    `json:"port"`
		Reason string `json:"reason"`
	} `json:"egress"`
	Resources struct {
		CPUMax    string `json:"cpu_max"`
		MemoryMax string `json:"memory_max"`
		PidsMax   int    `json:"pids_max"`
	} `json:"resources"`
	GatewayAuth  *json.RawMessage `json:"gateway_auth"`
	UpstreamAuth *struct {
		Method string `json:"method"`
		Param  string `json:"param"`
	} `json:"upstream_auth"`
}

type versionView struct {
	index.Version
	M manifestView
}

type packageView struct {
	index.Package
	Newest   versionView
	Versions []versionView
}

type pageData struct {
	Title       string
	Base        string
	GeneratedAt string
	SigningKeys []string
	Packages    []packageView
	Package     packageView
	Root        string
}

// SearchRecord is one row of search.json.
type SearchRecord struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
	Version     string   `json:"version"`
	Runtime     string   `json:"runtime"`
}

// Generate writes the static site for idx into dir: index.html, one
// servers/<name>/index.html per package, search.json and style.css. It
// writes nothing outside dir and nothing the index does not describe.
func Generate(idx index.Index, dir string) error {
	index.Canonicalise(&idx)
	var pkgs []packageView
	for _, p := range idx.Packages {
		if len(p.Versions) == 0 {
			continue
		}
		pv := packageView{Package: p}
		for _, v := range p.Versions {
			var m manifestView
			if err := json.Unmarshal(v.Manifest, &m); err != nil {
				return fmt.Errorf("site: %s@%s: manifest: %w", p.Name, v.Version, err)
			}
			pv.Versions = append(pv.Versions, versionView{Version: v, M: m})
		}
		pv.Newest = pv.Versions[0]
		pkgs = append(pkgs, pv)
	}
	data := pageData{Title: "mcp-library", Base: BaseURL, GeneratedAt: idx.GeneratedAt, SigningKeys: idx.SigningKeys, Packages: pkgs, Root: "."}
	if err := render(filepath.Join(dir, "index.html"), "index.html.tmpl", data); err != nil {
		return err
	}
	for _, p := range pkgs {
		if strings.ContainsAny(p.Name, `/\.`) || p.Name == "" {
			return fmt.Errorf("site: package name %q cannot be a directory name", p.Name)
		}
		d := data
		d.Package = p
		d.Title = p.Title + " | mcp-library"
		d.Root = "../.."
		if err := render(filepath.Join(dir, "servers", p.Name, "index.html"), "server.html.tmpl", d); err != nil {
			return err
		}
	}
	records := make([]SearchRecord, 0, len(pkgs))
	for _, p := range pkgs {
		cats := p.Categories
		if cats == nil {
			cats = []string{}
		}
		records = append(records, SearchRecord{Name: p.Name, Title: p.Title, Description: p.Description, Categories: cats, Version: p.Newest.Version.Version, Runtime: p.Newest.M.Runtime})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(records); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "search.json"), buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "style.css"), styleCSS, 0o644)
}

func render(path, name string, data pageData) error {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("site: %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
