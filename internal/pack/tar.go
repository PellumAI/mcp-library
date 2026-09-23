// Package pack turns a staged directory into the one byte-stable .tar.gz that
// is a package.
//
// Determinism here is a set of five choices, and every one of them is a choice
// somebody could undo by accident:
//
//  1. Entries are sorted by their slash-separated path, in byte order, rather
//     than in whatever order the filesystem walked them.
//  2. Every header's ModTime is the Unix epoch and its AccessTime and
//     ChangeTime are unset, and Uid, Gid, Uname and Gname are zero and empty.
//     Nothing about who built the archive, or when, survives into it.
//  3. Modes are normalised to 0o755 for directories and executables and 0o644
//     for everything else. The executable bit is the only bit that carries
//     information a package needs.
//  4. The tar format is PAX and no PAX records other than a long path are
//     emitted, so a long filename is representable without dragging atime and
//     ctime records in with it.
//  5. gzip runs at a fixed level with an empty Name and a zero ModTime.
//
// Choice 5 has a consequence the repository documents rather than hides:
// compress/gzip's output is a function of the Go release that produced it, so
// "reproducible" means "reproducible under the pinned toolchain". See
// docs/REPRODUCIBILITY.md.
package pack

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// ManifestName is the file that must sit at the root of every package.
const ManifestName = "mcpgw-package.json"

var epoch = time.Unix(0, 0).UTC()

// Result reports what Write produced.
type Result struct {
	// SHA256 is the lower-case hex digest of the compressed bytes. It is the
	// package's identity everywhere: in the index, in an instance's pin, in
	// the executor's cache and in the signature.
	SHA256 string
	// Size is the number of compressed bytes.
	Size int64
	// Entries is the number of tar entries, for the build log.
	Entries int
}

// Write archives the tree rooted at dir into w and returns its digest. The
// tree must contain ManifestName at its root; every entry is checked against
// the refusals in refuse.go before it is written, so a tree that would produce
// an archive the executor refuses to unpack fails here instead.
func Write(w io.Writer, dir string) (Result, error) {
	paths, err := collect(dir)
	if err != nil {
		return Result{}, err
	}
	if !slices.Contains(paths, ManifestName) {
		return Result{}, fmt.Errorf("pack: %s is not at the root of %s", ManifestName, dir)
	}
	if fi, err := os.Lstat(filepath.Join(dir, ManifestName)); err != nil || !fi.Mode().IsRegular() {
		return Result{}, fmt.Errorf("pack: %s at the root of %s is not a regular file", ManifestName, dir)
	}

	sum := sha256.New()
	counted := &countingWriter{w: io.MultiWriter(w, sum)}

	gz, err := gzip.NewWriterLevel(counted, gzip.BestCompression)
	if err != nil {
		return Result{}, fmt.Errorf("pack: gzip: %w", err)
	}
	gz.Name, gz.Comment, gz.ModTime = "", "", time.Time{}

	tw := tar.NewWriter(gz)
	for _, p := range paths {
		if err := writeEntry(tw, dir, p); err != nil {
			return Result{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Result{}, fmt.Errorf("pack: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Result{}, fmt.Errorf("pack: close gzip: %w", err)
	}
	return Result{
		SHA256:  hex.EncodeToString(sum.Sum(nil)),
		Size:    counted.n,
		Entries: len(paths),
	}, nil
}

// collect walks dir and returns every entry's slash-separated relative path,
// sorted in byte order. Directories are included so an empty directory that a
// server needs survives the round trip.
func collect(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pack: walk %s: %w", dir, err)
	}
	slices.Sort(out)
	return out, nil
}

// writeEntry normalises one entry's header and copies its content.
func writeEntry(tw *tar.Writer, dir, rel string) error {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	fi, err := os.Lstat(full)
	if err != nil {
		return fmt.Errorf("pack: stat %s: %w", rel, err)
	}

	link := ""
	if fi.Mode()&fs.ModeSymlink != 0 {
		if link, err = os.Readlink(full); err != nil {
			return fmt.Errorf("pack: readlink %s: %w", rel, err)
		}
	}
	if err := Refuse(rel, fi, link); err != nil {
		return err
	}

	hdr := &tar.Header{
		Name:     rel,
		Linkname: link,
		Format:   tar.FormatPAX,
		ModTime:  epoch,
		Mode:     normalisedMode(fi),
	}
	switch {
	case fi.IsDir():
		hdr.Typeflag = tar.TypeDir
		hdr.Name = rel + "/"
	case fi.Mode()&fs.ModeSymlink != 0:
		hdr.Typeflag = tar.TypeSymlink
	case fi.Mode().IsRegular():
		hdr.Typeflag = tar.TypeReg
		hdr.Size = fi.Size()
	default:
		return fmt.Errorf("%w: %s is not a file, directory or symlink", ErrRefused, rel)
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("pack: write header %s: %w", rel, err)
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil
	}
	f, err := os.Open(full)
	if err != nil {
		return fmt.Errorf("pack: open %s: %w", rel, err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("pack: copy %s: %w", rel, err)
	}
	return nil
}

// normalisedMode keeps the executable bit and discards everything else,
// because that is the only permission an unpacked package tree needs and
// anything more is a property of the machine that built it.
func normalisedMode(fi fs.FileInfo) int64 {
	switch {
	case fi.IsDir():
		return 0o755
	case fi.Mode()&fs.ModeSymlink != 0:
		return 0o777
	case fi.Mode().Perm()&0o111 != 0:
		return 0o755
	default:
		return 0o644
	}
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// WriteFile writes the package for dir to out atomically: into a temporary
// file beside out, renamed into place only once it is complete, so an
// interrupted run leaves no half-archive under a name something might trust.
// It also writes <out>.sha256 in sha256sum format, so shell consumers need no
// JSON parser.
func WriteFile(dir, out string) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return Result{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	res, err := Write(tmp, dir)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Result{}, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return Result{}, err
	}
	line := res.SHA256 + "  " + filepath.Base(out) + "\n"
	if err := os.WriteFile(out+".sha256", []byte(line), 0o644); err != nil {
		return Result{}, err
	}
	return res, nil
}
