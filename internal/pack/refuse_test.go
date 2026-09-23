package pack_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pellumai/mcp-library/internal/pack"
)

// Each test constructs the hazard on disk rather than describing it, and
// asserts Write refuses the whole tree with ErrRefused.

func refused(t *testing.T, dir string) {
	t.Helper()
	_, err := pack.Write(io.Discard, dir)
	if !errors.Is(err, pack.ErrRefused) {
		t.Fatalf("got %v, want ErrRefused", err)
	}
}

func base(t *testing.T) string {
	t.Helper()
	return stage(t, [][2]string{{"mcpgw-package.json", "{}"}}, time.Now())
}

func TestRefuse_SymlinkToAnAbsolutePath(t *testing.T) {
	dir := base(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "passwd")); err != nil {
		t.Fatal(err)
	}
	refused(t, dir)
}

func TestRefuse_SymlinkOutOfTheTree(t *testing.T) {
	dir := base(t)
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../etc/passwd", filepath.Join(dir, "a", "b", "passwd")); err != nil {
		t.Fatal(err)
	}
	refused(t, dir)
}

// TestRefuse_AllowsASymlinkInsideTheTree is the node_modules/.bin case, which
// is allowed on purpose.
func TestRefuse_AllowsASymlinkInsideTheTree(t *testing.T) {
	dir := stage(t, [][2]string{{"mcpgw-package.json", "{}"}, {"node_modules/x/cli.js", "x"}}, time.Now())
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", ".bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../x/cli.js", filepath.Join(dir, "node_modules", ".bin", "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Write(io.Discard, dir); err != nil {
		t.Fatalf("an in-tree symlink is refused: %v", err)
	}
}

func TestRefuse_Fifo(t *testing.T) {
	dir := base(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused(t, dir)
}

func TestRefuse_Setuid(t *testing.T) {
	dir := stage(t, [][2]string{{"mcpgw-package.json", "{}"}, {"bin/x", "x"}}, time.Now())
	if err := os.Chmod(filepath.Join(dir, "bin", "x"), 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "bin", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetuid == 0 {
		t.Skip("this filesystem does not keep the setuid bit, so the hazard cannot be constructed here")
	}
	refused(t, dir)
}

// TestRefuse_Device needs mknod, which needs privilege. It says it did not
// run rather than passing silently on a developer's laptop.
func TestRefuse_Device(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mknod needs root; the device refusal is not exercised in this run")
	}
	dir := base(t)
	if err := syscall.Mknod(filepath.Join(dir, "null"), syscall.S_IFCHR|0o600, 0x0103); err != nil {
		t.Fatal(err)
	}
	refused(t, dir)
}

// TestRefuse_DotDotPath is the one hazard a directory walk cannot produce, so
// it calls Refuse directly with the path an attacker-controlled archive would
// carry.
func TestRefuse_DotDotPath(t *testing.T) {
	fi, err := os.Stat(filepath.Join(base(t), "mcpgw-package.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../x", "a/../../x", "..", "/etc/passwd"} {
		if err := pack.Refuse(rel, fi, ""); !errors.Is(err, pack.ErrRefused) {
			t.Errorf("%s: got %v, want ErrRefused", rel, err)
		}
	}
}
