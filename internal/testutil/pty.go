// Package testutil holds helpers shared by tests: a terminal driver for
// running interactive programs (zsh with the plugin) under a PTY.
package testutil

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// DefaultTimeout bounds every Expect call.
const DefaultTimeout = 10 * time.Second

// Term runs a process under a pseudo-terminal and records everything it
// prints.
type Term struct {
	tb   testing.TB
	cmd  *exec.Cmd
	ptmx *os.File

	mu   sync.Mutex
	out  bytes.Buffer
	pos  int           // Expect searches output after this offset
	more chan struct{} // closed and replaced whenever output arrives
	done chan struct{} // closed when the reader stops
}

// StartTerm starts cmd under an 80x24 PTY. The process is killed and waited
// for when the test ends.
func StartTerm(tb testing.TB, cmd *exec.Cmd) *Term {
	tb.Helper()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		tb.Fatalf("start %s under a pty: %v", cmd.Path, err)
	}
	t := &Term{tb: tb, cmd: cmd, ptmx: ptmx, more: make(chan struct{}), done: make(chan struct{})}
	go t.read()
	tb.Cleanup(t.close)
	return t
}

func (t *Term) read() {
	defer close(t.done)
	buf := make([]byte, 4096)
	for {
		n, err := t.ptmx.Read(buf)
		t.mu.Lock()
		t.out.Write(buf[:n])
		close(t.more)
		t.more = make(chan struct{})
		t.mu.Unlock()
		if err != nil {
			return // EIO once the child side is closed
		}
	}
}

func (t *Term) close() {
	_ = t.cmd.Process.Kill()
	_ = t.cmd.Wait()
	_ = t.ptmx.Close()
	<-t.done
}

// Send writes keys to the terminal as if typed.
func (t *Term) Send(keys string) {
	t.tb.Helper()
	if _, err := io.WriteString(t.ptmx, keys); err != nil {
		t.tb.Fatalf("write to pty: %v", err)
	}
}

// Expect waits until the output after the previous match contains s, moves
// past it and returns the output it moved past.
func (t *Term) Expect(s string) string {
	t.tb.Helper()
	return t.ExpectRe(regexp.MustCompile(regexp.QuoteMeta(s)))
}

// ExpectRe is Expect with a regular expression.
func (t *Term) ExpectRe(re *regexp.Regexp) string {
	t.tb.Helper()
	deadline := time.After(DefaultTimeout)
	for {
		t.mu.Lock()
		rest := t.out.Bytes()[t.pos:]
		loc := re.FindIndex(rest)
		if loc != nil {
			m := string(rest[:loc[1]])
			t.pos += loc[1]
			t.mu.Unlock()
			return m
		}
		more := t.more
		t.mu.Unlock()
		select {
		case <-more:
		case <-t.done:
			t.mu.Lock()
			found := re.Match(t.out.Bytes()[t.pos:])
			t.mu.Unlock()
			if !found {
				t.tb.Fatalf("process exited before output matched %q\noutput after last match:\n%s", re, t.rest())
			}
		case <-deadline:
			t.tb.Fatalf("timed out after %v waiting for %q\noutput after last match:\n%s", DefaultTimeout, re, t.rest())
		}
	}
}

// Rest returns the output after the previous match, without moving past it.
func (t *Term) rest() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Printable(t.out.String()[t.pos:])
}

// Mark moves the Expect position to the end of the output received so far.
func (t *Term) Mark() {
	t.mu.Lock()
	t.pos = t.out.Len()
	t.mu.Unlock()
}

// Wait waits for the process to exit, up to DefaultTimeout.
func (t *Term) Wait() {
	t.tb.Helper()
	select {
	case <-t.done:
	case <-time.After(DefaultTimeout):
		t.tb.Fatalf("process did not exit\noutput after last match:\n%s", t.rest())
	}
}

var ansi = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07]*\x07|[()][0-9A-Za-z]|[=>78DEHMc])`)

// Printable strips terminal escape sequences and carriage returns, for
// readable failure messages.
func Printable(s string) string {
	return strings.ReplaceAll(ansi.ReplaceAllString(s, ""), "\r", "")
}
