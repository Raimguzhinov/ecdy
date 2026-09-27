package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// newLogger returns the logger for $XDG_STATE_HOME/ecdy/ecdy.log at the level
// named by ECDY_LOG (debug, info, warn, error), or nil when ECDY_LOG is unset
// or the file cannot be opened. Logs never go to the terminal
// (AGENTS.md, section 7).
func newLogger() (*slog.Logger, func()) {
	nop := func() {}
	name := strings.ToLower(os.Getenv("ECDY_LOG"))
	if name == "" {
		return nil, nop
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		level = slog.LevelInfo
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nop
		}
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "ecdy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nop
	}
	f, err := os.OpenFile(filepath.Join(dir, "ecdy.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nop
	}
	l := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})).With("pid", os.Getpid())
	return l, func() { _ = f.Close() }
}
