// This file is the twin of internal/mcpcatalog/library/verify.go in
// PellumAI/MCPGW. Everything below the package clause is byte-identical in
// both repositories; do not improve one copy without the other. The shared
// cosign-produced test vector under testdata/vector is what catches a
// divergence, and the cosign-compat workflow re-proves the encoding against
// the real cosign CLI on every push.

package sign

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// cosign sign-blob under a key signs the SHA-256 of the payload with the key's
// algorithm and emits base64 of the raw signature. For the ECDSA P-256 key
// the library uses, that raw signature is ASN.1 DER, which crypto/ecdsa
// verifies directly. So the whole of verification is: decode base64, hash the
// payload, ecdsa.VerifyASN1.

var (
	// ErrBadSignature means the signature did not verify. It is deliberately
	// indistinguishable between "wrong key" and "tampered payload", because a
	// caller that branches on the difference is a caller leaking which one it
	// was.
	ErrBadSignature = errors.New("signature does not verify")
	// ErrBadKey means a public key is not an ECDSA P-256 PKIX key.
	ErrBadKey = errors.New("not an ECDSA P-256 public key")
	// ErrBadEncoding means a signature file is not base64.
	ErrBadEncoding = errors.New("signature is not base64")
)

// Key is one public key and the id it is published under.
type Key struct {
	ID  string
	Pub *ecdsa.PublicKey
}

// ParsePublicKey reads a cosign PEM public key.
func ParsePublicKey(id string, pemBytes []byte) (Key, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return Key{}, fmt.Errorf("%w: %s has no PEM block", ErrBadKey, id)
	}
	parsed, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %s: %v", ErrBadKey, id, err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return Key{}, fmt.Errorf("%w: %s", ErrBadKey, id)
	}
	return Key{ID: id, Pub: pub}, nil
}

// Verify checks a detached cosign signature over payload.
func Verify(k Key, payload, signature []byte) error {
	sum := sha256.Sum256(payload)
	return VerifyDigest(k, sum, signature)
}

// VerifyDigest checks a detached cosign signature against the SHA-256 of a
// payload the caller has already hashed, so a large blob is verified while it
// streams rather than after it is buffered.
func VerifyDigest(k Key, sum [sha256.Size]byte, signature []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadEncoding, err)
	}
	if k.Pub == nil || !ecdsa.VerifyASN1(k.Pub, sum[:], raw) {
		return ErrBadSignature
	}
	return nil
}

// VerifyAny tries keys in order and returns the id of the one that verified.
// Order is the caller's: the index's signing_keys list is newest first, so the
// newest key that a holder recognises wins and an overlap window costs one
// extra failed verification at most. A key with no signature is skipped, not
// failed, which is what lets an object signed before a rotation opened keep
// verifying under the older key.
func VerifyAny(keys []Key, payload []byte, sigFor func(keyID string) ([]byte, bool)) (string, error) {
	sum := sha256.Sum256(payload)
	return VerifyAnyDigest(keys, sum, sigFor)
}

// VerifyAnyDigest is VerifyAny over a digest the caller already holds.
func VerifyAnyDigest(keys []Key, sum [sha256.Size]byte, sigFor func(keyID string) ([]byte, bool)) (string, error) {
	for _, k := range keys {
		sig, ok := sigFor(k.ID)
		if !ok {
			continue
		}
		if err := VerifyDigest(k, sum, sig); err == nil {
			return k.ID, nil
		}
	}
	return "", ErrBadSignature
}
