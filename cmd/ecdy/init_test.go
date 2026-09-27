package main

import (
	"bytes"
	"testing"

	"github.com/Raimguzhinov/ecdy/shell"
)

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func TestInitZsh(t *testing.T) {
	got, err := runRoot(t, "init", "zsh")
	if err != nil {
		t.Fatal(err)
	}
	if got != shell.Zsh || got == "" {
		t.Fatalf("init zsh printed %d bytes, want the embedded plugin (%d bytes)", len(got), len(shell.Zsh))
	}
	for _, args := range [][]string{{"init"}, {"init", "bash"}, {"init", "zsh", "extra"}} {
		if _, err := runRoot(t, args...); err == nil {
			t.Errorf("%q: expected error", args)
		}
	}
}
