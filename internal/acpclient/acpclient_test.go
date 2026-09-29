package acpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

func TestMain(m *testing.M) {
	if fakeagent.Enabled() {
		fakeagent.Main()
	}
	os.Exit(m.Run())
}

// recorder is a Handler that records updates and answers permission
// requests with choose.
type recorder struct {
	mu      sync.Mutex
	text    strings.Builder
	updates []acp.SessionUpdate
	asked   []acp.RequestPermissionRequest
	choose  func(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error)
	delay   time.Duration // before handling each update: a slow terminal
}

func (r *recorder) SessionUpdate(_ context.Context, u acp.SessionUpdate) {
	time.Sleep(r.delay)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, u)
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
		r.text.WriteString(u.AgentMessageChunk.Content.Text.Text)
	}
}

func (r *recorder) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	r.mu.Lock()
	r.asked = append(r.asked, req)
	choose := r.choose
	r.mu.Unlock()
	if choose == nil {
		return acp.RequestPermissionOutcome{}, errors.New("unexpected permission request")
	}
	return choose(ctx, req)
}

func (r *recorder) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String()
}

func selected(id string) acp.RequestPermissionOutcome {
	return acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: acp.PermissionOptionId(id)}}
}

type env struct {
	dir    string
	record string
	opts   acpclient.Options
}

func newEnv(t *testing.T, s fakeagent.Script) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{dir: dir, record: filepath.Join(dir, "record.jsonl")}
	s.Record = e.record
	scriptEnv, err := fakeagent.WriteScript(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	e.opts = acpclient.Options{
		Command:   []string{os.Args[0]},
		Dir:       dir,
		Env:       append(os.Environ(), scriptEnv),
		KillGrace: 5 * time.Second,
	}
	return e
}

func (e *env) start(t *testing.T, h acpclient.Handler) *acpclient.Conn {
	t.Helper()
	c, err := acpclient.Start(t.Context(), e.opts, h)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(c.Kill)
	return c
}

func (e *env) methods(t *testing.T) []string {
	t.Helper()
	reqs, err := fakeagent.ReadRecord(e.record)
	if err != nil {
		t.Fatal(err)
	}
	var m []string
	for _, r := range reqs {
		m = append(m, r.Method)
	}
	return m
}

func TestTextStreaming(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "Hel"}, {Text: "lo"}, {Text: ", world\n"}}})
	h := &recorder{}
	c := e.start(t, h)
	stop, err := c.Prompt(t.Context(), "say hello")
	if err != nil || stop != acp.StopReasonEndTurn {
		t.Fatalf("Prompt = %q, %v", stop, err)
	}
	if got := h.Text(); got != "Hello, world\n" {
		t.Errorf("text = %q", got)
	}
	reqs, _ := fakeagent.ReadRecord(e.record)
	if len(reqs) != 3 {
		t.Fatalf("requests = %v", e.methods(t))
	}
	for _, want := range []string{`"cwd":"` + e.dir + `"`, `"mcpServers":[]`} {
		if !strings.Contains(string(reqs[1].Params), want) {
			t.Errorf("session/new params %s lack %s", reqs[1].Params, want)
		}
	}
	if !strings.Contains(string(reqs[0].Params), `"name":"ecdy"`) {
		t.Errorf("initialize params %s lack clientInfo", reqs[0].Params)
	}
	if !strings.Contains(string(reqs[2].Params), `"text":"say hello"`) {
		t.Errorf("prompt params %s lack the prompt", reqs[2].Params)
	}
}

// TestNoClientCapabilities: ecdy declares no fs/* and no terminal/* (ADR
// 0005).
func TestNoClientCapabilities(t *testing.T) {
	e := newEnv(t, fakeagent.Script{})
	e.start(t, &recorder{})
	reqs, err := fakeagent.ReadRecord(e.record)
	if err != nil || len(reqs) == 0 || reqs[0].Method != acp.AgentMethodInitialize {
		t.Fatalf("requests = %v, %v", e.methods(t), err)
	}
	var init acp.InitializeRequest
	if err := json.Unmarshal(reqs[0].Params, &init); err != nil {
		t.Fatal(err)
	}
	if caps := init.ClientCapabilities; caps.Fs.ReadTextFile || caps.Fs.WriteTextFile || caps.Terminal {
		t.Errorf("client capabilities = %s", reqs[0].Params)
	}
}

