package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/pellumai/mcp-library/internal/build"
	"github.com/pellumai/mcp-library/internal/manifest"
	"github.com/pellumai/mcp-library/internal/recipe"
	"github.com/pellumai/mcp-library/internal/target"
)

func init() {
	register("build", "build one package for one arch inside the pinned build image", cmdBuild)
	register("arches", "print a recipe's arch list, one per line", cmdArches)
}

// cmdBuild is `mcplib build --server <name> --arch <arch> --out <dir>`.
func cmdBuild(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("build", stderr)
	root := fs.String("root", ".", "repository root")
	server := fs.String("server", "", "the server under servers/ to build")
	arch := fs.String("arch", "amd64", "the one architecture to build")
	out := fs.String("out", "dist", "where the tar, its .sha256 and its .meta.json land")
	keep := fs.Bool("keep-work", false, "keep the scratch tree, for debugging a failed step")
	image := fs.String("build-image", "", "override EXECUTOR_TARGET.yaml's build_image; for local debugging only, never for a published build")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" {
		return usageErr("--server is required")
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
	r, m, err := loadServer(dir, schema, tg)
	if err != nil {
		return err
	}
	img := tg.BuildImage
	if *image != "" {
		fmt.Fprintf(stderr, "build: --build-image overrides the pinned image; the result is not publishable\n")
		img = *image
	}
	if img == "" {
		return fmt.Errorf("EXECUTOR_TARGET.yaml names no build_image")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	meta, err := build.Run(ctx, build.Options{
		ServerDir:  dir,
		Recipe:     r,
		Manifest:   m,
		Arch:       *arch,
		Out:        *out,
		BuildImage: img,
		Runner:     build.Docker{Stdout: stderr, Stderr: stderr},
		KeepWork:   *keep,
		Log:        func(f string, a ...any) { fmt.Fprintf(stderr, "build: "+f+"\n", a...) },
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s  %s  %d bytes\n", meta.SHA256, filepath.Join(*out, build.FileBase(meta.Name, *arch)+".tar.gz"), meta.Size)
	return nil
}

// cmdArches is `mcplib arches --server <name>`. It exists so CI's
// double-build loop does not parse YAML in bash.
func cmdArches(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("arches", stderr)
	root := fs.String("root", ".", "repository root")
	server := fs.String("server", "", "the server under servers/")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" {
		return usageErr("--server is required")
	}
	r, err := recipe.Load(filepath.Join(*root, "servers", *server, recipe.FileName))
	if err != nil {
		return err
	}
	for _, a := range r.Arch {
		fmt.Fprintln(stdout, a)
	}
	return nil
}
