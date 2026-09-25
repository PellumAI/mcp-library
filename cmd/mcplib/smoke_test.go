package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/pellumai/mcp-library/internal/smoke"
)

func TestCmdSmoke_RequiresServerAndTar(t *testing.T) {
	for _, args := range [][]string{nil, {"--server", "context7"}, {"--tar", "x.tar.gz"}} {
		var stdout, stderr bytes.Buffer
		if err := cmdSmoke(args, &stdout, &stderr); !errors.Is(err, errUsage) {
			t.Errorf("%q: err = %v, want a usage error (exit 2)", args, err)
		}
	}
}

func TestSummarize(t *testing.T) {
	var w bytes.Buffer
	summarize(&w, "fixture-dialer", smoke.Report{
		Verdict:  smoke.VerdictFail,
		Failures: []string{"egress: denied example.com:443, which the manifest does not declare"},
		Mode:     smoke.ModeFull,
		Egress:   []smoke.Attempt{{Host: "example.com", Port: 443}},
		Stderr:   []byte("dialing example.com\n"),
	})
	out := w.String()
	for _, want := range []string{"fixture-dialer: fail", "egress example.com:443 DENIED", "FAIL egress: denied", "dialing example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

// TestCmdEgressProxy_RefusesMalformedAllow asserts a bad --allow entry is a
// usage error before the proxy listens, not a proxy that quietly admits
// less than smoke asked for.
func TestCmdEgressProxy_RefusesMalformedAllow(t *testing.T) {
	for _, allow := range []string{"a.example:https", "a.example:0", ":443"} {
		var stdout, stderr bytes.Buffer
		err := cmdEgressProxy([]string{"--listen", "127.0.0.1:0", "--allow", allow}, &stdout, &stderr)
		if !errors.Is(err, errUsage) {
			t.Errorf("--allow %q: err = %v, want a usage error (exit 2)", allow, err)
		}
	}
}
