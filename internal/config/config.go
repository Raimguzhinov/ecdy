// Package config loads ~/.config/ecdy/config.toml: which ACP agents exist and
// which one is the default.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Agent is how to launch one ACP agent.
type Agent struct {
	// Command is the agent's argv; the agent speaks ACP on its stdin and
	// stdout.
	Command []string `toml:"command"`
}

// Config is the user's configuration.
type Config struct {
	DefaultAgent string
	Agents       map[string]Agent
	// IdleTimeout is how long a shell session's daemon keeps its agents
	// without a prompt (docs/adr/0003-session-daemon.md).
	IdleTimeout time.Duration
	// ContextCommands is how many recent commands go to the agent with a
	// prompt; 0 sends none (docs/adr/0004-session-context.md).
	ContextCommands int
}

// file is the layout of config.toml.
type file struct {
	DefaultAgent string           `toml:"default_agent"`
	IdleTimeout  string           `toml:"idle_timeout"`
	Agents       map[string]Agent `toml:"agents"`
	Context      struct {
		Commands *int `toml:"commands"`
	} `toml:"context"`
}

// maxContextCommands bounds context.commands: the context block has a size
// limit of its own, so more would never be sent anyway.
const maxContextCommands = 1000

// Default returns the built-in presets. Launch commands are taken from the
// ACP Registry (https://github.com/agentclientprotocol/registry, the
// agent.json of each agent), checked on 2026-09-27.
func Default() Config {
	return Config{
		DefaultAgent: "claude",
		Agents: map[string]Agent{
			"claude":   {Command: []string{"npx", "-y", "@agentclientprotocol/claude-agent-acp"}},
			"codex":    {Command: []string{"npx", "-y", "@agentclientprotocol/codex-acp"}},
			"gemini":   {Command: []string{"gemini", "--acp"}},
			"opencode": {Command: []string{"opencode", "acp"}},
		},
		IdleTimeout:     30 * time.Minute,
		ContextCommands: 20,
	}
}

// Path returns the config file path: $XDG_CONFIG_HOME/ecdy/config.toml, or
// ~/.config/ecdy/config.toml when XDG_CONFIG_HOME is unset or relative.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find config directory: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "ecdy", "config.toml"), nil
}

// Load reads the config file at path on top of the presets: an agent defined
// in the file replaces the preset with the same name. A missing file is not
// an error.
func Load(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	var file file
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	if file.DefaultAgent != "" {
		c.DefaultAgent = file.DefaultAgent
	}
	if file.IdleTimeout != "" {
		d, err := time.ParseDuration(file.IdleTimeout)
		if err != nil || d < 0 {
			return c, fmt.Errorf("%s: idle_timeout: %q is not a duration like \"30m\"", path, file.IdleTimeout)
		}
		c.IdleTimeout = d
	}
	if n := file.Context.Commands; n != nil {
		if *n < 0 || *n > maxContextCommands {
			return c, fmt.Errorf("%s: context.commands: %d is not between 0 and %d", path, *n, maxContextCommands)
		}
		c.ContextCommands = *n
	}
	for name, a := range file.Agents {
		if len(a.Command) == 0 || a.Command[0] == "" {
			return c, fmt.Errorf("%s: agents.%s: command is empty", path, name)
		}
		c.Agents[name] = a
	}
	return c, nil
}

// Agent returns the agent called name, or the default agent if name is
// empty, with its resolved name.
func (c Config) Agent(name string) (string, Agent, error) {
	if name == "" {
		name = c.DefaultAgent
	}
	a, ok := c.Agents[name]
	if !ok {
		return name, Agent{}, fmt.Errorf("unknown agent %q (known: %s)", name, strings.Join(c.names(), ", "))
	}
	return name, a, nil
}

func (c Config) names() []string {
	names := make([]string, 0, len(c.Agents))
	for n := range c.Agents {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