// TestUndeclaredCalls: an agent that calls fs/* or terminal/* anyway (opencode
// does) gets method not found, nothing is read, written or run, and the turn
// goes on.
func TestUndeclaredCalls(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	written, touched := filepath.Join(dir, "written"), filepath.Join(dir, "touched")
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{
		{Call: acp.ClientMethodFsReadTextFile, Path: secret},
		{Call: acp.ClientMethodFsWriteTextFile, Path: written},
		{Call: acp.ClientMethodTerminalCreate, Path: touched},
		{Text: "still here\n"},
	}})
	h := &recorder{}
	c := e.start(t, h)
	stop, err := c.Prompt(t.Context(), "try")
	if err != nil || stop != acp.StopReasonEndTurn {
		t.Fatalf("Prompt = %q, %v", stop, err)
	}
	want := "call fs/read_text_file: error -32601\n" +
		"call fs/write_text_file: error -32601\n" +
		"call terminal/create: error -32601\n" +
		"still here\n"
	if got := h.Text(); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	for _, p := range []string{written, touched} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists (%v): the call was carried out", p, err)
		}
	}
}

func TestToolCall(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{
		{Tool: &fakeagent.Tool{ID: "t1", Title: "Read main.go", Kind: "read"}},
		{ToolDone: &fakeagent.Tool{ID: "t1", Status: "completed", Output: "package main"}},
	}})
	h := &recorder{}
	c := e.start(t, h)
	if _, err := c.Prompt(t.Context(), "read"); err != nil {
		t.Fatal(err)
	}
	if len(h.updates) != 2 || h.updates[0].ToolCall == nil || h.updates[1].ToolCallUpdate == nil {
		t.Fatalf("updates = %+v", h.updates)
	}
	if tc := h.updates[0].ToolCall; tc.Title != "Read main.go" || tc.Kind != acp.ToolKindRead {
		t.Errorf("tool call = %+v", tc)
	}
	if st := h.updates[1].ToolCallUpdate.Status; st == nil || *st != acp.ToolCallStatusCompleted {
		t.Errorf("tool call update status = %v", st)
	}
}

func TestPermission(t *testing.T) {
	for _, opt := range []string{fakeagent.OptAllow, fakeagent.OptReject} {
		t.Run(opt, func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Permission: &fakeagent.Tool{ID: "t1", Title: "rm -rf build", Kind: "execute"}}}})
			h := &recorder{choose: func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
				return selected(opt), nil
			}}
			c := e.start(t, h)
			stop, err := c.Prompt(t.Context(), "clean")
			if err != nil || stop != acp.StopReasonEndTurn {
				t.Fatalf("Prompt = %q, %v", stop, err)
			}
			if got, want := h.Text(), "permission: "+opt+"\n"; got != want {
				t.Errorf("agent saw %q, want %q", got, want)
			}
			if len(h.asked) != 1 || *h.asked[0].ToolCall.Title != "rm -rf build" || len(h.asked[0].Options) != 3 {
				t.Errorf("asked = %+v", h.asked)
			}
		})
	}
}

// Text the agent sent before a permission request is rendered before the
// dialog: it usually explains the request.
func TestPermissionAfterText(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "I need to clean the build. "}, {Permission: &fakeagent.Tool{ID: "t1", Title: "rm -rf build"}}}})
	var h *recorder
	h = &recorder{delay: 200 * time.Millisecond, choose: func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
		if got := h.Text(); got != "I need to clean the build. " {
			t.Errorf("text before the dialog = %q", got)
		}
		return selected(fakeagent.OptAllow), nil
	}}
	c := e.start(t, h)
	if _, err := c.Prompt(t.Context(), "clean"); err != nil {
		t.Fatal(err)
	}
}

