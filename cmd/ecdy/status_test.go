package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// slow starts in 600 ms and is silent for 600 ms more before it answers.
var slow = fakeagent.Script{StartDelayMS: 600, Turn: []fakeagent.Step{{SleepMS: 600}, {Text: "hello\n"}}}

// While nothing is shown, a status line says what ecdy waits for; the
// answer replaces it.
func TestAskWaiting(t *testing.T) { forEachMode(t, testAskWaiting) }

func testAskWaiting(t *testing.T, m mode) {
	e := newAskEnv(t, m, slow)
	e.env = append(e.env, "ECDY_STATUS_DELAY=0")
	start := time.Now()
	term := e.start(t, "hi")
	term.Expect("\x1b[2mstarting fake… 0s · Ctrl+C to cancel\x1b[0m")
	term.Expect("\r\x1b[2K\x1b[2mwaiting for fake… ")
	if got := term.Expect("hello"); !strings.HasSuffix(got, "s · Ctrl+C to cancel\x1b[0m\r\x1b[2Khello") {
		t.Errorf("before the answer: %q, want the status line erased", got)
	}
	expectExit(t, term, 0)
	// One draw at the start, one per phase, one per second.
	if out, most := term.Output(), 3+int(time.Since(start).Seconds()); strings.Count(out, "Ctrl+C to cancel") > most {
		t.Errorf("the status line was drawn more often than every second and at each phase:\n%s", out)
	}
}

func TestStatusDelay(t *testing.T) {
	for env, want := range map[string]time.Duration{
		"":      defaultStatusDelay,
		"0":     0,
		"250ms": 250 * time.Millisecond,
		"-1s":   defaultStatusDelay,
		"soon":  defaultStatusDelay,
	} {
		t.Setenv("ECDY_STATUS_DELAY", env)
		if got := statusDelay(); got != want {
			t.Errorf("ECDY_STATUS_DELAY=%q: %v, want %v", env, got, want)
		}
	}
}

// The status line waits for the delay since the start of `ecdy ask`.
func TestAskWaitingDelay(t *testing.T) {
	e := newAskEnv(t, oneShot, slow)
	e.env = append(e.env, "ECDY_STATUS_DELAY=400ms")
	start := time.Now()
	term := e.start(t, "hi")
	term.Expect("Ctrl+C to cancel")
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Errorf("status line after %v, want 400ms", d)
	}
	term.Expect("hello")
	expectExit(t, term, 0)

	// A quick answer: no status line with the default delay.
	e = newAskEnv(t, oneShot, fakeagent.Script{Turn: []fakeagent.Step{{Text: "hello\n"}}})
	term = e.start(t, "hi")
	expectExit(t, term, 0)
	if out := term.Output(); strings.Contains(out, "Ctrl+C") || !strings.Contains(out, "hello") {
		t.Errorf("output %q", out)
	}
}

// An agent that never answers initialize: the status line counts the
// seconds until Ctrl+C, which erases it.
func TestAskWaitingHung(t *testing.T) { forEachMode(t, testAskWaitingHung) }

