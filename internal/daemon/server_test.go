package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Raimguzhinov/ecdy/internal/config"
	"github.com/Raimguzhinov/ecdy/internal/testutil"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

func TestMain(m *testing.M) {
	if fakeagent.Enabled() {
		fakeagent.Main()
	}
	os.Exit(m.Run())
}

type env struct {
	dir    string
	record string
	srv    *Server
}

func newEnv(t *testing.T, s fakeagent.Script, cancelGrace time.Duration, opts ...func(*Options)) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{dir: dir, record: filepath.Join(dir, "record.jsonl")}
	s.Record = e.record
	scriptEnv, err := fakeagent.WriteScript(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DefaultAgent = "fake"
	cfg.Agents["fake"] = config.Agent{Command: []string{os.Args[0]}}
	o := Options{
		Config:      func() (config.Config, error) { return cfg, nil },
		Env:         append(os.Environ(), scriptEnv),
		KillGrace:   time.Second,
		CancelGrace: cancelGrace,
	}
	for _, f := range opts {
		f(&o)
	}
	e.srv = NewServer(o)
	t.Cleanup(func() {
		e.srv.Close()
		e.srv.Wait()
	})
	return e
}

// handler records a turn; Permission answers with choose.
type handler struct {
	mu     sync.Mutex
	text   strings.Builder
	events []string
	choose func(ctx context.Context) (acp.RequestPermissionOutcome, error)
	asked  chan struct{} // gets a value when a dialog opens
}

func (h *handler) Update(u acp.SessionUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
		h.text.WriteString(u.AgentMessageChunk.Content.Text.Text)
		h.events = append(h.events, "text")
	}
}

func (h *handler) Notice(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, "notice: "+text)
}

func (h *handler) Stderr(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, "stderr: "+text)
}

func (h *handler) Permission(ctx context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	h.mu.Lock()
	h.events = append(h.events, "permission")
	h.mu.Unlock()
	if h.asked != nil {
		h.asked <- struct{}{}
	}
	if h.choose == nil {
		<-ctx.Done()
		return acp.RequestPermissionOutcome{}, fmt.Errorf("dialog: %w", ctx.Err())
	}
	return h.choose(ctx)
}

func (h *handler) Text() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.text.String()
}

func (e *env) turn(t *testing.T, prompt string, h TurnHandler) *Turn {
	t.Helper()
	client, server := net.Pipe()
	go e.srv.ServeConn(server)
	turn, err := StartTurn(t.Context(), client, PromptRequest{Prompt: prompt, Cwd: e.dir, Verbose: true}, h)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func (e *env) methods(t *testing.T) []string {
	t.Helper()
	reqs, _ := fakeagent.ReadRecord(e.record)
	var m []string
	for _, r := range reqs {
		m = append(m, r.Method)
	}
	return m
}

func (e *env) pids(t *testing.T) []int {
	t.Helper()
	reqs, _ := fakeagent.ReadRecord(e.record)
	var pids []int
	for _, r := range reqs {
		if len(pids) == 0 || pids[len(pids)-1] != r.Pid {
			pids = append(pids, r.Pid)
		}
	}
	return pids
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testutil.DefaultTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTurnsContinue(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Stderr: "log line\n"}, {History: true}}}, time.Second)
	for _, p := range []string{"one", "two"} {
		h := &handler{}
		stop, err := e.turn(t, p, h).Wait()
		if err != nil || stop != acp.StopReasonEndTurn {
			t.Fatalf("turn %q: %q, %v", p, stop, err)
		}
		if p == "two" && h.Text() != "history: one | two\n" {
			t.Errorf("text = %q", h.Text())
		}
	}
	if pids := e.pids(t); len(pids) != 1 {
		t.Errorf("agent pids %v, want one process", pids)
	}
}

// TestContext: the context block goes before the prompt; each conversation
// passes back the time it got from the previous turn, and a new one (ecdy
// new) starts from zero; an empty block is not sent.
func TestContext(t *testing.T) {
	var (
		mu     sync.Mutex
		sinces []time.Time
		gen    int
	)
	t1 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Echo: true}}}, time.Second, func(o *Options) {
		o.Context = func(cfg config.Config, cwd string, since time.Time) (string, time.Time) {
			mu.Lock()
			defer mu.Unlock()
			sinces = append(sinces, since)
			n := len(sinces)
			if n == 3 {
				return "", since
			}
			return fmt.Sprintf("context %d for %s (%d commands)", n, filepath.Base(cwd), cfg.ContextCommands), t1.Add(time.Duration(n) * time.Minute)
		}
		o.State = func() (State, error) {
			mu.Lock()
			defer mu.Unlock()
			return State{Generation: gen}, nil
		}
	})
	for i, p := range []string{"one", "two", "three", "four"} {
		if i == 3 {
			mu.Lock()
			gen++ // ecdy new
			mu.Unlock()
		}
		h := &handler{}
		if _, err := e.turn(t, p, h).Wait(); err != nil {
			t.Fatalf("turn %q: %v", p, err)
		}
		if h.Text() != p+"\n" {
			t.Errorf("turn %q: the agent echoed %q", p, h.Text())
		}
	}
	base := filepath.Base(e.dir)
	prompts, err := fakeagent.Prompts(e.record)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"context 1 for " + base + " (20 commands)", "one"},
		{"context 2 for " + base + " (20 commands)", "two"},
		{"three"},
		{"context 4 for " + base + " (20 commands)", "four"},
	}
	if fmt.Sprint(prompts) != fmt.Sprint(want) {
		t.Errorf("prompts:\n%q\nwant:\n%q", prompts, want)
	}
	mu.Lock()
	defer mu.Unlock()
	wantSince := []time.Time{{}, t1.Add(time.Minute), t1.Add(2 * time.Minute), {}}
	if fmt.Sprint(sinces) != fmt.Sprint(wantSince) {
		t.Errorf("since = %v\nwant    %v", sinces, wantSince)
	}
}