// An update the SDK cannot decode delays the dialog by no more than the
// drain timeout.
func TestPermissionAfterBogusUpdate(t *testing.T) {
	bogus := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":5}}`
	for _, d := range []time.Duration{0, 300 * time.Millisecond} {
		t.Run(d.String(), func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Raw: bogus}, {Permission: &fakeagent.Tool{ID: "t1", Title: "x"}}}})
			var asked time.Time
			h := &recorder{choose: func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
				asked = time.Now()
				return selected(fakeagent.OptAllow), nil
			}}
			c := e.start(t, h)
			acpclient.SetDrainTimeout(c, d)
			start := time.Now()
			if _, err := c.Prompt(t.Context(), "x"); err != nil {
				t.Fatal(err)
			}
			if el := asked.Sub(start); el < d || el > d+time.Second {
				t.Errorf("the dialog opened after %v with drain timeout %v", el, d)
			}
		})
	}
}

func TestPermissionHandlerError(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Permission: &fakeagent.Tool{ID: "t1", Title: "x"}}}})
	c := e.start(t, &recorder{}) // no choose: the handler fails
	if _, err := c.Prompt(t.Context(), "x"); err == nil {
		t.Fatal("Prompt succeeded although permission was never granted")
	}
}

func TestCancel(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "working\n"}, {WaitCancel: true}}})
	h := &recorder{}
	c := e.start(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	stop, err := c.Prompt(ctx, "wait")
	if err != nil || stop != acp.StopReasonCancelled {
		t.Fatalf("Prompt = %q, %v; want cancelled", stop, err)
	}
	if m := e.methods(t); m[len(m)-1] != acp.AgentMethodSessionCancel {
		t.Errorf("requests = %v, want session/cancel last", m)
	}
}

// Cancelling while a permission dialog is open answers it with the cancelled
// outcome, and the dialog sees its context cancelled.
func TestCancelDuringPermission(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Permission: &fakeagent.Tool{ID: "t1", Title: "x"}}}})
	ctx, cancel := context.WithCancel(t.Context())
	h := &recorder{choose: func(dctx context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
		cancel()
		<-dctx.Done()
		return acp.RequestPermissionOutcome{}, dctx.Err()
	}}
	c := e.start(t, h)
	stop, err := c.Prompt(ctx, "x")
	if err != nil || stop != acp.StopReasonCancelled {
		t.Fatalf("Prompt = %q, %v; want cancelled", stop, err)
	}
	if got := h.Text(); got != "permission: cancelled\n" {
		t.Errorf("agent saw %q", got)
	}
}

// The dialog is closed on session/cancel even if the agent does not cancel
// its request ($/cancel_request).
func TestCancelDuringPermissionNoCancelRequest(t *testing.T) {
	e := newEnv(t, fakeagent.Script{NoCancelRequest: true, Turn: []fakeagent.Step{{Permission: &fakeagent.Tool{ID: "t1", Title: "x"}}}})
	ctx, cancel := context.WithCancel(t.Context())
	h := &recorder{choose: func(dctx context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
		cancel()
		select {
		case <-dctx.Done():
			return acp.RequestPermissionOutcome{}, dctx.Err()
		case <-time.After(5 * time.Second):
			t.Error("the dialog stayed open after session/cancel")
			return selected(fakeagent.OptAllow), nil
		}
	}}
	c := e.start(t, h)
	stop, err := c.Prompt(ctx, "x")
	if err != nil || stop != acp.StopReasonCancelled {
		t.Fatalf("Prompt = %q, %v; want cancelled", stop, err)
	}
	if got := h.Text(); got != "permission: cancelled\n" {
		t.Errorf("agent saw %q", got)
	}
}

