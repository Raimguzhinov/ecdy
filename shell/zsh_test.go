package shell_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Raimguzhinov/ecdy/internal/classify"
	"github.com/Raimguzhinov/ecdy/internal/testutil"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// ecdyBin is the ecdy binary built once for all PTY tests.
var ecdyBin string

// agentEnv points `ecdy ask` at the fake agent: the test binary run with
// the argument fake-agent, which replies agentReply followed by the prompt.
var agentEnv []string

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "fake-agent" {
		fakeagent.Main()
	}
	os.Exit(run(m))
}

// setupAgent writes the ecdy config and the fake agent's script to dir.
func setupAgent(dir string) error {
	env, err := agentConfig(dir, []namedScript{{"fake", fakeagent.Script{Turn: []fakeagent.Step{{Text: askReply}, {Echo: true}}}}})
	agentEnv = env
	return err
}

type namedScript struct {
	name   string
	script fakeagent.Script
}

// agentConfig writes an ecdy config to dir with the given fake agents, the
// first one the default, and returns the environment that selects it.
func agentConfig(dir string, agents []namedScript) ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the test binary: %w", err)
	}
	cfg := fmt.Sprintf("default_agent = %q\n", agents[0].name)
	for _, a := range agents {
		scriptEnv, err := fakeagent.WriteScript(dir, a.script)
		if err != nil {
			return nil, err
		}
		cfg += fmt.Sprintf("[agents.%s]\ncommand = [\"env\", %q, %q, \"fake-agent\"]\n", a.name, scriptEnv, self)
	}
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(filepath.Join(cfgDir, "ecdy"), 0o700); err != nil {
		return nil, fmt.Errorf("config directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "ecdy", "config.toml"), []byte(cfg), 0o600); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	return []string{"XDG_CONFIG_HOME=" + cfgDir}, nil
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ecdy-shell-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := setupAgent(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ecdyBin = filepath.Join(dir, "ecdy")
	build := exec.Command("go", "build", "-o", ecdyBin, "github.com/Raimguzhinov/ecdy/cmd/ecdy")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// Keys.
const (
	enter    = "\r"
	altEnter = "\x1b\r"
	esc      = "\x1b"
	ctrlU    = "\x15"
	ctrlT    = "\x14"
	up       = "\x1b[A"
)

// promptMark starts PS1 in the test shell.
const promptMark = "ecdy-test"

type zshOpts struct {
	// rc is sourced before the plugin; load is how the plugin is loaded
	// (default: eval "$(ecdy init zsh)"); after is sourced after it.
	rc, load, after string
	// path is $PATH; the default puts the test ecdy binary first.
	path string
	// agent replaces agentEnv: the environment that points ecdy at the fake
	// agent (see agentConfig).
	agent []string
}

type zshTerm struct {
	*testutil.Term
	t       *testing.T
	home    string // $HOME and the working directory
	runtime string // $XDG_RUNTIME_DIR: the daemons' sockets
}

// startZsh starts `zsh -f -i` under a PTY in a temporary $HOME and loads the
// plugin by sending a `source` line, as a user's .zshrc would.
func startZsh(t *testing.T, zsh string, o zshOpts) *zshTerm {
	t.Helper()
	home := t.TempDir()
	if o.path == "" {
		o.path = filepath.Dir(ecdyBin) + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	if o.load == "" {
		o.load = `eval "$(ecdy init zsh)"`
	}
	rc := strings.Join([]string{
		// Set here as well: a global /etc/zshenv (NixOS) may reset it.
		`export PATH=` + shellQuote(o.path),
		`PS1='` + promptMark + `%h%# '`,
		`HISTFILE=$HOME/.zsh_history HISTSIZE=100 SAVEHIST=100`,
		`setopt INC_APPEND_HISTORY`,
		// A loaded CI runner must not turn prompts into commands by missing
		// the default deadline; TestFailOpen/slow sets its own.
		`ECDY_CLASSIFY_TIMEOUT=5`,
		o.rc,
		o.load,
		o.after,
		`print -r -- loaded-$((40+2))`,
	}, "\n")
	rcPath := filepath.Join(home, "rc.zsh")
	if err := os.WriteFile(rcPath, []byte(rc+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Not t.TempDir(): a unix socket path must be short.
	runtime, err := os.MkdirTemp("", "ecdy-rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The shell is killed without zshexit. Its daemons would stop on
		// their own once it is gone, unless a test switched that off
		// (ECDY_SHELL_PID=0): stop them explicitly.
		stopDaemons(t, runtime)
		waitNoSockets(t, runtime)
		_ = os.RemoveAll(runtime)
	})
	if o.agent == nil {
		o.agent = agentEnv
	}
	cmd := exec.Command(zsh, "-f", "-i")
	cmd.Dir = home
	cmd.Env = append([]string{
		"HOME=" + home,
		"PATH=" + o.path,
		"TERM=xterm",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"XDG_RUNTIME_DIR=" + runtime,
	}, o.agent...)
	z := &zshTerm{Term: testutil.StartTerm(t, cmd), t: t, home: home, runtime: runtime}
	z.Diag = func() string {
		var b strings.Builder
		b.WriteString("files in $HOME:\n")
		entries, _ := os.ReadDir(home)
		for _, e := range entries {
			info, _ := e.Info()
			fmt.Fprintf(&b, "  %s %d %s\n", e.Name(), info.Size(), info.ModTime().Format("15:04:05.000"))
		}
		return b.String()
	}
	z.Send(" source " + rcPath + enter)
	z.Expect("loaded-42")
	z.ExpectPrompt()
	return z
}

// ExpectPrompt waits for a fresh prompt.
func (z *zshTerm) ExpectPrompt() {
	z.t.Helper()
	z.Expect(promptMark)
}

// Run types line, presses Enter and waits for want in the output and then
// for the next prompt.
func (z *zshTerm) Run(line, want string) {
	z.t.Helper()
	z.Send(line + enter)
	z.Expect(want)
	z.ExpectPrompt()
}

// askReply starts the fake agent's reply to a prompt.
const askReply = "agent: "

func TestCommand(t *testing.T) { forEachZsh(t, testCommand) }

func testCommand(t *testing.T, zsh string) {
	z := startZsh(t, zsh, zshOpts{})
	z.Run(`print -r -- cmd-$((6*7))`, "cmd-42")
	// A known command with a word argument still runs.
	z.Run(`ls -d rc.zsh && print -r -- ls-$((6*7))`, "ls-42")
}

func TestPrompt(t *testing.T) { forEachZsh(t, testPrompt) }

func testPrompt(t *testing.T, zsh string) {
	z := startZsh(t, zsh, zshOpts{})
	z.Run(`explain this error`, askReply+"explain this error")
	// Nothing in the prompt is expanded by the shell.
	z.Run(`why does $HOME != "x" fail?! * 'q'`, askReply+`why does $HOME != "x" fail?! * 'q'`)
	// The ? prefix forces a prompt and is stripped.
	z.Run(`? print -r -- hi`, askReply+"print -r -- hi")
}

// TestForceCommand: Alt+Enter runs the line as a command, skipping the
// classifier.
func TestForceCommand(t *testing.T) { forEachZsh(t, testForceCommand) }

func testForceCommand(t *testing.T, zsh string) {
	z := startZsh(t, zsh, zshOpts{})
	z.Send("explain this error" + altEnter)
	z.Expect("command not found: explain")
	z.ExpectPrompt()
}

func TestAskDialog(t *testing.T) { forEachZsh(t, testAskDialog) }

func testAskDialog(t *testing.T, zsh string) {
	const dangerous = "rm everything in tmp except configs"
	z := startZsh(t, zsh, zshOpts{})
	configs := filepath.Join(z.home, "configs")
	if err := os.WriteFile(configs, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exists := func(want bool) {
		t.Helper()
		_, err := os.Stat(configs)
		if got := err == nil; got != want {
			t.Fatalf("configs exists = %v, want %v", got, want)
		}
	}

	// Enter: the default goes to the agent.
	z.Send(dangerous + enter)
	z.Expect("destructive command")
	z.Send(enter)
	z.Expect(askReply + dangerous)
	z.ExpectPrompt()
	exists(true)

	// e: back to editing, nothing runs; the line is still in the buffer.
	z.Send(dangerous + enter)
	z.Expect("destructive command")
	z.Send("e")
	z.Send("; print -r -- edited-$((6*7))" + altEnter)
	z.Expect("edited-42")
	z.ExpectPrompt()
	exists(false)
	if err := os.WriteFile(configs, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Esc: the line is dropped.
	z.Send(dangerous + enter)
	z.Expect("destructive command")
	z.Send(esc)
	z.ExpectPrompt()
	z.Run(`print -r -- after-esc-$((6*7))`, "after-esc-42")
	exists(true)

	// r: run it as a command, as asked.
	z.Send(dangerous + enter)
	z.Expect("destructive command")
	z.Send("r")
	z.Expect("rm: ")
	z.ExpectPrompt()
	exists(false)
}

func TestAskTypo(t *testing.T) { forEachZsh(t, testAskTypo) }

func testAskTypo(t *testing.T, zsh string) {
	z := startZsh(t, zsh, zshOpts{})
	z.Send(`ehco typo-$((6*7))` + enter)
	z.Expect("did you mean `echo typo-$((6*7))`?")
	z.Send("f")
	z.Expect("typo-42")
	z.ExpectPrompt()

	z.Send(`ehco typo-$((6*7))` + enter)
	z.Expect("did you mean")
	z.Send(enter)
	z.Expect(askReply + "ehco typo-$((6*7))")
	z.ExpectPrompt()
}

// TestFailOpen: whatever goes wrong with the binary, Enter behaves like in
// vanilla zsh (AGENTS.md, invariant 2).
func TestFailOpen(t *testing.T) { forEachZsh(t, testFailOpen) }

func testFailOpen(t *testing.T, zsh string) {
	dir := t.TempDir()
	scripts := map[string]string{
		"crash": "#!/bin/sh\nexit 2\n",
		// Only the classifier hangs: every command also runs `ecdy log
		// record` in the background, and those must not pile up.
		"slow":    "#!/bin/sh\n[ \"$1\" = classify ] && exec sleep 30\nexit 0\n",
		"garbage": "#!/bin/sh\nprintf 'prompt\\000'\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil { //nolint:gosec // test executables
			t.Fatal(err)
		}
	}
	plugin := sourceFile(t)
	tests := []struct {
		name  string
		opts  zshOpts
		lines int // builtin command lines after the first one
	}{
		// No ecdy in $PATH: the plugin is sourced directly.
		{"missing", zshOpts{load: "source " + plugin, path: "/usr/bin:/bin:" + filepath.Dir(zsh)}, 0},
		{"crash", zshOpts{after: "ECDY_BIN=" + filepath.Join(dir, "crash")}, 0},
		{"slow", zshOpts{after: "ECDY_BIN=" + filepath.Join(dir, "slow") + " ECDY_CLASSIFY_TIMEOUT=0.2"}, 0},
		{"garbage", zshOpts{after: "ECDY_BIN=" + filepath.Join(dir, "garbage")}, 0},
		// The deadline passes before the classifier even reports its pid.
		// It must still be stopped: a child exiting while zsh 5.9 draws
		// the next prompt left that prompt blank.
		// It is a race, so give it many lines.
		{"deadline", zshOpts{after: "ECDY_CLASSIFY_TIMEOUT=0"}, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			z := startZsh(t, zsh, tt.opts)
			z.Run("explain this error", "command not found: explain")
			for range max(tt.lines, 3) {
				z.Run(`print -r -- still-$((6*7))`, "still-42")
			}
		})
	}
}

// TestHistory: the history gets the line the user typed, not the
// `ecdy ask -- ...` it was rewritten to.
func TestHistory(t *testing.T) { forEachZsh(t, testHistory) }

func testHistory(t *testing.T, zsh string) {
	z := startZsh(t, zsh, zshOpts{})
	z.Run("explain this error", askReply+"explain this error")
	z.Run(`print -r -- one-$((6*7))`, "one-42")
	z.Send(`fc -ln 1 | tr '\n' '|' | sed 's/^/hi''st=/;s/$/=e''nd/'` + enter)
	z.Expect("hist=")
	hist := testutil.Printable(z.Expect("=end"))
	z.ExpectPrompt()
	if strings.Contains(hist, "ecdy ask") || !strings.Contains(hist, "|explain this error|print -r -- one-$((6*7))|") {
		t.Fatalf("in-memory history = %q", hist)
	}
	z.Send("exit" + enter)
	z.Wait()
	data, err := os.ReadFile(filepath.Join(z.home, ".zsh_history"))
	if err != nil {
		t.Fatal(err)
	}
	if file := string(data); strings.Contains(file, "ecdy ask") || !strings.Contains(file, "explain this error\n") {
		t.Fatalf("history file:\n%s", file)
	}
}

// TestHistoryUp: Up right after a prompt recalls the line as typed.
func TestHistoryUp(t *testing.T) { forEachZsh(t, testHistoryUp) }

func testHistoryUp(t *testing.T, zsh string) {
	// Ctrl+T saves the buffer to a file, to look at it without running it.
	z := startZsh(t, zsh, zshOpts{rc: `_save_buffer() { print -r -- $BUFFER > $HOME/buffer }; zle -N _save_buffer; bindkey '^T' _save_buffer`})
	z.Run("explain this error", askReply+"explain this error")
	z.Send(up + ctrlT + ctrlU + `print -r -- saved-$((6*7))` + enter)
	z.Expect("saved-42")
	z.ExpectPrompt()
	data, err := os.ReadFile(filepath.Join(z.home, "buffer"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "explain this error" {
		t.Fatalf("Up recalled %q", got)
	}
}

// TestCoexistence loads zsh-syntax-highlighting and zsh-autosuggestions
// before and after ecdy. Their paths come from the nix devShell.
func TestCoexistence(t *testing.T) { forEachZsh(t, testCoexistence) }

func testCoexistence(t *testing.T, zsh string) {
	var plugins []string
	for _, env := range []string{"ECDY_TEST_ZSH_SYNTAX_HIGHLIGHTING", "ECDY_TEST_ZSH_AUTOSUGGESTIONS"} {
		p := os.Getenv(env)
		if p == "" {
			t.Skipf("%s is not set (run the tests in `nix develop`)", env)
		}
		plugins = append(plugins, "source "+p)
	}
	others := strings.Join(plugins, "\n")
	for _, tt := range []struct {
		name string
		opts zshOpts
	}{
		{"others first", zshOpts{rc: others}},
		{"ecdy first", zshOpts{after: others}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			z := startZsh(t, zsh, tt.opts)
			z.Run(`print -r -- cmd-$((6*7))`, "cmd-42")
			z.Run("explain this error", askReply+"explain this error")
			z.Send(`ehco typo-$((6*7))` + enter)
			z.Expect("did you mean")
			z.Send("f")
			z.Expect("typo-42")
			z.ExpectPrompt()
			z.Send("explain this error" + altEnter)
			z.Expect("command not found: explain")
			z.ExpectPrompt()
			// Both plugins are still active.
			z.Run(`print -r -- ${+functions[_zsh_highlight]}${+functions[_zsh_autosuggest_start]}-$((6*7))`, "11-42")
			z.Send("exit" + enter)
			z.Wait()
			data, err := os.ReadFile(filepath.Join(z.home, ".zsh_history"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "ecdy ask") {
				t.Fatalf("history file:\n%s", data)
			}
		})
	}
}

func sourceFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	return filepath.Join(filepath.Dir(file), "zsh", "ecdy.plugin.zsh")
}

// forEachZsh runs test as a subtest for every zsh in $ECDY_TEST_ZSH (a
// list of zsh binaries, like $PATH; the zsh-matrix devShell sets it), or
// for the zsh in $PATH.
func forEachZsh(t *testing.T, test func(t *testing.T, zsh string)) {
	t.Helper()
	zshes := filepath.SplitList(os.Getenv("ECDY_TEST_ZSH"))
	if len(zshes) == 0 {
		zsh, err := exec.LookPath("zsh")
		if err != nil {
			t.Skip("zsh is not installed")
		}
		zshes = []string{zsh}
	}
	for _, zsh := range zshes {
		out, err := exec.Command(zsh, "-f", "-c", "print -r -- $ZSH_VERSION").Output()
		if err != nil {
			t.Fatalf("%s: %v", zsh, err)
		}
		t.Run("zsh-"+strings.TrimSpace(string(out)), func(t *testing.T) { test(t, zsh) })
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestFirstKind checks that the plugin finds the same first word as
// classify.FirstWord and names its kind like `whence -w`.
func TestFirstKind(t *testing.T) { forEachZsh(t, testFirstKind) }

func testFirstKind(t *testing.T, zsh string) {
	cases := []struct{ line, word, kind string }{
		{"", "", "none"},
		{"ls -la", "ls", "command"},
		{"ll", "ll", "alias"},
		{"myfunc arg", "myfunc", "function"},
		{"cd /tmp", "cd", "builtin"},
		{"if true; then :; fi", "if", "reserved"},
		{"explain this error", "explain", "none"},
		{"FOO=1 BAR+=2 ls", "ls", "command"},
		{"sudo -u root ls", "ls", "command"},
		{"sudo -E explain", "explain", "none"},
		{"env -i FOO=1 cd", "cd", "builtin"},
		{"noglob command exec -a name ls", "ls", "command"},
		{"time myfunc", "myfunc", "function"},
		{"FOO=1 > out ls", "ls", "command"},
		{"FOO=1", "", "none"},
		{"'ls' -la", "ls", "command"},
		{"; ls", "", "none"},
		{"./nope", "./nope", "none"},
		{"/bin/sh -c true", "/bin/sh", "command"},
	}
	var script strings.Builder
	script.WriteString("alias ll='ls -l'\nmyfunc() { :; }\nsource " + shellQuote(sourceFile(t)) + "\n")
	for _, c := range cases {
		fmt.Fprintf(&script, "_ecdy_first_kind %s; print -r -- $REPLY\n", shellQuote(c.line))
	}
	cmd := exec.Command(zsh, "-f", "-i", "-c", script.String())
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin:" + filepath.Dir(zsh)}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("zsh: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(cases), out)
	}
	for i, c := range cases {
		if got[i] != c.kind {
			t.Errorf("_ecdy_first_kind %q = %s, want %s", c.line, got[i], c.kind)
		}
		if w := classify.FirstWord(c.line); w != c.word {
			t.Errorf("classify.FirstWord(%q) = %q, want %q", c.line, w, c.word)
		}
	}
}

// TestMinVersion: on a zsh older than 5.8 the plugin does not load, so Enter
// stays vanilla. $ZSH_VERSION is faked, which is all is-at-least looks at.
func TestMinVersion(t *testing.T) { forEachZsh(t, testMinVersion) }

func testMinVersion(t *testing.T, zsh string) {
	for version, want := range map[string]string{"5.7.1": "builtin", "5.8": "user:_ecdy_accept_line"} {
		script := "ZSH_VERSION=" + version + "; source " + shellQuote(sourceFile(t)) + "; print -r -- $widgets[accept-line]"
		cmd := exec.Command(zsh, "-f", "-i", "-c", script)
		cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + filepath.Dir(zsh)}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("zsh: %v\n%s", err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("zsh %s: accept-line is %q, want %q", version, got, want)
		}
	}
}
