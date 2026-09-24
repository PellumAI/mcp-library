package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/smoke"
	"github.com/pellumai/mcp-library/internal/target"
)

// stderrTail is how much of a failed package's stderr the summary shows.
const stderrTail = 4 << 10

func init() {
	register("smoke", "run a built package tar in a locked-down container and probe it over MCP", cmdSmoke)
}

// cmdSmoke is `mcplib smoke --server <name> --tar <path> [--write-snapshot]
// [--json]`. The report goes to stdout with --json; the human summary always
// goes to stderr. A failed verdict exits 1.
func cmdSmoke(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("smoke", stderr)
	root := fs.String("root", ".", "repository root")
	server := fs.String("server", "", "the server under servers/ the tar was built from")
	tar := fs.String("tar", "", "the package .tar.gz mcplib build wrote")
	write := fs.Bool("write-snapshot", false, "write servers/<name>/"+smoke.SnapshotName+" from this run instead of comparing against it")
	asJSON := fs.Bool("json", false, "print the report as JSON on stdout")
	image := fs.String("build-image", "", "override EXECUTOR_TARGET.yaml's build_image; for local debugging only")
	self, _ := os.Executable()
	mcplib := fs.String("mcplib-binary", self, "a static linux mcplib binary for the egress proxy sidecar")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *tar == "" {
		return usageErr("--server and --tar are required")
	}
	if *mcplib == "" {
		return usageErr("--mcplib-binary is required when the running binary cannot be located")
	}
	bin, err := filepath.Abs(*mcplib)
	if err != nil {
		return err
	}
	tg, err := target.Load(*root)
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
	dir := filepath.Join(*root, "servers", *server)
	r, _, err := loadServer(dir, schema, tg)
	if err != nil {
		return err
	}
	img := tg.BuildImage
	if *image != "" {
		fmt.Fprintf(stderr, "smoke: --build-image overrides the pinned image; the verdict is not the one CI gives\n")
		img = *image
	}
	if img == "" {
		return errors.New("EXECUTOR_TARGET.yaml names no build_image")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	rep, err := smoke.Run(ctx, smoke.Input{
		Tar:           *tar,
		Name:          r.Name,
		Mode:          r.Smoke.Mode,
		Snapshot:      filepath.Join(dir, smoke.SnapshotName),
		WriteSnapshot: *write,
		Image:         img,
		MCPLib:        bin,
		Log:           func(f string, a ...any) { fmt.Fprintf(stderr, "smoke: "+f+"\n", a...) },
	})
	if err != nil {
		return err
	}
	if *asJSON {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", b); err != nil {
			return err
		}
	}
	summarize(stderr, r.Name, rep)
	if rep.Verdict != smoke.VerdictPass {
		return fmt.Errorf("%s failed smoke with %d failure(s)", r.Name, len(rep.Failures))
	}
	return nil
}

// summarize writes the human reading of a report.
func summarize(w io.Writer, name string, rep smoke.Report) {
	fmt.Fprintf(w, "smoke: %s: %s (mode %s, protocol %q, initialize %d ms, %d tools)\n",
		name, rep.Verdict, rep.Mode, rep.Protocol, rep.InitMillis, rep.ToolCount)
	for _, a := range rep.Egress {
		verdict := "allowed"
		if !a.Allowed {
			verdict = "DENIED"
		}
		fmt.Fprintf(w, "smoke:   egress %s:%d %s\n", a.Host, a.Port, verdict)
	}
	for _, f := range rep.Failures {
		fmt.Fprintf(w, "smoke:   FAIL %s\n", f)
	}
	if rep.Verdict != smoke.VerdictPass && len(rep.Stderr) > 0 {
		tail := rep.Stderr
		if len(tail) > stderrTail {
			tail = tail[len(tail)-stderrTail:]
		}
		fmt.Fprintf(w, "smoke: the package's stderr ended with:\n%s\n", bytes.TrimRight(tail, "\n"))
	}
}
