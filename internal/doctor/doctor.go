// Package doctor checks that ecdy can work: the binary the plugin runs, the
// config, the agents' commands and logins, zsh, and the state of the shell
// the plugin is loaded in (keys and widgets other plugins may have taken).
//
// The checks are pure: everything they look at comes in Env, so that the
// command gathers it and the tests fake it.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
	"github.com/Raimguzhinov/ecdy/internal/config"
)

// Status of a check.
type Status int

const (
	OK Status = iota
	Warn
	Fail
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case Warn:
		return "warning"
	}
	return "failed"
}

// Result of one check.
type Result struct {
	Name   string
	Status Status
	Detail string
	Hint   string // what to do about it, if anything
}

// Env is what the checks look at.
type Env struct {
	Self, Version string // this executable and its version
	LookPath      func(string) (string, error)
	SameFile      func(a, b string) bool

	ConfigPath    string
	Config        config.Config
	ConfigErr     error
	ConfigMissing bool // no config file: the built-in presets

	// ZshVersion runs `zsh` from $PATH; used when Shell is empty.
	ZshVersion func() (string, error)
	// Shell is what the plugin reports about the shell `ecdy doctor` was
	// typed in (ECDY_DOCTOR_ZSH, key=value lines); empty when not run
	// through the plugin.
	Shell string

	Getenv   func(string) string
	Home     string
	ReadFile func(string) ([]byte, error)
}

// minZsh is the oldest zsh the plugin loads in.
var minZsh = []int{5, 8}

// Check runs every check that applies.
func Check(e Env) []Result {
	shell := parseShell(e.Shell)
	rs := []Result{checkBinary(e, shell), checkConfig(e)}
	if e.ConfigErr == nil {
		rs = append(rs, checkAgents(e)...)
	}
	if shell == nil {
		rs = append(rs, checkZsh(e), checkPlugin(e))
		return rs
	}
	rs = append(rs,
		Result{Name: "zsh", Status: OK, Detail: shell["zsh"] + ", keymap " + shell["keymap"]},
		Result{Name: "plugin", Status: OK, Detail: "loaded in this shell"},
		checkEnter(shell), checkAltEnter(shell), checkQuestion(shell), checkIndicator(shell))
	if shell["atuin"] == "1" {
		rs = append(rs, checkAtuin(e))
	}
	if v := shell["fzf-tab-accept-line"]; v != "" {
		rs = append(rs, Result{Name: "fzf-tab", Status: Fail,
			Detail: fmt.Sprintf("its accept-line key (%s) runs the line without ecdy's classification: a prompt could run as a command", v),
			Hint:   "remove `zstyle ':fzf-tab:*' accept-line …`, then accept the completion and press Enter"})
	}
	return rs
}

