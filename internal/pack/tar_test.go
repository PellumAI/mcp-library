package pack_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pellumai/mcp-library/internal/pack"
)

// stage writes files into a fresh directory in the order given, with every
// entry's mtime set to at, and returns the directory.
func stage(t *testing.T, files [][2]string, at time.Time) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f[0]))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f[1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func archive(t *testing.T, dir string) ([]byte, pack.Result) {
	t.Helper()
	var buf bytes.Buffer
	res, err := pack.Write(&buf, dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes(), res
}

func entries(t *testing.T, b []byte) []*tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []*tar.Header
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
}

// TestWrite_ByteStable is the property the whole repository exists to have.
// Two archives built from the same tree, with different file mtimes and in a
// different creation order, must be byte-identical.
func TestWrite_ByteStable(t *testing.T) {
	a := stage(t, [][2]string{
		{"mcpgw-package.json", `{"schema_version":1,"name":"xx"}`},
		{"lib/index.js", "console.log(1)\n"},
		{"LICENSE", "MIT\n"},
	}, time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC))
	b := stage(t, [][2]string{
		{"LICENSE", "MIT\n"},
		{"lib/index.js", "console.log(1)\n"},
		{"mcpgw-package.json", `{"schema_version":1,"name":"xx"}`},
	}, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))

	ba, ra := archive(t, a)
	bb, rb := archive(t, b)
	if !bytes.Equal(ba, bb) {
		t.Fatalf("archives differ: %d bytes vs %d bytes", len(ba), len(bb))
	}
	if ra.SHA256 != rb.SHA256 {
		t.Fatalf("digests differ: %s vs %s", ra.SHA256, rb.SHA256)
	}
	sum := sha256.Sum256(ba)
	if ra.SHA256 != hex.EncodeToString(sum[:]) || ra.Size != int64(len(ba)) {
		t.Fatalf("Result %+v does not describe the bytes written", ra)
	}
}

// TestWrite_OrdersEntriesByPath pins the ordering rule rather than leaving it
// to whatever filepath.WalkDir happens to do on the host filesystem.
func TestWrite_OrdersEntriesByPath(t *testing.T) {
	dir := stage(t, [][2]string{
		{"z.txt", "z"},
		{"mcpgw-package.json", "{}"},
		{"a/b.txt", "b"},
		{"A.txt", "A"},
	}, time.Now())
	b, _ := archive(t, dir)
	var names []string
	for _, h := range entries(t, b) {
		names = append(names, h.Name)
	}
	want := "A.txt,a/,a/b.txt,mcpgw-package.json,z.txt"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("order is %s, want %s", got, want)
	}
}

// TestWrite_NormalisesHeaders asserts uid, gid, uname, gname, mtime and mode
// are all normalised, one subtest per field, because a single "headers are
// normalised" assertion tells you nothing when it fails.
func TestWrite_NormalisesHeaders(t *testing.T) {
	dir := stage(t, [][2]string{{"mcpgw-package.json", "{}"}, {"bin/x", "#!/bin/sh\n"}, {"lib/y", "y"}}, time.Now())
	if err := os.Chmod(filepath.Join(dir, "bin", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := archive(t, dir)
	hs := entries(t, b)
	byName := map[string]*tar.Header{}
	for _, h := range hs {
		byName[h.Name] = h
	}
	check := func(name string, f func(*tar.Header) bool) {
		t.Run(name, func(t *testing.T) {
			for _, h := range hs {
				if !f(h) {
					t.Fatalf("%s: %+v", h.Name, h)
				}
			}
		})
	}
	check("uid", func(h *tar.Header) bool { return h.Uid == 0 })
	check("gid", func(h *tar.Header) bool { return h.Gid == 0 })
	check("uname", func(h *tar.Header) bool { return h.Uname == "" })
	check("gname", func(h *tar.Header) bool { return h.Gname == "" })
	check("mtime", func(h *tar.Header) bool { return h.ModTime.Unix() == 0 })
	check("pax records", func(h *tar.Header) bool { return len(h.PAXRecords) == 0 })
	t.Run("mode", func(t *testing.T) {
		if m := byName["bin/x"].Mode; m != 0o755 {
			t.Fatalf("executable mode is %o, want 755", m)
		}
		if m := byName["lib/y"].Mode; m != 0o644 {
			t.Fatalf("plain file mode is %o, want 644", m)
		}
		if m := byName["bin/"].Mode; m != 0o755 {
			t.Fatalf("directory mode is %o, want 755", m)
		}
	})
}

// TestWrite_RequiresManifestAtRoot refuses a tree with no mcpgw-package.json
// at its root, because an archive that unpacks to something the executor
// cannot read is not a package.
func TestWrite_RequiresManifestAtRoot(t *testing.T) {
	dir := stage(t, [][2]string{{"lib/mcpgw-package.json", "{}"}}, time.Now())
	if _, err := pack.Write(io.Discard, dir); err == nil {
		t.Fatal("a tree with no root manifest is packed")
	}
}

func TestWriteFile_WritesTheChecksumBeside(t *testing.T) {
	dir := stage(t, [][2]string{{"mcpgw-package.json", "{}"}}, time.Now())
	out := filepath.Join(t.TempDir(), "x-amd64.tar.gz")
	res, err := pack.WriteFile(dir, out)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out + ".sha256")
	if err != nil {
		t.Fatal(err)
	}
	if want := res.SHA256 + "  x-amd64.tar.gz\n"; string(b) != want {
		t.Fatalf("sidecar is %q, want %q", b, want)
	}
}
