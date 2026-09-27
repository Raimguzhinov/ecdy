package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