func parseShell(s string) map[string]string {
	if s == "" {
		return nil
	}
	m := map[string]string{}
	for l := range strings.SplitSeq(s, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

// checkBinary: the plugin runs ECDY_BIN, or `ecdy` from $PATH; it should be
// this ecdy.
func checkBinary(e Env, shell map[string]string) Result {
	r := Result{Name: "ecdy", Detail: e.Version + " at " + e.Self}
	if bin := shell["bin"]; bin != "" {
		r.Detail += ", run by the plugin as ECDY_BIN"
		if !e.SameFile(bin, e.Self) {
			r.Status, r.Detail = Warn, fmt.Sprintf("the plugin runs ECDY_BIN=%s, not this ecdy (%s)", bin, e.Self)
		}
		return r
	}
	p, err := e.LookPath("ecdy")
	switch {
	case err != nil:
		r.Status = Warn
		r.Detail += "; ecdy is not on $PATH, so the plugin cannot run it"
		r.Hint = "put ecdy on $PATH, or set ECDY_BIN before loading the plugin"
	case !e.SameFile(p, e.Self):
		r.Status = Warn
		r.Detail += "; the plugin runs " + p + ", found first on $PATH"
		r.Hint = "remove the other ecdy or reorder $PATH"
	}
	return r
}

func checkConfig(e Env) Result {
	switch {
	case e.ConfigErr != nil:
		return Result{Name: "config", Status: Fail, Detail: e.ConfigErr.Error()}
	case e.ConfigMissing:
		return Result{Name: "config", Detail: e.ConfigPath + " not found: built-in presets, default agent " + e.Config.DefaultAgent}
	}
	return Result{Name: "config", Detail: e.ConfigPath + ", default agent " + e.Config.DefaultAgent}
}

// requires lists what an adapter runs besides its own command.
var requires = map[string]string{"pi-acp": "pi"}

// checkAgents: each agent's command can be found. A missing default agent
// fails: every prompt goes to it.
func checkAgents(e Env) []Result {
	var names []string
	for n := range e.Config.Agents {
		names = append(names, n)
	}
	slices.Sort(names)
	var rs []Result
	for _, n := range names {
		argv := e.Config.Agents[n].Command
		r := Result{Name: "agent " + n, Detail: strings.Join(argv, " ")}
		if n == e.Config.DefaultAgent {
			r.Detail += " (default)"
		}
		missing := ""
		if _, err := e.LookPath(argv[0]); err != nil {
			missing = argv[0]
		}
		for _, a := range argv[1:] {
			if need, ok := requires[a]; ok && missing == "" {
				if _, err := e.LookPath(need); err != nil {
					missing = need
				}
			}
		}
		if missing != "" {
			r.Status = Warn
			if n == e.Config.DefaultAgent {
				r.Status = Fail
			}
			r.Detail = fmt.Sprintf("`%s` not found on $PATH (%s)", missing, strings.Join(argv, " "))
			r.Hint = "install it, or choose another agent: `ecdy use NAME`, default_agent in the config"
		}
		rs = append(rs, r)
	}
	return rs
}

func checkZsh(e Env) Result {
	v, err := e.ZshVersion()
	if err != nil {
		return Result{Name: "zsh", Status: Fail, Detail: "cannot run zsh: " + err.Error()}
	}
	if !atLeast(v, minZsh) {
		return Result{Name: "zsh", Status: Fail, Detail: v + ": the plugin needs zsh 5.8 or newer and does not load"}
	}
	return Result{Name: "zsh", Detail: v}
}

func atLeast(v string, min []int) bool {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' })
	for i, m := range min {
		if i >= len(parts) {
			return false
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return false
		}
		if n != m {
			return n > m
		}
	}
	return true
}

func checkPlugin(e Env) Result {
	r := Result{Name: "plugin", Status: Warn}
	if e.Getenv("ECDY_SESSION") != "" {
		r.Detail = "loaded, but `ecdy doctor` did not come through it (typed as `command ecdy` or from a script), so the shell was not checked"
		r.Hint = "type `ecdy doctor` at the zsh prompt"
		return r
	}
	r.Detail = "not run from a zsh with the ecdy plugin, so the shell was not checked"
	r.Hint = "add `eval \"$(ecdy init zsh)\"` to ~/.zshrc, open a new shell and run `ecdy doctor` there"
	return r
}

// checkEnter: Enter must reach ecdy's accept-line. A plugin loaded later
// may wrap it (fine when it calls the widget it wrapped, as
// zsh-autosuggestions and zsh-syntax-highlighting do) or replace it.
func checkEnter(s map[string]string) Result {
	r := Result{Name: "Enter"}
	switch w := s["accept-line"]; {
	case w == "user:_ecdy_accept_line":
		r.Detail = "classified by ecdy"
	case strings.HasPrefix(w, "user:"):
		r.Detail = "accept-line is wrapped by " + strings.TrimPrefix(w, "user:") + ", loaded after ecdy; ecdy classifies the line if that widget calls the one it wrapped"
	default:
		r.Status = Fail
		r.Detail = "accept-line is " + w + ", not ecdy's widget: lines run without classification"
		r.Hint = "something reset accept-line after ecdy loaded (`zle -A .accept-line accept-line`?); load ecdy last"
		return r
	}
	if w := s["enter"]; w != "accept-line" {
		r.Status = Fail
		r.Detail = "Enter runs " + w + ", not accept-line: ecdy does not see the line"
		r.Hint = "bind it back: `bindkey '^M' accept-line`"
	}
	return r
}

func checkAltEnter(s map[string]string) Result {
	if w := s["alt-enter"]; w != "ecdy-force-command" {
		return Result{Name: "Alt+Enter", Status: Warn,
			Detail: "runs " + w + ", not ecdy-force-command: a line cannot be forced to run as a command",
			Hint:   "bind it after the other plugins: `bindkey '^[^M' ecdy-force-command`"}
	}
	return Result{Name: "Alt+Enter", Detail: "runs the line as a command"}
}

// checkQuestion: the ? prefix is typed as a character.
func checkQuestion(s map[string]string) Result {
	switch w := s["question"]; {
	case w == "self-insert" || w == "self-insert-unmeta":
		return Result{Name: "? prefix", Detail: "sends the line to the agent"}
	case strings.Contains(w, "atuin"):
		return Result{Name: "? prefix", Status: Warn,
			Detail: "? on an empty line starts atuin's AI (" + w + ") instead of ecdy's prompt prefix",
			Hint:   "use `eval \"$(atuin init zsh --disable-ai)\"`"}
	default:
		return Result{Name: "? prefix", Status: Warn,
			Detail: "? runs " + w + ", so the prompt prefix cannot be typed",
			Hint:   "bind it back: `bindkey '?' self-insert`"}
	}
}

func checkIndicator(s map[string]string) Result {
	mode := s["indicator"]
	if mode == "off" {
		return Result{Name: "indicator", Detail: "off (ECDY_INDICATOR=off)"}
	}
	if s["pre-redraw"] == "" {
		return Result{Name: "indicator", Status: Warn,
			Detail: "zle-line-pre-redraw is not defined: a plugin loaded later removed ecdy's hook, no indicator is shown",
			Hint:   "load ecdy after that plugin, or set ECDY_INDICATOR=off"}
	}
	return Result{Name: "indicator", Detail: "shown as " + mode + " (ECDY_INDICATOR)"}
}

// checkAtuin: atuin records the rewritten `ecdy ask -- '…'` line of every
// prompt unless its history_filter drops it.
func checkAtuin(e Env) Result {
	dir := e.Getenv("ATUIN_CONFIG_DIR")
	if dir == "" {
		base := e.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(base) {
			base = filepath.Join(e.Home, ".config")
		}
		dir = filepath.Join(base, "atuin")
	}
	path := filepath.Join(dir, "config.toml")
	data, _ := e.ReadFile(path)
	if strings.Contains(string(data), "ecdy ask") {
		return Result{Name: "atuin", Detail: "its history_filter keeps ecdy's rewritten lines out (" + path + ")"}
	}
	return Result{Name: "atuin", Status: Warn,
		Detail: "atuin records every prompt as `ecdy ask -- '…'` (zsh's own history gets the typed line)",
		Hint:   `add history_filter = ["^ecdy ask -- "] to ` + path}
}

// Login turns the outcome of starting an agent (initialize and session/new)
// into a result.
func Login(name string, err error, timeout time.Duration) Result {
	r := Result{Name: "login " + name}
	var auth *acpclient.AuthError
	var exit *acpclient.ExitError
	switch {
	case err == nil:
		r.Detail = "started, session created"
	case errors.As(err, &auth):
		r.Status, r.Detail = Fail, auth.Error()
		r.Hint = "log in with the agent's own CLI; ecdy uses that login"
	case errors.Is(err, context.DeadlineExceeded):
		r.Status, r.Detail = Fail, fmt.Sprintf("no answer within %s", timeout)
		r.Hint = "npx may still be downloading the agent: try again, or with a longer --timeout"
	case errors.As(err, &exit):
		r.Status, r.Detail = Fail, err.Error()
		if s := strings.TrimSpace(exit.Stderr); s != "" {
			r.Detail += ": " + lastLine(s)
		}
	case errors.Is(err, exec.ErrNotFound):
		r.Status, r.Detail = Fail, "command not found: "+err.Error()
	default:
		r.Status, r.Detail = Fail, err.Error()
	}
	return r
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Format writes the results, one line each and an indented hint.
func Format(w io.Writer, rs []Result, color bool) {
	marks := map[Status]string{OK: "✓", Warn: "!", Fail: "✗"}
	colors := map[Status]string{OK: "\x1b[32m", Warn: "\x1b[33m", Fail: "\x1b[31m"}
	for _, r := range rs {
		mark := marks[r.Status]
		if color {
			mark = colors[r.Status] + mark + "\x1b[0m"
		}
		_, _ = fmt.Fprintf(w, "%s %s: %s\n", mark, r.Name, r.Detail)
		if r.Hint != "" {
			_, _ = fmt.Fprintf(w, "  → %s\n", r.Hint)
		}
	}
}
