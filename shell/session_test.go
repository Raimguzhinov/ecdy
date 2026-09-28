package shell_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/testutil"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// waitNoSockets waits until no daemon socket is left in the runtime
// directory dir: every daemon has stopped.
func waitNoSockets(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for {
		socks, _ := filepath.Glob(filepath.Join(dir, "ecdy", "*.sock"))
		if len(socks) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("daemon sockets left: %v", socks)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stopDaemons stops every daemon with a socket in the runtime directory dir.
func stopDaemons(t *testing.T, dir string) {
	t.Helper()
	socks, _ := filepath.Glob(filepath.Join(dir, "ecdy", "*.sock"))
	for _, sock := range socks {
		cmd := exec.Command(ecdyBin, "daemon", "stop")
		cmd.Env = []string{"XDG_RUNTIME_DIR=" + dir, "ECDY_SESSION=" + strings.TrimSuffix(filepath.Base(sock), ".sock")}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("ecdy daemon stop for %s: %v\n%s", sock, err, out)
		}
	}
}

// sessionShell starts zsh with two fake agents that print what they remember
// of the conversation, and returns it with the file their requests are
// recorded to.
func sessionShell(t *testing.T, zsh string, o zshOpts) (*zshTerm, string) {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "record.jsonl")
	env, err := agentConfig(dir, []namedScript{
		{"fake", fakeagent.Script{Record: record, Turn: []fakeagent.Step{{History: true}}}},
		{"other", fakeagent.Script{Record: record, Turn: []fakeagent.Step{{Text: "other "}, {History: true}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	o.agent = env
	return startZsh(t, zsh, o), record
}

func pids(t *testing.T, record string) []int {
	t.Helper()
	reqs, err := fakeagent.ReadRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, r := range reqs {
		if !contains(pids, r.Pid) {
			pids = append(pids, r.Pid)
		}
	}
	return pids
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var daemonPidRe = regexp.MustCompile(`daemon pid (\d+)`)

func (z *zshTerm) daemonPid() int {
	z.t.Helper()
	z.Send("ecdy daemon status" + enter)
	m := daemonPidRe.FindStringSubmatch(testutil.Printable(z.ExpectRe(daemonPidRe)))
	z.ExpectPrompt()
	pid, _ := strconv.Atoi(m[1])
	return pid
}

func waitGone(t *testing.T, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for _, pid := range pids {
		for testutil.Alive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d is still running", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestConversation: prompts typed in one shell are one conversation;
// `ecdy new` and `ecdy use` work from the shell.
func TestConversation(t *testing.T) { forEachZsh(t, testConversation) }

func testConversation(t *testing.T, zsh string) {
	z, record := sessionShell(t, zsh, zshOpts{})
	z.Run("remember the number 42", "history: remember the number 42")
	// zshexit runs in a subshell that calls exit too; it must not end the
	// session (found in M5: `(exit 3)` stopped the daemon).
	z.Run("(print -r -- in-a-subshell; exit 3)", "in-a-subshell")
	z.Run("what was the number", "history: remember the number 42 | what was the number")
	z.Run("ecdy new", "new conversation")
	z.Run("start over please", "history: start over please\r\n")
	z.Run("ecdy use other", "using other")
	z.Run("hello other agent", "other history: hello other agent")
	z.Run("ecdy use fake", "using fake")
	z.Run("back to you", "history: start over please | back to you")
	if n := len(pids(t, record)); n != 2 {
		t.Errorf("%d agent processes, want 2 (one per agent)", n)
	}
}

// TestExitStopsDaemon: after the shell exits, neither the daemon nor its
// agents are left, whether zshexit ran or the shell was killed.
func TestExitStopsDaemon(t *testing.T) { forEachZsh(t, testExitStopsDaemon) }

func testExitStopsDaemon(t *testing.T, zsh string) {
	for _, how := range []string{"exit", "SIGKILL"} {
		t.Run(how, func(t *testing.T) {
			var o zshOpts
			if how == "exit" {
				// Only zshexit can stop the daemon: it does not know the
				// shell's pid.
				o.after = "export ECDY_SHELL_PID=0"
			}
			z, record := sessionShell(t, zsh, o)
			z.Run("hello there agent", "history: hello there agent")
			z.Run("ecdy use other", "using other")
			z.Run("hello other agent", "other history: hello other agent")
			procs := append(pids(t, record), z.daemonPid())
			state, _ := filepath.Glob(filepath.Join(z.runtime, "ecdy", "*.state"))
			if len(state) != 1 {
				t.Fatalf("state files: %v", state)
			}
			z.waitRecords(2) // ecdy use other, ecdy daemon status
			start := time.Now()
			if how == "exit" {
				z.Send("exit" + enter)
				z.Wait()
			} else {
				if err := syscall.Kill(z.Pid(), syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				// Reap it, as a terminal would: a zombie still has its pid.
				z.ExitCode()
			}
			waitGone(t, procs...)
			t.Logf("stopped %d processes %v after the shell", len(procs), time.Since(start))
			waitNoSockets(t, z.runtime)
			if _, err := os.Stat(state[0]); !os.IsNotExist(err) {
				t.Errorf("state file after the shell exited: %v", err)
			}
			// The command log goes too: removed by zshexit, or by the daemon
			// once the shell is gone.
			if logs := z.sessionLogs(); len(logs) != 0 {
				t.Errorf("session logs after the shell exited: %v", logs)
			}
		})
	}
}

// TestSessionEnv: every interactive shell with the plugin has a session of
// its own, a nested one too.
func TestSessionEnv(t *testing.T) { forEachZsh(t, testSessionEnv) }

func testSessionEnv(t *testing.T, zsh string) {
	z := startZsh(t, zsh, zshOpts{})
	z.Send(`print -r -- "s=$ECDY_SESSION p=$ECDY_SHELL_PID self=$$"` + enter)
	outerRe := regexp.MustCompile(`s=([0-9-]+) p=(\d+) self=(\d+)`)
	m := outerRe.FindStringSubmatch(testutil.Printable(z.ExpectRe(outerRe)))
	z.ExpectPrompt()
	if m[2] != m[3] || !strings.HasPrefix(m[1], m[3]+"-") {
		t.Fatalf("outer shell: %v", m)
	}
	// A child process sees the session; a nested shell with the plugin
	// starts its own.
	z.Send(`env | grep '^ECDY_SESSION=' | sed 's/^/child-/'; ` + zsh + ` -f -i -c 'source ` + sourceFile(t) + `; print -r -- nested-$ECDY_SESSION'` + enter)
	childRe, nestedRe := regexp.MustCompile(`child-ECDY_SESSION=[0-9-]+`), regexp.MustCompile(`nested-[0-9-]*`)
	child := childRe.FindString(testutil.Printable(z.ExpectRe(childRe)))
	nested := nestedRe.FindString(testutil.Printable(z.ExpectRe(nestedRe)))
	z.ExpectPrompt()
	if child != "child-ECDY_SESSION="+m[1] {
		t.Errorf("child sees %q, want %s", child, m[1])
	}
	if nested == "nested-"+m[1] || nested == "nested-" {
		t.Errorf("nested shell has session %q, the outer one %s", nested, m[1])
	}
}
