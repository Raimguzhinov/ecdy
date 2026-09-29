// Package acpclient runs an ACP agent as a child process and talks to it
// through github.com/coder/acp-go-sdk (docs/adr/0002-acp-go-sdk.md).
//
// Protocol: https://agentclientprotocol.com/protocol/overview. A Conn does
// one initialize and one session/new, then any number of session/prompt
// turns; NewSession replaces the session with a new one.
package acpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// ErrInterrupted is returned by Handler.RequestPermission when the user
// cancels the whole turn from the permission dialog (Ctrl+C in raw mode).
var ErrInterrupted = errors.New("interrupted by the user")

// Handler receives what the agent sends during a turn.
type Handler interface {
	// SessionUpdate is called for every session/update, in the order the
	// agent sent them, and never concurrently with itself.
	SessionUpdate(ctx context.Context, u acp.SessionUpdate)
	// RequestPermission asks the user to choose one of req.Options. It may
	// run concurrently with SessionUpdate. ctx is cancelled when the turn is
	// cancelled; returning ErrInterrupted cancels the turn.
	RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error)
}

// Options configure the agent process.
type Options struct {
	// Command is the agent's argv.
	Command []string
	// Dir is the agent's working directory and the session's cwd. It must
	// be absolute: ACP requires absolute paths.
	Dir string
	// Env is the agent's environment; nil means os.Environ().
	Env []string
	// Stderr, if set, receives a copy of the agent's stderr. The last
	// stderrTail bytes are kept in any case for error messages.
	Stderr io.Writer
	// Logger receives the SDK's diagnostics; nil discards them. The SDK must
	// never log to the user's terminal.
	Logger *slog.Logger
	// KillGrace is how long Close waits after SIGTERM before SIGKILL.
	KillGrace time.Duration
	// ClientVersion is sent as clientInfo.version.
	ClientVersion string
}

// ExitError reports that the agent process exited or closed its output
// before the conversation was over.
type ExitError struct {
	Err    error  // from exec.Cmd.Wait; nil for exit status 0
	Stderr string // the tail of the agent's stderr
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return "agent exited"
	}
	return "agent exited: " + e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// AuthError reports that the agent requires authentication. ecdy does not log
// in on the user's behalf: the user logs in with the agent's own CLI.
type AuthError struct {
	Methods []acp.AuthMethod
}

func (e *AuthError) Error() string {
	var names []string
	for _, m := range e.Methods {
		switch {
		case m.Agent != nil:
			names = append(names, m.Agent.Name)
		case m.Terminal != nil:
			names = append(names, m.Terminal.Name)
		case m.EnvVar != nil:
			names = append(names, m.EnvVar.Name)
		}
	}
	if len(names) == 0 {
		return "agent requires authentication"
	}
	return "agent requires authentication (" + strings.Join(names, "; ") + ")"
}

// Conn is a running agent with one ACP session.
type Conn struct {
	opts    Options
	handler Handler

	cmd         *exec.Cmd
	conn        *acp.ClientSideConnection
	authMethods []acp.AuthMethod
	stdin       *os.File // our end of the agent's stdin
	stdout      *os.File // our end of the agent's stdout
	stderr      *tail
	exited      chan struct{} // closed once the process is reaped
	errDone     chan struct{} // closed when the agent's stderr is read to EOF
	waitErr     error         // valid after exited is closed

	mu         sync.Mutex
	session    acp.SessionId
	turnCtx    context.Context // the current turn; nil between turns
	cancelTurn context.CancelFunc

	closeOnce sync.Once

	// updates counts session/update notifications read from the agent and
	// handled, so that the last ones before a crash are rendered before the
	// error (see drain).
	updates updateCount
	drainNS atomic.Int64 // drainTimeout; changed by tests
}

// Start launches the agent, initializes the connection and creates a
// session. Cancelling ctx aborts the start; the returned error then wraps
// ctx.Err().
func Start(ctx context.Context, opts Options, h Handler) (*Conn, error) {
	if len(opts.Command) == 0 {
		return nil, errors.New("agent command is empty")
	}
	if !filepath.IsAbs(opts.Dir) {
		return nil, fmt.Errorf("session directory %q is not absolute", opts.Dir)
	}
	c := &Conn{opts: opts, handler: h, stderr: newTail(stderrTail, opts.Stderr), exited: make(chan struct{}), errDone: make(chan struct{})}
	c.drainNS.Store(int64(drainTimeout))
	c.updates.cond = sync.NewCond(&c.updates.mu)
	if err := c.spawn(); err != nil {
		return nil, err
	}
	if err := c.setup(ctx); err != nil {
		c.Kill()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("start agent: %w", ctx.Err())
		}
		return nil, err
	}
	return c, nil
}

