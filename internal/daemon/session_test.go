package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSessionPaths(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	p, err := SessionPaths("123-456.7")
	if err != nil {
		t.Fatal(err)
	}
	if p.Socket != filepath.Join(rt, "ecdy", "123-456.7.sock") || p.State != filepath.Join(rt, "ecdy", "123-456.7.state") {
		t.Errorf("paths = %+v", p)
	}
	info, err := os.Stat(p.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("runtime directory: %v, %v", info.Mode(), err)
	}
	for _, bad := range []string{"", "../x", ".hidden", "a/b", "a b", strings.Repeat("x", 65)} {
		if _, err := SessionPaths(bad); err == nil {
			t.Errorf("SessionPaths(%q): no error", bad)
		}
	}
}

func TestRuntimeDirFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", tmp)
	dir, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(tmp, "ecdy-"+strconv.Itoa(os.Getuid())); dir != want {
		t.Errorf("RuntimeDir() = %q, want %q", dir, want)
	}
}

func TestRuntimeDirUnsafe(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	dir := filepath.Join(rt, "ecdy")

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := RuntimeDir(); err == nil || !strings.Contains(err.Error(), "accessible by others") {
		t.Errorf("group-readable: %v", err)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := RuntimeDir(); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("symlink: %v", err)
	}
}

func TestState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.state")
	if s, err := LoadState(path); err != nil || s != (State{}) {
		t.Fatalf("missing file: %+v, %v", s, err)
	}
	if _, err := UpdateState(path, func(s *State) { s.Agent = "codex" }); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateState(path, func(s *State) { s.Generation++ }); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadState(path); err != nil || s != (State{Agent: "codex", Generation: 1}) {
		t.Fatalf("state = %+v, %v", s, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Error("broken state: no error")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}
}
