package sign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The vector under testdata/vector is the same committed, cosign-produced
// payload, signature and public key that internal/mcpcatalog/library holds in
// PellumAI/MCPGW. Both verifiers run over it on every test run.
func vector(t *testing.T) (Key, []byte, []byte) {
	t.Helper()
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", "vector", n))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	k, err := ParsePublicKey("vector", read("vector.pub"))
	if err != nil {
		t.Fatal(err)
	}
	return k, read("payload.bin"), read("payload.bin.sig")
}

func newKey(t *testing.T, id string) (Key, func([]byte) []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Key{ID: id, Pub: &priv.PublicKey}, func(p []byte) []byte {
		sum := sha256.Sum256(p)
		sig, err := ecdsa.SignASN1(rand.Reader, priv, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		return []byte(base64.StdEncoding.EncodeToString(sig))
	}
}

func TestVerify_GoodSignature(t *testing.T) {
	k, payload, sig := vector(t)
	if err := Verify(k, payload, sig); err != nil {
		t.Fatalf("the real cosign CLI's signature does not verify: %v", err)
	}
}

func TestVerify_TamperedPayload(t *testing.T) {
	k, payload, sig := vector(t)
	if err := Verify(k, append(bytes.Clone(payload), '!'), sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("got %v", err)
	}
}

func TestVerify_WrongKey(t *testing.T) {
	_, payload, sig := vector(t)
	other, _ := newKey(t, "other")
	if err := Verify(other, payload, sig); err != ErrBadSignature { //nolint:errorlint // the error must not be distinguishable at all
		t.Fatalf("got %v, want exactly ErrBadSignature", err)
	}
}

func TestVerify_MalformedBase64(t *testing.T) {
	k, payload, _ := vector(t)
	if err := Verify(k, payload, []byte("%%%")); !errors.Is(err, ErrBadEncoding) {
		t.Fatalf("got %v", err)
	}
}

func TestParsePublicKey_RefusesRSA(t *testing.T) {
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	if _, err := ParsePublicKey("rsa", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); !errors.Is(err, ErrBadKey) {
		t.Fatalf("got %v", err)
	}
}

func TestParsePublicKey_RefusesP384(t *testing.T) {
	ek, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&ek.PublicKey)
	if _, err := ParsePublicKey("p384", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); !errors.Is(err, ErrBadKey) {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyAny_PrefersTheFirstKeyItHolds(t *testing.T) {
	v2, s2 := newKey(t, "library-v2")
	v1, s1 := newKey(t, "library-v1")
	p := []byte("index")
	sigs := map[string][]byte{"library-v2": s2(p), "library-v1": s1(p)}
	id, err := VerifyAny([]Key{v2, v1}, p, func(id string) ([]byte, bool) { b, ok := sigs[id]; return b, ok })
	if err != nil || id != "library-v2" {
		t.Fatalf("%q, %v", id, err)
	}
}

func TestVerifyAny_FallsBackDuringOverlap(t *testing.T) {
	v1, s1 := newKey(t, "library-v1")
	_, s2 := newKey(t, "library-v2")
	p := []byte("index")
	sigs := map[string][]byte{"library-v2": s2(p), "library-v1": s1(p)}
	id, err := VerifyAny([]Key{v1}, p, func(id string) ([]byte, bool) { b, ok := sigs[id]; return b, ok })
	if err != nil || id != "library-v1" {
		t.Fatalf("%q, %v", id, err)
	}
}

// TestKeys_CommittedLibraryKey pins keys/library-v1.pub to the fingerprint
// the owner recorded when the key was generated.
func TestKeys_CommittedLibraryKey(t *testing.T) {
	keys, err := LoadKeys(filepath.Join("..", "..", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 || keys[0].ID != "library-v1" {
		t.Fatalf("keys %v", keys)
	}
	fp, err := Fingerprint(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if fp != "3838613f0f3482f6a355bc39fe52585c58b4e6ee047f577590558aa920502aaf" {
		t.Fatalf("library-v1 fingerprint %s differs from the recorded one", fp)
	}
}

func TestFileSigs_SiblingThenBare(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "index.json")
	for name, body := range map[string]string{f + ".sig": "bare", f + ".sig.library-v1": "sibling"} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	look := FileSigs(f)
	if b, _ := look("library-v1"); string(b) != "sibling" {
		t.Fatalf("got %q", b)
	}
	if b, _ := look("library-v2"); string(b) != "bare" {
		t.Fatalf("got %q", b)
	}
}
