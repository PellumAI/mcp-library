package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/fixture"
)

func init() {
	register("fixture", "generate the miniature library MCPGW's contract test consumes", cmdFixture)
}

// cmdFixture is `mcplib fixture --out <dir> [--rotation] [--build-image ref]`.
func cmdFixture(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("fixture", stderr)
	root := fs.String("root", ".", "repository root")
	out := fs.String("out", "dist/fixture", "the fixture tree to write; it is replaced")
	rotation := fs.Bool("rotation", false, "write the key-rotation rehearsal instead")
	image := fs.String("build-image", os.Getenv("MCPLIB_BUILD_IMAGE"), "override the pinned build image; PROVENANCE records the one used")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := fixture.Generate(ctx, fixture.Options{
		Root:       *root,
		Out:        *out,
		Rotation:   *rotation,
		BuildImage: *image,
		Runner:     build.Docker{Stdout: stderr, Stderr: stderr},
		Log:        func(f string, a ...any) { fmt.Fprintf(stderr, "fixture: "+f+"\n", a...) },
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "fixture: wrote %s\n", *out)
	return nil
}
