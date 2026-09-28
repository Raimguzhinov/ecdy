package shell_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/sessionlog"
	"github.com/Raimguzhinov/ecdy/internal/testutil"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// sessionLogs returns the command logs in the test shell's state directory.
func (z *zshTerm) sessionLogs() []string {
	logs, _ := filepath.Glob(filepath.Join(z.home, ".local", "state", "ecdy", "sessions", "*.jsonl"))
	return logs
}

// waitRecords waits until the shell's log has n records: `ecdy log record`
// runs in the background, after the prompt is drawn.
func (z *zshTerm) waitRecords(n int) []sessionlog.Record {
	z.t.Helper()
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for {
		var recs []sessionlog.Record
		if logs := z.sessionLogs(); len(logs) == 1 {
			l, err := sessionlog.Open(filepath.Dir(logs[0]), strings.TrimSuffix(filepath.Base(logs[0]), ".jsonl"))
			if err != nil {
				z.t.Fatal(err)
			}
			recs, _ = l.Read()
		}
		if len(recs) >= n {
			if len(recs) > n {
				z.t.Fatalf("%d records, want %d: %+v", len(recs), n, recs)
			}
			return recs
		}
		if time.Now().After(deadline) {
			z.t.Fatalf("timed out waiting for %d records, have %+v", n, recs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func lines(recs []sessionlog.Record) []string {
	var l []string
	for _, r := range recs {
		l = append(l, r.Line)
	}
	return l
}

// TestSessionContext: commands typed in the shell reach the log (redacted,
// with their status even when another precmd hook runs first) and then
// the agent; prompts are not commands; each prompt of a conversation gets
// only the commands since the previous one.
func TestSessionContext(t *testing.T) { forEachZsh(t, testSessionContext) }

func testSessionContext(t *testing.T, zsh string) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record.jsonl")
	env, err := agentConfig(dir, []namedScript{{"fake", fakeagent.Script{Record: record, Turn: []fakeagent.Step{{Echo: true}}}}})
	if err != nil {
		t.Fatal(err)
	}
	z := startZsh(t, zsh, zshOpts{
		agent: env,
		// A precmd hook before ecdy's that changes $?.
		rc: "autoload -Uz add-zsh-hook; _other_precmd() { false }; add-zsh-hook precmd _other_precmd",
	})
	z.Send("mkdir sub && cd sub" + enter)
	z.ExpectPrompt()
	z.Send("(exit 3)" + enter)
	z.ExpectPrompt()
	z.Send("export API_TOKEN=abc123" + enter)
	z.ExpectPrompt()
	z.Send("print -r -- one \\" + enter)
	z.Run("two", "one two")
	recs := z.waitRecords(4)
	sub := filepath.Join(z.home, "sub")
	want := []string{"mkdir sub && cd sub", "(exit 3)", "export API_TOKEN=[REDACTED]", "print -r -- one \\\ntwo"}
	if strings.Join(lines(recs), "|") != strings.Join(want, "|") {
		t.Fatalf("records %q\nwant    %q", lines(recs), want)
	}
	if recs[0].Cwd != z.home || recs[1].Cwd != sub || recs[1].Exit != 3 || recs[0].Exit != 0 {
		t.Errorf("records: %+v", recs)
	}

	z.Run("why did it fail", "why did it fail")
	z.Run("ls -a", ".")
	z.Run("and now", "and now")
	z.waitRecords(5) // + ls -a; the prompts are not recorded

	prompts, err := fakeagent.Prompts(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || len(prompts[0]) != 2 || len(prompts[1]) != 2 {
		t.Fatalf("prompts: %q", prompts)
	}
	first, second := prompts[0][0], prompts[1][0]
	for _, want := range []string{
		"cwd: " + sub + "\n",
		"Recent commands, oldest first:\n",
		"[exit 0, ", ", in " + z.home + "] mkdir sub && cd sub\n",
		"[exit 3, ", "] (exit 3)\n",
		"] export API_TOKEN=[REDACTED]\n",
		"] print -r -- one \\\n    two\n",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("first context lacks %q:\n%s", want, first)
		}
	}
	// No wait for the record of ls -a before "and now": the plugin waits
	// for the recorder before it sends a prompt (seen failing on one CPU).
	if !regexp.MustCompile(`Commands since the previous prompt, oldest first:\n\[exit 0, [0-9.]+s\] ls -a\n$`).MatchString(second) {
		t.Errorf("second context:\n%s", second)
	}
	for _, c := range []string{first, second} {
		if strings.Contains(c, "abc123") || strings.Contains(c, "ecdy ask") || strings.Contains(c, "why did it fail") {
			t.Errorf("context with a secret or a prompt:\n%s", c)
		}
	}
	if prompts[0][1] != "why did it fail" || prompts[1][1] != "and now" {
		t.Errorf("prompts: %q", prompts)
	}

	// ecdy log shows the same records.
	z.Run("ecdy log", "API_TOKEN=[REDACTED]")
	if out := z.Output(); regexp.MustCompile(`\[\d+\] \d+`).MatchString(out) {
		t.Errorf("a job notice in the output:\n%s", out)
	}
	logs := z.sessionLogs()
	data, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "abc123") {
		t.Errorf("the secret reached the disk:\n%s", data)
	}

	// The log goes with the session, also when a recorder is still running
	// at exit: zshexit waits for it (seen failing on one CPU).
	z.Send("exit" + enter)
	z.Wait()
	if _, err := os.Stat(logs[0]); !os.IsNotExist(err) {
		t.Errorf("log after exit: %v", err)
	}
}

// TestSessionContextIgnoreSpace: with HIST_IGNORE_SPACE, a line starting
// with a space stays out of the log as it stays out of the history.
func TestSessionContextIgnoreSpace(t *testing.T) { forEachZsh(t, testSessionContextIgnoreSpace) }

func testSessionContextIgnoreSpace(t *testing.T, zsh string) {
	z := startZsh(t, zsh, zshOpts{rc: "setopt HIST_IGNORE_SPACE"})
	z.Run(" print -r -- hidden-$((1+1))", "hidden-2")
	z.Run("print -r -- shown-$((1+2))", "shown-3")
	recs := z.waitRecords(1)
	if recs[0].Line != "print -r -- shown-$((1+2))" {
		t.Errorf("records: %+v", recs)
	}
}

// TestSessionContextHungRecorder: an `ecdy log record` that never returns
// does not hold the shell (AGENTS.md, invariant 2).
func TestSessionContextHungRecorder(t *testing.T) { forEachZsh(t, testSessionContextHungRecorder) }

func testSessionContextHungRecorder(t *testing.T, zsh string) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "pids")
	stub := filepath.Join(dir, "ecdy")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = log ] || exec " + ecdyBin + " \"$@\"\n" +
		"echo $$ >> " + pidfile + "\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	t.Cleanup(func() {
		data, _ := os.ReadFile(pidfile)
		for f := range strings.FieldsSeq(string(data)) {
			if pid, err := strconv.Atoi(f); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	z := startZsh(t, zsh, zshOpts{after: "ECDY_BIN=" + stub})
	start := time.Now()
	for i := range 3 {
		z.Run("print -r -- still-$((40+"+strconv.Itoa(i)+"))", "still-4"+strconv.Itoa(i))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("three commands took %v with a hung recorder", d)
	}
	var recorders []int
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for len(recorders) < 3 {
		if time.Now().After(deadline) {
			t.Fatal("the recorders never ran")
		}
		time.Sleep(10 * time.Millisecond)
		data, _ := os.ReadFile(pidfile)
		recorders = recorders[:0]
		for f := range strings.FieldsSeq(string(data)) {
			if pid, err := strconv.Atoi(f); err == nil {
				recorders = append(recorders, pid)
			}
		}
	}

	// A prompt waits for the hung recorders once (0.5 s), then forgets them.
	for i, p := range []string{"first prompt", "second prompt"} {
		start := time.Now()
		z.Run(p, askReply+p)
		d := time.Since(start)
		if i == 0 && d < 400*time.Millisecond || d > 3*time.Second || i == 1 && d > 400*time.Millisecond {
			t.Errorf("prompt %d took %v", i+1, d)
		}
	}

	// exit waits for the recorder of the last command, then kills it.
	z.Send("print -r -- last" + enter)
	z.Expect("last")
	z.ExpectPrompt()
	start = time.Now()
	z.Send("exit" + enter)
	z.Wait()
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("exit took %v", d)
	}
	data, _ := os.ReadFile(pidfile)
	last, _ := strconv.Atoi(strings.Fields(string(data))[len(strings.Fields(string(data)))-1])
	waitGone(t, last)
}
