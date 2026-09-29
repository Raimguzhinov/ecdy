package shell_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/testutil"
)

// screen is a test shell in a tmux pane: tmux is a real terminal, and
// `capture-pane` shows what the user sees once the escape sequences are
// applied, which the byte stream of a PTY does not tell.
type screen struct {
	t    *testing.T
	tmux []string // tmux and its server options
}

// startScreen starts the test shell in a detached tmux session cols wide
// and loads the plugin like startZsh does.
func startScreen(t *testing.T, zsh string, o zshOpts, cols int) *screen {
	t.Helper()
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not found")
	}
	cmd, rcPath, home, _ := zshSetup(t, zsh, o)
	// Not t.TempDir(): a unix socket path must be short.
	dir, err := os.MkdirTemp("", "ecdy-tmux")
	if err != nil {
		t.Fatal(err)
	}
	s := &screen{t: t, tmux: []string{tmux, "-S", filepath.Join(dir, "s"), "-f", "/dev/null"}}
	args := []string{"new-session", "-d", "-x", strconv.Itoa(cols), "-y", "30", "-c", home, "--", "env", "-i"}
	args = append(append(args, cmd.Env...), cmd.Args...)
	s.run(args...)
	t.Cleanup(func() {
		_ = exec.Command(s.tmux[0], append(s.tmux[1:], "kill-server")...).Run()
		_ = os.RemoveAll(dir)
	})
	s.send(" source " + rcPath)
	s.wait("loaded-42")
	return s
}

func (s *screen) run(args ...string) string {
	s.t.Helper()
	c := exec.Command(s.tmux[0], append(s.tmux[1:], args...)...)
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	out, err := c.CombinedOutput()
	if err != nil {
		s.t.Fatalf("tmux %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// send types text and presses Enter.
func (s *screen) send(text string) {
	s.t.Helper()
	s.run("send-keys", "-l", text)
	s.run("send-keys", "Enter")
}

// capture returns the pane's rows, trailing spaces removed.
func (s *screen) capture() string {
	s.t.Helper()
	return s.run("capture-pane", "-p")
}

// wait waits until the screen shows want and returns it.
func (s *screen) wait(want string) string {
	s.t.Helper()
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for {
		scr := s.capture()
		if strings.Contains(scr, want) {
			return scr
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("the screen does not show %q:\n%s", want, scr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestScrollback: after a prompt, the scrollback shows the line as typed,
// not the `ecdy ask -- '...'` it was rewritten to (docs/adr/0006-ux.md).
func TestScrollback(t *testing.T) { forEachZsh(t, testScrollback) }

func testScrollback(t *testing.T, zsh string) {
	s := startScreen(t, zsh, zshOpts{}, 60)
	s.send("explain this error")
	scr := s.wait(askReply + "explain this error")
	if !regexp.MustCompile(`(?m)^` + promptMark + `\d+% explain this error\n` + askReply + `explain this error$`).MatchString(scr) {
		t.Errorf("the typed line is not followed by the reply:\n%s", scr)
	}
	// Rewritten, this one takes two rows; typed, one.
	line := "why is the build " + strings.Repeat("so ", 9) + "slow"
	s.send(line)
	scr = s.wait(askReply + line)
	if !regexp.MustCompile(`(?m)^` + promptMark + `\d+% ` + line + `\n(\n)?` + askReply + line + `$`).MatchString(scr) {
		t.Errorf("a wrapped rewritten line left something behind:\n%s", scr)
	}
	// A command's line is left alone.
	s.send("print -r -- cmd-$((6*7))")
	scr = s.wait("cmd-42")
	if !strings.Contains(scr, "% print -r -- cmd-$((6*7))\ncmd-42") {
		t.Errorf("the command line changed:\n%s", scr)
	}
	if strings.Contains(scr, "ecdy ask") {
		t.Errorf("the rewritten line is on the screen:\n%s", scr)
	}
}
