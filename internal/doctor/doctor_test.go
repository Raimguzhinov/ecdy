package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
	"github.com/Raimguzhinov/ecdy/internal/config"
)

// healthy is a probe of a zsh with the plugin and nothing in its way.
const healthy = "zsh=5.9\nbin=\nkeymap=emacs\naccept-line=user:_ecdy_accept_line\nenter=accept-line\n" +
	"alt-enter=ecdy-force-command\nquestion=self-insert\npre-redraw=user:azhw:zle-line-pre-redraw\n" +
	"indicator=rprompt\natuin=0\nfzf-tab-accept-line="

func env() Env {
	return Env{
		Self:    "/opt/ecdy/bin/ecdy",
		Version: "v1",
		LookPath: func(name string) (string, error) {
			switch name {
			case "ecdy":
				return "/opt/ecdy/bin/ecdy", nil
			case "npx", "zsh":
				return "/usr/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		SameFile:   func(a, b string) bool { return a == b },
		ConfigPath: "/home/u/.config/ecdy/config.toml",
		Config: config.Config{DefaultAgent: "claude", Agents: map[string]config.Agent{
			"claude": {Command: []string{"npx", "-y", "@agentclientprotocol/claude-agent-acp"}},
		}},
		ZshVersion: func() (string, error) { return "5.9", nil },
		Shell:      healthy,
		Getenv:     func(string) string { return "" },
		Home:       "/home/u",
		ReadFile:   func(string) ([]byte, error) { return nil, fs.ErrNotExist },
	}
}

// find returns the result named name.
func find(t *testing.T, rs []Result, name string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %q in %+v", name, rs)
	return Result{}
}

func TestHealthy(t *testing.T) {
	for _, r := range Check(env()) {
		if r.Status != OK {
			t.Errorf("%s: %s %s (%s)", r.Name, r.Status, r.Detail, r.Hint)
		}
	}
}

func TestCheck(t *testing.T) {
	probe := func(kv ...string) string {
		s := healthy
		for i := 0; i < len(kv); i += 2 {
			lines := strings.Split(s, "\n")
			for j, l := range lines {
				if strings.HasPrefix(l, kv[i]+"=") {
					lines[j] = kv[i] + "=" + kv[i+1]
				}
			}
			s = strings.Join(lines, "\n")
		}
		return s
	}
	tests := []struct {
		name   string
		change func(e *Env)
		check  string // the result's name
		status Status
		want   string // in the detail or the hint
	}{
		{"ecdy not on PATH", func(e *Env) {
			look := e.LookPath
			e.LookPath = func(n string) (string, error) {
				if n == "ecdy" {
					return "", exec.ErrNotFound
				}
				return look(n)
			}
		}, "ecdy", Warn, "not on $PATH"},
		{"another ecdy first on PATH", func(e *Env) {
			look := e.LookPath
			e.LookPath = func(n string) (string, error) {
				if n == "ecdy" {
					return "/usr/local/bin/ecdy", nil
				}
				return look(n)
			}
		}, "ecdy", Warn, "/usr/local/bin/ecdy"},
		{"ECDY_BIN is what the plugin runs", func(e *Env) {
			e.Shell = probe("bin", "/opt/ecdy/bin/ecdy")
			e.LookPath = func(n string) (string, error) {
				if n == "ecdy" {
					return "", exec.ErrNotFound
				}
				return "/usr/bin/" + n, nil
			}
		}, "ecdy", OK, "ECDY_BIN"},
		{"config error", func(e *Env) { e.ConfigErr = errors.New("parse: bad key") }, "config", Fail, "bad key"},
		{"no config file", func(e *Env) { e.ConfigMissing = true }, "config", OK, "built-in presets"},
		{"default agent missing", func(e *Env) {
			e.Config.Agents["claude"] = config.Agent{Command: []string{"nope"}}
		}, "agent claude", Fail, "nope"},
		{"other agent missing", func(e *Env) {
			e.Config.Agents["gemini"] = config.Agent{Command: []string{"gemini", "--acp"}}
		}, "agent gemini", Warn, "gemini"},
		{"pi needs pi", func(e *Env) {
			e.Config.Agents["pi"] = config.Agent{Command: []string{"npx", "-y", "pi-acp"}}
		}, "agent pi", Warn, "`pi`"},
		{"not run through the plugin", func(e *Env) { e.Shell = "" }, "plugin", Warn, "ecdy init zsh"},
		{"plugin loaded, but ecdy run directly", func(e *Env) {
			e.Shell = ""
			e.Getenv = func(k string) string {
				if k == "ECDY_SESSION" {
					return "1-2"
				}
				return ""
			}
		}, "plugin", Warn, "command ecdy"},
		{"zsh too old", func(e *Env) {
			e.Shell = ""
			e.ZshVersion = func() (string, error) { return "5.7.1", nil }
		}, "zsh", Fail, "5.8"},
		{"no zsh", func(e *Env) {
			e.Shell = ""
			e.ZshVersion = func() (string, error) { return "", exec.ErrNotFound }
		}, "zsh", Fail, "zsh"},
		{"accept-line replaced", func(e *Env) { e.Shell = probe("accept-line", "builtin") }, "Enter", Fail, "accept-line"},
		{"accept-line wrapped", func(e *Env) {
			e.Shell = probe("accept-line", "user:_zsh_autosuggest_bound_1_accept-line")
		}, "Enter", OK, "_zsh_autosuggest_bound_1_accept-line"},
		{"Enter bound elsewhere", func(e *Env) { e.Shell = probe("enter", "my-widget") }, "Enter", Fail, "my-widget"},
		{"Alt+Enter bound elsewhere", func(e *Env) { e.Shell = probe("alt-enter", "undefined-key") }, "Alt+Enter", Warn, "undefined-key"},
		{"atuin AI takes ?", func(e *Env) { e.Shell = probe("question", "self-atuin-ai-question-mark") }, "? prefix", Warn, "--disable-ai"},
		{"? bound elsewhere", func(e *Env) { e.Shell = probe("question", "my-help") }, "? prefix", Warn, "my-help"},
		{"no pre-redraw hook", func(e *Env) { e.Shell = probe("pre-redraw", "") }, "indicator", Warn, "zle-line-pre-redraw"},
		{"indicator off", func(e *Env) { e.Shell = probe("indicator", "off", "pre-redraw", "") }, "indicator", OK, "off"},
		{"atuin without the filter", func(e *Env) { e.Shell = probe("atuin", "1") }, "atuin", Warn, `history_filter = ["^ecdy ask -- "]`},
		{"atuin with the filter", func(e *Env) {
			e.Shell = probe("atuin", "1")
			e.ReadFile = func(p string) ([]byte, error) {
				if p == "/home/u/.config/atuin/config.toml" {
					return []byte("history_filter = [\n  \"^ecdy ask -- \",\n]\n"), nil
				}
				return nil, fs.ErrNotExist
			}
		}, "atuin", OK, "filter"},
		{"atuin config dir from the environment", func(e *Env) {
			e.Shell = probe("atuin", "1")
			e.Getenv = func(k string) string {
				if k == "ATUIN_CONFIG_DIR" {
					return "/etc/atuin"
				}
				return ""
			}
			e.ReadFile = func(p string) ([]byte, error) {
				if p == "/etc/atuin/config.toml" {
					return []byte(`history_filter = ["^ecdy ask -- "]`), nil
				}
				return nil, fs.ErrNotExist
			}
		}, "atuin", OK, "filter"},
		{"fzf-tab accept-line", func(e *Env) { e.Shell = probe("fzf-tab-accept-line", "enter") }, "fzf-tab", Fail, "classif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := env()
			e.Config.Agents = map[string]config.Agent{"claude": e.Config.Agents["claude"]}
			tt.change(&e)
			r := find(t, Check(e), tt.check)
			if r.Status != tt.status || !strings.Contains(r.Detail+" "+r.Hint, tt.want) {
				t.Errorf("got %s %q (hint %q), want %s with %q", r.Status, r.Detail, r.Hint, tt.status, tt.want)
			}
		})
	}
}

// Checks that do not apply are left out: no atuin, no fzf-tab.
func TestOnlyWhatApplies(t *testing.T) {
	for _, r := range Check(env()) {
		if r.Name == "atuin" || r.Name == "fzf-tab" {
			t.Errorf("unexpected %s", r.Name)
		}
	}
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status Status
		want   string
	}{
		{"ok", nil, OK, "session created"},
		{"auth", &acpclient.AuthError{Methods: []acp.AuthMethod{{Agent: &acp.AuthMethodAgent{Name: "Log in with Claude"}}}}, Fail, "Log in with Claude"},
		{"crash", &acpclient.ExitError{Err: errors.New("exit status 1"), Stderr: "boom\n"}, Fail, "boom"},
		{"timeout", context.DeadlineExceeded, Fail, "no answer within 1m0s"},
		{"not found", exec.ErrNotFound, Fail, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Login("claude", tt.err, time.Minute)
			if r.Name != "login claude" || r.Status != tt.status || !strings.Contains(r.Detail+" "+r.Hint, tt.want) {
				t.Errorf("got %+v, want %s with %q", r, tt.status, tt.want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	var b strings.Builder
	Format(&b, []Result{
		{Name: "ecdy", Status: OK, Detail: "v1 at /opt/ecdy"},
		{Name: "? prefix", Status: Warn, Detail: "? runs x", Hint: "do y"},
		{Name: "zsh", Status: Fail, Detail: "5.7"},
	}, false)
	want := "✓ ecdy: v1 at /opt/ecdy\n! ? prefix: ? runs x\n  → do y\n✗ zsh: 5.7\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