// A verbose turn gets the agent's stderr.
func TestStderr(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Stderr: "log line\n"}, {SleepMS: 200}, {Text: "done"}}}, time.Second)
	h := &handler{}
	if _, err := e.turn(t, "p", h).Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(h.events, "|"), "stderr: log line") {
		t.Errorf("events = %v", h.events)
	}
}

// TestClientGone: a client that disconnects mid-turn cancels it. An agent
// that ends the turn keeps running; one that ignores session/cancel is
// killed after CancelGrace, 0 included.
func TestClientGone(t *testing.T) {
	tests := []struct {
		name   string
		steps  []fakeagent.Step
		grace  time.Duration
		killed bool
	}{
		{"agent stops", []fakeagent.Step{{Text: "working"}, {WaitCancel: true}}, 5 * time.Second, false},
		{"hung, grace 0", []fakeagent.Step{{Text: "working"}, {Hang: true}}, 0, true},
		{"hung, grace 300ms", []fakeagent.Step{{Text: "working"}, {Hang: true}}, 300 * time.Millisecond, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{Turn: tt.steps}, tt.grace)
			client, server := net.Pipe()
			served := make(chan struct{})
			go func() {
				e.srv.ServeConn(server)
				close(served)
			}()
			h := &handler{}
			if _, err := StartTurn(t.Context(), client, PromptRequest{Prompt: "p", Cwd: e.dir}, h); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the first chunk", func() bool { return h.Text() == "working" })
			start := time.Now()
			_ = client.Close()
			pid := e.pids(t)[0]
			if tt.grace > 0 {
				waitFor(t, "session/cancel", func() bool {
					m := e.methods(t)
					return len(m) > 0 && m[len(m)-1] == "session/cancel"
				})
			}
			if tt.killed {
				waitFor(t, "the agent to be killed", func() bool { return !testutil.Alive(pid) })
				if d := time.Since(start); d < tt.grace {
					t.Errorf("killed after %v, before the grace of %v", d, tt.grace)
				}
			}
			// The turn ends either way; a hung turn would hold the server.
			select {
			case <-served:
			case <-time.After(testutil.DefaultTimeout):
				t.Fatal("the turn did not end after the client left")
			}
			if tt.killed {
				return
			}
			time.Sleep(200 * time.Millisecond)
			if !testutil.Alive(pid) {
				t.Fatal("an agent that ended the turn was killed")
			}
			// Its conversation goes on.
			h2 := &handler{}
			turn := e.turn(t, "q", h2)
			waitFor(t, "the second turn", func() bool { return h2.Text() == "working" })
			turn.Cancel()
			if stop, err := turn.Wait(); err != nil || stop != acp.StopReasonCancelled {
				t.Fatalf("second turn: %q, %v", stop, err)
			}
		})
	}
}

// TestPermission: the client's answer reaches the agent; a request the
// agent withdraws (session/cancel) closes the dialog.
func TestPermission(t *testing.T) {
	steps := []fakeagent.Step{{Text: "why"}, {Permission: &fakeagent.Tool{ID: "t1", Title: "rm x"}}}
	t.Run("answer", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turn: steps}, time.Second)
		h := &handler{choose: func(context.Context) (acp.RequestPermissionOutcome, error) {
			return acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: fakeagent.OptAllow}}, nil
		}}
		if _, err := e.turn(t, "p", h).Wait(); err != nil {
			t.Fatal(err)
		}
		if got := h.Text(); got != "whypermission: allow\n" {
			t.Errorf("text = %q", got)
		}
		// The text before the request is shown before the dialog.
		if strings.Join(h.events, ",") != "text,permission,text" {
			t.Errorf("events = %v", h.events)
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turn: steps}, time.Second)
		h := &handler{choose: func(context.Context) (acp.RequestPermissionOutcome, error) {
			return acp.RequestPermissionOutcome{}, ErrInterrupted
		}}
		stop, err := e.turn(t, "p", h).Wait()
		if err != nil || stop != acp.StopReasonCancelled {
			t.Fatalf("turn: %q, %v", stop, err)
		}
		if m := e.methods(t); m[len(m)-1] != "session/cancel" {
			t.Errorf("requests = %v", m)
		}
	})
	t.Run("withdrawn", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turn: steps}, time.Second)
		h := &handler{asked: make(chan struct{}, 1)} // waits for ctx
		turn := e.turn(t, "p", h)
		<-h.asked
		turn.Cancel()
		stop, err := turn.Wait()
		if err != nil || stop != acp.StopReasonCancelled {
			t.Fatalf("turn: %q, %v", stop, err)
		}
	})
}

