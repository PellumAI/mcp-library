package sign

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Cosign signs with the pinned cosign CLI. mcplib never links a sigstore
// library: signing shells out to the one binary the workflow pins, and
// verification is the twenty lines of crypto/ecdsa in verify.go, the same
// twenty lines a gateway runs.
type Cosign struct {
	// Binary is the cosign CLI. Empty means "cosign" on PATH.
	Binary string
	// KeyRef is what --key receives: env://COSIGN_LIBRARY_KEY in the publish
	// workflow, a file path for the fixture's throwaway key.
	KeyRef string
	// Password is COSIGN_PASSWORD for the key. The publish workflow passes it
	// in the environment and leaves this empty.
	Password *string
}

// SignBlob writes a detached signature over file to out.
//
// --tlog-upload=false keeps key signing offline and gateway-irrelevant: the
// gateway verifies against a key it shipped, never against Rekor. The keyless
// attestation the publish workflow makes separately is what goes to the
// transparency log.
func (c Cosign) SignBlob(ctx context.Context, file, out string) error {
	bin := c.Binary
	if bin == "" {
		bin = "cosign"
	}
	cmd := exec.CommandContext(ctx, bin, "sign-blob", "--yes", "--tlog-upload=false",
		"--key", c.KeyRef, "--output-signature", out, file)
	cmd.Env = os.Environ()
	if c.Password != nil {
		cmd.Env = append(cmd.Env, "COSIGN_PASSWORD="+*c.Password)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cosign sign-blob %s: %w: %s", file, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}

// SignFile signs file under keyID, writing <file>.sig.<keyID> always and the
// unsuffixed <file>.sig when keyID is the newest key, so the two layouts the
// contract publishes are produced by one call.
func (c Cosign) SignFile(ctx context.Context, file, keyID string, newest bool) error {
	sibling := file + SigSuffix + "." + keyID
	if err := c.SignBlob(ctx, file, sibling); err != nil {
		return err
	}
	if !newest {
		return nil
	}
	b, err := os.ReadFile(sibling)
	if err != nil {
		return err
	}
	return os.WriteFile(file+SigSuffix, b, 0o644)
}
