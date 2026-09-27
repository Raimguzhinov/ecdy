package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Raimguzhinov/ecdy/internal/config"
	"github.com/Raimguzhinov/ecdy/internal/daemon"
)

// The zsh plugin exports these (docs/adr/0003-session-daemon.md).
const (
	envSession  = "ECDY_SESSION"   // the shell session's id
	envShellPid = "ECDY_SHELL_PID" // the shell's pid: the daemon stops when it is gone
)

// stopTimeout bounds `ecdy daemon stop`: the daemon gives each agent
// killGrace after SIGTERM.
const stopTimeout = killGrace + 3*time.Second

func loadConfig() (config.Config, error) {
	path, err := config.Path()
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(path)
}

// serverOptions configure the server that runs agents, in a daemon or in
// `ecdy ask` itself; statePath is the session's state file, if any.
func serverOptions(logger *slog.Logger, statePath string) daemon.Options {
	o := daemon.Options{
		Config:        loadConfig,
		Logger:        logger,
		KillGrace:     killGrace,
		CancelGrace:   cancelGrace,
		ClientVersion: resolveVersion(),
	}
	if statePath != "" {
		o.State = func() (daemon.State, error) { return daemon.LoadState(statePath) }
	}
	return o
}

// daemonCommand is `ecdy daemon` for the session in the environment.
func daemonCommand() *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exec.Command(exe, "daemon", "--ready-fd", strconv.Itoa(daemon.ReadyFD)) //nolint:gosec // ecdy itself
}

// sessionPaths returns the files of the shell session in the environment.
func sessionPaths() (daemon.Paths, error) {
	session := os.Getenv(envSession)
	if session == "" {
		return daemon.Paths{}, errors.New(envSession + " is not set: run this in a shell with the ecdy plugin (eval \"$(ecdy init zsh)\")")
	}
	return daemon.SessionPaths(session)
}

func newDaemonCmd() *cobra.Command {
	var readyFD int
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the agent daemon of this shell session",
		Long: "daemon keeps the agents of one shell session (ECDY_SESSION) running, so\n" +
			"that the conversation continues across prompts. `ecdy ask` starts it on the\n" +
			"first prompt; it stops when the shell exits or after idle_timeout\n" +
			"(config.toml, default 30m) without prompts.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ready io.WriteCloser
			if readyFD > 0 {
				ready = os.NewFile(uintptr(readyFD), "ready")
			}
			fail := func(err error) error {
				if ready != nil {
					_, _ = io.WriteString(ready, "error: "+err.Error()+"\n")
					_ = ready.Close()
				}
				return err
			}
			p, err := sessionPaths()
			if err != nil {
				return fail(err)
			}
			// A broken config is reported at every prompt; the daemon itself
			// runs with the defaults.
			cfg, err := loadConfig()
			if err != nil {
				cfg = config.Default()
			}
			shellPid, _ := strconv.Atoi(os.Getenv(envShellPid))
			logger, closeLog := newLogger()
			defer closeLog()
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
			defer stop()
			return daemon.Run(ctx, daemon.RunOptions{
				Paths:       p,
				ShellPid:    shellPid,
				IdleTimeout: cfg.IdleTimeout,
				Ready:       ready,
				Server:      serverOptions(logger, p.State),
			})
		},
	}
	cmd.Flags().IntVar(&readyFD, "ready-fd", 0, "report readiness on this file descriptor")
	_ = cmd.Flags().MarkHidden("ready-fd")
	cmd.AddCommand(newDaemonStopCmd(), newDaemonStatusCmd())
	return cmd
}

func newDaemonStopCmd() *cobra.Command {
	var noWait, endSession bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon of this shell session and its agents",
		Long: "stop ends the conversations of this shell session: the next prompt starts\n" +
			"the agent again. It waits until the agents are gone unless --no-wait.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := sessionPaths()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), stopTimeout)
			defer cancel()
			running, err := daemon.Stop(ctx, p.Socket, !noWait)
			if endSession {
				_ = os.Remove(p.State)
			}
			if err != nil {
				return err
			}
			if !running {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no daemon is running for this shell session")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "do not wait for the agents to exit")
	cmd.Flags().BoolVar(&endSession, "end-session", false, "also forget the session's agent (the plugin's zshexit hook)")
	_ = cmd.Flags().MarkHidden("end-session")
	return cmd
}

func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the daemon of this shell session and its agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := sessionPaths()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			st, running, err := daemon.GetStatus(ctx, p.Socket)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if !running {
				_, _ = fmt.Fprintf(w, "session %s: no daemon (agent: %s)\n", p.Session, currentAgent(p))
				return nil
			}
			_, _ = fmt.Fprintf(w, "session %s: daemon pid %d, agent %s\n", p.Session, st.Pid, st.Agent)
			for _, a := range st.Agents {
				busy := ""
				if a.Busy {
					busy = ", busy"
				}
				_, _ = fmt.Fprintf(w, "  %s: pid %d, session %s, cwd %s%s\n", a.Name, a.Pid, a.Session, a.Cwd, busy)
			}
			return nil
		},
	}
}

// currentAgent is the session's agent: chosen with `ecdy use`, or the
// config's default.
func currentAgent(p daemon.Paths) string {
	if st, err := daemon.LoadState(p.State); err == nil && st.Agent != "" {
		return st.Agent
	}
	if cfg, err := loadConfig(); err == nil {
		return cfg.DefaultAgent
	}
	return config.Default().DefaultAgent
}
