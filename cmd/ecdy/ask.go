package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
	"github.com/Raimguzhinov/ecdy/internal/config"
	"github.com/Raimguzhinov/ecdy/internal/render"
	"github.com/Raimguzhinov/ecdy/internal/tty"
)

// killGrace is how long an agent gets to exit after SIGTERM.
const killGrace = 2 * time.Second

func newAskCmd() *cobra.Command {
	var (
		agentName string
		verbose   bool
	)
	cmd := &cobra.Command{
		Use:   "ask [--agent NAME] [-v] [--] PROMPT",
		Short: "Send a prompt to the agent",
		Long: "ask starts the agent, sends PROMPT in a new session, streams the reply\n" +
			"and exits. The zsh plugin runs it for lines the classifier recognizes as\n" +
			"prompts.\n\n" +
			"Every permission request of the agent is shown as a dialog on the\n" +
			"terminal. Ctrl+C cancels the turn; a second Ctrl+C stops the agent.\n\n" +
			"Exit status: 0 the turn completed, 1 agent or protocol error, 130 cancelled.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAsk(cmd, agentName, verbose, strings.Join(args, " "))
		},
	}
	cmd.Flags().StringVarP(&agentName, "agent", "a", "", "agent from the config (default: default_agent)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also show thoughts, plans, tool output and the agent's stderr")
	return cmd
}

func runAsk(cmd *cobra.Command, agentName string, verbose bool, prompt string) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	name, agent, err := cfg.Agent(agentName)
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("current directory: %w", err)
	}
	logger, closeLog := newLogger()
	defer closeLog()

	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	onTerm := isTerminal(stdout)
	color := onTerm && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	r := render.New(stdout, render.Options{Color: color, Rewrite: onTerm, Verbose: verbose})
	h := &askHandler{r: r, stderr: stderr, color: color}

	opts := acpclient.Options{
		Command:       agent.Command,
		Dir:           dir,
		Logger:        logger,
		KillGrace:     killGrace,
		ClientVersion: resolveVersion(),
	}
	if verbose {
		opts.Stderr = stderr
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	var (
		mu   sync.Mutex
		conn *acpclient.Conn
	)
	// The agent runs in its own process group, so only ecdy gets the
	// terminal's signals. First Ctrl+C: session/cancel. Second Ctrl+C,
	// SIGTERM, SIGHUP: stop the agent now.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	stopSigs := make(chan struct{})
	defer close(stopSigs)
	go func() {
		interrupts := 0
		for {
			select {
			case <-stopSigs:
				return
			case sig := <-sigs:
				if sig == os.Interrupt {
					interrupts++
				}
				if sig == os.Interrupt && interrupts == 1 {
					cancel() // first: it also closes an open dialog, which holds r
					r.Notice("^C cancelling… (^C again to stop the agent)")
					continue
				}
				cancel()
				mu.Lock()
				if conn != nil {
					conn.Kill()
				}
				mu.Unlock()
			}
		}
	}()

	c, err := acpclient.Start(ctx, opts, h)
	if err != nil {
		if ctx.Err() != nil {
			return exitCode(130, nil)
		}
		return agentError(name, err)
	}
	mu.Lock()
	conn = c
	mu.Unlock()
	defer c.Close()

	stop, err := c.Prompt(ctx, prompt)
	r.Finish()
	if err != nil {
		if ctx.Err() != nil {
			// Stopped by the second Ctrl+C.
			return exitCode(130, nil)
		}
		return agentError(name, err)
	}
	switch stop {
	case acp.StopReasonEndTurn:
		return nil
	case acp.StopReasonCancelled:
		r.Notice("cancelled")
		return exitCode(130, nil)
	case acp.StopReasonMaxTokens:
		r.Notice("stopped: the agent reached its token limit")
	case acp.StopReasonMaxTurnRequests:
		r.Notice("stopped: the agent reached its request limit for one turn")
	case acp.StopReasonRefusal:
		r.Notice("stopped: the agent refused to continue")
	default:
		r.Notice("stopped: %s", stop)
	}
	return nil
}

// askHandler renders the turn and asks for permissions on the terminal.
type askHandler struct {
	r      *render.Renderer
	stderr io.Writer
	color  bool
}

func (h *askHandler) SessionUpdate(_ context.Context, u acp.SessionUpdate) { h.r.Update(u) }

func (h *askHandler) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	// One dialog at a time, and no agent output under it.
	resume := h.r.Pause()
	defer resume()
	t, err := tty.Open()
	if err != nil {
		title := ""
		if req.ToolCall.Title != nil {
			title = *req.ToolCall.Title
		}
		_, _ = fmt.Fprintf(h.stderr, "ecdy: no terminal to ask for permission, rejected: %s\n", title)
		return tty.Reject(req)
	}
	defer func() { _ = t.Close() }()
	outcome, err := tty.Permission(ctx, t, t, req, tty.Style{Color: h.color})
	if errors.Is(err, tty.ErrInterrupted) {
		return outcome, acpclient.ErrInterrupted
	}
	return outcome, err
}

// agentError explains a failed agent: its exit status and the end of its
// stderr, or how to log in.
func agentError(name string, err error) error {
	var ae *acpclient.AuthError
	if errors.As(err, &ae) {
		// Logging in through ACP (authenticate) may open a browser or need
		// its own terminal UI; the agent's CLI does it better.
		return fmt.Errorf("%s: %w\n  log in with the agent's own CLI, then try again", name, err)
	}
	var ee *acpclient.ExitError
	if errors.As(err, &ee) {
		msg := fmt.Sprintf("%s: %v", name, err)
		if tail := lastLines(ee.Stderr, 10); tail != "" {
			msg += "\n  " + strings.ReplaceAll(tail, "\n", "\n  ")
		}
		return errors.New(msg)
	}
	return fmt.Errorf("%s: %w", name, err)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
