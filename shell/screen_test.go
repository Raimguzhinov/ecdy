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
	s.typeText(text)
	s.run("send-keys", "Enter")
}

// typeText types text without pressing Enter.
func (s *screen) typeText(text string) {
	s.t.Helper()
	s.run("send-keys", "-l", text)
}

// clearLine empties the line being edited (Ctrl+U).
func (s *screen) clearLine() {
	s.t.Helper()
	s.run("send-keys", "C-u")
}

// row returns the last screen row that starts with the test prompt, with
// the spaces between the line and the right prompt squeezed to one.
func (s *screen) row(scr string) string {
	rows := regexp.MustCompile(`(?m)^`+promptMark+`.*$`).FindAllString(scr, -1)
	if len(rows) == 0 {
		return ""
	}
	return regexp.MustCompile(` {2,}`).ReplaceAllString(rows[len(rows)-1], " ")
}

// waitRow waits until the prompt row, squeezed, ends with want.
func (s *screen) waitRow(want string) {
	s.t.Helper()
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for {
		scr := s.capture()
		if strings.HasSuffix(s.row(scr), want) {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("the prompt row does not end with %q:\n%s", want, scr)
		}
		time.Sleep(20 * time.Millisecond)
	}
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

// TestIndicator: while typing, the right prompt shows what Enter will do,
// computed by the classifier in the background (docs/adr/0006-ux.md).
func TestIndicator(t *testing.T) { forEachZsh(t, testIndicator) }

func testIndicator(t *testing.T, zsh string) {
	t.Run("verdicts", func(t *testing.T) {
		s := startScreen(t, zsh, zshOpts{rc: "RPS1=R"}, 80)
		s.waitRow("% R")
		s.typeText("explain this error")
		s.waitRow("% explain this error → agent R")
		s.clearLine()
		s.typeText("ls -la")
		s.waitRow("% ls -la R") // a command: nothing by default
		s.clearLine()
		s.typeText("rm everything in tmp except configs")
		s.waitRow("% rm everything in tmp except configs ? ask R")
		s.clearLine()
		s.waitRow("% R")
		// Accepted, the line keeps only the user's right prompt.
		s.send("explain this error")
		scr := s.wait(askReply + "explain this error")
		if !strings.Contains(scr, "% explain this error\n") || strings.Contains(scr, "→ agent") {
			t.Errorf("the indicator stayed on the accepted line:\n%s", scr)
		}
		s.waitRow("% R")
	})
	t.Run("custom", func(t *testing.T) {
		s := startScreen(t, zsh, zshOpts{rc: "RPS1=R", after: "ECDY_INDICATOR_CMD='[cmd]' ECDY_INDICATOR_PROMPT='[ai]'"}, 80)
		s.typeText("explain")
		s.waitRow("% explain [ai] R")
		s.clearLine()
		s.typeText("print -r -- ok-$((6*7))")
		s.waitRow("% print -r -- ok-$((6*7)) [cmd] R")
		// Accepted, the line keeps only the user's right prompt.
		s.run("send-keys", "Enter")
		scr := s.wait("ok-42")
		if !regexp.MustCompile(`% print -r -- ok-\$\(\(6\*7\)\) +R\nok-42`).MatchString(scr) || strings.Contains(scr, "[cmd]") {
			t.Errorf("the indicator stayed on the accepted line:\n%s", scr)
		}
	})
	t.Run("var", func(t *testing.T) {
		s := startScreen(t, zsh, zshOpts{rc: "setopt PROMPT_SUBST; RPS1='<$ECDY_VERDICT>'", after: "ECDY_INDICATOR=var"}, 80)
		s.waitRow("% <>")
		s.typeText("explain this error")
		s.waitRow("% explain this error <prompt>")
	})
	t.Run("off", func(t *testing.T) {
		// No classifier runs while typing: only Enter's.
		dir := t.TempDir()
		calls := filepath.Join(dir, "calls")
		counting := "#!/bin/sh\n[ \"$1\" = classify ] && echo x >>" + calls + "\nexec " + ecdyBin + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, "ecdy"), []byte(counting), 0o755); err != nil { //nolint:gosec // a test executable
			t.Fatal(err)
		}
		s := startScreen(t, zsh, zshOpts{rc: "RPS1=R", after: "ECDY_INDICATOR=off ECDY_BIN=" + filepath.Join(dir, "ecdy")}, 80)
		s.typeText("print -r -- off-$((6*7))")
		s.waitRow("% print -r -- off-$((6*7)) R")
		time.Sleep(300 * time.Millisecond) // an indicator would have started by now
		s.run("send-keys", "Enter")
		s.wait("off-42")
		if data, _ := os.ReadFile(calls); string(data) != "x\n" {
			t.Errorf("classifier runs: %q, want only Enter's", data)
		}
	})
}

// TestIndicatorFailOpen: without a working classifier there is no
// indicator, and typing and Enter work as usual.
func TestIndicatorFailOpen(t *testing.T) { forEachZsh(t, testIndicatorFailOpen) }

func testIndicatorFailOpen(t *testing.T, zsh string) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	// Classify nothing: record the call and hang, before or after writing
	// part of an answer.
	scripts := map[string]string{
		"slow":    "#!/bin/sh\n[ \"$1\" = classify ] || exit 0\necho \"$$\" >>" + calls + "\nexec sleep 30\n",
		"partial": "#!/bin/sh\n[ \"$1\" = classify ] || exit 0\necho \"$$\" >>" + calls + "\nprintf 'prompt\\000'\nexec sleep 30\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil { //nolint:gosec // a test executable
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name string
		opts zshOpts
	}{
		{"missing", zshOpts{load: "source " + sourceFile(t), path: "/usr/bin:/bin:" + filepath.Dir(zsh), rc: "RPS1=R"}},
		{"hung", zshOpts{rc: "RPS1=R", after: "ECDY_BIN=" + filepath.Join(dir, "slow") + " ECDY_CLASSIFY_TIMEOUT=0.2"}},
		{"partial", zshOpts{rc: "RPS1=R", after: "ECDY_BIN=" + filepath.Join(dir, "partial") + " ECDY_CLASSIFY_TIMEOUT=0.2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := startScreen(t, zsh, tt.opts, 80)
			s.typeText("explain")
			s.waitRow("% explain R")
			s.typeText(" this")
			s.waitRow("% explain this R")
			s.clearLine()
			s.send("print -r -- typed-$((6*7))")
			s.wait("typed-42")
			s.waitRow("% R")
			// Each hung classifier was killed when the line changed.
			data, _ := os.ReadFile(calls)
			for _, f := range strings.Fields(string(data)) {
				pid, _ := strconv.Atoi(f)
				waitGone(t, pid)
			}
		})
	}
}
