package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/pellumai/mcp-library/internal/audit"
)

func init() {
	register("audit", "collect deterministic supply-chain evidence for a server or a resolved source", cmdAudit)
}

// cmdAudit is `mcplib audit --server <name> | --resolve <kind>:<coordinate> [--json]`.
func cmdAudit(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("audit", stderr)
	root := fs.String("root", ".", "repository root")
	server := fs.String("server", "", "the server under servers/ to audit, from its committed recipe")
	resolve := fs.String("resolve", "", "a <kind>:<coordinate> source to audit before servers/<name> exists: npm:<name>@<version>, pypi:<name>==<version>, git:<repo>@<commit>")
	jsonOut := fs.Bool("json", false, "print the report as JSON on stdout, in addition to the summary on stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*server == "") == (*resolve == "") {
		return usageErr("exactly one of --server or --resolve is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	report, err := audit.Run(ctx, audit.Input{
		Root:    *root,
		Server:  *server,
		Resolve: *resolve,
	})
	if err != nil {
		return err
	}

	fmt.Fprint(stderr, report.Summary())
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	}
	if len(report.Blocking) > 0 {
		return fmt.Errorf("audit: %d blocking finding(s)", len(report.Blocking))
	}
	return nil
}
