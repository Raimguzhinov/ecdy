package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/testutil"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// The test binary doubles as ecdy (ECDY_TEST_MAIN=1) and as the fake agent
// (first argument fake-agent), so `ecdy ask` runs end to end without a build.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "fake-agent" {
		fakeagent.Main()
	}
	if os.Getenv("ECDY_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Keys.
const (
	ctrlC = "\x03"
	esc   = "\x1b"
)

type askEnv struct {
	dir    string // working directory and $HOME
	record string // requests the fake agent received
	env    []string
}

func newAskEnv(t *testing.T, s fakeagent.Script) *askEnv {
	t.Helper()
	dir := t.TempDir()
	e := &askEnv{dir: dir, record: filepath.Join(dir, "record.jsonl")}
	s.Record = e.record
	scriptEnv, err := fakeagent.WriteScript(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config", "ecdy", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	toml := "default_agent = \"fake\"\n[agents.fake]\ncommand = [" + quote(self) + ", \"fake-agent\"]\n"
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	e.env = []string{
		"ECDY_TEST_MAIN=1",
		scriptEnv,
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"PATH=" + os.Getenv("PATH"),
		"TERM=xterm",
		"LANG=C.UTF-8",
	}
	return e
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func (e *askEnv) command(args ...string) *exec.Cmd {
	self, _ := os.Executable()
	cmd := exec.Command(self, append([]string{"ask"}, args...)...)
	cmd.Dir = e.dir
	cmd.Env = e.env
	return cmd
}

// start runs `ecdy ask args...` under a PTY.
func (e *askEnv) start(t *testing.T, args ...string) *testutil.Term {
	t.Helper()
	return testutil.StartTerm(t, e.command(args...))
}

func (e *askEnv) methods(t *testing.T) []string {
	t.Helper()
	reqs, err := fakeagent.ReadRecord(e.record)
	if err != nil {
		t.Fatal(err)
	}
	var m []string
	for _, r := range reqs {
		m = append(m, r.Method)
	}
	return m
}

func expectExit(t *testing.T, term *testutil.Term, want int) {
	t.Helper()
	if got := term.ExitCode(); got != want {
		t.Fatalf("exit code %d, want %d", got, want)
	}
}

func TestAskStreaming(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{Turn: []fakeagent.Step{
		{Text: "Let me look"},
		{Text: " at it."},
		{Tool: &fakeagent.Tool{ID: "t1", Title: "Read main.go", Kind: "read"}},
		{ToolDone: &fakeagent.Tool{ID: "t1", Status: "completed"}},
		{Tool: &fakeagent.Tool{ID: "t2", Title: "go test ./...", Kind: "execute"}},
		{ToolDone: &fakeagent.Tool{ID: "t2", Status: "failed"}},
		{Text: "Echo: "},
		{Echo: true},
	}})
	term := e.start(t, "--", "why is $HOME empty?")
	term.Expect("Let me look at it.")
	term.Expect("Read main.go")
	term.Expect("✓")
	term.Expect("$ ")
	term.Expect("go test ./...")
	term.Expect("✗")
	term.Expect("Echo: why is $HOME empty?")
	expectExit(t, term, 0)
}

func TestAskPermission(t *testing.T) {
	tests := []struct {
		name, key, want string
	}{
		{"allow", "1", "permission: allow"},
		{"y", "y", "permission: allow"},
		{"always", "2", "permission: always"},
		{"Esc rejects", esc, "permission: reject"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAskEnv(t, fakeagent.Script{Turn: []fakeagent.Step{
				{Text: "cleaning"},
				{Permission: &fakeagent.Tool{ID: "t1", Title: "rm -rf build", Kind: "execute"}},
			}})
			term := e.start(t, "clean")
			term.Expect("cleaning")
			term.Expect("Allow?")
			term.Expect("rm -rf build")
			term.Expect("^C cancel")
			// Enter is not an answer.
			term.Send("\r")
			term.Send(tt.key)
			term.Expect(tt.want)
			expectExit(t, term, 0)
		})
	}
}

// Keys typed before the dialog appeared do not answer it.
func TestAskPermissionTypeahead(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{Turn: []fakeagent.Step{
		{Text: "thinking"},
		{SleepMS: 500},
		{Permission: &fakeagent.Tool{ID: "t1", Title: "rm -rf build"}},
	}})
	term := e.start(t, "clean")
	term.Expect("thinking")
	term.Send("1")
	term.Expect("Allow?")
	term.Expect("^C cancel")
	term.Send("n")
	term.Expect("permission: reject")
	expectExit(t, term, 0)
}

