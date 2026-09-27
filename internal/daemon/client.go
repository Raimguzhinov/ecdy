package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
)

// ErrInterrupted is returned by TurnHandler.Permission when the user cancels
// the whole turn from the dialog.
var ErrInterrupted = acpclient.ErrInterrupted

// ReadyFD is the file descriptor on which a spawned daemon reports that it
// is ready (see RunOptions.Ready).
const ReadyFD = 3

// spawnTimeout bounds the wait for a spawned daemon to be ready.
const spawnTimeout = 5 * time.Second

// notRunning reports whether a dial error means that no daemon listens.
func notRunning(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// Connect connects to the daemon of the session at p, starting it with cmd
// if no daemon listens. cmd is `ecdy daemon` for the session; Spawn sets its
// I/O, directory and process attributes.
func Connect(ctx context.Context, p Paths, cmd func() *exec.Cmd) (net.Conn, error) {
	c, err := net.Dial("unix", p.Socket)
	if err == nil {
		return c, nil
	}
	if !notRunning(err) {
		return nil, fmt.Errorf("connect to the ecdy daemon: %w", err)
	}
	if err := Spawn(ctx, cmd()); err != nil {
		return nil, err
	}
	c, err = net.Dial("unix", p.Socket)
	if err != nil {
		return nil, fmt.Errorf("connect to the ecdy daemon: %w", err)
	}
	return c, nil
}

// Spawn starts the daemon cmd detached from the terminal (a new session,
// stdio on /dev/null, cwd /) and waits until it reports that it is ready.
func Spawn(ctx context.Context, cmd *exec.Cmd) error {
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("start the ecdy daemon: %w", err)
	}
	defer func() { _ = r.Close() }()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.ExtraFiles = []*os.File{w} // ReadyFD
	cmd.Dir = "/"
	// Its own session: no controlling terminal, so the terminal's signals
	// and hangup do not reach it; it stops with the shell on its own.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		return fmt.Errorf("start the ecdy daemon: %w", err)
	}
	// Reap it if it exits while we run.
	go func() { _ = cmd.Wait() }()

	_ = r.SetReadDeadline(time.Now().Add(spawnTimeout))
	defer context.AfterFunc(ctx, func() { _ = r.SetReadDeadline(time.Now()) })()
	line, err := bufio.NewReader(r).ReadString('\n')
	line = strings.TrimSpace(line)
	switch {
	case line == readyOK || line == readyRunning:
		return nil
	case strings.HasPrefix(line, "error: "):
		return fmt.Errorf("start the ecdy daemon: %s", strings.TrimPrefix(line, "error: "))
	case ctx.Err() != nil:
		return fmt.Errorf("start the ecdy daemon: %w", ctx.Err())
	case errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("start the ecdy daemon: not ready after %v", spawnTimeout)
	default:
		return errors.New("start the ecdy daemon: it exited before it was ready")
	}
}

// Stop asks the daemon at socket to stop. With wait, it returns once the
// daemon's agents are gone. running is false if no daemon was listening.
func Stop(ctx context.Context, socket string, wait bool) (running bool, err error) {
	c, err := dial(ctx, socket)
	if err != nil || c == nil {
		return false, err
	}
	defer func() { _ = c.Close() }()
	w := newWire(c)
	if err := w.send(Msg{T: tStop}); err != nil {
		return true, err
	}
	if !wait {
		return true, nil
	}
	defer context.AfterFunc(ctx, func() { _ = c.SetReadDeadline(time.Now()) })()
	// ok, or EOF when the daemon exits first.
	if _, err := w.recv(); err != nil && ctx.Err() != nil {
		return true, fmt.Errorf("wait for the ecdy daemon to stop: %w", ctx.Err())
	}
	return true, nil
}

// GetStatus returns the status of the daemon at socket; running is false if
// no daemon is listening.
func GetStatus(ctx context.Context, socket string) (st Status, running bool, err error) {
	c, err := dial(ctx, socket)
	if err != nil || c == nil {
		return st, false, err
	}
	defer func() { _ = c.Close() }()
	defer context.AfterFunc(ctx, func() { _ = c.SetReadDeadline(time.Now()) })()
	w := newWire(c)
	if err := w.send(Msg{T: tStatus}); err != nil {
		return st, true, err
	}
	m, err := w.recv()
	if err != nil {
		return st, true, fmt.Errorf("ecdy daemon status: %w", err)
	}
	if m.Status == nil {
		return st, true, fmt.Errorf("ecdy daemon status: %s", m.Text)
	}
	return *m.Status, true, nil
}

// dial connects to socket; a nil conn and nil error mean no daemon.
func dial(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socket)
	if notRunning(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("connect to the ecdy daemon: %w", err)
	}
	return c, nil
}

// PromptRequest is one prompt for the server.
type PromptRequest struct {
	Prompt string
	// Agent is the agent's name; empty means the session's agent.
	Agent string
	// Cwd is the absolute directory the prompt was typed in.
	Cwd string
	// Verbose also sends the agent's stderr.
	Verbose bool
}

