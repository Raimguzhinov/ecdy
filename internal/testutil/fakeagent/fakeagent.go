// Package fakeagent is a scriptable ACP agent for tests. A test binary runs it
// as a child process by re-executing itself:
//
//	func TestMain(m *testing.M) {
//		if fakeagent.Enabled() {
//			fakeagent.Main()
//		}
//		os.Exit(m.Run())
//	}
//
// and uses os.Args[0] as the agent command, with EnvScript naming a JSON
// Script in the environment.
package fakeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// EnvScript is the environment variable holding the path of the Script.
const EnvScript = "ECDY_FAKE_AGENT"

// Permission option ids offered by a Permission step.
const (
	OptAllow  = "allow"
	OptAlways = "always"
	OptReject = "reject"
)

// Script drives the agent.
type Script struct {
	// ProtocolVersion is returned from initialize; 0 means the SDK's version.
	ProtocolVersion int `json:"protocol_version,omitempty"`
	// AuthRequired makes session/new fail with auth_required and advertises
	// AuthMethods in initialize.
	AuthRequired bool `json:"auth_required,omitempty"`
	// IgnoreTerm makes the agent ignore SIGTERM, as a hung agent would.
	IgnoreTerm bool `json:"ignore_term,omitempty"`
	// NoCancelRequest makes permission requests ignore session/cancel: the
	// agent never sends $/cancel_request for them (an unstable part of the
	// protocol that not every agent implements).
	NoCancelRequest bool `json:"no_cancel_request,omitempty"`
	// ExitDelayMS delays the exit after stdin is closed, as a slow agent
	// shutting down would.
	ExitDelayMS int `json:"exit_delay_ms,omitempty"`
	// Record, if set, is a file the agent appends every request it receives
	// to, one JSON object ({"method": ..., "params": ...}) per line.
	Record string `json:"record,omitempty"`
	// Turn is played on every session/prompt.
	Turn []Step `json:"turn"`
}

// Step is one action of a prompt turn. Exactly one field is set.
type Step struct {
	Text string `json:"text,omitempty"` // agent_message_chunk
	Echo bool   `json:"echo,omitempty"` // the prompt's text and "\n" as a chunk
	// History sends "history: " and the prompts of this session so far,
	// this one included, joined by " | ", and "\n": what the agent
	// remembers of the conversation.
	History bool   `json:"history,omitempty"`
	Thought string `json:"thought,omitempty"` // agent_thought_chunk
	Stderr  string `json:"stderr,omitempty"`  // written to stderr
	Raw     string `json:"raw,omitempty"`     // written to stdout as one line, bypassing the SDK
	SleepMS int    `json:"sleep_ms,omitempty"`

	// Tool starts a tool call (status pending).
	Tool *Tool `json:"tool,omitempty"`
	// ToolDone finishes the tool call Tool.ID with Tool.Status.
	ToolDone *Tool `json:"tool_done,omitempty"`
	// Permission asks for permission for tool call Tool.ID with the options
	// OptAllow, OptAlways, OptReject, then sends a text chunk
	// "permission: <option id or cancelled>\n". A cancelled outcome ends the
	// turn with stop reason cancelled.
	Permission *Tool `json:"permission,omitempty"`
	// WithdrawMS, with Permission, withdraws the request ($/cancel_request)
	// if it is not answered within that many milliseconds; the agent then
	// sends "permission: withdrawn\n" and goes on with the turn.
	WithdrawMS int `json:"withdraw_ms,omitempty"`
	// Call calls a client method the client did not declare (fs/*,
	// terminal/*), as some agents do, with Path as its file (terminal/create
	// runs "touch Path"), then sends a text chunk "call <method>: ok\n" or
	// "call <method>: error <code>\n".
	Call string `json:"call,omitempty"`
	Path string `json:"path,omitempty"`
	// TerminalOutput sends what a command printed the way codex-acp and
	// pi-acp do: a tool_call_update whose _meta holds
	// {Key: {"terminal_id": ID, "data": Data}}.
	TerminalOutput *TerminalOutput `json:"terminal_output,omitempty"`
	// WaitCancel blocks until session/cancel and ends the turn with stop
	// reason cancelled.
	WaitCancel bool `json:"wait_cancel,omitempty"`
	// AwaitCancel blocks until session/cancel, then goes on with the turn.
	AwaitCancel bool `json:"await_cancel,omitempty"`
	// Hang blocks forever, ignoring session/cancel.
	Hang bool `json:"hang,omitempty"`
	// Exit exits the process with this code (a crash mid-turn).
	Exit *int `json:"exit,omitempty"`
	// Stop ends the turn with this stop reason.
	Stop string `json:"stop,omitempty"`
}

