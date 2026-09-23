package build

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pellumai/mcp-library/internal/recipe"
)

// stage applies the recipe's stage rules from the built source tree into a
// clean directory. Anything no rule names is not packaged.
func stage(src, dst string, rules []recipe.StageRule) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for i, r := range rules {
		from := filepath.Join(src, filepath.FromSlash(r.From))
		if _, err := os.Lstat(from); err != nil {
			if errors.Is(err, fs.ErrNotExist) && r.Optional {
				continue
			}
			return fmt.Errorf("stage[%d]: %s is not in the built tree", i, r.From)
		}
		to := filepath.Join(dst, filepath.FromSlash(r.To))
		if _, err := os.Lstat(to); err == nil {
			return fmt.Errorf("stage[%d]: %s is staged twice", i, r.To)
		}
		if err := copyTree(from, to, false); err != nil {
			return fmt.Errorf("stage[%d]: %w", i, err)
		}
	}
	return nil
}

// copyTree copies a file, a symlink or a directory tree from src to dst,
// keeping symlinks as symlinks and the executable bit. With merge, an absent
// src is not an error and existing files in dst are overwritten, which is
// the overlay's semantics.
func copyTree(src, dst string, merge bool) error {
	fi, err := os.Lstat(src)
	if err != nil {
		if merge && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.IsDir() {
		return copyEntry(src, dst, fi)
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if merge {
			_ = os.Remove(target)
		}
		return copyEntry(p, target, info)
	})
}

func copyEntry(src, dst string, fi fs.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	case fi.Mode().IsRegular():
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0o644)
		if fi.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("%s is not a file, directory or symlink", src)
}
