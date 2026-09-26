package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCmd(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("version: %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "ecdy ") || !strings.HasSuffix(got, "\n") {
		t.Fatalf("unexpected output %q", got)
	}
}

func TestVersionCmdRejectsArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"version", "extra"})

	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("expected error for extra argument")
	}
}

func TestResolveVersionPrefersLdflags(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })

	version = "1.2.3"
	if got := resolveVersion(); got != "1.2.3" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "1.2.3")
	}
}
