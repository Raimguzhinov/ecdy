// Command agent is the scripted ACP agent of the README demo (docs/demo): it
// answers each prompt of the demo tapes with a canned turn, picked by a word
// of the prompt, so that the recording needs no real agent, login or network.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// step is one action of a turn. Exactly one field is set.
type step struct {
	text  string // streamed a few characters at a time
	pause time.Duration
	tool  *tool // started, then finished with status after a pause
	ask   *tool // a permission request for the tool, then the tool as above
}

type tool struct {
	id, title string
	kind      acp.ToolKind
	status    acp.ToolCallStatus
	took      time.Duration
	// edit, if set, replaces edit[1] by edit[2] in the file edit[0] of the
	// session's directory: the fix is real, so the tests pass after it.
	edit *[3]string
}

// reply is a turn played when the prompt contains match.
type reply struct {
	match string
	turn  []step
}

var replies = []reply{
	{"what does this project do", []step{
		{tool: &tool{id: "r1", title: "Read README.md", kind: acp.ToolKindRead, status: acp.ToolCallStatusCompleted, took: 400 * time.Millisecond}},
		{tool: &tool{id: "r2", title: "Read calc.go", kind: acp.ToolKindRead, status: acp.ToolCallStatusCompleted, took: 300 * time.Millisecond}},
		{text: "## calc\n\nA tiny **expression calculator** in Go:\n\n" +
			"- `Parse` turns `\"2 + 3 * 4\"` into a syntax tree\n" +
			"- `Eval` walks the tree and returns a `float64`\n" +
			"- `calc_test.go` covers both, table-driven\n"},
	}},
	{"why did", []step{
		{tool: &tool{id: "t1", title: "go test ./...", kind: acp.ToolKindExecute, status: acp.ToolCallStatusFailed, took: 900 * time.Millisecond}},
		{tool: &tool{id: "t2", title: "Read calc.go:50", kind: acp.ToolKindRead, status: acp.ToolCallStatusCompleted, took: 300 * time.Millisecond}},
		{text: "`TestEval/division` fails: `Eval` swaps the operands of `/`,\n" +
			"so `8 / 2` gives `0.25`.\n\nThe fix is to swap `r / l` for `l / r` in `calc.go:50`.\n"},
	}},
	{"fix it", []step{
		{ask: &tool{id: "e1", title: "Edit calc.go", kind: acp.ToolKindEdit, status: acp.ToolCallStatusCompleted, took: 300 * time.Millisecond,
			edit: &[3]string{"calc.go", "r / l", "l / r"}}},
		{tool: &tool{id: "e2", title: "go test ./...", kind: acp.ToolKindExecute, status: acp.ToolCallStatusCompleted, took: 900 * time.Millisecond}},
		{text: "Fixed: `l / r` in `calc.go:50`, and all tests pass.\n"},
	}},
	{"except configs", []step{
		{tool: &tool{id: "f1", title: "List tmp/", kind: acp.ToolKindSearch, status: acp.ToolCallStatusCompleted, took: 400 * time.Millisecond}},
		{text: "This keeps `*.conf` and `*.toml` and deletes the rest:\n\n" +
			"```sh\nfind tmp -type f ! -name '*.conf' ! -name '*.toml' -delete\n```\n\n" +
			"Nothing was run: say *go* and I will run it.\n"},
	}},
	{"today", []step{
		{tool: &tool{id: "g1", title: "git log --since=midnight", kind: acp.ToolKindExecute, status: acp.ToolCallStatusCompleted, took: 500 * time.Millisecond}},
		{text: "Hi from **{agent}**. Today: the division fix in `calc.go` and a new test case.\n"},
	}},
}

func main() {
	name := flag.String("name", "agent", "the name the agent calls itself")
	flag.Parse()
	a := &agent{name: *name}
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	a.conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	<-a.conn.Done()
}

type agent struct {
	name string
	cwd  string
	conn *acp.AgentSideConnection
}

var _ acp.Agent = (*agent)(nil)

func (a *agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AuthMethods: []acp.AuthMethod{}}, nil
}

func (a *agent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.cwd = p.Cwd
	return acp.NewSessionResponse{SessionId: "demo"}, nil
}

func (a *agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	// The prompt is the last text block; ecdy sends the session context
	// in a block before it.
	var prompt string
	for _, b := range p.Prompt {
		if b.Text != nil {
			prompt = b.Text.Text
		}
	}
	turn := []step{{text: "I am a demo agent: I only know the prompts of the demo.\n"}}
	for _, r := range replies {
		if strings.Contains(strings.ToLower(prompt), r.match) {
			turn = r.turn
			break
		}
	}
	send := func(u acp.SessionUpdate) error {
		return a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: u})
	}
	time.Sleep(300 * time.Millisecond) // thinking
	for _, s := range turn {
		var err error
		switch {
		case s.text != "":
			err = a.stream(strings.ReplaceAll(s.text, "{agent}", a.name), send)
		case s.pause > 0:
			time.Sleep(s.pause)
		case s.ask != nil:
			res, rerr := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
				SessionId: p.SessionId,
				ToolCall:  acp.ToolCallUpdate{ToolCallId: acp.ToolCallId(s.ask.id), Title: acp.Ptr(s.ask.title), Kind: acp.Ptr(s.ask.kind)},
				Options: []acp.PermissionOption{
					{OptionId: "allow", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
					{OptionId: "always", Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways},
					{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
				},
			})
			if rerr != nil || res.Outcome.Selected == nil || res.Outcome.Selected.OptionId == "reject" {
				return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, send(acp.UpdateAgentMessageText("OK, I left it as it is.\n"))
			}
			err = a.runTool(s.ask, send)
		case s.tool != nil:
			err = a.runTool(s.tool, send)
		}
		if err != nil {
			return acp.PromptResponse{}, fmt.Errorf("demo turn: %w", err)
		}
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *agent) runTool(t *tool, send func(acp.SessionUpdate) error) error {
	if err := send(acp.StartToolCall(acp.ToolCallId(t.id), t.title, acp.WithStartKind(t.kind), acp.WithStartStatus(acp.ToolCallStatusInProgress))); err != nil {
		return err
	}
	time.Sleep(t.took)
	if t.edit != nil {
		path := filepath.Join(a.cwd, t.edit[0])
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(data), t.edit[1], t.edit[2], 1)), 0o644); err != nil {
			return fmt.Errorf("edit: %w", err)
		}
	}
	return send(acp.UpdateToolCall(acp.ToolCallId(t.id), acp.WithUpdateStatus(t.status)))
}

// stream sends text in small chunks, as a model generating it would.
func (a *agent) stream(text string, send func(acp.SessionUpdate) error) error {
	for len(text) > 0 {
		n := min(6, len(text))
		for n < len(text) && text[n]&0xC0 == 0x80 { // keep UTF-8 sequences whole
			n++
		}
		if err := send(acp.UpdateAgentMessageText(text[:n])); err != nil {
			return err
		}
		text = text[n:]
		time.Sleep(18 * time.Millisecond)
	}
	return nil
}

func (a *agent) Cancel(context.Context, acp.CancelNotification) error { return nil }

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
