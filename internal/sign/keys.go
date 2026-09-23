package sign

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Signature file names. The unsuffixed .sig carries the newest key's
// signature, which is what a single-key gateway reads; a .sig.<key id>
// sibling exists for every key in signing_keys.
const SigSuffix = ".sig"

var keyIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*?(-v([0-9]+))?$`)

// LoadKeys reads every <dir>/*.pub, ordered newest first by the numeric
// suffix of its id, the same order MCPGW's library.Keys returns. The key id
// is the file name without .pub.
func LoadKeys(dir string) ([]Key, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.pub"))
	if err != nil {
		return nil, err
	}
	var keys []Key
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".pub")
		if !keyIDRE.MatchString(id) {
			return nil, fmt.Errorf("sign: %s is not a key id; use <name>-v<generation>", id)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		k, err := ParsePublicKey(id, b)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		gi, gj := generation(keys[i].ID), generation(keys[j].ID)
		if gi != gj {
			return gi > gj
		}
		return keys[i].ID < keys[j].ID
	})
	return keys, nil
}

func generation(id string) int {
	m := keyIDRE.FindStringSubmatch(id)
	if m == nil || m[2] == "" {
		return 1
	}
	n, _ := strconv.Atoi(m[2])
	return n
}

// Fingerprint is the lower-case hex SHA-256 of the DER public key, the number
// `openssl pkey -pubin -in <key>.pub -outform DER | sha256sum` prints.
func Fingerprint(k Key) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(k.Pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// Order restricts keys to the ids names lists, in that order. An empty list
// means every key, in the order given.
func Order(keys []Key, names []string) []Key {
	if len(names) == 0 {
		return keys
	}
	var out []Key
	for _, n := range names {
		for _, k := range keys {
			if k.ID == n {
				out = append(out, k)
			}
		}
	}
	return out
}

// FileSigs is the lookup VerifyAny takes, over the signature files published
// beside path: the key's own .sig.<id> sibling when there is one, otherwise
// the unsuffixed .sig.
func FileSigs(path string) func(keyID string) ([]byte, bool) {
	return func(keyID string) ([]byte, bool) {
		if b, err := os.ReadFile(path + SigSuffix + "." + keyID); err == nil {
			return b, true
		}
		if b, err := os.ReadFile(path + SigSuffix); err == nil {
			return b, true
		}
		return nil, false
	}
}
