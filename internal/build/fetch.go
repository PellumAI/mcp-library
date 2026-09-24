package build

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pellumai/mcp-library/internal/pack"
	"github.com/pellumai/mcp-library/internal/recipe"
)

// maxFetch caps one fetched upstream artefact.
const maxFetch = 512 << 20

var httpClient = &http.Client{Timeout: 10 * time.Minute}

// Registries, as variables so tests can point them at an httptest server.
var (
	npmRegistry  = "https://registry.npmjs.org"
	pypiRegistry = "https://pypi.org"
)

// FetchSource places the pinned upstream source into dir and verifies its
// pin. It is the only network-touching function in a build outside the three
// lockfile installers, and it is separated from the steps for exactly that
// reason.
func FetchSource(ctx context.Context, src recipe.Source, dir string) error {
	switch src.Kind {
	case "git":
		return fetchGit(ctx, src, dir)
	case "npm":
		return fetchNPM(ctx, src, dir)
	case "pypi":
		return fetchPyPI(ctx, src, dir)
	case "archive":
		b, err := get(ctx, src.URL)
		if err != nil {
			return err
		}
		if err := checkSHA256(b, src.SHA256); err != nil {
			return fmt.Errorf("%s: %w", src.URL, err)
		}
		return extract(b, src.URL, dir, true)
	}
	return fmt.Errorf("source kind %q is not git, npm, pypi or archive", src.Kind)
}

// fetchGit fetches exactly one commit. Fetching a commit rather than a branch
// is what makes the pin a pin; .git is removed afterwards because it carries
// the fetch time and would otherwise be one stage rule away from a digest.
func fetchGit(ctx context.Context, src recipe.Source, dir string) error {
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
		}
		return nil
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth", "1", src.Repo, src.Commit},
		{"-c", "advice.detachedHead=false", "checkout", "-q", "FETCH_HEAD"},
	} {
		if err := run(args...); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git rev-parse: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != src.Commit {
		return fmt.Errorf("git: fetched %s, the recipe pins %s", got, src.Commit)
	}
	return os.RemoveAll(filepath.Join(dir, ".git"))
}

