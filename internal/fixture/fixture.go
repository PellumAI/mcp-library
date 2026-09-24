// Package fixture generates the miniature published library MCPGW's contract
// test consumes: two tiny first-party packages built by the real builder,
// signed by the real signing path with a throwaway key, laid out exactly as
// the Pages site lays out the real library.
//
// The whole point of generating it rather than hand-writing JSON is that a
// hand-written fixture tests the fixture author's understanding of the format,
// while this one tests the format.
package fixture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/index"
	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/recipe"
	"github.com/pellumai/mcp-library/internal/sign"
	"github.com/pellumai/mcp-library/internal/target"
)

// Dir is where the fixture servers and keys live, relative to the repository
// root. The curation job only ever looks at servers/, so nothing here can
// reach the index the library publishes.
const Dir = "internal/fixture"

// Servers are the packages the fixture carries: one native, one python, so
// the python backend is exercised end to end on every refresh. The list is
// explicit on purpose: fixture-dialer and fixture-crash sit beside them as
// smoke's negative fixtures, and packaging either would change the contract
// fixture MCPGW's tests consume.
var Servers = []string{"fixture-echo", "fixture-count"}

// The key ids. Old is the fixture's ordinary key; New is the second key the
// rotation rehearsal opens an overlap with.
const (
	OldKey = "library-fixture"
	NewKey = "library-fixture-v2"
)

// GeneratedAt is fixed so that regenerating the fixture from unchanged inputs
// changes only its signatures, which are randomised by ECDSA itself.
var GeneratedAt = time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)

// Options is everything Generate needs.
type Options struct {
	// Root is the repository root.
	Root string
	// Out is the fixture tree to write. It is replaced.
	Out string
	// Rotation writes the key-rotation rehearsal: the index signed by both
	// keys under signing_keys [NewKey, OldKey], fixture-echo signed by both,
	// and fixture-count by OldKey only, modelling a blob published before the
	// overlap opened.
	Rotation bool
	// BuildImage overrides the target's pinned image, for a contributor
	// without pull access to it. PROVENANCE records which one was used.
	BuildImage string
	Runner     build.Runner
	Log        func(format string, a ...any)
}

// Generate writes the fixture tree.
func Generate(ctx context.Context, o Options) error {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	tg, err := target.Load(o.Root)
	if err != nil {
		return err
	}
	schemaBytes, err := tg.Schema()
	if err != nil {
		return err
	}
	schema, err := manifest.CompileSchema(schemaBytes)
	if err != nil {
		return err
	}
	image := tg.BuildImage
	if o.BuildImage != "" {
		image = o.BuildImage
	}
	commit, err := head(o.Root)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "mcplib-fixture-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	dist := filepath.Join(work, "dist")

	tarByName := map[string]string{}
	for _, name := range Servers {
		dir := filepath.Join(o.Root, Dir, "servers", name)
		r, err := recipe.Load(filepath.Join(dir, recipe.FileName))
		if err != nil {
			return err
		}
		r.Source = recipe.Source{Kind: "git", Repo: "https://github.com/PellumAI/mcp-library", Commit: commit}
		raw, err := os.ReadFile(filepath.Join(dir, manifest.FileName))
		if err != nil {
			return err
		}
		if err := schema.Validate(raw); err != nil {
			return fmt.Errorf("fixture: %s: %w", name, err)
		}
		m, err := manifest.Parse(raw)
		if err != nil {
			return err
		}
		if err := recipe.Validate(r, m, tg); err != nil {
			return fmt.Errorf("fixture: %w", err)
		}
		src := filepath.Join(dir, "src")
		meta, err := build.Run(ctx, build.Options{
			ServerDir:  dir,
			Recipe:     r,
			Manifest:   m,
			Arch:       "amd64",
			Out:        dist,
			BuildImage: image,
			Runner:     o.Runner,
			Fetch: func(_ context.Context, _ recipe.Source, into string) error {
				return copyDir(src, into)
			},
			Log: logf,
		})
		if err != nil {
			return err
		}
		tarByName[name] = meta.SHA256
	}

	idx, err := index.Generate(dist, "", "pellumai/mcp-library-fixture", GeneratedAt)
	if err != nil {
		return err
	}
	idx.SigningKeys = []string{OldKey}
	if o.Rotation {
		idx.SigningKeys = []string{NewKey, OldKey}
	}
	b, err := index.Marshal(idx)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(o.Out); err != nil {
		return err
	}
	blobs := filepath.Join(o.Out, "blobs", "sha256")
	keysOut := filepath.Join(o.Out, "keys")
	for _, d := range []string{blobs, keysOut} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	indexPath := filepath.Join(o.Out, "index.json")
	if err := os.WriteFile(indexPath, b, 0o644); err != nil {
		return err
	}

	signer := func(id string) sign.Cosign {
		empty := ""
		return sign.Cosign{KeyRef: filepath.Join(o.Root, Dir, "keys", id+".key"), Password: &empty}
	}
	// The index: under every signing key, newest writing the bare .sig.
	for i, id := range idx.SigningKeys {
		if err := signer(id).SignFile(ctx, indexPath, id, i == 0); err != nil {
			return err
		}
	}
	for _, name := range Servers {
		hex := tarByName[name]
		dst := filepath.Join(blobs, hex)
		if err := copyFile(filepath.Join(dist, build.FileBase(name, "amd64")+".tar.gz"), dst); err != nil {
			return err
		}
		keys := idx.SigningKeys
		if o.Rotation && name == "fixture-count" {
			// Published before the overlap opened: the old key only, and the
			// bare .sig is that old key's.
			keys = []string{OldKey}
		}
		for i, id := range keys {
			if err := signer(id).SignFile(ctx, dst, id, i == 0); err != nil {
				return err
			}
		}
	}
	for _, id := range idx.SigningKeys {
		if err := copyFile(filepath.Join(o.Root, Dir, "keys", id+".pub"), filepath.Join(keysOut, id+".pub")); err != nil {
			return err
		}
	}

	prov := map[string]any{
		"generator":     "mcplib fixture",
		"mcplib_commit": commit,
		"build_image":   image,
		"generated_at":  GeneratedAt.Format(time.RFC3339),
		"rotation":      o.Rotation,
		"signing_keys":  idx.SigningKeys,
		"packages":      tarByName,
	}
	pb, err := json.MarshalIndent(prov, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(o.Out, "PROVENANCE"), append(pb, '\n'), 0o644)
}

func head(root string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("fixture: the fixture records the generating commit, and git rev-parse HEAD failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
