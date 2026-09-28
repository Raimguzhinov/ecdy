package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	name, a, err := c.Agent("")
	if err != nil || name != "claude" || a.Command[0] != "npx" {
		t.Fatalf("default agent = %q %v, %v", name, a, err)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(write(t, `
default_agent = "mine"

[agents.mine]
command = ["my-agent", "--acp"]

[agents.claude]
command = ["claude-agent-acp"]
`))
	if err != nil {
		t.Fatal(err)
	}
	name, a, err := c.Agent("")
	if err != nil || name != "mine" || !slices.Equal(a.Command, []string{"my-agent", "--acp"}) {
		t.Fatalf("default agent = %q %v, %v", name, a, err)
	}
	if _, a, _ := c.Agent("claude"); !slices.Equal(a.Command, []string{"claude-agent-acp"}) {
		t.Errorf("claude = %v, want the override", a.Command)
	}
	if _, a, _ := c.Agent("codex"); len(a.Command) == 0 {
		t.Error("codex preset lost")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]string{
		"syntax":        `default_agent = `,
		"unknown field": "default_agnet = \"x\"\n",
		"empty command": "[agents.x]\ncommand = []\n",
		"empty argv0":   "[agents.x]\ncommand = [\"\"]\n",
		"idle number":   "idle_timeout = 30\n",
		"idle unit":     "idle_timeout = \"30\"\n",
		"idle negative": "idle_timeout = \"-1m\"\n",
		"ctx negative":  "[context]\ncommands = -1\n",
		"ctx huge":      "[context]\ncommands = 1001\n",
		"ctx string":    "[context]\ncommands = \"20\"\n",
		"ctx unknown":   "[context]\noutput = true\n",
	}
	for name, content := range tests {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestUnknownAgent(t *testing.T) {
	_, _, err := Default().Agent("nope")
	if err == nil || !strings.Contains(err.Error(), "claude, codex") {
		t.Fatalf("err = %v, want a list of known agents", err)
	}
}

func TestPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if p, _ := Path(); p != "/xdg/ecdy/config.toml" {
		t.Errorf("Path() = %q", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("HOME", "/home/u")
	if p, _ := Path(); p != "/home/u/.config/ecdy/config.toml" {
		t.Errorf("Path() with relative XDG_CONFIG_HOME = %q", p)
	}
}

func TestIdleTimeout(t *testing.T) {
	if d := Default().IdleTimeout; d != 30*time.Minute {
		t.Errorf("default idle timeout = %v", d)
	}
	for text, want := range map[string]time.Duration{"0s": 0, "90s": 90 * time.Second, "2h": 2 * time.Hour} {
		c, err := Load(write(t, "idle_timeout = \""+text+"\"\n"))
		if err != nil || c.IdleTimeout != want {
			t.Errorf("idle_timeout = %q: %v, %v; want %v", text, c.IdleTimeout, err, want)
		}
	}
}

func TestContextCommands(t *testing.T) {
	if n := Default().ContextCommands; n != 20 {
		t.Errorf("default context.commands = %d", n)
	}
	for text, want := range map[string]int{"": 20, "[context]\n": 20, "[context]\ncommands = 0\n": 0, "[context]\ncommands = 5\n": 5} {
		c, err := Load(write(t, text))
		if err != nil || c.ContextCommands != want {
			t.Errorf("%q: %d, %v; want %d", text, c.ContextCommands, err, want)
		}
	}
}