const stderrTail = 4096

// spawn starts the process with three pipes. The pipes are created here and
// not by exec.Cmd, so that Wait only reaps the process: exec.Cmd would wait
// for its copying goroutines, which never finish while a grandchild (npx
// starts one) holds the other end.
func (c *Conn) spawn() error {
	inR, inW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("agent stdin: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		closeAll(inR, inW)
		return fmt.Errorf("agent stdout: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeAll(inR, inW, outR, outW)
		return fmt.Errorf("agent stderr: %w", err)
	}
	cmd := exec.Command(c.opts.Command[0], c.opts.Command[1:]...)
	cmd.Dir = c.opts.Dir
	cmd.Env = c.opts.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	// Own process group: Ctrl+C in the terminal reaches ecdy only (ecdy
	// turns it into session/cancel), and Close can signal the agent's
	// children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	closeAll(inR, outW, errW) // the child has its copies
	if err != nil {
		closeAll(inW, outR, errR)
		return fmt.Errorf("start agent %q: %w", c.opts.Command[0], err)
	}
	c.cmd, c.stdin, c.stdout = cmd, inW, outR

	go func() {
		_, _ = io.Copy(c.stderr, errR)
		_ = errR.Close()
		close(c.errDone)
	}()
	logger := c.opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	c.conn = acp.NewClientSideConnection(&client{c}, inW, &lineCounter{r: outR, count: &c.updates})
	c.conn.SetLogger(logger)

	go func() {
		c.waitErr = cmd.Wait()
		close(c.exited)
		// The agent is gone: take its children with it, so that nothing
		// keeps stdout open and the connection sees EOF.
		c.signalGroup(syscall.SIGKILL)
		// A child that left the group (setsid) may still hold stdout: stop
		// reading after the drain timeout, or the connection never ends.
		t := time.NewTimer(c.drainTimeout())
		defer t.Stop()
		select {
		case <-c.conn.Done():
		case <-t.C:
			_ = outR.Close()
		}
	}()
	return nil
}

func (c *Conn) setup(ctx context.Context) error {
	// https://agentclientprotocol.com/protocol/initialization. No fs/* and
	// no terminal/*: the agent uses its own tools (ADR 0005).
	init, err := c.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: "ecdy", Version: c.opts.ClientVersion},
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: false, WriteTextFile: false},
			Terminal: false,
		},
	})
	if err != nil {
		return c.connErr("initialize", err)
	}
	if init.ProtocolVersion != acp.ProtocolVersionNumber {
		// The client SHOULD close the connection if it does not support the
		// agent's version (initialization#version-negotiation).
		return fmt.Errorf("agent speaks ACP version %d, ecdy supports %d", init.ProtocolVersion, acp.ProtocolVersionNumber)
	}
	c.authMethods = init.AuthMethods
	return c.NewSession(ctx, c.opts.Dir)
}

// NewSession starts a new session in dir (absolute) with session/new; the
// following turns belong to it. It must not be called during a turn.
func (c *Conn) NewSession(ctx context.Context, dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("session directory %q is not absolute", dir)
	}
	sess, err := c.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{}})
	if err != nil {
		var re *acp.RequestError
		if errors.As(err, &re) && re.Code == acp.NewAuthRequired(nil).Code {
			return &AuthError{Methods: c.authMethods}
		}
		return c.connErr("session/new", err)
	}
	c.mu.Lock()
	c.session = sess.SessionId
	c.mu.Unlock()
	return nil
}

