package main

import (
	"fmt"
	"io"

	"github.com/pellumai/mcp-library/internal/pack"
)

func init() {
	register("pack", "pack a staged tree into a deterministic .tar.gz", cmdPack)
}

// cmdPack is `mcplib pack --in <dir> --out <file>`.
func cmdPack(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("pack", stderr)
	in := fs.String("in", "", "the staged tree, with mcpgw-package.json at its root")
	out := fs.String("out", "", "the .tar.gz to write; <out>.sha256 is written beside it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return usageErr("--in and --out are required")
	}
	res, err := pack.WriteFile(*in, *out)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s  %s  %d bytes  %d entries\n", res.SHA256, *out, res.Size, res.Entries)
	return nil
}