// A permission request that arrives after session/cancel is answered
// cancelled without asking the user.
func TestPermissionAfterCancel(t *testing.T) {
	e := newEnv(t, fakeagent.Script{NoCancelRequest: true, Turn: []fakeagent.Step{{AwaitCancel: true}, {Permission: &fakeagent.Tool{ID: "t1", Title: "x"}}}})
	h := &recorder{choose: func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
		return selected(fakeagent.OptAllow), nil
	}}
	c := e.start(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	stop, err := c.Prompt(ctx, "x")
	if err != nil || stop != acp.StopReasonCancelled {
		t.Fatalf("Prompt = %q, %v; want cancelled", stop, err)
	}
	if len(h.asked) != 0 || h.Text() != "permission: cancelled\n" {
		t.Errorf("asked %d times, agent saw %q", len(h.asked), h.Text())
	}
}

// Ctrl+C inside the permission dialog cancels the whole turn.
func TestInterruptInPermission(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Permission: &fakeagent.Tool{ID: "t1", Title: "x"}}, {Text: "not reached"}}})
	h := &recorder{choose: func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
		return acp.RequestPermissionOutcome{}, acpclient.ErrInterrupted
	}}
	c := e.start(t, h)
	stop, err := c.Prompt(t.Context(), "x")
	if err != nil || stop != acp.StopReasonCancelled {
		t.Fatalf("Prompt = %q, %v; want cancelled", stop, err)
	}
	if got := h.Text(); got != "permission: cancelled\n" {
		t.Errorf("agent saw %q", got)
	}
	if m := e.methods(t); !slices.Contains(m, acp.AgentMethodSessionCancel) {
		t.Errorf("requests = %v, want session/cancel", m)
	}
}

// An agent that ignores session/cancel keeps Prompt waiting until Kill.
func TestKillHungTurn(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Hang: true}}})
	c := e.start(t, &recorder{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Prompt(ctx, "x")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Prompt returned %v before Kill", err)
	case <-time.After(200 * time.Millisecond):
	}
	c.Kill()
	select {
	case err := <-done:
		var ee *acpclient.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("Prompt after Kill = %v, want ExitError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt still blocked after Kill")
	}
}

func TestAgentCrash(t *testing.T) {
	code := 3
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "partial"}, {Stderr: "panic: boom\n"}, {Exit: &code}}})
	h := &recorder{}
	c := e.start(t, h)
	start := time.Now()
	_, err := c.Prompt(t.Context(), "x")
	if d := time.Since(start); d > time.Second {
		t.Errorf("the crash was reported after %v; nothing holds the pipes, so no drain timeout applies", d)
	}
	var ee *acpclient.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Prompt = %v, want ExitError", err)
	}
	var xe *exec.ExitError
	if !errors.As(err, &xe) || xe.ExitCode() != 3 {
		t.Errorf("exit error = %v, want exit status 3", ee.Err)
	}
	if !strings.Contains(ee.Stderr, "panic: boom") || !strings.Contains(c.Stderr(), "panic: boom") {
		t.Errorf("stderr = %q", ee.Stderr)
	}
	if h.Text() != "partial" {
		t.Errorf("text before the crash = %q", h.Text())
	}
}

// A session/update the SDK cannot decode is never handled: the wait for the
// last updates before a crash is bounded.
func TestAgentCrashDrainTimeout(t *testing.T) {
	code := 3
	bogus := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":5}}`
	for _, d := range []time.Duration{0, 300 * time.Millisecond} {
		t.Run(d.String(), func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Raw: bogus}, {Exit: &code}}})
			c := e.start(t, &recorder{})
			acpclient.SetDrainTimeout(c, d)
			start := time.Now()
			_, err := c.Prompt(t.Context(), "x")
			el := time.Since(start)
			var ee *acpclient.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("Prompt = %v, want ExitError", err)
			}
			if el < d || el > d+time.Second {
				t.Errorf("Prompt took %v with drain timeout %v", el, d)
			}
		})
	}
}

