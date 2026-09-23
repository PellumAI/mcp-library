package main

import (
	"fmt"
	"io"
	"os"

	"github.com/pellumai/mcp-library/internal/index"
	"github.com/pellumai/mcp-library/internal/site"
)

func init() {
	register("site", "generate the static library site from index.json and check its size budget", cmdSite)
}

// cmdSite is `mcplib site --index index.json --out site [--budget-mib n]`.
func cmdSite(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("site", stderr)
	indexPath := fs.String("index", "dist/index.json", "the index the site is generated from")
	out := fs.String("out", "site", "the site tree; the contract files may already be in it")
	budget := fs.Int64("budget-mib", site.DefaultBudgetMiB, "fail when the assembled tree is larger than this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	b, err := os.ReadFile(*indexPath)
	if err != nil {
		return err
	}
	idx, err := index.Parse(b)
	if err != nil {
		return err
	}
	if err := site.Generate(idx, *out); err != nil {
		return err
	}
	total, err := site.CheckBudget(*out, *budget)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "site: %s generated for %d packages, %d MiB of a %d MiB budget\n", *out, len(idx.Packages), total>>20, *budget)
	return nil
}
