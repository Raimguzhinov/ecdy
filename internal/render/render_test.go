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

func TestMarkdownText(t *testing.T) {
	o := Options{Markdown: true, Rewrite: true, Verbose: true}
	tests := []struct {
		name    string
		updates []acp.SessionUpdate
		want    string
	}{
		{
			name:    "rendered as it streams",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("a **b"), acp.UpdateAgentMessageText("** c\n- d\n")},
			want:    "a <0;1>b<0> c\n• d\n",
		},
		{
			name:    "a line is ended before a tool call",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("## Head"), readMain, status("t1", acp.ToolCallStatusCompleted), acp.UpdateAgentMessageText("more")},
			want:    "<0;1;35>Head<0>\n⚙ Read main.go\r\x1b[2K⚙ Read main.go ✓\nmore\n",
		},
		{
			name:    "a code block goes on after a tool call",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("```\nx\n"), readMain, status("t1", acp.ToolCallStatusCompleted), acp.UpdateAgentMessageText("*y*\n```\n")},
			want:    "<0;2>```<0>\n<0;36>x<0>\n⚙ Read main.go\r\x1b[2K⚙ Read main.go ✓\n<0;36>*y*<0>\n<0;2>```<0>\n",
		},
		{
			name:    "thoughts are not markdown",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("**a**"), acp.UpdateAgentThoughtText("**b**")},
			want:    "<0;1>a<0>\n**b**\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sgr(run(o, tt.updates...)); got != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
	var b strings.Builder
	r := New(&b, o)
	r.Update(acp.UpdateAgentMessageText("say *it* "))
	resume := r.Pause()
	b.WriteString("[dialog]\n")
	resume()
	r.Notice("done")
	if got, want := sgr(b.String()), "say <0;3>it<0> \n[dialog]\ndone\n"; got != want {
		t.Errorf("pause: got %q, want %q", got, want)
	}
}

func TestSpacing(t *testing.T) {
	o := Options{Rewrite: true, Spacing: true}
	tests := []struct {
		name    string
		o       Options
		updates []acp.SessionUpdate
		want    string
	}{
		{
			name:    "text, tool calls, text",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("Let me look."), readMain, status("t1", acp.ToolCallStatusCompleted), goTest, status("t2", acp.ToolCallStatusCompleted), acp.UpdateAgentMessageText("Done.")},
			want:    "Let me look.\n\n⚙ Read main.go\r\x1b[2K⚙ Read main.go ✓\n$ go test ./...\r\x1b[2K$ go test ./... ✓\n\nDone.\n\n",
		},
		{
			name:    "no second blank line after a paragraph",
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("Para.\n"), acp.UpdateAgentMessageText("\n"), readMain, status("t1", acp.ToolCallStatusCompleted), acp.UpdateAgentMessageText("End.\n\n")},
			want:    "Para.\n\n⚙ Read main.go\r\x1b[2K⚙ Read main.go ✓\n\nEnd.\n\n",
		},
		{
			name:    "a finished tool call further up",
			updates: []acp.SessionUpdate{goTest, acp.UpdateAgentMessageText("waiting"), status("t2", acp.ToolCallStatusFailed)},
			want:    "$ go test ./...\n\nwaiting\n\n$ go test ./... ✗\n\n",
		},
		{
			name:    "thoughts and plans are blocks",
			o:       Options{Verbose: true, Spacing: true},
			updates: []acp.SessionUpdate{acp.UpdateAgentThoughtText("hmm"), acp.UpdatePlan(acp.PlanEntry{Content: "read"}), acp.UpdateAgentMessageText("ok")},
			want:    "hmm\n\nPlan:\n  ○ read\n\nok\n\n",
		},
		{
			name:    "markdown",
			o:       Options{Markdown: true, Rewrite: true, Spacing: true},
			updates: []acp.SessionUpdate{acp.UpdateAgentMessageText("# A"), readMain, status("t1", acp.ToolCallStatusCompleted)},
			want:    "<0;1;35>A<0>\n\n⚙ Read main.go\r\x1b[2K⚙ Read main.go ✓\n\n",
		},
		{
			name: "nothing printed, nothing padded",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.o.Spacing {
				tt.o = o
			}
			var b strings.Builder
			r := New(&b, tt.o)
			for _, u := range tt.updates {
				r.Update(u)
			}
			r.Finish()
			r.Pad()
			r.Pad()
			if got := sgr(b.String()); got != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestSpacingNotices(t *testing.T) {
	var b strings.Builder
	r := New(&b, Options{Rewrite: true, Spacing: true})
	r.Update(acp.UpdateAgentMessageText("partial"))
	r.Notice("cancelling")
	r.Notice("cancelled")
	r.Pad()
	if got, want := b.String(), "partial\n\ncancelling\ncancelled\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNoSpacingByDefault(t *testing.T) {
	var b strings.Builder
	r := New(&b, Options{})
	r.Update(acp.UpdateAgentMessageText("a"))
	r.Update(readMain)
	r.Pad()
	if got, want := b.String(), "a\n⚙ Read main.go\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
