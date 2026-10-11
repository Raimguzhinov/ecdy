package daemon

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/Raimguzhinov/ecdy/internal/acpclient"
	"github.com/Raimguzhinov/ecdy/internal/testutil"
	"github.com/Raimguzhinov/ecdy/internal/testutil/fakeagent"
)

// wait waits for the end of turn, failing the test if it does not end.
func wait(t *testing.T, turn *Turn) (acp.StopReason, error) {
	t.Helper()
	type result struct {
		stop acp.StopReason
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		stop, err := turn.Wait()
		ch <- result{stop, err}
	}()
	select {
	case r := <-ch:
		return r.stop, r.err
	case <-time.After(testutil.DefaultTimeout):
		t.Fatal("the turn did not end")
		return "", nil
	}
}

// The client hears what the turn waits for before the agent's first update:
// starting the agent (or a new session), then the reply.
func TestPhases(t *testing.T) {
	var (
		mu  sync.Mutex
		gen int
	)
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Text: "hi"}}}, time.Second, func(o *Options) {
		o.State = func() (State, error) {
			mu.Lock()
			defer mu.Unlock()
			return State{Generation: gen}, nil
		}
	})
	starting := []string{"starting fake after 0 texts", "waiting fake after 0 texts"}
	for i, want := range [][]string{starting, starting[1:], starting} {
		if i == 2 {
			mu.Lock()
			gen++ // ecdy new
			mu.Unlock()
		}
		h := &handler{}
		if _, err := wait(t, e.turn(t, "p", h)); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(h.phases) != fmt.Sprint(want) {
			t.Errorf("turn %d: phases %q, want %q", i, h.phases, want)
		}
	}
}

// A verbose turn gets what the agent writes to stderr while it starts.
func TestStartupStderr(t *testing.T) {
	e := newEnv(t, fakeagent.Script{StartStderr: "npm warn exec\n", StartDelayMS: 200, Turn: []fakeagent.Step{{Text: "hi"}}}, time.Second)
	h := &handler{}
	if _, err := wait(t, e.turn(t, "p", h)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(h.events, "|"), "stderr: npm warn exec") {
		t.Errorf("events = %v", h.events)
	}
}

// auth_required at session/prompt (an expired login) gets the login hint,
// and the agent is stopped: the next prompt starts it with the new login.
func TestPromptAuthRequired(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{AuthRequired: true}}}, time.Second)
	_, err := wait(t, e.turn(t, "p", &handler{}))
	want := "fake: agent requires authentication\n  log in with the agent's own CLI, then try again"
	if err == nil || err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
	pid := e.pids(t)[0]
	waitFor(t, "the agent to stop", func() bool { return !testutil.Alive(pid) })
}

// An error answer is followed by the last 10 lines of the agent's stderr,
// redacted.
func TestRequestErrorStderr(t *testing.T) {
	var stderr strings.Builder
	for i := 1; i <= 11; i++ {
		fmt.Fprintf(&stderr, "line %02d\n", i)
	}
	stderr.WriteString("retrying with x-api-key: sk-ant-api03-abcdefghijklmnopqrstuvwxyz\n")
	e := newEnv(t, fakeagent.Script{Turn: []fakeagent.Step{{Stderr: stderr.String()}, {Error: "API Error: 529 overloaded"}}}, time.Second)
	_, err := wait(t, e.turn(t, "p", &handler{}))
	if err == nil {
		t.Fatal("the turn did not fail")
	}
	msg := err.Error()
	for _, want := range []string{"fake: session/prompt: Internal error: API Error: 529 overloaded\n  line 03\n", "\n  retrying with x-api-key: [REDACTED]"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "line 02") || strings.Contains(msg, "abcdefghij") {
		t.Errorf("error:\n%s", msg)
	}
}

func TestDescribe(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	tests := []struct {
		err  error
		want string
	}{
		{&acpclient.ExitError{Stderr: "token " + token + "\n"}, "x: agent exited\n  token [REDACTED]"},
		{&acpclient.RequestError{Method: "initialize", Err: acp.NewInternalError(nil), Stderr: "a\nb\n"}, "x: initialize: Internal error\n  a\n  b"},
		{&acpclient.RequestError{Method: "initialize", Err: acp.NewInternalError(nil)}, "x: initialize: Internal error"},
		{&acpclient.AuthError{}, "x: agent requires authentication\n  log in with the agent's own CLI, then try again"},
	}
	for _, tt := range tests {
		if got := describe("x", tt.err); got != tt.want {
			t.Errorf("describe(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

// syncBuf is a bytes.Buffer safe for concurrent use.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p) //nolint:wrapcheck // a transparent writer
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The agent's stderr goes to the log, line by line and redacted, even
// when the turn is not verbose.
func TestStderrLogged(t *testing.T) {
	var log syncBuf
	token := "sk-ant-api03-" + strings.Repeat("x", 30)
	e := newEnv(t, fakeagent.Script{
		StartStderr: "starting\n",
		Turn:        []fakeagent.Step{{Stderr: "auth " + token + "\nhalf"}, {SleepMS: 50}, {Stderr: " line\n"}, {Text: "hi"}},
	}, time.Second, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&log, nil))
	})
	client := &handler{}
	conn, server := net.Pipe()
	go e.srv.ServeConn(server)
	turn, err := StartTurn(t.Context(), conn, PromptRequest{Prompt: "p", Cwd: e.dir}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wait(t, turn); err != nil {
		t.Fatal(err)
	}
	want := []string{`line=starting`, `line="auth [REDACTED]"`, `line="half line"`}
	waitFor(t, "the log", func() bool { return strings.Contains(log.String(), want[2]) })
	for _, w := range want {
		if !strings.Contains(log.String(), `msg="agent stderr" agent=fake `+w) {
			t.Errorf("log lacks %s:\n%s", w, log.String())
		}
	}
	if strings.Contains(log.String(), token) {
		t.Errorf("the token reached the log:\n%s", log.String())
	}
	if len(client.events) > 1 {
		t.Errorf("a turn that is not verbose got %v", client.events)
	}
}

// A line longer than maxStderrLine is cut, and the rest of it dropped.
func TestStderrLogBounded(t *testing.T) {
	var log syncBuf
	a := &agent{name: "x", s: NewServer(Options{Logger: slog.New(slog.NewTextHandler(&log, nil))})}
	_, _ = a.Write([]byte(strings.Repeat("a", maxStderrLine-1)))
	_, _ = a.Write([]byte(strings.Repeat("b", 3000)))
	_, _ = a.Write([]byte("c\nnext\n"))
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log:\n%s", log.String())
	}
	if want := "line=" + strings.Repeat("a", maxStderrLine-1) + "b cut=true"; !strings.HasSuffix(lines[0], want) {
		t.Errorf("first line %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "line=next") {
		t.Errorf("second line %q", lines[1])
	}

	// Without a logger nothing is kept.
	a = &agent{name: "x", s: NewServer(Options{})}
	_, _ = a.Write([]byte("no newline"))
	if len(a.errLine) != 0 {
		t.Errorf("kept %d bytes without a logger", len(a.errLine))
	}
}
