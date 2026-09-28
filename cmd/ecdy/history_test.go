package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/sessionlog"
)

func TestEpoch(t *testing.T) {
	for in, want := range map[string]time.Time{
		"1790624277.7849361897": time.Unix(1790624277, 784936189),
		"1790624277.5":          time.Unix(1790624277, 500000000),
		"1790624277,25":         time.Unix(1790624277, 250000000),
		"1790624277":            time.Unix(1790624277, 0),
		"1790624277.":           time.Unix(1790624277, 0),
		"0.000000001":           time.Unix(0, 1),
	} {
		got, err := epoch(in)
		if err != nil || !got.Equal(want) {
			t.Errorf("epoch(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".5", "-1.5", "+1.5", "1.5x", "1.-5", "abc", "1e9", "99999999999999999999"} {
		if got, err := epoch(in); err == nil {
			t.Errorf("epoch(%q) = %v, want an error", in, got)
		}
	}
}

// runCmd runs ecdy in-process with args and returns its stdout.
func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	return out.String(), err
}

func TestLogCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv(envSession, "")
	if _, err := runCmd(t, "log"); err == nil || !strings.Contains(err.Error(), envSession) {
		t.Fatalf("log without a session: %v", err)
	}
	t.Setenv(envSession, "123-456")
	if out, err := runCmd(t, "log"); err != nil || out != "" {
		t.Fatalf("empty log: %q, %v", out, err)
	}

	for _, args := range [][]string{
		{"log", "record", "--exit=2", "--start=1790624277.5", "--end=1790624279", "--cwd=/src", "--", "make test"},
		{"log", "record", "--start=1790624280", "--end=1790624280.25", "--cwd=/src", "--", "export GITHUB_TOKEN=abc123"},
	} {
		if out, err := runCmd(t, args...); err != nil || out != "" {
			t.Fatalf("%v: %q, %v", args, out, err)
		}
	}
	for _, args := range [][]string{
		{"log", "record", "--start=x", "--end=1", "--", "ls"},
		{"log", "record", "--start=1", "--end=1"},
		{"log", "--json", "--context"},
	} {
		if _, err := runCmd(t, args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}

	out, err := runCmd(t, "log")
	if err != nil || !strings.Contains(out, "exit 2 ") || !strings.Contains(out, "1.5s  /src  make test\n") ||
		!strings.Contains(out, "export GITHUB_TOKEN=[REDACTED]\n") || strings.Contains(out, "abc123") {
		t.Errorf("log:\n%s%v", out, err)
	}

	out, err = runCmd(t, "log", "--json")
	var r sessionlog.Record
	if err != nil || json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &r) != nil ||
		r.Line != "make test" || r.Exit != 2 || r.Dur != 1500*time.Millisecond || r.Cwd != "/src" {
		t.Errorf("log --json:\n%s%v", out, err)
	}

	cwd, _ := os.Getwd()
	out, err = runCmd(t, "log", "--context")
	if err != nil || !strings.Contains(out, "cwd: "+cwd+"\n") ||
		!strings.Contains(out, "[exit 2, 1.5s, in /src] make test\n") ||
		!strings.Contains(out, "export GITHUB_TOKEN=[REDACTED]\n") {
		t.Errorf("log --context:\n%s%v", out, err)
	}

	if err := os.MkdirAll(filepath.Join(home, "config", "ecdy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "ecdy", "config.toml"), []byte("[context]\ncommands = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runCmd(t, "log", "--context")
	if err != nil || strings.Contains(out, "make test") || !strings.Contains(out, "cwd: ") {
		t.Errorf("log --context with commands = 0:\n%s%v", out, err)
	}
}