// A process outside the agent's group (setsid) that keeps its stdout and
// stderr open delays the error by the drain timeout (twice at most: stdout,
// then stderr).
func TestAgentCrashStderrHeld(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	code := 3
	for _, d := range []time.Duration{0, 300 * time.Millisecond} {
		t.Run(d.String(), func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Exit: &code}}})
			pidFile := filepath.Join(e.dir, "holder.pid")
			e.opts.Command = []string{"sh", "-c", `setsid sh -c 'echo $$ > "$1"; exec sleep 60' sh "$1" & exec "$0"`, os.Args[0], pidFile}
			c := e.start(t, &recorder{})
			t.Cleanup(func() {
				if b, err := os.ReadFile(pidFile); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			acpclient.SetDrainTimeout(c, d)
			start := time.Now()
			_, err := c.Prompt(t.Context(), "x")
			el := time.Since(start)
			var ee *acpclient.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("Prompt = %v, want ExitError", err)
			}
			if el < d || el > d+time.Second {
				t.Errorf("Prompt took %v with drain timeout %v", el, d)
			}
		})
	}
}

func TestAgentExitsAtStart(t *testing.T) {
	e := newEnv(t, fakeagent.Script{})
	e.opts.Env = append(os.Environ(), fakeagent.EnvScript+"="+filepath.Join(e.dir, "missing.json"))
	_, err := acpclient.Start(t.Context(), e.opts, &recorder{})
	var ee *acpclient.ExitError
	if !errors.As(err, &ee) || !strings.Contains(ee.Stderr, "missing.json") {
		t.Fatalf("Start = %v, want ExitError with the agent's stderr", err)
	}
}

func TestCommandNotFound(t *testing.T) {
	e := newEnv(t, fakeagent.Script{})
	e.opts.Command = []string{filepath.Join(e.dir, "no-such-agent")}
	if _, err := acpclient.Start(t.Context(), e.opts, &recorder{}); err == nil || !strings.Contains(err.Error(), "no-such-agent") {
		t.Fatalf("Start = %v", err)
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t, fakeagent.Script{AuthRequired: true})
	_, err := acpclient.Start(t.Context(), e.opts, &recorder{})
	var ae *acpclient.AuthError
	if !errors.As(err, &ae) || len(ae.Methods) != 2 {
		t.Fatalf("Start = %v, want AuthError with 2 methods", err)
	}
	if !strings.Contains(err.Error(), "Log in in a terminal") {
		t.Errorf("error %q lacks the method names", err)
	}
}

func TestRequestErrorMessage(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&acp.RequestError{Code: -32603, Message: "Internal error", Data: map[string]any{"details": "discovery timed out"}}, "session/new: Internal error: discovery timed out"},
		{&acp.RequestError{Code: -32603, Message: "Internal error", Data: "boom"}, "session/new: Internal error: boom"},
		{&acp.RequestError{Code: -32601, Message: "Method not found"}, "session/new: Method not found"},
		{errors.New("plain"), "session/new: plain"},
	}
	for _, tt := range tests {
		if got := (&acpclient.RequestError{Method: "session/new", Err: tt.err}).Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}

func TestProtocolVersionMismatch(t *testing.T) {
	e := newEnv(t, fakeagent.Script{ProtocolVersion: 99})
	if _, err := acpclient.Start(t.Context(), e.opts, &recorder{}); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("Start = %v", err)
	}
}

