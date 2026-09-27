package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/testutil"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// ask runs `ecdy ask prompt` under a PTY and returns its output once it
// exited with code 0.
func (e *askEnv) ask(t *testing.T, args ...string) string {
	t.Helper()
	term := e.start(t, args...)
	expectExit(t, term, 0)
	term.Wait()
	return term.Output()
}

func (e *askEnv) mustEcdy(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.ecdy(t, args...)
	if err != nil {
		t.Fatalf("ecdy %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (e *askEnv) count(t *testing.T, method string) int {
	t.Helper()
	n := 0
	for _, m := range e.methods(t) {
		if m == method {
			n++
		}
	}
	return n
}

var daemonPidRe = regexp.MustCompile(`daemon pid (\d+)`)

// daemonPid returns the pid of the session's daemon from `ecdy daemon status`.
func (e *askEnv) daemonPid(t *testing.T) int {
	t.Helper()
	out := e.mustEcdy(t, "daemon", "status")
	m := daemonPidRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("ecdy daemon status:\n%s", out)
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}

func (e *askEnv) writeConfig(t *testing.T, toml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, "config", "ecdy", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// addAgent adds agent name to the config: the fake agent with its own
// script, recording to the same file.
func (e *askEnv) addAgent(t *testing.T, name string, s fakeagent.Script) {
	t.Helper()
	s.Record = e.record
	scriptEnv, err := fakeagent.WriteScript(e.dir, s)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	cfg := filepath.Join(e.dir, "config", "ecdy", "config.toml")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString("[agents." + name + "]\ncommand = [\"env\", " + quote(scriptEnv) + ", " + quote(self) + ", \"fake-agent\"]\n")
	if err != nil {
		t.Fatal(err)
	}
}

var historyScript = fakeagent.Script{Turn: []fakeagent.Step{{History: true}}}

// TestContinuity: the second prompt sees the first; the agent starts once.
func TestContinuity(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	e.ask(t, "one")
	pid := e.daemonPid(t)
	if out := e.ask(t, "two"); !strings.Contains(out, "history: one | two") {
		t.Fatalf("second prompt:\n%s", out)
	}
	if n := e.count(t, "initialize"); n != 1 {
		t.Errorf("the agent was started %d times", n)
	}
	if got := e.daemonPid(t); got != pid {
		t.Errorf("daemon pid changed from %d to %d", pid, got)
	}
}

// Without ECDY_SESSION every prompt starts a new agent and conversation.
func TestOneShotForgets(t *testing.T) {
	e := newAskEnv(t, oneShot, historyScript)
	e.ask(t, "one")
	if out := e.ask(t, "two"); !strings.Contains(out, "history: two") || strings.Contains(out, "one") {
		t.Fatalf("second prompt:\n%s", out)
	}
	if n := e.count(t, "initialize"); n != 2 {
		t.Errorf("the agent was started %d times, want 2", n)
	}
}

func TestNew(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	e.ask(t, "one")
	if out := e.mustEcdy(t, "new"); !strings.Contains(out, "new conversation") {
		t.Errorf("ecdy new: %s", out)
	}
	if out := e.ask(t, "two"); !strings.Contains(out, "history: two\n") {
		t.Fatalf("after ecdy new:\n%s", out)
	}
	if out := e.ask(t, "three"); !strings.Contains(out, "history: two | three") {
		t.Fatalf("after the new conversation:\n%s", out)
	}
	// A new ACP session on the same agent process.
	if i, n := e.count(t, "initialize"), e.count(t, "session/new"); i != 1 || n != 2 {
		t.Errorf("initialize ×%d, session/new ×%d; want 1 and 2", i, n)
	}
}

// ecdy new before the daemon runs is harmless.
func TestNewWithoutDaemon(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	e.mustEcdy(t, "new")
	if out := e.ask(t, "one"); !strings.Contains(out, "history: one") {
		t.Fatalf("ask:\n%s", out)
	}
}

// TestUse: switching agents keeps each agent's conversation; --agent picks
// an agent for one prompt without switching.
func TestUse(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	e.addAgent(t, "other", fakeagent.Script{Turn: []fakeagent.Step{{Text: "other "}, {History: true}}})
	if out := strings.TrimSpace(e.mustEcdy(t, "use")); out != "fake" {
		t.Errorf("ecdy use = %q, want fake", out)
	}
	e.ask(t, "one")
	if out := e.mustEcdy(t, "use", "other"); !strings.Contains(out, "using other") {
		t.Errorf("ecdy use other: %s", out)
	}
	if out := strings.TrimSpace(e.mustEcdy(t, "use")); out != "other" {
		t.Errorf("ecdy use = %q, want other", out)
	}
	if out := e.ask(t, "two"); !strings.Contains(out, "other history: two") {
		t.Fatalf("after ecdy use other:\n%s", out)
	}
	if out := e.ask(t, "--agent", "fake", "three"); !strings.Contains(out, "history: one | three") || strings.Contains(out, "other") {
		t.Fatalf("ask --agent fake:\n%s", out)
	}
	if out := e.ask(t, "four"); !strings.Contains(out, "other history: two | four") {
		t.Fatalf("--agent must not switch:\n%s", out)
	}
	e.mustEcdy(t, "use", "fake")
	if out := e.ask(t, "five"); !strings.Contains(out, "history: one | three | five") {
		t.Fatalf("back to fake:\n%s", out)
	}
	status := e.mustEcdy(t, "daemon", "status")
	for _, want := range []string{"agent fake", "  fake: pid", "  other: pid"} {
		if !strings.Contains(status, want) {
			t.Errorf("status lacks %q:\n%s", want, status)
		}
	}
	if out, err := e.ecdy(t, "use", "nope"); err == nil || !strings.Contains(out, `unknown agent "nope"`) {
		t.Errorf("ecdy use nope: %v\n%s", err, out)
	}
	if out := strings.TrimSpace(e.mustEcdy(t, "use")); out != "fake" {
		t.Errorf("a failed use changed the agent to %q", out)
	}
}

func TestSessionCommandsNeedSession(t *testing.T) {
	e := newAskEnv(t, oneShot, historyScript)
	for _, args := range [][]string{{"use"}, {"new"}, {"daemon", "stop"}, {"daemon", "status"}, {"daemon"}} {
		out, err := e.ecdy(t, args...)
		if err == nil || !strings.Contains(out, "ECDY_SESSION is not set") {
			t.Errorf("ecdy %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// A second prompt to an agent in a turn fails at once instead of waiting.
func TestBusy(t *testing.T) {
	e := newAskEnv(t, withDaemon, fakeagent.Script{Turn: []fakeagent.Step{{Text: "working"}, {WaitCancel: true}}})
	first := e.start(t, "first")
	first.Expect("working")
	second := e.start(t, "second")
	second.Expect("fake is busy with another prompt in this shell session")
	expectExit(t, second, 1)
	first.Send(ctrlC)
	expectExit(t, first, 130)
}

// When `ecdy ask` is killed mid-turn, the daemon cancels the turn and the
// agent lives on for the next prompt.
func TestClientKilled(t *testing.T) {
	e := newAskEnv(t, withDaemon, fakeagent.Script{Turn: []fakeagent.Step{{History: true}, {AwaitCancel: true}, {Text: "stopped"}}})
	term := e.start(t, "one")
	term.Expect("history: one")
	pids := e.agentPids(t)
	if err := syscall.Kill(term.Pid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for e.count(t, "session/cancel") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no session/cancel; requests = %v", e.methods(t))
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The turn ends; the conversation goes on with the same agent.
	two := e.start(t, "two")
	two.Expect("history: one | two")
	two.Send(ctrlC)
	two.Expect("stopped") // this agent goes on after session/cancel
	expectExit(t, two, 0)
	if got := e.agentPids(t); !slices.Equal(got, pids) {
		t.Errorf("agent pids %v, want %v", got, pids)
	}
}

// An agent that died between prompts is started again.
func TestAgentDiedBetweenPrompts(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	e.ask(t, "one")
	pids := e.agentPids(t)
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pids...)
	out := e.ask(t, "two")
	if !strings.Contains(out, "fake had exited; starting a new conversation") || !strings.Contains(out, "history: two") {
		t.Fatalf("after the agent died:\n%s", out)
	}
}

func TestCwdNotice(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	sub := filepath.Join(e.dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	e.ask(t, "one")
	// Below the session's directory: no notice.
	cmd := e.command("two")
	cmd.Dir = sub
	term := testutil.StartTerm(t, cmd)
	expectExit(t, term, 0)
	term.Wait()
	if out := term.Output(); strings.Contains(out, "conversation runs in") {
		t.Errorf("notice in a subdirectory:\n%s", out)
	}
	// Outside it: the same conversation, with a notice.
	elsewhere := t.TempDir()
	cmd = e.command("three")
	cmd.Dir = elsewhere
	term = testutil.StartTerm(t, cmd)
	term.Expect("this conversation runs in " + e.dir + "; `ecdy new` starts one here")
	term.Expect("history: one | two | three")
	expectExit(t, term, 0)

	// ecdy new there starts the session in that directory.
	e.mustEcdy(t, "new")
	term = testutil.StartTerm(t, cmd2(e, elsewhere, "four"))
	term.Expect("history: four")
	expectExit(t, term, 0)
	if out := e.mustEcdy(t, "daemon", "status"); !strings.Contains(out, "cwd "+elsewhere) {
		t.Errorf("status:\n%s", out)
	}
}

func cmd2(e *askEnv, dir string, args ...string) *exec.Cmd {
	cmd := e.command(args...)
	cmd.Dir = dir
	return cmd
}

func TestDaemonStop(t *testing.T) {
	// A slow agent: stop must wait for it (it ignores SIGTERM, and takes
	// less than the kill grace to exit).
	e := newAskEnv(t, withDaemon, fakeagent.Script{IgnoreTerm: true, ExitDelayMS: 500, Turn: historyScript.Turn})
	if out := e.mustEcdy(t, "daemon", "stop"); !strings.Contains(out, "no daemon is running") {
		t.Errorf("stop without a daemon: %s", out)
	}
	e.ask(t, "one")
	e.mustEcdy(t, "use", "fake")
	daemon, agents := e.daemonPid(t), e.agentPids(t)
	// stop waits for the agents.
	e.mustEcdy(t, "daemon", "stop")
	for _, pid := range agents {
		if testutil.Alive(pid) {
			t.Errorf("agent %d still running after ecdy daemon stop", pid)
		}
	}
	waitGone(t, daemon)
	if out := e.mustEcdy(t, "daemon", "status"); !strings.Contains(out, "no daemon (agent: fake)") {
		t.Errorf("status after stop: %s", out)
	}
	// The next prompt starts everything again: a new conversation.
	if out := e.ask(t, "two"); !strings.Contains(out, "history: two") || strings.Contains(out, "one") {
		t.Fatalf("after stop:\n%s", out)
	}
	// --end-session (the plugin's zshexit) also forgets the agent.
	state := filepath.Join(e.runtime, "ecdy", session+".state")
	e.mustEcdy(t, "daemon", "stop", "--no-wait", "--end-session")
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state file after --end-session: %v", err)
	}
	waitGone(t, e.agentPids(t)...)
}

// TestShellGone: the daemon stops with its shell even without zshexit.
func TestShellGone(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	shell := exec.Command("sleep", "60")
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shell.Process.Kill(); _ = shell.Wait() })
	e.env = append(e.env, "ECDY_SHELL_PID="+strconv.Itoa(shell.Process.Pid))
	e.ask(t, "one")
	e.mustEcdy(t, "use", "fake")
	daemon, agents := e.daemonPid(t), e.agentPids(t)
	time.Sleep(700 * time.Millisecond) // more than one check of the shell
	if !testutil.Alive(daemon) {
		t.Fatal("the daemon stopped while the shell was alive")
	}
	_ = shell.Process.Kill()
	_ = shell.Wait()
	waitGone(t, append(agents, daemon)...)
	state := filepath.Join(e.runtime, "ecdy", session+".state")
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state file after the shell exited: %v", err)
	}
}

func TestIdleTimeout(t *testing.T) {
	for _, tt := range []struct {
		timeout string
		alive   bool // right after the prompt
	}{
		{"0s", false},
		{"5s", true}, // long enough for a slow -race run to look
	} {
		t.Run(tt.timeout, func(t *testing.T) {
			e := newAskEnv(t, withDaemon, historyScript)
			self, _ := os.Executable()
			e.writeConfig(t, "default_agent = \"fake\"\nidle_timeout = \""+tt.timeout+"\"\n[agents.fake]\ncommand = ["+quote(self)+", \"fake-agent\"]\n")
			e.ask(t, "one")
			agents := e.agentPids(t)
			// Look at the daemon's socket instead of asking it: a status
			// request is a client too, and would restart the countdown.
			sock := filepath.Join(e.runtime, "ecdy", session+".sock")
			_, err := os.Stat(sock)
			if tt.alive && err != nil {
				t.Fatalf("the daemon stopped at once: %v", err)
			}
			waitGone(t, agents...)
			deadline := time.Now().Add(testutil.DefaultTimeout)
			for {
				if _, err := os.Stat(sock); os.IsNotExist(err) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the daemon did not stop after its idle timeout")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if out := e.ask(t, "two"); !strings.Contains(out, "history: two") {
				t.Fatalf("after the idle timeout:\n%s", out)
			}
		})
	}
}

// TestVersionMismatch: a daemon of another protocol version is stopped and
// replaced.
func TestVersionMismatch(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	dir := filepath.Join(e.runtime, "ecdy")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(dir, session+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []string, 1)
	go func() {
		var reqs []string
		defer func() { got <- reqs }()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			var m struct {
				T string `json:"t"`
			}
			_ = json.Unmarshal([]byte(line), &m)
			reqs = append(reqs, m.T)
			if m.T == "stop" {
				_ = c.Close()
				_ = l.Close() // removes the socket
				return
			}
			_, _ = c.Write([]byte(`{"t":"error","code":"version","text":"old daemon"}` + "\n"))
			_ = c.Close()
		}
	}()
	if out := e.ask(t, "one"); !strings.Contains(out, "history: one") {
		t.Fatalf("ask:\n%s", out)
	}
	if reqs := <-got; !slices.Equal(reqs, []string{"prompt", "stop"}) {
		t.Errorf("the old daemon got %v", reqs)
	}
}

// The runtime directory must not be readable by others.
func TestRuntimeDirPermissions(t *testing.T) {
	e := newAskEnv(t, withDaemon, historyScript)
	dir := filepath.Join(e.runtime, "ecdy")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // umask
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // before stopDaemon
	term := e.start(t, "one")
	term.Expect("accessible by others")
	expectExit(t, term, 1)
}