// Session returns the id of the current session.
func (c *Conn) Session() acp.SessionId {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// Prompt sends one prompt, each of texts as a text content block, and blocks
// until the turn ends, returning the stop reason. Cancelling ctx sends session/cancel and keeps waiting for the
// agent's answer, normally stop reason cancelled
// (https://agentclientprotocol.com/protocol/prompt-turn#cancellation); Kill
// unblocks it for good.
func (c *Conn) Prompt(ctx context.Context, texts ...string) (acp.StopReason, error) {
	blocks := make([]acp.ContentBlock, len(texts))
	for i, t := range texts {
		blocks[i] = acp.TextBlock(t)
	}
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	session := c.Session()
	sendCancel := sync.OnceFunc(func() {
		_ = c.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: session})
	})
	c.mu.Lock()
	// Cancelling from the permission dialog sends session/cancel before the
	// dialog answers: the agent's reply to the answer may end the turn first.
	c.turnCtx, c.cancelTurn = turnCtx, func() { sendCancel(); cancel() }
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.turnCtx, c.cancelTurn = nil, nil
		c.mu.Unlock()
	}()

	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// Not turnCtx: cancelling the SDK's context would stop waiting for
		// the stop reason. A dead agent ends this call through the
		// connection.
		resp, err := c.conn.Prompt(context.Background(), acp.PromptRequest{
			SessionId: session,
			Prompt:    blocks,
		})
		done <- result{resp, err}
	}()

	var r result
	select {
	case r = <-done:
	case <-turnCtx.Done():
		sendCancel()
		r = <-done
	}
	if r.err != nil {
		return "", c.connErr("session/prompt", r.err)
	}
	return r.resp.StopReason, nil
}

// connErr describes a failed request: if the agent is gone, as an ExitError
// with its exit status and stderr.
func (c *Conn) connErr(method string, err error) error {
	select {
	case <-c.conn.Done():
		// The agent closed its output; it is exiting or useless.
		c.signalGroup(syscall.SIGKILL)
		<-c.exited
		c.drain()
		return &ExitError{Err: c.waitErr, Stderr: c.stderr.String()}
	default:
		return &RequestError{Method: method, Err: err}
	}
}

// RequestError is an error the agent answered a request with.
type RequestError struct {
	Method string
	Err    error
}

// Error formats a JSON-RPC error as "method: message: details" instead of
// the SDK's JSON.
func (e *RequestError) Error() string {
	var re *acp.RequestError
	if !errors.As(e.Err, &re) {
		return e.Method + ": " + e.Err.Error()
	}
	msg := e.Method + ": " + re.Message
	if d, ok := re.Data.(map[string]any); ok {
		for _, k := range []string{"details", "error", "message"} {
			if v, ok := d[k].(string); ok && v != "" {
				return msg + ": " + v
			}
		}
	}
	if s, ok := re.Data.(string); ok && s != "" {
		return msg + ": " + s
	}
	return msg
}

func (e *RequestError) Unwrap() error { return e.Err }

// Stderr returns the tail of the agent's stderr.
func (c *Conn) Stderr() string { return c.stderr.String() }

// Kill stops the agent and its children at once (SIGKILL) and waits for it.
func (c *Conn) Kill() {
	c.closeOnce.Do(func() {
		c.signalGroup(syscall.SIGKILL)
		c.finish()
	})
}

// Close stops the agent: it closes the agent's stdin, sends SIGTERM to its
// process group, and SIGKILL after KillGrace.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		c.signalGroup(syscall.SIGTERM)
		t := time.NewTimer(c.opts.KillGrace)
		defer t.Stop()
		select {
		case <-c.exited:
		case <-t.C:
		}
		c.signalGroup(syscall.SIGKILL)
		c.finish()
	})
}

func (c *Conn) finish() {
	<-c.exited
	closeAll(c.stdin, c.stdout)
}

func (c *Conn) signalGroup(sig syscall.Signal) {
	// The group outlives its leader while children remain; ESRCH once
	// everything is gone is expected.
	_ = syscall.Kill(-c.cmd.Process.Pid, sig)
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// client implements acp.Client: the methods the agent calls on ecdy.
type client struct{ c *Conn }

var _ acp.Client = (*client)(nil)

func (cl *client) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	defer cl.c.updates.handle()
	if n.SessionId == cl.c.Session() {
		cl.c.handler.SessionUpdate(ctx, n.Update)
	}
	return nil
}

func (cl *client) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	cancelled := acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}
	c := cl.c
	c.mu.Lock()
	turnCtx, cancelTurn, session := c.turnCtx, c.cancelTurn, c.session
	c.mu.Unlock()
	if turnCtx == nil || req.SessionId != session {
		return cancelled, nil
	}
	// After session/cancel every pending permission request must be
	// answered with the cancelled outcome (prompt-turn#cancellation).
	if turnCtx.Err() != nil {
		return cancelled, nil
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	defer context.AfterFunc(turnCtx, stop)()
	// The SDK runs this request on its own goroutine, ahead of the
	// notifications before it: render those first, they often explain the
	// request.
	wctx, wcancel := context.WithTimeout(ctx, c.drainTimeout())
	c.waitUpdates(wctx, c.updates.readCount())
	wcancel()

	outcome, err := c.handler.RequestPermission(ctx, req)
	switch {
	case errors.Is(err, ErrInterrupted):
		cancelTurn()
		return cancelled, nil
	case turnCtx.Err() != nil:
		return cancelled, nil
	case err != nil:
		return acp.RequestPermissionResponse{}, fmt.Errorf("ask for permission: %w", err)
	}
	return acp.RequestPermissionResponse{Outcome: outcome}, nil
}

