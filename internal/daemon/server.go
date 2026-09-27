package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
	"github.com/Raimguzhinov/ecdy/internal/config"
)

// Options configure a Server.
type Options struct {
	// Config returns the configuration; it is read at every prompt, so that
	// edits apply to agents started later.
	Config func() (config.Config, error)
	// State returns the session's state (ecdy use, ecdy new); nil means no
	// session: the zero State.
	State func() (State, error)
	// Env is the agents' environment; nil means os.Environ().
	Env []string
	// Logger receives diagnostics; nil discards them.
	Logger *slog.Logger
	// KillGrace is how long an agent gets to exit after SIGTERM.
	KillGrace time.Duration
	// CancelGrace is how long a turn whose client disconnected may take to
	// end after session/cancel before its agent is killed.
	CancelGrace time.Duration
	// ClientVersion is sent to agents as clientInfo.version.
	ClientVersion string
	// OnConn, if set, is called with +1 when a connection starts being
	// served and -1 when it is done.
	OnConn func(delta int)
}

// Server runs agents for the prompts of one shell session. Each agent name
// has at most one agent process, and each process one current ACP session:
// the conversation.
type Server struct {
	opts   Options
	log    *slog.Logger
	ctx    context.Context // cancelled by Close
	cancel context.CancelFunc
	stop   chan struct{} // closed when a client asks the daemon to stop
	closed chan struct{} // closed when Close is done

	mu       sync.Mutex
	agents   map[string]*agent
	shutdown bool
	stopOnce sync.Once
	wg       sync.WaitGroup // connections being served
}

// NewServer returns a Server.
func NewServer(opts Options) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		opts: opts, log: log, ctx: ctx, cancel: cancel,
		stop: make(chan struct{}), closed: make(chan struct{}),
		agents: map[string]*agent{},
	}
}

// StopRequested is closed when a client sent stop.
func (s *Server) StopRequested() <-chan struct{} { return s.stop }

// Close ends every turn and stops every agent (stdin EOF, SIGTERM, SIGKILL
// after KillGrace). New prompts fail. Close does not wait for the
// connections; Wait does.
func (s *Server) Close() {
	s.mu.Lock()
	if s.shutdown {
		s.mu.Unlock()
		<-s.closed
		return
	}
	s.shutdown = true
	agents := make([]*agent, 0, len(s.agents))
	for _, a := range s.agents {
		agents = append(agents, a)
	}
	s.mu.Unlock()
	s.cancel()
	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Go(a.close)
	}
	wg.Wait()
	close(s.closed)
}

// Wait waits until every connection has been served.
func (s *Server) Wait() { s.wg.Wait() }

// requestTimeout bounds the wait for a client's request.
const requestTimeout = 5 * time.Second

// ServeConn serves one request on c and closes it.
func (s *Server) ServeConn(c net.Conn) {
	s.wg.Add(1)
	defer s.wg.Done()
	if s.opts.OnConn != nil {
		s.opts.OnConn(1)
		defer s.opts.OnConn(-1)
	}
	defer func() { _ = c.Close() }()
	w := newWire(c)
	// A client that connects and says nothing must not keep the daemon
	// from stopping.
	_ = c.SetReadDeadline(time.Now().Add(requestTimeout))
	m, err := w.recv()
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	switch m.T {
	case tPrompt:
		s.prompt(w, m)
	case tStatus:
		st := s.status()
		_ = w.send(Msg{T: tOK, Status: &st})
	case tStop:
		s.stopOnce.Do(func() { close(s.stop) })
		// Answer once the agents are gone, so that `ecdy daemon stop` can
		// wait for them.
		<-s.closed
		_ = w.send(Msg{T: tOK})
	default:
		_ = w.send(Msg{T: tError, Text: fmt.Sprintf("unknown request %q", m.T)})
	}
}

func (s *Server) state() (State, error) {
	if s.opts.State == nil {
		return State{}, nil
	}
	return s.opts.State()
}

// agent is one agent process and its conversation.
type agent struct {
	name string
	s    *Server

	busy bool // a turn holds the agent; guarded by Server.mu

	mu     sync.Mutex
	conn   *acpclient.Conn // nil until started, and after it died
	cwd    string          // of the current ACP session
	gen    int             // State.Generation the session was started for
	turn   *turn           // the turn in progress, if any
	closed bool            // by Server.Close
}

func (s *Server) acquire(name string) (*agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutdown {
		return nil, errors.New("the ecdy daemon is stopping")
	}
	a := s.agents[name]
	if a == nil {
		a = &agent{name: name, s: s}
		s.agents[name] = a
	}
	if a.busy {
		return nil, &Error{Code: CodeBusy, Msg: name + " is busy with another prompt in this shell session"}
	}
	a.busy = true
	return a, nil
}

func (s *Server) release(a *agent) {
	s.mu.Lock()
	a.busy = false
	s.mu.Unlock()
}

