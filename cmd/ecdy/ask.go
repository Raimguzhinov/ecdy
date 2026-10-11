package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Raimguzhinov/ecdy/internal/daemon"
	"github.com/Raimguzhinov/ecdy/internal/render"
	"github.com/Raimguzhinov/ecdy/internal/tty"
)

// killGrace is how long an agent gets to exit after SIGTERM.
const killGrace = 2 * time.Second

// cancelGrace is how long a turn whose `ecdy ask` went away may take to
// end after session/cancel before the daemon kills its agent.
const cancelGrace = 5 * time.Second

func newAskCmd() *cobra.Command {
	var (
		agentName string
		verbose   bool
	)
	cmd := &cobra.Command{
		Use:   "ask [--agent NAME] [-v] [--] PROMPT",
		Short: "Send a prompt to the agent",
		Long: "ask sends PROMPT to the agent and streams the reply. The zsh plugin runs\n" +
			"it for lines the classifier recognizes as prompts.\n\n" +
			"In a shell with the ecdy plugin (ECDY_SESSION is set), the session's\n" +
			"daemon keeps the agent running and the conversation continues across\n" +
			"prompts; it is started on the first prompt. Without ECDY_SESSION, the\n" +
			"agent is started for this prompt only.\n\n" +
			"Every permission request of the agent is shown as a dialog on the\n" +
			"terminal. Ctrl+C cancels the turn; a second Ctrl+C stops the agent.\n\n" +
			"Exit status: 0 the turn completed, 1 agent or protocol error, 130 cancelled.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAsk(cmd, agentName, verbose, strings.Join(args, " "))
		},
	}
	cmd.Flags().StringVarP(&agentName, "agent", "a", "", "agent from the config (default: the session's agent, see `ecdy use`)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also show thoughts, plans, tool output and the agent's stderr")
	return cmd
}

func runAsk(cmd *cobra.Command, agentName string, verbose bool, prompt string) error {
	start := time.Now()
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("current directory: %w", err)
	}
	logger, closeLog := newLogger()
	defer closeLog()

	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	onTerm := isTerminal(stdout)
	color := onTerm && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	r := render.New(stdout, render.Options{Color: color, Rewrite: onTerm, Verbose: verbose, Markdown: color, Width: termWidth(stdout), Spacing: onTerm})
	defer r.Pad()
	h := &askHandler{r: r, stderr: stderr, color: color, agent: agentName, phased: make(chan struct{}, 1)}
	if onTerm {
		defer h.showWaiting(start, statusDelay())()
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	var (
		mu   sync.Mutex
		turn *daemon.Turn
	)
	// The agent runs in its own process group (and, with a daemon, in
	// another session), so only ecdy gets the terminal's signals. First
	// Ctrl+C: session/cancel. Second Ctrl+C, SIGTERM, SIGHUP: stop the agent
	// now.
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
				cancel() // it also closes an open dialog, which holds r
				mu.Lock()
				t := turn
				mu.Unlock()
				if sig == os.Interrupt && interrupts == 1 {
					if t != nil {
						t.Cancel()
					}
					r.Notice("^C cancelling… (^C again to stop the agent)")
					continue
				}
				if t != nil {
					t.Kill()
				}
			}
		}
	}()

	req := daemon.PromptRequest{Prompt: prompt, Agent: agentName, Cwd: dir, Verbose: verbose}
	var (
		stop acp.StopReason
		derr *daemon.Error
	)
	for attempt := 0; ; attempt++ {
		conn, done, err := connect(ctx, logger)
		if err != nil {
			if ctx.Err() != nil {
				return exitCode(130, nil)
			}
			return err
		}
		t, err := daemon.StartTurn(ctx, conn, req, h)
		if err != nil {
			done()
			return err
		}
		mu.Lock()
		turn = t
		mu.Unlock()
		if ctx.Err() != nil {
			t.Cancel() // Ctrl+C came before the turn existed
		}
		stop, err = t.Wait()
		done()
		r.Finish()
		if errors.As(err, &derr) && derr.Code == daemon.CodeVersion && attempt == 0 {
			// A daemon of another ecdy version: replace it.
			if err := restartDaemon(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				// Cancelled at start, or stopped by the second Ctrl+C.
				return exitCode(130, nil)
			}
			return err
		}
		break
	}
	switch stop {
	case acp.StopReasonEndTurn:
		if !h.replied() {
			// gemini-cli ends a blocked or empty response this way.
			r.Notice("%s returned an empty reply", h.agentName())
		}
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

// connect returns a connection to the session's daemon, starting it if
// needed, or without ECDY_SESSION to a server of this process that lives
// for one prompt. done must be called after the turn.
func connect(ctx context.Context, logger *slog.Logger) (conn net.Conn, done func(), err error) {
	session := os.Getenv(envSession)
	if session == "" {
		srv := daemon.NewServer(serverOptions(logger, daemon.Paths{}))
		client, server := net.Pipe()
		go srv.ServeConn(server)
		return client, func() {
			srv.Close()
			srv.Wait()
		}, nil
	}
	p, err := daemon.SessionPaths(session)
	if err != nil {
		return nil, nil, err
	}
	conn, err = daemon.Connect(ctx, p, daemonCommand)
	if err != nil {
		return nil, nil, err
	}
	return conn, func() {}, nil
}

func restartDaemon(ctx context.Context) error {
	p, err := daemon.SessionPaths(os.Getenv(envSession))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	_, err = daemon.Stop(ctx, p.Socket, true)
	return err
}

// askHandler renders the turn and asks for permissions on the terminal.
type askHandler struct {
	r      *render.Renderer
	stderr io.Writer
	color  bool

	mu      sync.Mutex
	agent   string       // the agent's name, once known
	phase   daemon.Phase // "" until the daemon says
	phased  chan struct{}
	content bool // the agent sent text, a thought, a tool call or a plan
}

func (h *askHandler) Update(u acp.SessionUpdate) {
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil && u.AgentMessageChunk.Content.Text.Text != "" ||
		u.AgentThoughtChunk != nil || u.ToolCall != nil || u.ToolCallUpdate != nil || u.Plan != nil {
		h.mu.Lock()
		h.content = true
		h.mu.Unlock()
	}
	h.r.Update(u)
}

func (h *askHandler) Notice(text string) { h.r.Notice("%s", text) }

func (h *askHandler) Stderr(text string) {
	h.r.Hide()
	_, _ = io.WriteString(h.stderr, text)
}

func (h *askHandler) Phase(p daemon.Phase, agent string) {
	h.mu.Lock()
	h.phase, h.agent = p, agent
	h.mu.Unlock()
	select {
	case h.phased <- struct{}{}:
	default:
	}
}

func (h *askHandler) replied() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.content
}

