package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pellumai/mcp-library/internal/index"
)

func init() {
	register("index", "generate index.json from dist/, merged into the previous index", cmdIndex)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

var _ flag.Value = (*multiFlag)(nil)

// cmdIndex is `mcplib index --dist dist --out dist/index.json [--previous f]
// [--servers servers] [--library l] [--generated-at t] [--signing-keys ids]
// [--retire name@version]...`.
func cmdIndex(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("index", stderr)
	dist := fs.String("dist", "dist", "directory holding the .meta.json sidecars and their tars")
	servers := fs.String("servers", "servers", "directory holding the recipes, for categories")
	previous := fs.String("previous", "", "the previously published index to merge into; absent means the first publish")
	library := fs.String("library", "pellumai/mcp-library", "the library name the index carries")
	generatedAt := fs.String("generated-at", "", "RFC 3339 generation time; defaults to SOURCE_DATE_EPOCH, then the clock")
	signingKeys := fs.String("signing-keys", "library-v1", "comma-separated key ids that sign this index, newest first")
	out := fs.String("out", "dist/index.json", "where to write the index")
	var retire multiFlag
	fs.Var(&retire, "retire", "remove <name>@<version> from the index; repeatable. The blob stays published")
	if err := fs.Parse(args); err != nil {
		return err
	}
	at, err := index.GeneratedAt(*generatedAt)
	if err != nil {
		return usageErr("%v", err)
	}
	next, err := index.Generate(*dist, *servers, *library, at)
	if err != nil {
		return err
	}
	next.SigningKeys = splitList(*signingKeys)

	prev := index.Empty(*library)
	if *previous != "" {
		b, err := os.ReadFile(*previous)
		switch {
		case errors.Is(err, os.ErrNotExist):
			fmt.Fprintf(stderr, "index: %s does not exist; treating this as the first publish\n", *previous)
		case err != nil:
			return err
		default:
			if prev, err = index.Parse(b); err != nil {
				return err
			}
		}
	}
	merged, err := index.Merge(prev, next)
	if err != nil {
		return err
	}
	for _, r := range retire {
		if err := index.Retire(&merged, r); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "index: retired %s; its blob stays published\n", r)
	}
	b, err := index.Marshal(merged)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return err
	}
	versions := 0
	for _, p := range merged.Packages {
		versions += len(p.Versions)
	}
	fmt.Fprintf(stdout, "index: %s lists %d packages, %d versions\n", *out, len(merged.Packages), versions)
	return nil
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
