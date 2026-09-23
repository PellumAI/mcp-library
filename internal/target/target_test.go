package target_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/target"
)

const window = `{"schema_version":1,"runtimes":["node@22","native"]}`

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func doc(extra string) string {
	return `schema_version: 1
mcpgw_release: ""
mcpgw_commit: "27fbc1d0621ac5a3f6dce7682f59450c5c578193"
schema_asset: mcpgw-package.schema.json
schema_sha256: "` + digest("schema") + `"
window_asset: runtime-window.json
window_sha256: "` + digest(window) + `"
runtimes: [node@22, native]
` + extra
}

func parse(t *testing.T, s string) target.Target {
	t.Helper()
	tg, err := target.Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return tg
}

func TestAllowsRuntime_InsideTheWindow(t *testing.T) {
	tg := parse(t, doc(""))
	if !tg.AllowsRuntime("node@22") || !tg.AllowsRuntime("native") {
		t.Fatal("a line inside the window is refused")
	}
}

// TestRuntimeError_NamesBothSides asserts the refusal carries the line asked
// for and the whole window, the sentence the gateway's runtime_unavailable
// refusal also prints.
func TestRuntimeError_NamesBothSides(t *testing.T) {
	tg := parse(t, doc(""))
	if tg.AllowsRuntime("node@18") {
		t.Fatal("node@18 is allowed")
	}
	err := tg.RuntimeError("node@18")
	if !errors.Is(err, target.ErrRuntime) {
		t.Fatalf("got %v, want ErrRuntime", err)
	}
	for _, s := range []string{"node@18", "node@22, native", "27fbc1d0621a"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q does not name %q", err, s)
		}
	}
}

func TestCheckSchemaDigest_RefusesAMismatch(t *testing.T) {
	tg := parse(t, doc(""))
	if err := tg.CheckSchemaDigest([]byte("schema")); err != nil {
		t.Fatalf("matching schema refused: %v", err)
	}
	if err := tg.CheckSchemaDigest([]byte("schema, edited")); !errors.Is(err, target.ErrDigest) {
		t.Fatalf("got %v, want ErrDigest", err)
	}
}

func TestCheckWindow_RefusesARestatementThatDrifted(t *testing.T) {
	tg := parse(t, doc(""))
	if err := tg.CheckWindow([]byte(window)); err != nil {
		t.Fatalf("matching window refused: %v", err)
	}
	drifted := parse(t, strings.Replace(doc(""), "runtimes: [node@22, native]", "runtimes: [node@22]", 1))
	if err := drifted.CheckWindow([]byte(window)); err == nil {
		t.Fatal("a restated window that differs from the file is accepted")
	}
}

func TestParse_RefusesAHigherSchemaVersion(t *testing.T) {
	_, err := target.Parse([]byte(strings.Replace(doc(""), "schema_version: 1", "schema_version: 2", 1)))
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("got %v, want a refusal of schema_version 2", err)
	}
}

func TestParse_RefusesAnUnpinnedBuild(t *testing.T) {
	_, err := target.Parse([]byte(strings.Replace(doc(""), `"27fbc1d0621ac5a3f6dce7682f59450c5c578193"`, `"main"`, 1)))
	if err == nil {
		t.Fatal("a target naming a branch rather than a commit is accepted")
	}
}

func TestParse_RefusesABuildImageByTag(t *testing.T) {
	if _, err := target.Parse([]byte(doc("build_image: ghcr.io/pellumai/mcp-library/build:20260923\n"))); err == nil {
		t.Fatal("a build image pinned by tag is accepted")
	}
	pinned := "build_image: ghcr.io/pellumai/mcp-library/build@sha256:" + strings.Repeat("a", 64) + "\n"
	if _, err := target.Parse([]byte(doc(pinned))); err != nil {
		t.Fatalf("a digest-pinned build image is refused: %v", err)
	}
}

func TestParse_RefusesAnUnknownField(t *testing.T) {
	if _, err := target.Parse([]byte(doc("latest: true\n"))); err == nil {
		t.Fatal("an unknown field is accepted")
	}
}
