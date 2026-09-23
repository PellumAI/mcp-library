package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pellumai/mcp-library/internal/index"
	"github.com/pellumai/mcp-library/internal/sign"
)

func init() {
	register("sign", "sign an index and blobs with the cosign CLI", cmdSign)
	register("verify", "verify an index and its blobs with the pure-Go verifier", cmdVerify)
	register("verify-blob", "verify one detached cosign signature with the pure-Go verifier", cmdVerifyBlob)
}

// cmdSign is `mcplib sign --key <ref> --key-id <id> [--signing-keys ids]
// [--index index.json] [file...]`. It writes <file>.sig.<id> for every file,
// and the unsuffixed <file>.sig when <id> is the newest signing key.
func cmdSign(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("sign", stderr)
	keyRef := fs.String("key", "env://COSIGN_LIBRARY_KEY", "the cosign --key reference")
	keyID := fs.String("key-id", "library-v1", "the id the key is published under")
	signingKeys := fs.String("signing-keys", "", "comma-separated signing key ids, newest first; defaults to the index's")
	indexPath := fs.String("index", "", "an index.json to sign; refused if its signing_keys does not name --key-id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ids := splitList(*signingKeys)
	files := fs.Args()
	if *indexPath != "" {
		b, err := os.ReadFile(*indexPath)
		if err != nil {
			return err
		}
		idx, err := index.Parse(b)
		if err != nil {
			return err
		}
		if !slices.Contains(idx.SigningKeys, *keyID) {
			return fmt.Errorf("%s names signing_keys %v, which does not include %s; the document and its signatures would disagree", *indexPath, idx.SigningKeys, *keyID)
		}
		if len(ids) == 0 {
			ids = idx.SigningKeys
		} else if !slices.Equal(ids, idx.SigningKeys) {
			return fmt.Errorf("--signing-keys %v disagrees with the index's %v", ids, idx.SigningKeys)
		}
		files = append([]string{*indexPath}, files...)
	}
	if len(ids) == 0 {
		ids = []string{*keyID}
	}
	if !slices.Contains(ids, *keyID) {
		return usageErr("--key-id %s is not among --signing-keys %v", *keyID, ids)
	}
	if len(files) == 0 {
		return usageErr("nothing to sign")
	}
	c := sign.Cosign{KeyRef: *keyRef}
	newest := ids[0] == *keyID
	for _, f := range files {
		if err := c.SignFile(context.Background(), f, *keyID, newest); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "signed %s with %s\n", f, *keyID)
	}
	return nil
}

// cmdVerify is `mcplib verify --keys keys --index index.json [--dist dist]
// [--blobs site/blobs/sha256]`.
func cmdVerify(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("verify", stderr)
	keysDir := fs.String("keys", "keys", "directory of *.pub keys")
	indexPath := fs.String("index", "dist/index.json", "the index to verify")
	dist := fs.String("dist", "", "also verify every <name>-<arch>.tar.gz here against its signature and the index")
	blobs := fs.String("blobs", "", "also verify every blob the index lists, as <dir>/<sha256>")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keys, err := sign.LoadKeys(*keysDir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*indexPath)
	if err != nil {
		return err
	}
	idx, err := index.Parse(raw)
	if err != nil {
		return err
	}
	order := sign.Order(keys, idx.SigningKeys)
	id, err := sign.VerifyAny(order, raw, sign.FileSigs(*indexPath))
	if err != nil {
		return fmt.Errorf("%s: %w under %v", *indexPath, err, idx.SigningKeys)
	}
	fmt.Fprintf(stdout, "verified %s under %s\n", *indexPath, id)

	listed := map[string]bool{}
	for _, p := range idx.Packages {
		for _, v := range p.Versions {
			for _, b := range v.Blobs {
				listed[b.SHA256] = true
			}
		}
	}
	check := func(path string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		hexsum := hex.EncodeToString(sum[:])
		if !listed[hexsum] {
			return fmt.Errorf("%s hashes to %s, which the index does not list", path, hexsum)
		}
		id, err := sign.VerifyAny(order, b, sign.FileSigs(path))
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Fprintf(stdout, "verified %s under %s\n", filepath.Base(path), id)
		return nil
	}
	if *dist != "" {
		tars, err := filepath.Glob(filepath.Join(*dist, "*.tar.gz"))
		if err != nil {
			return err
		}
		for _, t := range tars {
			if err := check(t); err != nil {
				return err
			}
		}
	}
	if *blobs != "" {
		for hexsum := range listed {
			p := filepath.Join(*blobs, hexsum)
			if err := check(p); err != nil {
				return err
			}
			if !strings.HasSuffix(p, hexsum) {
				return fmt.Errorf("%s is not named by its digest", p)
			}
		}
	}
	return nil
}

// cmdVerifyBlob is `mcplib verify-blob --key k.pub --signature f.sig f`.
func cmdVerifyBlob(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("verify-blob", stderr)
	keyPath := fs.String("key", "", "the PEM public key")
	sigPath := fs.String("signature", "", "the detached signature")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" || *sigPath == "" || fs.NArg() != 1 {
		return usageErr("--key, --signature and one file are required")
	}
	pemBytes, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	k, err := sign.ParsePublicKey(strings.TrimSuffix(filepath.Base(*keyPath), ".pub"), pemBytes)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*sigPath)
	if err != nil {
		return err
	}
	payload, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := sign.Verify(k, payload, sig); err != nil {
		return fmt.Errorf("%s: %w", fs.Arg(0), err)
	}
	fmt.Fprintf(stdout, "verified %s under %s\n", fs.Arg(0), k.ID)
	return nil
}
