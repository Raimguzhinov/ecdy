package sessionlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func rec(line string, sec int) Record {
	return Record{Line: line, Cwd: "/home/me/proj", Exit: 0, Start: t0.Add(time.Duration(sec) * time.Second), Dur: 100 * time.Millisecond}
}

func TestOpenRejectsBadSession(t *testing.T) {
	for _, s := range []string{"", ".", "..", "a/b", ".hidden", "a\x00b"} {
		if _, err := Open(t.TempDir(), s); err == nil {
			t.Errorf("Open(%q) succeeded", s)
		}
	}
}

func TestAppendRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	l, err := Open(dir, "1-2")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := l.Read(); err != nil || len(got) != 0 {
		t.Fatalf("Read of a missing log = %v, %v; want nothing", got, err)
	}
	r := rec("export GITHUB_TOKEN=abc123", 1)
	r.Exit = 3
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(rec("ls", 2)); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "abc123") {
		t.Errorf("the secret reached the disk:\n%s", data)
	}
	got, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Line != "export GITHUB_TOKEN=[REDACTED]" || got[0].Exit != 3 ||
		!got[0].Start.Equal(r.Start) || got[0].Dur != r.Dur || got[0].Cwd != r.Cwd || got[1].Line != "ls" {
		t.Errorf("Read = %+v", got)
	}

	for path, want := range map[string]os.FileMode{dir: 0o700, l.Path(): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

func TestAppendRedactsCwd(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	r := rec("ls", 1)
	r.Cwd = "/tmp/ghp_0123456789abcdefghijABCDEFGHIJ012345"
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	got, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Cwd != "/tmp/[REDACTED]" {
		t.Errorf("cwd = %q", got[0].Cwd)
	}
}

func TestReadSortsByEndAndSkipsBadLines(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	long := rec("sleep 10", 0)
	long.Dur = 10 * time.Second // ends at 10 s
	for _, r := range []Record{rec("b", 5), long, rec("a", 1)} {
		if err := l.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(l.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("not json\n{\"line\":\"half")
	_ = f.Close()

	got, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, r := range got {
		lines = append(lines, r.Line)
	}
	if strings.Join(lines, ",") != "a,b,sleep 10" {
		t.Errorf("lines = %v, want a,b,sleep 10", lines)
	}
}

func TestAppendTrims(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	l.maxSize, l.keep = 1000, 5
	for i := range 40 {
		if err := l.Append(rec(fmt.Sprintf("echo %d", i), i)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1000 {
		t.Errorf("log is %d bytes, want at most 1000", info.Size())
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("trimmed log has mode %v", info.Mode().Perm())
	}
	got, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 5 || got[len(got)-1].Line != "echo 39" {
		t.Errorf("after trimming: %d records, last %+v", len(got), got[len(got)-1])
	}
}

// TestAppendConcurrent is the background recorders of fast commands: no
// record may be lost or torn, also while the log is being trimmed.
func TestAppendConcurrent(t *testing.T) {
	l, err := Open(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	l.maxSize, l.keep = 4000, 1000
	const n = 60
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			// A separate Log per writer, like separate processes.
			w, err := Open(filepath.Dir(l.Path()), "s")
			if err == nil {
				err = w.Append(rec(fmt.Sprintf("cmd %02d %s", i, strings.Repeat("x", 30)), i))
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.Read()
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != len(got) {
		t.Errorf("%d lines, %d records: torn writes", lines, len(got))
	}
	if len(got) == 0 {
		t.Fatal("no records")
	}
}

func TestRemoveAndPrune(t *testing.T) {
	dir := t.TempDir()
	old, err := Open(dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Append(rec("ls", 1)); err != nil {
		t.Fatal(err)
	}
	week := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old.Path(), week, week); err != nil {
		t.Fatal(err)
	}
	recent, err := Open(dir, "recent")
	if err != nil {
		t.Fatal(err)
	}
	if err := recent.Append(rec("ls", 1)); err != nil {
		t.Fatal(err)
	}

	// The first record of a new session prunes logs idle for a week.
	cur, err := Open(dir, "cur")
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.Append(rec("ls", 1)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		l    *Log
		want bool
	}{{old, false}, {recent, true}, {cur, true}} {
		if _, err := os.Stat(c.l.Path()); (err == nil) != c.want {
			t.Errorf("%s exists: %v, want %v", c.l.Path(), err == nil, c.want)
		}
	}
	if _, err := os.Stat(old.lockPath); err == nil {
		t.Errorf("the pruned log's lock file is left")
	}

	if err := cur.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := cur.Remove(); err != nil {
		t.Errorf("second Remove: %v", err)
	}
	for _, p := range []string{cur.Path(), cur.lockPath} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is left after Remove", p)
		}
	}
}

func TestDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if d, err := Dir(); err != nil || d != "/state/ecdy/sessions" {
		t.Errorf("Dir() = %q, %v", d, err)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", "/home/me")
	if d, err := Dir(); err != nil || d != "/home/me/.local/state/ecdy/sessions" {
		t.Errorf("Dir() with a relative XDG_STATE_HOME = %q, %v", d, err)
	}
}

// TestAppendLockTimeout: a writer that holds the lock forever (a hung
// recorder) makes the others give up in time, not wait with it.
func TestAppendLockTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, 200 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			l, err := Open(t.TempDir(), "s")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(l.Path()), 0o700); err != nil {
				t.Fatal(err)
			}
			unlock, err := l.lock()
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			w, err := Open(filepath.Dir(l.Path()), "s")
			if err != nil {
				t.Fatal(err)
			}
			w.lockTimeout = timeout
			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- w.Append(rec("ls", 1)) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("Append succeeded while another writer held the lock")
				}
				if d := time.Since(start); d < timeout || d > timeout+time.Second {
					t.Errorf("gave up after %v, timeout %v", d, timeout)
				}
			case <-time.After(timeout + 5*time.Second):
				t.Fatal("Append is still waiting for the lock")
			}
		})
	}
}