// File system and terminal methods are not implemented and not declared (ADR
// 0005). Some agents call them anyway (opencode's fs/write_text_file): they get
// method not found and go on.

func (cl *client) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsReadTextFile)
}

func (cl *client) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsWriteTextFile)
}

func (cl *client) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalCreate)
}

func (cl *client) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalKill)
}

func (cl *client) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalOutput)
}

func (cl *client) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalRelease)
}

func (cl *client) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalWaitForExit)
}

// tail keeps the last n bytes written to it and copies everything to tee.
type tail struct {
	mu  sync.Mutex
	n   int
	buf []byte
	tee io.Writer
}

func newTail(n int, tee io.Writer) *tail { return &tail{n: n, tee: tee} }

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	if t.tee != nil {
		_, _ = t.tee.Write(p)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// Pid returns the agent's pid, which is also its process group id.
func (c *Conn) Pid() int { return c.cmd.Process.Pid }

// Done is closed once the agent process has exited.
func (c *Conn) Done() <-chan struct{} { return c.exited }

// drainTimeout bounds the waits for session/updates already read (before a
// permission dialog, after the agent exits): a session/update the SDK fails
// to decode is read but never handled. After an exit it also bounds the wait
// for EOF: a child outside the process group may hold stdout or stderr open.
const drainTimeout = 2 * time.Second

// drain waits, up to drainTimeout, until every session/update read before
// the connection closed has been handled and the agent's stderr is read to
// the end, so that the last output before a crash is not lost. The SDK
// handles notifications on its own goroutine and may report the disconnect
// first.
func (c *Conn) drainTimeout() time.Duration { return time.Duration(c.drainNS.Load()) }

func (c *Conn) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), c.drainTimeout())
	defer cancel()
	c.waitUpdates(ctx, c.updates.readCount())
	// A child that left the group may keep stderr open.
	select {
	case <-c.errDone:
	case <-ctx.Done():
	}
}

// waitUpdates waits until n session/updates have been handled or ctx is
// done.
func (c *Conn) waitUpdates(ctx context.Context, n int) {
	u := &c.updates
	defer context.AfterFunc(ctx, func() {
		u.mu.Lock()
		u.cond.Broadcast()
		u.mu.Unlock()
	})()
	u.mu.Lock()
	defer u.mu.Unlock()
	for u.handled < n && ctx.Err() == nil {
		u.cond.Wait()
	}
}

type updateCount struct {
	mu            sync.Mutex
	cond          *sync.Cond
	read, handled int
}

func (u *updateCount) readCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.read
}

func (u *updateCount) add() {
	u.mu.Lock()
	u.read++
	u.mu.Unlock()
}

func (u *updateCount) handle() {
	u.mu.Lock()
	u.handled++
	u.cond.Broadcast()
	u.mu.Unlock()
}

// lineCounter passes the agent's output to the SDK and counts the
// session/update notifications in it. Messages are newline-delimited JSON
// (https://agentclientprotocol.com/protocol/transports#stdio).
type lineCounter struct {
	r       io.Reader
	count   *updateCount
	partial []byte
}

func (l *lineCounter) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	data := p[:n]
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			if len(l.partial) < maxLine {
				l.partial = append(l.partial, data...)
			}
			break
		}
		line := data[:i]
		if len(l.partial) > 0 {
			line = append(l.partial, line...)
			l.partial = l.partial[:0]
		}
		if isUpdate(line) {
			l.count.add()
		}
		data = data[i+1:]
	}
	return n, err //nolint:wrapcheck // a transparent reader: the SDK expects io.EOF as is
}

// maxLine matches the SDK's scanner limit; longer lines are dropped by it.
const maxLine = 10 << 20

func isUpdate(line []byte) bool {
	if !bytes.Contains(line, []byte(acp.ClientMethodSessionUpdate)) {
		return false
	}
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	return json.Unmarshal(line, &m) == nil && m.ID == nil && m.Method == acp.ClientMethodSessionUpdate
}
