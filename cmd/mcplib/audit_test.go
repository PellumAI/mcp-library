package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestCmdAudit_Exclusive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := cmdAudit([]string{"--server", "foo", "--resolve", "npm:foo@1.0.0"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error when --server and --resolve are both given")
	}
	if !errors.Is(err, errUsage) {
		t.Errorf("got error %v, want it to wrap errUsage (exit 2)", err)
	}
}

func TestCmdAudit_RequiresOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := cmdAudit(nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error when neither --server nor --resolve is given")
	}
	if !errors.Is(err, errUsage) {
		t.Errorf("got error %v, want it to wrap errUsage (exit 2)", err)
	}
}

func TestCmdAudit_UnknownResolveKind(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := cmdAudit([]string{"--resolve", "bogus:thing@1.0.0"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error for an unknown --resolve kind")
	}
	if errors.Is(err, errUsage) {
		t.Error("an unknown --resolve kind is a detected failure (exit 1), not a usage error (exit 2)")
	}
}