func TestBusyAgent(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "working"}, {WaitCancel: true}}}, time.Second)
	h := &handler{}
	first := e.turn(t, "one", h)
	waitFor(t, "the first turn", func() bool { return h.Text() == "working" })
	_, err := e.turn(t, "two", &handler{}).Wait()
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeBusy {
		t.Fatalf("second turn: %v, want busy", err)
	}
	first.Cancel()
	if _, err := first.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolVersion(t *testing.T) {
	e := newEnv(t, fakeagent.Script{}, time.Second)
	client, server := net.Pipe()
	go e.srv.ServeConn(server)
	w := newWire(client)
	if err := w.send(Msg{T: tPrompt, Version: ProtocolVersion + 1, Prompt: "p", Cwd: e.dir}); err != nil {
		t.Fatal(err)
	}
	m, err := w.recv()
	if err != nil || m.T != tError || m.Code != CodeVersion {
		t.Fatalf("reply = %+v, %v", m, err)
	}
	if len(e.methods(t)) != 0 {
		t.Error("the agent was started")
	}
}

// A connection that never sends a request does not keep the server busy
// forever.
func TestSilentClient(t *testing.T) {
	e := newEnv(t, fakeagent.Script{}, time.Second)
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan struct{})
	go func() {
		e.srv.ServeConn(server)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(requestTimeout + 5*time.Second):
		t.Fatal("ServeConn still waits for a request")
	}
}

func TestWireTooLong(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		_, _ = client.Write([]byte(strings.Repeat("x", maxMsg+1)))
		_ = client.Close()
	}()
	if _, err := newWire(server).recv(); !errors.Is(err, errTooLong) {
		t.Fatalf("recv = %v, want errTooLong", err)
	}
}

func TestIdleTimer(t *testing.T) {
	for _, timeout := range []time.Duration{0, 50 * time.Millisecond} {
		it := newIdleTimer(timeout)
		it.add(1) // the first client: the countdown starts when it leaves
		select {
		case <-it.fired:
			t.Fatalf("%v: fired with a client connected", timeout)
		case <-time.After(timeout + 100*time.Millisecond):
		}
		start := time.Now()
		it.add(-1)
		select {
		case <-it.fired:
			if d := time.Since(start); d < timeout {
				t.Errorf("%v: fired after %v", timeout, d)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: did not fire", timeout)
		}
	}
}

func TestWithin(t *testing.T) {
	for _, tt := range []struct {
		dir, root string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/a/..b", "/a", true},
	} {
		if got := within(tt.dir, tt.root); got != tt.want {
			t.Errorf("within(%q, %q) = %v", tt.dir, tt.root, got)
		}
	}
}

// dialogHandler logs when its dialog closes, to check that a withdrawn
// request closes it at once, not at the end of the turn.
type dialogHandler struct{ handler }

func (h *dialogHandler) Permission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	_, err := h.handler.Permission(ctx, req)
	h.mu.Lock()
	h.events = append(h.events, "closed")
	h.mu.Unlock()
	return acp.RequestPermissionOutcome{}, err
}

// An agent that withdraws a permission request and goes on: the dialog
// closes before the rest of the turn.
func TestPermissionWithdrawnByAgent(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{
		{Permission: &fakeagent.Tool{ID: "t1", Title: "rm x"}, WithdrawMS: 200},
		{SleepMS: 300},
		{Text: "after"},
	}}, time.Second)
	h := &dialogHandler{}
	stop, err := e.turn(t, "p", h).Wait()
	if err != nil || stop != acp.StopReasonEndTurn {
		t.Fatalf("turn: %q, %v", stop, err)
	}
	if got := h.Text(); got != "permission: withdrawn\nafter" {
		t.Errorf("text = %q", got)
	}
	// The dialog closes when the request is withdrawn, not at the end of
	// the turn: before "after", sent 300 ms later. Relative to the
	// "withdrawn" text it may go either way (the renderer is paused while
	// a dialog is open, so the terminal shows the text after it).
	if got := strings.Join(h.events, ","); got != "permission,closed,text,text" && got != "permission,text,closed,text" {
		t.Errorf("events = %s, want the dialog closed before the last text", got)
	}
}