func TestAskCtrlCInPermission(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Permission: &fakeagent.Tool{ID: "t1", Title: "rm -rf build"}}}})
	term := e.start(t, "clean")
	term.Expect("^C cancel")
	term.Send(ctrlC)
	term.Expect("permission: cancelled")
	term.Expect("cancelled")
	expectExit(t, term, 130)
}

func TestAskCancel(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "working"}, {WaitCancel: true}}})
	term := e.start(t, "wait")
	term.Expect("working")
	term.Send(ctrlC)
	term.Expect("cancelling")
	term.Expect("cancelled")
	expectExit(t, term, 130)
	if m := e.methods(t); m[len(m)-1] != "session/cancel" {
		t.Errorf("requests = %v, want session/cancel last", m)
	}
}

// An agent that ignores session/cancel is stopped by the second Ctrl+C.
func TestAskSecondCtrlC(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{IgnoreTerm: true, Turn: []fakeagent.Step{{Text: "working"}, {Hang: true}}})
	term := e.start(t, "wait")
	term.Expect("working")
	term.Send(ctrlC)
	term.Expect("cancelling")
	term.Send(ctrlC)
	start := time.Now()
	expectExit(t, term, 130)
	if d := time.Since(start); d > killGrace {
		t.Errorf("exit took %v after the second Ctrl+C; it must not wait for SIGTERM", d)
	}
}

// Ctrl+C before the agent answered initialize.
func TestAskCtrlCAtStart(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{})
	cfg := filepath.Join(e.dir, "config", "ecdy", "config.toml")
	if err := os.WriteFile(cfg, []byte("default_agent = \"mute\"\n[agents.mute]\ncommand = [\"sleep\", \"60\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	term := e.start(t, "hello")
	time.Sleep(200 * time.Millisecond)
	term.Send(ctrlC)
	expectExit(t, term, 130)
}

func TestAskAgentCrash(t *testing.T) {
	code := 3
	e := newAskEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "partial"}, {Stderr: "panic: boom\n"}, {Exit: &code}}})
	term := e.start(t, "crash")
	term.Expect("partial")
	term.Expect("ecdy: fake: agent exited: exit status 3")
	term.Expect("panic: boom")
	expectExit(t, term, 1)
}

func TestAskAuthRequired(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{AuthRequired: true})
	term := e.start(t, "hello")
	term.Expect("fake: agent requires authentication (Log in with a browser; Log in in a terminal)")
	term.Expect("log in with the agent's own CLI")
	expectExit(t, term, 1)
}

func TestAskUnknownAgent(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{})
	term := e.start(t, "--agent", "nope", "hello")
	term.Expect(`unknown agent "nope"`)
	expectExit(t, term, 1)
}

func TestAskAgentNotFound(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{})
	cfg := filepath.Join(e.dir, "config", "ecdy", "config.toml")
	if err := os.WriteFile(cfg, []byte("[agents.gone]\ncommand = [\"no-such-agent-ecdy\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	term := e.start(t, "-a", "gone", "hello")
	term.Expect(`gone: start agent "no-such-agent-ecdy"`)
	expectExit(t, term, 1)
}

// Without a controlling terminal nothing can be allowed: the request is
// rejected, and the output (a pipe) has no escape sequences.
func TestAskNoTerminal(t *testing.T) {
	e := newAskEnv(t, fakeagent.Script{Turn: []fakeagent.Step{
		{Tool: &fakeagent.Tool{ID: "t1", Title: "Read main.go"}},
		{ToolDone: &fakeagent.Tool{ID: "t1", Status: "completed"}},
		{Permission: &fakeagent.Tool{ID: "t2", Title: "rm -rf build"}},
	}})
	cmd := e.command("clean")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // no controlling terminal
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ecdy ask: %v\n%s", err, stderr.String())
	}
	if want := "⚙ Read main.go\n⚙ Read main.go ✓\npermission: reject\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "no terminal to ask for permission, rejected: rm -rf build") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestAskNoPrompt(t *testing.T) {
	if _, err := runRoot(t, "ask"); err == nil {
		t.Fatal("ask without a prompt: expected error")
	}
}
