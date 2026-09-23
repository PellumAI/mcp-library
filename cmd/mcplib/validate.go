package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/recipe"
	"github.com/pellumai/mcp-library/internal/target"
)

func init() {
	register("validate", "validate every recipe and manifest against the pinned executor target", cmdValidate)
}

// cmdValidate is `mcplib validate [--root dir] [--schema file] [./servers/... | servers/<name>]...`.
func cmdValidate(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("validate", stderr)
	root := fs.String("root", ".", "repository root holding EXECUTOR_TARGET.yaml and contract/")
	schemaPath := fs.String("schema", "", "validate against this schema file instead of contract/, after checking its digest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tg, err := target.Load(*root)
	if err != nil {
		return err
	}
	var schemaBytes []byte
	if *schemaPath != "" {
		if schemaBytes, err = os.ReadFile(*schemaPath); err == nil {
			err = tg.CheckSchemaDigest(schemaBytes)
		}
	} else {
		schemaBytes, err = tg.Schema()
	}
	if err != nil {
		return err
	}
	if _, err := tg.Window(); err != nil {
		return err
	}
	schema, err := manifest.CompileSchema(schemaBytes)
	if err != nil {
		return err
	}

	dirs, err := serverDirs(*root, fs.Args())
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		fmt.Fprintf(stdout, "validate: no servers to validate against %s; nothing to do\n", tg.Ref())
		return nil
	}
	failed := 0
	for _, d := range dirs {
		if err := validateServer(d, schema, tg); err != nil {
			failed++
			fmt.Fprintf(stdout, "FAIL %s: %v\n", filepath.Base(d), err)
			continue
		}
		fmt.Fprintf(stdout, "ok   %s\n", filepath.Base(d))
	}
	fmt.Fprintf(stdout, "validate: %d of %d servers valid against %s\n", len(dirs)-failed, len(dirs), tg.Ref())
	if failed > 0 {
		return fmt.Errorf("%d server(s) failed validation", failed)
	}
	return nil
}

func validateServer(dir string, schema *manifest.Schema, tg target.Target) error {
	_, _, err := loadServer(dir, schema, tg)
	return err
}

// loadServer reads, schema-checks and rule-checks one servers/<name>
// directory. It is the one loader validate and build share, so nothing is
// built that validate would have refused.
func loadServer(dir string, schema *manifest.Schema, tg target.Target) (recipe.Recipe, manifest.Doc, error) {
	r, err := recipe.Load(filepath.Join(dir, recipe.FileName))
	if err != nil {
		return recipe.Recipe{}, manifest.Doc{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, manifest.FileName))
	if err != nil {
		return recipe.Recipe{}, manifest.Doc{}, fmt.Errorf("manifest: %w", err)
	}
	if err := schema.Validate(raw); err != nil {
		return recipe.Recipe{}, manifest.Doc{}, err
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return recipe.Recipe{}, manifest.Doc{}, err
	}
	if filepath.Base(dir) != r.Name {
		return recipe.Recipe{}, manifest.Doc{}, fmt.Errorf("recipe: %s: lives in servers/%s; the directory is the name", r.Name, filepath.Base(dir))
	}
	if err := recipe.Validate(r, m, tg); err != nil {
		return recipe.Recipe{}, manifest.Doc{}, err
	}
	return r, m, nil
}

// serverDirs expands the arguments to server directories. No argument and
// ./servers/... both mean every servers/*/ holding a package.yaml.
func serverDirs(root string, args []string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"./servers/..."}
	}
	var out []string
	for _, a := range args {
		if strings.HasSuffix(a, "/...") {
			base := filepath.Join(root, strings.TrimSuffix(a, "/..."))
			entries, err := os.ReadDir(base)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				d := filepath.Join(base, e.Name())
				if _, err := os.Stat(filepath.Join(d, recipe.FileName)); err == nil {
					out = append(out, d)
				}
			}
			continue
		}
		d := filepath.Join(root, a)
		if _, err := os.Stat(filepath.Join(d, recipe.FileName)); err != nil {
			return nil, usageErr("%s has no %s", a, recipe.FileName)
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}