func (h *askHandler) agentName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agent == "" {
		return "the agent"
	}
	return h.agent
}

// defaultStatusDelay is how long `ecdy ask` stays silent before it says
// what it waits for.
const defaultStatusDelay = 2 * time.Second

// statusDelay returns ECDY_STATUS_DELAY (a Go duration; for tests), or
// defaultStatusDelay.
func statusDelay() time.Duration {
	d, err := time.ParseDuration(os.Getenv("ECDY_STATUS_DELAY"))
	if err != nil || d < 0 {
		return defaultStatusDelay
	}
	return d
}

// showWaiting draws a status line saying what the turn waits for, once
// nothing has been shown for delay since start, redrawn every second and
// when the phase changes, until anything else is shown. stop ends it and
// erases the line.
func (h *askHandler) showWaiting(start time.Time, delay time.Duration) (stop func()) {
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
		}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for h.r.Status(h.waitingText(time.Since(start))) {
			select {
			case <-done:
				return
			case <-tick.C:
			case <-h.phased:
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		h.r.Finish()
	}
}

func (h *askHandler) waitingText(elapsed time.Duration) string {
	h.mu.Lock()
	phase := h.phase
	h.mu.Unlock()
	what := "waiting for " + h.agentName()
	if phase == daemon.PhaseStarting {
		what = "starting " + h.agentName()
	}
	return fmt.Sprintf("%s… %ds · Ctrl+C to cancel", what, int(elapsed.Seconds()))
}

func (h *askHandler) Permission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
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
		return outcome, daemon.ErrInterrupted
	}
	return outcome, err
}

// termWidth returns a function reporting w's width in columns, asked at
// each call so that a resize is seen; 80 if it cannot tell.
func termWidth(w io.Writer) func() int {
	return func() int {
		if f, ok := w.(*os.File); ok {
			if cols, _, err := term.GetSize(int(f.Fd())); err == nil && cols > 0 {
				return cols
			}
		}
		return 80
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