// Tool describes a tool call.
type Tool struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`
	Output string `json:"output,omitempty"` // text content of a finished tool call
	// Terminal, with ToolDone, sets the content to a terminal block with the
	// tool call's id, as agents that stream output in _meta do.
	Terminal bool `json:"terminal,omitempty"`
}

// TerminalOutput is a piece of command output sent in _meta.
type TerminalOutput struct {
	ID   string `json:"id"`
	Key  string `json:"key"` // "terminal_output" or "terminal_output_delta"
	Data string `json:"data"`
}

// Enabled reports whether the current process was started as the fake agent.
func Enabled() bool { return os.Getenv(EnvScript) != "" }

// Main runs the agent on stdin and stdout with the script named by EnvScript
// and exits.
func Main() {
	data, err := os.ReadFile(os.Getenv(EnvScript))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(2)
	}
	var s Script
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(2)
	}
	if s.IgnoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	a := &agent{script: s}
	// The SDK starts reading in the constructor, before it has stored its
	// own fields; a.conn and SetLogger come after it. Nothing is read
	// until all three are done, or the handlers race with them (seen under
	// -race: a.conn in Prompt, the SDK's conn in RequestPermission).
	ready := make(chan struct{})
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, gatedReader{os.Stdin, ready})
	a.conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	close(ready)
	<-a.conn.Done()
	time.Sleep(time.Duration(s.ExitDelayMS) * time.Millisecond)
	os.Exit(0)
}

// gatedReader reads from r once ready is closed.
type gatedReader struct {
	r     io.Reader
	ready <-chan struct{}
}

func (g gatedReader) Read(p []byte) (int, error) {
	<-g.ready
	return g.r.Read(p) //nolint:wrapcheck // a transparent reader: the SDK expects io.EOF as is
}

// WriteScript writes s to a file in dir and returns the environment entry
// that makes a re-executed test binary run it.
func WriteScript(dir string, s Script) (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal script: %w", err)
	}
	f, err := os.CreateTemp(dir, "fakeagent-*.json")
	if err != nil {
		return "", fmt.Errorf("write script: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write script: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write script: %w", err)
	}
	return EnvScript + "=" + f.Name(), nil
}

// Request is one line of the Record file.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Pid    int             `json:"pid"` // of the agent process that received it
}

// Prompts returns the text blocks of every session/prompt recorded to path.
func Prompts(path string) ([][]string, error) {
	reqs, err := ReadRecord(path)
	var prompts [][]string
	for _, r := range reqs {
		if r.Method != acp.AgentMethodSessionPrompt {
			continue
		}
		var p acp.PromptRequest
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return prompts, fmt.Errorf("decode session/prompt: %w", err)
		}
		var texts []string
		for _, b := range p.Prompt {
			if b.Text != nil {
				texts = append(texts, b.Text.Text)
			}
		}
		prompts = append(prompts, texts)
	}
	return prompts, err
}

// ReadRecord reads the requests recorded to path.
func ReadRecord(path string) ([]Request, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read record: %w", err)
	}
	defer func() { _ = f.Close() }()
	var reqs []Request
	dec := json.NewDecoder(f)
	for {
		var r Request
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			return reqs, nil
		} else if err != nil {
			return reqs, fmt.Errorf("read record: %w", err)
		}
		reqs = append(reqs, r)
	}
}

type agent struct {
	script Script
	conn   *acp.AgentSideConnection

	recordMu sync.Mutex

	mu       sync.Mutex
	cancel   chan struct{}              // of the current turn; closed by session/cancel
	sessions int                        // session/new calls so far
	history  map[acp.SessionId][]string // the prompts of each session
}

var _ acp.Agent = (*agent)(nil)

func (a *agent) record(method string, params any) {
	if a.script.Record == "" {
		return
	}
	p, _ := json.Marshal(params)
	line, _ := json.Marshal(Request{Method: method, Params: p, Pid: os.Getpid()})
	a.recordMu.Lock()
	defer a.recordMu.Unlock()
	f, err := os.OpenFile(a.script.Record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func (a *agent) Initialize(_ context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.record(acp.AgentMethodInitialize, p)
	v := acp.ProtocolVersion(acp.ProtocolVersionNumber)
	if a.script.ProtocolVersion != 0 {
		v = acp.ProtocolVersion(a.script.ProtocolVersion)
	}
	resp := acp.InitializeResponse{ProtocolVersion: v, AuthMethods: []acp.AuthMethod{}}
	if a.script.AuthRequired {
		resp.AuthMethods = []acp.AuthMethod{
			{Agent: &acp.AuthMethodAgent{Id: "oauth", Name: "Log in with a browser"}},
			{Terminal: &acp.AuthMethodTerminalInline{Id: "setup", Name: "Log in in a terminal", Type: "terminal", Args: []string{"--login"}}},
		}
	}
	return resp, nil
}

func (a *agent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.record(acp.AgentMethodSessionNew, p)
	if a.script.AuthRequired {
		return acp.NewSessionResponse{}, acp.NewAuthRequired(nil)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions++
	return acp.NewSessionResponse{SessionId: acp.SessionId(fmt.Sprintf("fake-session-%d", a.sessions))}, nil
}

func (a *agent) Cancel(_ context.Context, p acp.CancelNotification) error {
	a.record(acp.AgentMethodSessionCancel, p)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel == nil {
		return nil // no turn yet
	}
	select {
	case <-a.cancel:
	default:
		close(a.cancel)
	}
	return nil
}

func (a *agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	a.record(acp.AgentMethodSessionPrompt, p)
	stop := func(r acp.StopReason) (acp.PromptResponse, error) { return acp.PromptResponse{StopReason: r}, nil }
	// The prompt is the last text block; ecdy sends the session context
	// in a block before it (see Prompts).
	var text string
	for _, b := range p.Prompt {
		if b.Text != nil {
			text = b.Text.Text
		}
	}
	a.mu.Lock()
	if a.history == nil {
		a.history = map[acp.SessionId][]string{}
	}
	a.history[p.SessionId] = append(a.history[p.SessionId], text)
	history := strings.Join(a.history[p.SessionId], " | ")
	cancel := make(chan struct{})
	a.cancel = cancel
	a.mu.Unlock()
	for _, s := range a.script.Turn {
		var err error
		switch {
		case s.Text != "":
			err = a.update(ctx, p.SessionId, acp.UpdateAgentMessageText(s.Text))
		case s.Echo:
			err = a.update(ctx, p.SessionId, acp.UpdateAgentMessageText(text+"\n"))
		case s.History:
			err = a.update(ctx, p.SessionId, acp.UpdateAgentMessageText("history: "+history+"\n"))
		case s.Thought != "":
			err = a.update(ctx, p.SessionId, acp.UpdateAgentThoughtText(s.Thought))
		case s.Raw != "":
			_, _ = os.Stdout.WriteString(s.Raw + "\n")
		case s.Stderr != "":
			_, _ = os.Stderr.WriteString(s.Stderr)
		case s.SleepMS > 0:
			time.Sleep(time.Duration(s.SleepMS) * time.Millisecond)
		case s.Tool != nil:
			err = a.update(ctx, p.SessionId, acp.StartToolCall(acp.ToolCallId(s.Tool.ID), s.Tool.Title,
				acp.WithStartKind(acp.ToolKind(s.Tool.Kind)), acp.WithStartStatus(acp.ToolCallStatusPending)))
		case s.ToolDone != nil:
			opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(acp.ToolCallStatus(s.ToolDone.Status))}
			if s.ToolDone.Output != "" {
				opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(s.ToolDone.Output))}))
			}
			if s.ToolDone.Terminal {
				opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolTerminalRef(s.ToolDone.ID)}))
			}
			err = a.update(ctx, p.SessionId, acp.UpdateToolCall(acp.ToolCallId(s.ToolDone.ID), opts...))
		case s.Call != "":
			err = a.update(ctx, p.SessionId, acp.UpdateAgentMessageText(a.call(ctx, p.SessionId, s.Call, s.Path)))
		case s.TerminalOutput != nil:
			o := s.TerminalOutput
			u := acp.UpdateToolCall(acp.ToolCallId(o.ID))
			u.ToolCallUpdate.Meta = map[string]any{o.Key: map[string]any{"terminal_id": o.ID, "data": o.Data}}
			err = a.update(ctx, p.SessionId, u)
		case s.Permission != nil:
			rctx := ctx
			if a.script.NoCancelRequest {
				rctx = context.WithoutCancel(ctx)
			}
			withdraw := context.CancelFunc(func() {})
			if s.WithdrawMS > 0 {
				rctx, withdraw = context.WithTimeout(rctx, time.Duration(s.WithdrawMS)*time.Millisecond)
			}
			var res acp.RequestPermissionResponse
			res, err = a.conn.RequestPermission(rctx, acp.RequestPermissionRequest{
				SessionId: p.SessionId,
				ToolCall: acp.ToolCallUpdate{
					ToolCallId: acp.ToolCallId(s.Permission.ID),
					Title:      acp.Ptr(s.Permission.Title),
					Kind:       acp.Ptr(acp.ToolKind(s.Permission.Kind)),
				},
				Options: []acp.PermissionOption{
					{OptionId: OptAllow, Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
					{OptionId: OptAlways, Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways},
					{OptionId: OptReject, Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
				},
			})
			withdrawn := rctx.Err() != nil
			withdraw()
			// The SDK cancels ctx on session/cancel, which also fails a
			// pending permission request.
			if err != nil && s.WithdrawMS > 0 && ctx.Err() == nil && withdrawn {
				err = a.update(ctx, p.SessionId, acp.UpdateAgentMessageText("permission: withdrawn\n"))
				break
			}
			if err != nil && ctx.Err() == nil {
				return acp.PromptResponse{}, fmt.Errorf("request permission: %w", err)
			}
			outcome := "cancelled"
			if err == nil && res.Outcome.Selected != nil {
				outcome = string(res.Outcome.Selected.OptionId)
			}
			err = a.update(ctx, p.SessionId, acp.UpdateAgentMessageText("permission: "+outcome+"\n"))
			if outcome == "cancelled" {
				return stop(acp.StopReasonCancelled)
			}
		case s.WaitCancel:
			<-cancel
			return stop(acp.StopReasonCancelled)
		case s.AwaitCancel:
			<-cancel
		case s.Hang:
			select {}
		case s.Exit != nil:
			os.Exit(*s.Exit)
		case s.Stop != "":
			return stop(acp.StopReason(s.Stop))
		}
		if err != nil {
			return acp.PromptResponse{}, err
		}
	}
	return stop(acp.StopReasonEndTurn)
}

func (a *agent) update(ctx context.Context, id acp.SessionId, u acp.SessionUpdate) error {
	// Updates may follow session/cancel, which cancels ctx: the protocol
	// lets the agent report what it did until it answers the prompt.
	if err := a.conn.SessionUpdate(context.WithoutCancel(ctx), acp.SessionNotification{SessionId: id, Update: u}); err != nil {
		return fmt.Errorf("session update: %w", err)
	}
	return nil
}

// call runs a Call step and returns the text chunk that reports it.
func (a *agent) call(ctx context.Context, id acp.SessionId, method, path string) string {
	var err error
	switch method {
	case acp.ClientMethodFsReadTextFile:
		_, err = a.conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: id, Path: path})
	case acp.ClientMethodFsWriteTextFile:
		_, err = a.conn.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: id, Path: path, Content: "written by the agent\n"})
	case acp.ClientMethodTerminalCreate:
		_, err = a.conn.CreateTerminal(ctx, acp.CreateTerminalRequest{SessionId: id, Command: "touch", Args: []string{path}})
	default:
		err = fmt.Errorf("fakeagent: no call step for %s", method)
	}
	var re *acp.RequestError
	switch {
	case err == nil:
		return "call " + method + ": ok\n"
	case errors.As(err, &re):
		return fmt.Sprintf("call %s: error %d\n", method, re.Code)
	default:
		return fmt.Sprintf("call %s: %v\n", method, err)
	}
}

func (a *agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

func (a *agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func (a *agent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
}

func (a *agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

func (a *agent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

func (a *agent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

func (a *agent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}