func TestStartCancelled(t *testing.T) {
	e := newEnv(t, fakeagent.Script{})
	// An agent that never answers initialize.
	e.opts.Command = []string{"sleep", "60"}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := acpclient.Start(ctx, e.opts, &recorder{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}
}

func TestRelativeDir(t *testing.T) {
	e := newEnv(t, fakeagent.Script{})
	e.opts.Dir = "relative"
	if _, err := acpclient.Start(t.Context(), e.opts, &recorder{}); err == nil {
		t.Fatal("Start accepted a relative directory")
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestCloseGraceful(t *testing.T) {
	e := newEnv(t, fakeagent.Script{})
	c := e.start(t, &recorder{})
	start := time.Now()
	c.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Close of a well-behaved agent took %v", d)
	}
}

// An agent that ignores SIGTERM is killed after KillGrace; with 0 at once.
func TestCloseKillGrace(t *testing.T) {
	for _, grace := range []time.Duration{0, 300 * time.Millisecond} {
		t.Run(grace.String(), func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{IgnoreTerm: true})
			e.opts.KillGrace = grace
			// The fake agent exits on stdin EOF; the shell then runs a
			// sleep, and both ignore SIGTERM, as a hung agent would.
			e.opts.Command = []string{"sh", "-c", `trap "" TERM; "$0"; sleep 60`, os.Args[0]}
			c := e.start(t, &recorder{})
			pids := groupPids(t, c)
			start := time.Now()
			c.Close()
			d := time.Since(start)
			if d < grace || d > grace+2*time.Second {
				t.Errorf("Close took %v with KillGrace %v", d, grace)
			}
			waitGone(t, pids)
		})
	}
}

// Kill takes the agent's children with it, so nothing is orphaned.
func TestKillGroup(t *testing.T) {
	e := newEnv(t, fakeagent.Script{})
	e.opts.Command = []string{"sh", "-c", `"$0"; sleep 60`, os.Args[0]}
	c := e.start(t, &recorder{})
	pids := groupPids(t, c)
	c.Kill()
	waitGone(t, pids)
}

// groupPids lists the members of the agent's process group (Linux /proc).
func groupPids(t *testing.T, c *acpclient.Conn) []int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	pgid := c.Pid()
	var pids []int
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if g, err := syscall.Getpgid(pid); err == nil && g == pgid {
			pids = append(pids, pid)
		}
	}
	if len(pids) < 2 {
		t.Fatalf("process group %d has %v, want the shell and the agent", pgid, pids)
	}
	return pids
}

func waitGone(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for alive(pid) && !zombie(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d still alive", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// zombie reports whether pid has exited and waits to be reaped by init.
func zombie(pid int) bool {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(stat), ')')
	return i >= 0 && strings.HasPrefix(string(stat[i+1:]), " Z")
}

// Turns on one Conn continue the conversation; NewSession starts a new one
// in the given directory, and the updates of the old session are ignored.
func TestNewSession(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{History: true}}})
	h := &recorder{}
	c := e.start(t, h)
	first := c.Session()
	for _, p := range []string{"one", "two"} {
		if stop, err := c.Prompt(t.Context(), p); err != nil || stop != acp.StopReasonEndTurn {
			t.Fatalf("Prompt(%q) = %q, %v", p, stop, err)
		}
	}
	sub := filepath.Join(e.dir, "sub")
	if err := c.NewSession(t.Context(), sub); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if c.Session() == first {
		t.Errorf("session id %q did not change", first)
	}
	if stop, err := c.Prompt(t.Context(), "three"); err != nil || stop != acp.StopReasonEndTurn {
		t.Fatalf("Prompt = %q, %v", stop, err)
	}
	if got, want := h.Text(), "history: one\nhistory: one | two\nhistory: three\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	reqs, _ := fakeagent.ReadRecord(e.record)
	var news []string
	for _, r := range reqs {
		if r.Method == "session/new" {
			news = append(news, string(r.Params))
		}
	}
	if len(news) != 2 || !strings.Contains(news[1], `"cwd":"`+sub+`"`) {
		t.Errorf("session/new requests = %v", news)
	}
	if err := c.NewSession(t.Context(), "rel"); err == nil {
		t.Error("NewSession with a relative directory: expected an error")
	}
}

func TestDone(t *testing.T) {
	code := 0
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Exit: &code}}})
	c := e.start(t, &recorder{})
	select {
	case <-c.Done():
		t.Fatal("Done closed before the agent exited")
	default:
	}
	if _, err := c.Prompt(t.Context(), "exit"); err == nil {
		t.Fatal("Prompt: expected an error")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after the agent exited")
	}
}