func (s *Server) status() Status {
	st := Status{Pid: os.Getpid()}
	if cfg, err := s.opts.Config(); err == nil {
		st.Agent = cfg.DefaultAgent
	}
	if state, err := s.state(); err == nil && state.Agent != "" {
		st.Agent = state.Agent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agents {
		a.mu.Lock()
		if a.conn != nil {
			st.Agents = append(st.Agents, AgentStatus{
				Name: a.name, Pid: a.conn.Pid(), Session: string(a.conn.Session()), Cwd: a.cwd, Busy: a.busy,
			})
		}
		a.mu.Unlock()
	}
	slices.SortFunc(st.Agents, func(x, y AgentStatus) int { return strings.Compare(x.Name, y.Name) })
	return st
}

// prompt runs one turn for the client on w.
func (s *Server) prompt(w *wire, m Msg) {
	fail := func(err error) {
		msg := Msg{T: tError, Text: err.Error()}
		var e *Error
		if errors.As(err, &e) {
			msg.Code = e.Code
		}
		_ = w.send(msg)
	}
	if m.Version != ProtocolVersion {
		fail(&Error{Code: CodeVersion, Msg: fmt.Sprintf("the ecdy daemon speaks protocol %d, the client %d", ProtocolVersion, m.Version)})
		return
	}
	if !filepath.IsAbs(m.Cwd) {
		fail(fmt.Errorf("directory %q is not absolute", m.Cwd))
		return
	}
	cfg, err := s.opts.Config()
	if err != nil {
		fail(err)
		return
	}
	state, err := s.state()
	if err != nil {
		fail(err)
		return
	}
	name := m.Agent
	if name == "" {
		name = state.Agent
	}
	name, spec, err := cfg.Agent(name)
	if err != nil {
		fail(err)
		return
	}
	a, err := s.acquire(name)
	if err != nil {
		fail(err)
		return
	}
	defer s.release(a)

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	t := &turn{w: w, verbose: m.Verbose, pending: map[int]chan Msg{}}
	ended := make(chan struct{})
	defer close(ended)
	// The turn is over before the client hears so: a client that closes
	// the connection right after the last message must not look like one
	// that left in the middle of the turn.
	end := func(msg Msg) {
		a.mu.Lock()
		t.ended = true
		a.mu.Unlock()
		_ = w.send(msg)
	}
	endErr := func(err error) { end(Msg{T: tError, Text: describe(name, err)}) }
	go s.readClient(w, t, a, cancel, ended)

	conn, fresh, err := a.ensure(ctx, t, spec, m.Cwd, state.Generation)
	if err != nil {
		endErr(err)
		return
	}
	if !fresh && !within(m.Cwd, a.cwd) {
		t.notice(fmt.Sprintf("this conversation runs in %s; `ecdy new` starts one here", a.cwd))
	}
	a.setTurn(t)
	stop, err := conn.Prompt(ctx, m.Prompt)
	a.setTurn(nil)
	if err != nil {
		var ee *acpclient.ExitError
		if errors.As(err, &ee) {
			a.drop(conn)
		}
		endErr(err)
		return
	}
	end(Msg{T: tDone, StopReason: stop})
}

// readClient handles what the client sends during a turn: cancel, kill,
// permission answers. A client that goes away cancels the turn, and its
// agent is killed if the turn has not ended within CancelGrace.
func (s *Server) readClient(w *wire, t *turn, a *agent, cancel context.CancelFunc, ended <-chan struct{}) {
	for {
		m, err := w.recv()
		if err != nil {
			select {
			case <-ended:
				return
			default:
			}
			s.log.Info("client gone during a turn; cancelling", "agent", a.name, "err", err)
			cancel()
			t.closeAnswers()
			timer := time.AfterFunc(s.opts.CancelGrace, func() {
				s.log.Warn("the turn did not end after the client left; killing the agent", "agent", a.name)
				a.kill(t)
			})
			<-ended
			timer.Stop()
			return
		}
		switch m.T {
		case tCancel:
			cancel()
		case tKill:
			cancel()
			a.kill(t)
		case tAnswer:
			t.answer(m)
		}
	}
}

// ensure returns the agent's connection with a session for the state's
// generation, starting the agent or a new session if needed; fresh reports
// that the session is new.
func (a *agent) ensure(ctx context.Context, t *turn, spec config.Agent, cwd string, gen int) (conn *acpclient.Conn, fresh bool, err error) {
	a.mu.Lock()
	conn = a.conn
	a.mu.Unlock()
	if conn != nil {
		select {
		case <-conn.Done():
			a.drop(conn)
			t.notice(a.name + " had exited; starting a new conversation")
			conn = nil
		default:
		}
	}
	if conn == nil {
		s := a.s
		c, err := acpclient.Start(ctx, acpclient.Options{
			Command:       spec.Command,
			Dir:           cwd,
			Env:           s.opts.Env,
			Stderr:        a,
			Logger:        s.log.With("agent", a.name),
			KillGrace:     s.opts.KillGrace,
			ClientVersion: s.opts.ClientVersion,
		}, a)
		if err != nil {
			return nil, false, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed || t.killed {
			c.Kill()
			return nil, false, errors.New("agent stopped")
		}
		a.conn, a.cwd, a.gen = c, cwd, gen
		s.log.Info("agent started", "agent", a.name, "pid", c.Pid(), "session", c.Session())
		return c, true, nil
	}
	if a.gen != gen {
		if err := conn.NewSession(ctx, cwd); err != nil {
			var ee *acpclient.ExitError
			if errors.As(err, &ee) {
				a.drop(conn)
			}
			return nil, false, err
		}
		a.mu.Lock()
		a.cwd, a.gen = cwd, gen
		a.mu.Unlock()
		return conn, true, nil
	}
	return conn, false, nil
}

// drop forgets conn (which died or was killed) and makes sure it is gone.
func (a *agent) drop(conn *acpclient.Conn) {
	a.mu.Lock()
	if a.conn == conn {
		a.conn = nil
	}
	a.mu.Unlock()
	conn.Kill()
}

// kill kills the agent if turn t is still running.
func (a *agent) kill(t *turn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t.ended {
		return
	}
	t.killed = true
	if a.conn != nil {
		// Under a.mu: a turn that ends meanwhile must not leave the agent
		// killed behind its back. Kill only waits for the process.
		a.conn.Kill()
	}
}

func (a *agent) close() {
	a.mu.Lock()
	conn := a.conn
	a.conn, a.closed = nil, true
	a.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

func (a *agent) setTurn(t *turn) {
	a.mu.Lock()
	a.turn = t
	a.mu.Unlock()
}

func (a *agent) currentTurn() *turn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turn
}

// SessionUpdate implements acpclient.Handler. Updates outside a turn (after
// session/new, say) have nobody to show them to.
func (a *agent) SessionUpdate(_ context.Context, u acp.SessionUpdate) {
	if t := a.currentTurn(); t != nil {
		_ = t.w.send(Msg{T: tUpdate, Update: &u})
	}
}

// RequestPermission implements acpclient.Handler.
func (a *agent) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	t := a.currentTurn()
	if t == nil {
		return acp.RequestPermissionOutcome{}, errors.New("no turn in progress")
	}
	return t.permission(ctx, req)
}

// Write receives the agent's stderr: it goes to the client of a verbose turn.
func (a *agent) Write(p []byte) (int, error) {
	if t := a.currentTurn(); t != nil && t.verbose {
		_ = t.w.send(Msg{T: tStderr, Text: string(p)})
	}
	return len(p), nil
}

// turn is one prompt of one client.
type turn struct {
	w       *wire
	verbose bool

	// Guarded by agent.mu.
	ended  bool
	killed bool

	mu      sync.Mutex
	nextID  int
	pending map[int]chan Msg // permission requests waiting for an answer
	gone    bool             // the client left: nobody will answer
}

func (t *turn) notice(text string) { _ = t.w.send(Msg{T: tNotice, Text: text}) }

// permission forwards a permission request to the client and waits for its
// answer. If ctx ends first (the turn was cancelled, the agent withdrew the
// request), the client is told to close the dialog.
func (t *turn) permission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	ch := make(chan Msg, 1)
	t.mu.Lock()
	if t.gone {
		t.mu.Unlock()
		return acp.RequestPermissionOutcome{}, errors.New("the client left")
	}
	t.nextID++
	id := t.nextID
	t.pending[id] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()
	if err := t.w.send(Msg{T: tPermission, ID: id, Permission: &req}); err != nil {
		return acp.RequestPermissionOutcome{}, err
	}
	select {
	case m, ok := <-ch:
		switch {
		case !ok:
			return acp.RequestPermissionOutcome{}, errors.New("the client left")
		case m.Interrupted:
			return acp.RequestPermissionOutcome{}, acpclient.ErrInterrupted
		case m.Outcome == nil:
			return acp.RequestPermissionOutcome{}, errors.New("empty permission answer")
		}
		return *m.Outcome, nil
	case <-ctx.Done():
		_ = t.w.send(Msg{T: tWithdraw, ID: id})
		return acp.RequestPermissionOutcome{}, fmt.Errorf("permission request: %w", ctx.Err())
	}
}

func (t *turn) answer(m Msg) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ch, ok := t.pending[m.ID]; ok {
		delete(t.pending, m.ID)
		ch <- m
	}
}

// closeAnswers fails the pending permission requests: the client is gone.
func (t *turn) closeAnswers() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gone = true
	for id, ch := range t.pending {
		delete(t.pending, id)
		close(ch)
	}
}

// within reports whether dir is root or below it.
func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// describe explains a failed agent: its exit status and the end of its
// stderr, or how to log in.
func describe(name string, err error) string {
	var ae *acpclient.AuthError
	if errors.As(err, &ae) {
		// Logging in through ACP (authenticate) may open a browser or need
		// its own terminal UI; the agent's CLI does it better.
		return fmt.Sprintf("%s: %v\n  log in with the agent's own CLI, then try again", name, ae)
	}
	var ee *acpclient.ExitError
	if errors.As(err, &ee) {
		msg := fmt.Sprintf("%s: %v", name, ee)
		if tail := lastLines(ee.Stderr, 10); tail != "" {
			msg += "\n  " + strings.ReplaceAll(tail, "\n", "\n  ")
		}
		return msg
	}
	return fmt.Sprintf("%s: %v", name, err)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