// fetchNPM fetches name@version from the registry and verifies it against the
// registry's own integrity string, the one the recipe records, so the pin is
// the pin npm itself publishes.
func fetchNPM(ctx context.Context, src recipe.Source, dir string) error {
	at := strings.LastIndex(src.Package, "@")
	name, version := src.Package[:at], src.Package[at+1:]
	meta, err := get(ctx, npmRegistry+"/"+strings.Replace(url.PathEscape(name), "%40", "@", 1)+"/"+version)
	if err != nil {
		return err
	}
	var doc struct {
		Dist struct {
			Tarball   string `json:"tarball"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(meta, &doc); err != nil {
		return fmt.Errorf("npm %s: %w", src.Package, err)
	}
	if doc.Dist.Integrity != src.Integrity {
		return fmt.Errorf("npm %s: the registry publishes integrity %s and the recipe pins %s", src.Package, doc.Dist.Integrity, src.Integrity)
	}
	b, err := get(ctx, doc.Dist.Tarball)
	if err != nil {
		return err
	}
	sum := sha512.Sum512(b)
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != src.Integrity {
		return fmt.Errorf("npm %s: the tarball hashes to %s and the recipe pins %s", src.Package, got, src.Integrity)
	}
	return extract(b, doc.Dist.Tarball, dir, true)
}

// fetchPyPI fetches the one file of name==version whose sha256 the recipe
// pins, sdist or wheel.
func fetchPyPI(ctx context.Context, src recipe.Source, dir string) error {
	name, version, _ := strings.Cut(src.Package, "==")
	want := strings.TrimPrefix(src.Integrity, "sha256=")
	meta, err := get(ctx, pypiRegistry+"/pypi/"+url.PathEscape(name)+"/"+url.PathEscape(version)+"/json")
	if err != nil {
		return err
	}
	var doc struct {
		URLs []struct {
			URL     string            `json:"url"`
			Digests map[string]string `json:"digests"`
		} `json:"urls"`
	}
	if err := json.Unmarshal(meta, &doc); err != nil {
		return fmt.Errorf("pypi %s: %w", src.Package, err)
	}
	for _, u := range doc.URLs {
		if u.Digests["sha256"] != want {
			continue
		}
		b, err := get(ctx, u.URL)
		if err != nil {
			return err
		}
		if err := checkSHA256(b, want); err != nil {
			return fmt.Errorf("pypi %s: %w", src.Package, err)
		}
		return extract(b, u.URL, dir, strings.HasSuffix(u.URL, ".tar.gz"))
	}
	return fmt.Errorf("pypi %s: no published file has sha256 %s", src.Package, want)
}

func get(ctx context.Context, u string) ([]byte, error) {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return nil, fmt.Errorf("refusing to fetch %s: not https", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFetch+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFetch {
		return nil, fmt.Errorf("GET %s: over %d bytes", u, maxFetch)
	}
	return b, nil
}

func checkSHA256(b []byte, want string) error {
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("the download hashes to %s and the recipe pins %s", got, want)
	}
	return nil
}

// Unpack extracts a package .tar.gz into dir under the same refusals as
// extract, so smoke runs a package exactly as far as the executor's own
// unpacker would let it get: a tar that smuggles a symlink escape or a device
// fails here rather than running.
func Unpack(b []byte, dir string) error { return extract(b, "package.tar.gz", dir, false) }

// extract unpacks a .tar.gz, .tgz or .zip into dir, refusing any entry
// pack.Refuse would refuse -- the same function, reused, so a malicious
// upstream archive cannot smuggle a symlink escape in through the fetch
// phase. strip removes a single top-level directory when every entry sits
// under one, which is how release archives and npm tarballs are laid out.
func extract(b []byte, name, dir string, strip bool) error {
	type entry struct {
		hdr  *tar.Header
		body []byte
	}
	var entries []entry
	switch {
	case strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".whl"):
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, f := range zr.File {
			hdr, err := tar.FileInfoHeader(f.FileInfo(), "")
			if err != nil {
				return err
			}
			hdr.Name = f.Name
			var body []byte
			if !f.FileInfo().IsDir() {
				if f.FileInfo().Mode()&fs.ModeSymlink != 0 {
					return fmt.Errorf("%w: %s is a symlink inside a zip", pack.ErrRefused, f.Name)
				}
				rc, err := f.Open()
				if err != nil {
					return err
				}
				body, err = io.ReadAll(io.LimitReader(rc, maxFetch))
				_ = rc.Close()
				if err != nil {
					return err
				}
			}
			entries = append(entries, entry{hdr, body})
		}
	default:
		gz, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			var body []byte
			if hdr.Typeflag == tar.TypeReg {
				if body, err = io.ReadAll(io.LimitReader(tr, maxFetch)); err != nil {
					return err
				}
			}
			entries = append(entries, entry{hdr, body})
		}
	}

	prefix := ""
	if strip {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.hdr.Typeflag == tar.TypeXGlobalHeader {
				continue
			}
			names = append(names, e.hdr.Name)
		}
		prefix = commonTop(names)
	}
	for _, e := range entries {
		rel := strings.TrimPrefix(strings.TrimPrefix(e.hdr.Name, "./"), prefix)
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" {
			continue
		}
		switch e.hdr.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeLink:
			return fmt.Errorf("%w: %s is a hard link", pack.ErrRefused, e.hdr.Name)
		}
		if err := pack.Refuse(rel, e.hdr.FileInfo(), e.hdr.Linkname); err != nil {
			return err
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		switch e.hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(full, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.Symlink(e.hdr.Linkname, full); err != nil {
				return err
			}
		case tar.TypeReg:
			mode := os.FileMode(0o644)
			if e.hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := os.WriteFile(full, e.body, mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s has tar type %q", pack.ErrRefused, e.hdr.Name, e.hdr.Typeflag)
		}
	}
	return nil
}

// commonTop returns "<dir>/" when every name sits under the same single
// top-level directory, and "" otherwise.
func commonTop(names []string) string {
	top := ""
	for _, n := range names {
		first, _, found := strings.Cut(strings.TrimPrefix(n, "./"), "/")
		if !found {
			return ""
		}
		if top == "" {
			top = first
		} else if first != top {
			return ""
		}
	}
	if top == "" {
		return ""
	}
	return top + "/"
}
