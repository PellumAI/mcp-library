package pack

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// ErrRefused marks an entry that may not go into a package, so callers can
// tell "this tree is not packageable" from "the disk broke".
var ErrRefused = errors.New("pack: refused")

// Refuse reports why rel may not go into a package, or nil. Its rules are the
// pack-time mirror of the gateway-side unpacker's: an archive this function
// accepts is one the unpacker accepts, and the point of checking twice is that
// the failure is a contributor's CI run rather than an operator's deploy.
//
// rel is slash-separated and relative to the package root; link is the
// symlink target when fi is a symlink.
//
// A relative symlink that stays inside the tree is allowed on purpose:
// node_modules/.bin/ is a directory of them, and rewriting them into copies
// would both bloat the tar and break packages that check realpath.
func Refuse(rel string, fi fs.FileInfo, link string) error {
	switch {
	case strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\"):
		return fmt.Errorf("%w: %s is an absolute path", ErrRefused, rel)
	case rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || strings.HasSuffix(rel, "/.."):
		return fmt.Errorf("%w: %s escapes the package root", ErrRefused, rel)
	case fi.Mode()&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0:
		return fmt.Errorf("%w: %s is a device, fifo or socket", ErrRefused, rel)
	case fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0:
		return fmt.Errorf("%w: %s carries setuid, setgid or sticky", ErrRefused, rel)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	if strings.HasPrefix(link, "/") {
		return fmt.Errorf("%w: %s is a symlink to the absolute path %s", ErrRefused, rel, link)
	}
	target := path.Clean(path.Join(path.Dir(rel), link))
	if target == ".." || strings.HasPrefix(target, "../") {
		return fmt.Errorf("%w: %s is a symlink to %s, outside the package", ErrRefused, rel, link)
	}
	return nil
}
