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
)

// ecdyBin is the ecdy binary built once for all PTY tests.
var ecdyBin string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ecdy-shell-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
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
}

type zshTerm struct {
	*testutil.Term
	t    *testing.T
	home string // $HOME and the working directory
}

// startZsh starts `zsh -f -i` under a PTY in a temporary $HOME and loads the
// plugin by sending a `source` line, as a user's .zshrc would.
func startZsh(t *testing.T, o zshOpts) *zshTerm {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
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
		o.rc,
		o.load,
		o.after,
		`print -r -- loaded-$((40+2))`,
	}, "\n")
	rcPath := filepath.Join(home, "rc.zsh")
	if err := os.WriteFile(rcPath, []byte(rc+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(zsh, "-f", "-i")
	cmd.Dir = home
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + o.path,
		"TERM=xterm",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	z := &zshTerm{Term: testutil.StartTerm(t, cmd), t: t, home: home}
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

const askReply = "ecdy ask (no agent yet): "

func TestCommand(t *testing.T) {
	z := startZsh(t, zshOpts{})
	z.Run(`print -r -- cmd-$((6*7))`, "cmd-42")
	// A known command with a word argument still runs.
	z.Run(`ls -d rc.zsh && print -r -- ls-$((6*7))`, "ls-42")
}

func TestPrompt(t *testing.T) {
	z := startZsh(t, zshOpts{})
	z.Run(`explain this error`, askReply+"explain this error")
	// Nothing in the prompt is expanded by the shell.
	z.Run(`why does $HOME != "x" fail?! * 'q'`, askReply+`why does $HOME != "x" fail?! * 'q'`)
	// The ? prefix forces a prompt and is stripped.
	z.Run(`? print -r -- hi`, askReply+"print -r -- hi")
}

// TestForceCommand: Alt+Enter runs the line as a command, skipping the
// classifier.
func TestForceCommand(t *testing.T) {
	z := startZsh(t, zshOpts{})
	z.Send("explain this error" + altEnter)
	z.Expect("command not found: explain")
	z.ExpectPrompt()
}

func TestAskDialog(t *testing.T) {
	const dangerous = "rm everything in tmp except configs"
	z := startZsh(t, zshOpts{})
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

func TestAskTypo(t *testing.T) {
	z := startZsh(t, zshOpts{})
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
func TestFailOpen(t *testing.T) {
	dir := t.TempDir()
	scripts := map[string]string{
		"crash":   "#!/bin/sh\nexit 2\n",
		"slow":    "#!/bin/sh\nexec sleep 30\n",
		"garbage": "#!/bin/sh\nprintf 'prompt\\000'\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil { //nolint:gosec // test executables
			t.Fatal(err)
		}
	}
	plugin := sourceFile(t)
	tests := []struct {
		name string
		opts zshOpts
	}{
		// No ecdy in $PATH: the plugin is sourced directly.
		{"missing", zshOpts{load: "source " + plugin, path: "/usr/bin:/bin:" + filepath.Dir(zshPath(t))}},
		{"crash", zshOpts{after: "ECDY_BIN=" + filepath.Join(dir, "crash")}},
		{"slow", zshOpts{after: "ECDY_BIN=" + filepath.Join(dir, "slow") + " ECDY_CLASSIFY_TIMEOUT=0.2"}},
		{"garbage", zshOpts{after: "ECDY_BIN=" + filepath.Join(dir, "garbage")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			z := startZsh(t, tt.opts)
			z.Run("explain this error", "command not found: explain")
			z.Run(`print -r -- still-$((6*7))`, "still-42")
		})
	}
}

// TestHistory: the history gets the line the user typed, not the
// `ecdy ask -- ...` it was rewritten to.
func TestHistory(t *testing.T) {
	z := startZsh(t, zshOpts{})
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
func TestHistoryUp(t *testing.T) {
	// Ctrl+T saves the buffer to a file, to look at it without running it.
	z := startZsh(t, zshOpts{rc: `_save_buffer() { print -r -- $BUFFER > $HOME/buffer }; zle -N _save_buffer; bindkey '^T' _save_buffer`})
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
func TestCoexistence(t *testing.T) {
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
			z := startZsh(t, tt.opts)
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

func zshPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	return p
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestFirstKind checks that the plugin finds the same first word as
// classify.FirstWord and names its kind like `whence -w`.
func TestFirstKind(t *testing.T) {
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
	cmd := exec.Command(zshPath(t), "-f", "-i", "-c", script.String())
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin:" + filepath.Dir(zshPath(t))}
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
