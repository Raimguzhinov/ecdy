package render

import (
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func run(o Options, updates ...acp.SessionUpdate) string {
	var b strings.Builder
	r := New(&b, o)
	for _, u := range updates {
		r.Update(u)
	}
	r.Finish()
	return b.String()
}

var (
	readMain = acp.StartToolCall("t1", "Read main.go", acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusPending))
	goTest   = acp.StartToolCall("t2", "go test ./...", acp.WithStartKind(acp.ToolKindExecute))
)

func status(id acp.ToolCallId, s acp.ToolCallStatus) acp.SessionUpdate {
	return acp.UpdateToolCall(id, acp.WithUpdateStatus(s))
}

func TestRender(t *testing.T) {
	tests := []struct {
		name    string
		o       Options
		updates []acp.SessionUpdate
		want    string
	}{
		{
			name:    "text chunks stream as is",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("Hel"), acp.UpdateAgentMessageText("lo\nworld")},
			want:    "Hello\nworld\n",
		},
		{
			name:    "tool call on a pipe: start and end lines",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("Let me look."), readMain, status("t1", acp.ToolCallStatusInProgress), status("t1", acp.ToolCallStatusCompleted), acp.UpdateAgentMessageText("Done.")},
			want:    "Let me look.\n⚙ Read main.go\n⚙ Read main.go ✓\nDone.\n",
		},
		{
			name:    "tool call on a terminal: redrawn in place",
			o:       Options{Rewrite: true},
			updates: []acp.SessionUpdate{readMain, status("t1", acp.ToolCallStatusInProgress), status("t1", acp.ToolCallStatusCompleted)},
			want:    "⚙ Read main.go\r\x1b[2K⚙ Read main.go\r\x1b[2K⚙ Read main.go ✓\n",
		},
		{
			name:    "a finished tool call further up gets a new line",
			o:       Options{Rewrite: true},
			updates: []acp.SessionUpdate{goTest, acp.UpdateAgentMessageText("waiting"), status("t2", acp.ToolCallStatusFailed)},
			want:    "$ go test ./...\nwaiting\n$ go test ./... ✗\n",
		},
		{
			name:    "title update of the last line",
			o:       Options{Rewrite: true},
			updates: []acp.SessionUpdate{readMain, acp.UpdateToolCall("t1", acp.WithUpdateTitle("Read cmd/main.go"), acp.WithUpdateStatus(acp.ToolCallStatusCompleted))},
			want:    "⚙ Read main.go\r\x1b[2K⚙ Read cmd/main.go ✓\n",
		},
		{
			name:    "update without a tool_call",
			updates: []acp.SessionUpdate{acp.UpdateToolCall("t9", acp.WithUpdateTitle("Search"), acp.WithUpdateStatus(acp.ToolCallStatusCompleted))},
			want:    "⚙ Search ✓\n",
		},
		{
			name:    "multi-line title",
			updates: []acp.SessionUpdate{acp.StartToolCall("t3", "cat <<EOF\nx\nEOF", acp.WithStartStatus(acp.ToolCallStatusCompleted))},
			want:    "⚙ cat <<EOF … ✓\n",
		},
		{
			name:    "thoughts and plans hidden by default",
			updates: []acp.SessionUpdate{acp.UpdateAgentThoughtText("hmm"), acp.UpdatePlan(acp.PlanEntry{Content: "step"}), acp.UpdateAgentMessageText("ok")},
			want:    "ok\n",
		},
		{
			name: "verbose: thoughts, plans and tool output",
			o:    Options{Verbose: true},
			updates: []acp.SessionUpdate{
				acp.UpdateAgentThoughtText("hmm"),
				acp.UpdateAgentMessageText("ok"),
				acp.UpdatePlan(acp.PlanEntry{Content: "read", Status: acp.PlanEntryStatusCompleted}, acp.PlanEntry{Content: "fix", Status: acp.PlanEntryStatusPending}),
				readMain,
				acp.UpdateToolCall("t1", acp.WithUpdateStatus(acp.ToolCallStatusCompleted), acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock("package main\n"))})),
			},
			want: "hmm\nok\nPlan:\n  ● read\n  ○ fix\n⚙ Read main.go\n⚙ Read main.go ✓\n  │ package main\n",
		},
		{
			name:    "color",
			o:       Options{Color: true},
			updates: []acp.SessionUpdate{acp.StartToolCall("t1", "Read", acp.WithStartStatus(acp.ToolCallStatusCompleted))},
			want:    dim + "⚙ " + reset + "Read " + green + "✓" + reset + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(tt.o, tt.updates...); got != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestNoticeAndPause(t *testing.T) {
	var b strings.Builder
	r := New(&b, Options{Rewrite: true})
	r.Update(acp.UpdateAgentMessageText("partial"))
	r.Notice("cancelling")
	r.Update(readMain)
	resume := r.Pause()
	b.WriteString("[dialog]\n")
	resume()
	r.Update(status("t1", acp.ToolCallStatusCompleted))
	r.Finish()
	want := "partial\ncancelling\n⚙ Read main.go\n[dialog]\n⚙ Read main.go ✓\n"
	if got := b.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestVerboseOutputTruncated(t *testing.T) {
	long := strings.Repeat("line\n", 30)
	got := run(Options{Verbose: true}, acp.StartToolCall("t1", "Read", acp.WithStartStatus(acp.ToolCallStatusCompleted),
		acp.WithStartContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(long))})))
	if n := strings.Count(got, "│ line"); n != 20 || !strings.Contains(got, "… 10 more lines") {
		t.Errorf("got %d lines:\n%s", n, got)
	}
}