func testAskWaitingHung(t *testing.T, m mode) {
	e := newAskEnv(t, m, fakeagent.Script{})
	cfg := filepath.Join(e.dir, "config", "ecdy", "config.toml")
	if err := os.WriteFile(cfg, []byte("default_agent = \"mute\"\n[agents.mute]\ncommand = [\"sleep\", \"60\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.env = append(e.env, "ECDY_STATUS_DELAY=0")
	term := e.start(t, "hello")
	term.Expect("starting mute… 0s")
	term.Expect("starting mute… 1s")
	term.Send(ctrlC)
	term.Expect("\r\x1b[2K\x1b[2m^C cancelling")
	expectExit(t, term, 130)
}

// Into a pipe there is no status line: the output is the reply only.
func TestAskWaitingPipe(t *testing.T) { forEachMode(t, testAskWaitingPipe) }

func testAskWaitingPipe(t *testing.T, m mode) {
	e := newAskEnv(t, m, slow)
	e.env = append(e.env, "ECDY_STATUS_DELAY=0")
	cmd := e.command("hi")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ecdy ask: %v\n%s", err, stderr.String())
	}
	if stdout.String() != "hello\n" || stderr.String() != "" {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

// A turn that ends without any content says so.
func TestAskEmptyReply(t *testing.T) { forEachMode(t, testAskEmptyReply) }

func testAskEmptyReply(t *testing.T, m mode) {
	e := newAskEnv(t, m, fakeagent.Script{})
	term := e.start(t, "hi")
	term.Expect("fake returned an empty reply")
	expectExit(t, term, 0)

	e = newAskEnv(t, m, fakeagent.Script{Turn: []fakeagent.Step{{Tool: &fakeagent.Tool{ID: "t1", Title: "Read main.go"}}, {ToolDone: &fakeagent.Tool{ID: "t1", Status: "completed"}}}})
	term = e.start(t, "hi")
	expectExit(t, term, 0)
	if out := term.Output(); strings.Contains(out, "empty reply") {
		t.Errorf("a reply with a tool call only: %q", out)
	}
}

// An expired login fails the prompt with auth_required.
func TestAskAuthRequiredAtPrompt(t *testing.T) { forEachMode(t, testAskAuthRequiredAtPrompt) }

func testAskAuthRequiredAtPrompt(t *testing.T, m mode) {
	e := newAskEnv(t, m, fakeagent.Script{Turn: []fakeagent.Step{{AuthRequired: true}}})
	term := e.start(t, "hi")
	term.Expect("ecdy: fake: agent requires authentication")
	term.Expect("log in with the agent's own CLI")
	expectExit(t, term, 1)
}

// An error answer comes with the end of the agent's stderr, redacted.
func TestAskRequestError(t *testing.T) { forEachMode(t, testAskRequestError) }

func testAskRequestError(t *testing.T, m mode) {
	token := "sk-ant-api03-" + strings.Repeat("q", 30)
	e := newAskEnv(t, m, fakeagent.Script{Turn: []fakeagent.Step{
		{Stderr: "retry 4/10: 529 overloaded\nANTHROPIC_API_KEY=" + token + "\n"},
		{Error: "API Error: 529"},
	}})
	term := e.start(t, "hi")
	term.Expect("ecdy: fake: session/prompt: Internal error: API Error: 529")
	term.Expect("retry 4/10: 529 overloaded")
	term.Expect("ANTHROPIC_API_KEY=[REDACTED]")
	expectExit(t, term, 1)
	if strings.Contains(term.Output(), token) {
		t.Errorf("the token reached the terminal: %q", term.Output())
	}
}

// With -v the agent's stderr is shown from its start.
func TestAskStartupStderr(t *testing.T) { forEachMode(t, testAskStartupStderr) }

func testAskStartupStderr(t *testing.T, m mode) {
	e := newAskEnv(t, m, fakeagent.Script{StartStderr: "npm warn exec\n", StartDelayMS: 200, Turn: []fakeagent.Step{{Text: "hello\n"}}})
	term := e.start(t, "-v", "--", "hi")
	term.Expect("npm warn exec")
	term.Expect("hello")
	expectExit(t, term, 0)
}

// The agent's stderr (-v) erases the status line, which does not come back.
func TestAskWaitingStderr(t *testing.T) { forEachMode(t, testAskWaitingStderr) }

func testAskWaitingStderr(t *testing.T, m mode) {
	e := newAskEnv(t, m, fakeagent.Script{Turn: []fakeagent.Step{{SleepMS: 300}, {Stderr: "retrying\n"}, {SleepMS: 1200}, {Text: "hello\n"}}})
	e.env = append(e.env, "ECDY_STATUS_DELAY=0")
	term := e.start(t, "-v", "--", "hi")
	if got := term.Expect("retrying"); !strings.HasSuffix(got, "Ctrl+C to cancel\x1b[0m\r\x1b[2Kretrying") {
		t.Errorf("before the stderr: %q, want the status line erased", got)
	}
	if got := term.Expect("hello"); strings.Contains(got, "Ctrl+C") {
		t.Errorf("the status line came back after the stderr: %q", got)
	}
	expectExit(t, term, 0)
}