// TurnHandler shows a turn to the user. Update, Notice and Stderr are called
// in the order the server sent them, never concurrently with each other.
type TurnHandler interface {
	Update(u acp.SessionUpdate)
	Notice(text string)
	Stderr(text string)
	// Permission asks the user; it runs on its own goroutine, after every
	// update sent before the request was handled. ctx is cancelled when
	// the request is withdrawn or the turn ends. Returning ErrInterrupted
	// cancels the turn.
	Permission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error)
}

// Turn is a prompt in progress on a connection.
type Turn struct {
	c   net.Conn
	w   *wire
	h   TurnHandler
	ctx context.Context

	mu        sync.Mutex
	cond      *sync.Cond
	queue     []item
	dialogs   map[int]context.CancelFunc
	withdrawn map[int]bool
	wg        sync.WaitGroup // dialogs

	done chan struct{}
	stop acp.StopReason
	err  error
}

type item struct {
	m   Msg
	err error // the connection failed
}

// StartTurn sends req on c and starts handling what the server sends; c is
// closed when the turn ends. ctx is the parent of the permission dialogs'
// contexts only: Cancel cancels the turn.
func StartTurn(ctx context.Context, c net.Conn, req PromptRequest, h TurnHandler) (*Turn, error) {
	t := &Turn{c: c, w: newWire(c), h: h, ctx: ctx, dialogs: map[int]context.CancelFunc{}, withdrawn: map[int]bool{}, done: make(chan struct{})}
	t.cond = sync.NewCond(&t.mu)
	err := t.w.send(Msg{T: tPrompt, Version: ProtocolVersion, Prompt: req.Prompt, Agent: req.Agent, Cwd: req.Cwd, Verbose: req.Verbose})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("send the prompt to the ecdy daemon: %w", err)
	}
	go t.read()
	go t.dispatch()
	return t, nil
}

// Cancel asks the agent to cancel the turn (session/cancel); the turn ends
// when the agent says so.
func (t *Turn) Cancel() { _ = t.w.send(Msg{T: tCancel}) }

// Kill makes the server kill the turn's agent.
func (t *Turn) Kill() { _ = t.w.send(Msg{T: tKill}) }

// Wait waits for the end of the turn and returns the stop reason, or the
// error the server reported (*Error) or the connection failed with.
func (t *Turn) Wait() (acp.StopReason, error) {
	<-t.done
	return t.stop, t.err
}

// read receives messages. Withdrawals take effect at once: an open dialog
// must close even while updates wait for it.
func (t *Turn) read() {
	for {
		m, err := t.w.recv()
		if err != nil {
			t.push(item{err: err})
			return
		}
		if m.T == tWithdraw {
			t.withdraw(m.ID)
			continue
		}
		t.push(item{m: m})
		if m.T == tDone || m.T == tError {
			return
		}
	}
}

func (t *Turn) push(it item) {
	t.mu.Lock()
	t.queue = append(t.queue, it)
	t.cond.Signal()
	t.mu.Unlock()
}

func (t *Turn) pop() item {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.queue) == 0 {
		t.cond.Wait()
	}
	it := t.queue[0]
	t.queue = t.queue[1:]
	return it
}

func (t *Turn) dispatch() {
	for {
		it := t.pop()
		if it.err != nil {
			t.finish("", fmt.Errorf("lost the connection to the ecdy daemon: %w", it.err))
			return
		}
		m := it.m
		switch m.T {
		case tUpdate:
			if m.Update != nil {
				t.h.Update(*m.Update)
			}
		case tNotice:
			t.h.Notice(m.Text)
		case tStderr:
			t.h.Stderr(m.Text)
		case tPermission:
			if m.Permission != nil {
				t.openDialog(m.ID, *m.Permission)
			}
		case tDone:
			t.finish(m.StopReason, nil)
			return
		case tError:
			t.finish("", &Error{Code: m.Code, Msg: m.Text})
			return
		}
	}
}

func (t *Turn) openDialog(id int, req acp.RequestPermissionRequest) {
	ctx, cancel := context.WithCancel(t.ctx)
	t.mu.Lock()
	if t.withdrawn[id] {
		t.mu.Unlock()
		cancel()
		return
	}
	t.dialogs[id] = cancel
	t.mu.Unlock()
	t.wg.Go(func() {
		defer cancel()
		out, err := t.h.Permission(ctx, req)
		t.mu.Lock()
		delete(t.dialogs, id)
		t.mu.Unlock()
		switch {
		case errors.Is(err, ErrInterrupted):
			_ = t.w.send(Msg{T: tAnswer, ID: id, Interrupted: true})
		case ctx.Err() != nil:
			// Withdrawn, cancelled or over: nobody waits for the answer.
		case err != nil:
			cancelled := acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}
			_ = t.w.send(Msg{T: tAnswer, ID: id, Outcome: &cancelled})
		default:
			_ = t.w.send(Msg{T: tAnswer, ID: id, Outcome: &out})
		}
	})
}

func (t *Turn) withdraw(id int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cancel, ok := t.dialogs[id]; ok {
		cancel()
		return
	}
	t.withdrawn[id] = true // not open yet
}

// finish closes the dialogs, then reports the result.
func (t *Turn) finish(stop acp.StopReason, err error) {
	t.mu.Lock()
	for _, cancel := range t.dialogs {
		cancel()
	}
	t.mu.Unlock()
	t.wg.Wait()
	_ = t.c.Close()
	t.stop, t.err = stop, err
	close(t.done)
}
