package sessionlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBuildContext(t *testing.T) {
	failed := rec("make test", 2)
	failed.Exit, failed.Dur = 2, 1300*time.Millisecond
	elsewhere := rec("git pull", 3)
	elsewhere.Cwd = "/srv"
	multi := rec("for f in *; do\n  echo $f\ndone", 4)

	text, last := BuildContext(ContextOptions{
		Cwd:      "/home/me/proj",
		Branch:   "main",
		Records:  []Record{rec("ls", 1), failed, elsewhere, multi},
		Commands: 20,
	})
	want := "Context from the user's shell, added by ecdy (secrets redacted, command output not captured):\n" +
		"cwd: /home/me/proj\n" +
		"git branch: main\n" +
		"Recent commands, oldest first:\n" +
		"[exit 0, 0.1s] ls\n" +
		"[exit 2, 1.3s] make test\n" +
		"[exit 0, 0.1s, in /srv] git pull\n" +
		"[exit 0, 0.1s] for f in *; do\n" +
		"      echo $f\n" +
		"    done\n"
	if text != want {
		t.Errorf("BuildContext:\n%s\nwant:\n%s", text, want)
	}
	if !last.Equal(multi.End()) {
		t.Errorf("last = %v, want %v", last, multi.End())
	}
}

func TestBuildContextSince(t *testing.T) {
	recs := []Record{rec("one", 1), rec("two", 2), rec("three", 3)}
	text, last := BuildContext(ContextOptions{Cwd: "/home/me/proj", Records: recs, Since: recs[1].End(), Commands: 20})
	if !strings.Contains(text, "Commands since the previous prompt, oldest first:\n[exit 0, 0.1s] three\n") ||
		strings.Contains(text, "two") {
		t.Errorf("since the second command:\n%s", text)
	}
	if !last.Equal(recs[2].End()) {
		t.Errorf("last = %v", last)
	}

	text, last = BuildContext(ContextOptions{Cwd: "/home/me/proj", Records: recs, Since: recs[2].End(), Commands: 20})
	if strings.Contains(text, "ommands") || !last.Equal(recs[2].End()) {
		t.Errorf("nothing new: %q, last %v", text, last)
	}
	if strings.Contains(text, "git branch") {
		t.Errorf("no branch, but the block has one:\n%s", text)
	}
}

func TestBuildContextCommandsLimit(t *testing.T) {
	var recs []Record
	for i := range 30 {
		recs = append(recs, rec(fmt.Sprintf("echo %d", i), i))
	}
	text, last := BuildContext(ContextOptions{Cwd: "/x", Records: recs, Commands: 20})
	if strings.Contains(text, "echo 9\n") || !strings.Contains(text, "] echo 10\n") || !strings.Contains(text, "] echo 29\n") {
		t.Errorf("want echo 10..29:\n%s", text)
	}
	if !last.Equal(recs[29].End()) {
		t.Errorf("last = %v", last)
	}

	text, last = BuildContext(ContextOptions{Cwd: "/x", Records: recs, Commands: 0})
	if strings.Contains(text, "echo") || !strings.Contains(text, "cwd: /x\n") {
		t.Errorf("commands = 0:\n%s", text)
	}
	if !last.Equal(recs[29].End()) {
		t.Errorf("commands = 0: last = %v; the commands are consumed all the same", last)
	}
}

func TestBuildContextBounded(t *testing.T) {
	var recs []Record
	for i := range 20 {
		recs = append(recs, rec(fmt.Sprintf("echo %d %s", i, strings.Repeat("ж", 400)), i))
	}
	text, _ := BuildContext(ContextOptions{Cwd: "/home/me/proj", Records: recs, Commands: 20})
	if len(text) > MaxContextBytes {
		t.Errorf("block is %d bytes, max %d", len(text), MaxContextBytes)
	}
	if !utf8.ValidString(text) {
		t.Error("invalid UTF-8")
	}
	if !strings.Contains(text, "] echo 19 ") {
		t.Errorf("the newest command was dropped:\n%s", text)
	}
	if strings.Contains(text, "] echo 0 ") {
		t.Errorf("the oldest command was kept:\n%s", text)
	}
	for line := range strings.SplitSeq(text, "\n") {
		if len(line) > maxCommandBytes+len("[exit 0, 0.1s] ")+len("…") {
			t.Errorf("a command line of %d bytes", len(line))
		}
	}
	if !strings.Contains(text, "…\n") {
		t.Errorf("long commands are not marked as cut:\n%s", text)
	}

	huge, _ := BuildContext(ContextOptions{Cwd: "/" + strings.Repeat("д", 5000), Records: recs, Commands: 20})
	if len(huge) > MaxContextBytes || !utf8.ValidString(huge) {
		t.Errorf("huge cwd: %d bytes, valid UTF-8 %v", len(huge), utf8.ValidString(huge))
	}
}

// TestBuildContextRedacts: the records were redacted when written, but a
// log from elsewhere (an older ecdy) may not be; so are the cwd and branch.
// A secret is redacted before it is cut, or its tail would leak.
func TestBuildContextRedacts(t *testing.T) {
	token := "ghp_0123456789abcdefghijABCDEFGHIJ012345"
	long := rec(strings.Repeat("a", maxCommandBytes-20)+" "+token, 2)
	text, _ := BuildContext(ContextOptions{
		Cwd:      "/tmp/" + token,
		Branch:   "fix/" + token,
		Records:  []Record{rec("export TOKEN=abc123", 1), long},
		Commands: 20,
	})
	for _, secret := range []string{"abc123", "ghp_", "0123456789abcdef"} {
		if strings.Contains(text, secret) {
			t.Errorf("%q in the context:\n%s", secret, text)
		}
	}
}

func TestGitBranch(t *testing.T) {
	root := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	write(filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/m5-context\n")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	detached := filepath.Join(root, "detached")
	write(filepath.Join(detached, ".git", "HEAD"), "e3c0720aa1b2c3d4e5f60718293a4b5c6d7e8f90\n")
	wt := filepath.Join(root, "wt")
	write(filepath.Join(repo, ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/feature\n")
	write(filepath.Join(wt, ".git"), "gitdir: ../repo/.git/worktrees/wt\n")
	absWt := filepath.Join(root, "abswt")
	write(filepath.Join(absWt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")
	broken := filepath.Join(root, "broken")
	write(filepath.Join(broken, ".git"), "garbage\n")

	for dir, want := range map[string]string{
		repo:     "m5-context",
		sub:      "m5-context",
		detached: "detached at e3c0720",
		wt:       "feature",
		absWt:    "feature",
		broken:   "",
		root:     "",
	} {
		if got := GitBranch(dir); got != want {
			t.Errorf("GitBranch(%s) = %q, want %q", dir, got, want)
		}
	}
}
