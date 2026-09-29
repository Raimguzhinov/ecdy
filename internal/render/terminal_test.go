package render

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
)

// termOut is a tool_call_update carrying command output in _meta, the way
// codex-acp (terminal_output_delta) and pi-acp (terminal_output) send it.
func termOut(id acp.ToolCallId, key, data string) acp.SessionUpdate {
	u := acp.UpdateToolCall(id)
	u.ToolCallUpdate.Meta = map[string]any{key: map[string]any{"terminal_id": string(id), "data": data}}
	return u
}

func finishedInTerminal(id acp.ToolCallId) acp.SessionUpdate {
	return acp.UpdateToolCall(id, acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
		acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolTerminalRef(string(id))}))
}

func TestTerminalOutput(t *testing.T) {
	tests := []struct {
		name    string
		o       Options
		updates []acp.SessionUpdate
		want    string
	}{
		{
			name:    "deltas are joined, the placeholder is gone",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "ok  \tpkg/a\n"), termOut("t2", "terminal_output_delta", "FAIL\tpkg/"), termOut("t2", "terminal_output_delta", "b\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ ok  \tpkg/a\n  │ FAIL\tpkg/b\n",
		},
		{
			name:    "terminal_output (pi-acp) is appended too",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output", "one\n"), termOut("t2", "terminal_output", "two\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ one\n  │ two\n",
		},
		{
			name: "output without a terminal block, the final update carrying the rest",
			o:    Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\n"), func() acp.SessionUpdate {
				u := termOut("t2", "terminal_output_delta", "b")
				u.ToolCallUpdate.Status = ptr(acp.ToolCallStatusFailed)
				return u
			}()},
			want: "$ go test ./...\n$ go test ./... ✗\n  │ a\n  │ b\n",
		},
		{
			name:    "a terminal block without output prints nothing",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n",
		},
		{
			name:    "not verbose: no output",
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n",
		},
		{
			name: "other _meta keys and malformed values are ignored",
			o:    Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_info", "x"), func() acp.SessionUpdate {
				u := acp.UpdateToolCall("t2")
				u.ToolCallUpdate.Meta = map[string]any{"terminal_output_delta": "flat"}
				return u
			}(), func() acp.SessionUpdate {
				u := acp.UpdateToolCall("t2")
				u.ToolCallUpdate.Meta = map[string]any{"terminal_output_delta": map[string]any{"data": 42}}
				return u
			}(), finishedInTerminal("t2")},
			want: "$ go test ./...\n$ go test ./... ✓\n",
		},
		{
			name:    "escape sequences and control characters are removed",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "\x1b[31mred\x1b[0m \x1b]0;title\x07\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\ bell\a bs\b del\x7f c1\u009b31m\x1bc end\r\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ red link bell bs del c131m end\n",
		},
		{
			name:    "an escape sequence split across deltas",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\x1b["), termOut("t2", "terminal_output_delta", "2Kb\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ ab\n",
		},
		{
			name:    "a newline ends an unfinished escape sequence",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\x1b\nb\x1b[3\nc\x1b]0;t\nd\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ a\n  │ b\n  │ c\n  │ d\n",
		},
		{
			name:    "carriage return overwrites the line (progress bars)",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "10%\r50%\r100% done\nnext\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ 100% done\n  │ next\n",
		},
		{
			name:    "invalid UTF-8 is replaced",
			o:       Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\xffb\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ a�b\n",
		},
		{
			name: "text content is cleaned as well",
			o:    Options{Verbose: true},
			updates: []acp.SessionUpdate{goTest, acp.UpdateToolCall("t2", acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
				acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock("\x1b[1mbold\x1b[0m\n"))}))},
			want: "$ go test ./...\n$ go test ./... ✓\n  │ bold\n",
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

func ptr[T any](v T) *T { return &v }

// TestTerminalOutputTail: the last lines are shown, not the first: that is
// where a command reports what went wrong.
func TestTerminalOutputTail(t *testing.T) {
	var updates []acp.SessionUpdate
	updates = append(updates, goTest)
	for i := 1; i <= 30; i++ {
		updates = append(updates, termOut("t2", "terminal_output_delta", fmt.Sprintf("line %d\n", i)))
	}
	updates = append(updates, finishedInTerminal("t2"))
	got := run(Options{Verbose: true}, updates...)
	if n := strings.Count(got, "│ line"); n != 20 {
		t.Errorf("%d lines shown, want 20:\n%s", n, got)
	}
	if !strings.Contains(got, "│ … 10 earlier lines\n") || !strings.Contains(got, "│ line 11\n") || !strings.Contains(got, "│ line 30\n") || strings.Contains(got, "│ line 10\n") {
		t.Errorf("got:\n%s", got)
	}
}

// TestTerminalOutputBounded: a command that prints a lot keeps a bounded
// tail in memory, and the line count stays right.
func TestTerminalOutputBounded(t *testing.T) {
	var b strings.Builder
	r := New(&b, Options{Verbose: true})
	r.Update(goTest)
	chunk := strings.Repeat(strings.Repeat("x", 99)+"\n", 100) // 10 000 bytes, 100 lines
	for range 1000 {
		r.Update(termOut("t2", "terminal_output_delta", chunk))
	}
	if n := len(r.tools["t2"].term.b); n > maxTermBytes {
		t.Errorf("kept %d bytes, at most %d", n, maxTermBytes)
	}
	r.Update(finishedInTerminal("t2"))
	r.Finish()
	got := b.String()
	if !strings.Contains(got, "│ … 99980 earlier lines\n") || strings.Count(got, "│ xxx") != 20 {
		t.Errorf("got:\n%s", got)
	}
}

// TestTerminalOutputCutMidRune: the kept tail may start inside a line, a
// UTF-8 character (7 bytes after the last "я": an odd offset) or an escape
// sequence; that partial line is not shown.
func TestTerminalOutputCutMidRune(t *testing.T) {
	var b strings.Builder
	r := New(&b, Options{Verbose: true})
	r.Update(goTest)
	r.Update(termOut("t2", "terminal_output_delta", strings.Repeat("ж", maxTermBytes)+"\x1b[31m"+strings.Repeat("я", maxTermBytes/2)+"\nlast!\n"))
	r.Update(finishedInTerminal("t2"))
	r.Finish()
	want := "$ go test ./...\n$ go test ./... ✓\n  │ … 1 earlier lines\n  │ last!\n"
	if got := b.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// FuzzClean: whatever a program prints, the cleaned text is valid UTF-8 with
// no control characters but tab and newline, and cleaning it again changes
// nothing.
func FuzzClean(f *testing.F) {
	for _, s := range []string{"plain\n", "\x1b[31mred\x1b[0m", "\x1b]0;t\x07", "\x1b]8;;u\x1b\\l", "a\rb\r\n", "\xff\u009b", "\x1bP1$r\x1b\\", "\x1b", "\x1b\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := clean(s)
		if strings.Count(got, "\n") != strings.Count(s, "\n") {
			t.Fatalf("clean(%q) = %q: line count changed", s, got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("clean(%q) = %q: invalid UTF-8", s, got)
		}
		for _, r := range got {
			if (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				t.Fatalf("clean(%q) = %q: control character %U", s, got, r)
			}
		}
		if again := clean(got); again != got {
			t.Fatalf("clean not idempotent: %q → %q → %q", s, got, again)
		}
	})
}

func width(n int) func() int { return func() int { return n } }

// TestLiveOutput: on a terminal with -v, the last lines of a running
// command's output are drawn under its status line and redrawn in place.
func TestLiveOutput(t *testing.T) {
	o := Options{Verbose: true, Rewrite: true, Width: width(20)}
	tests := []struct {
		name    string
		o       Options
		updates []acp.SessionUpdate
		want    string
	}{
		{
			name:    "drawn and redrawn, then the final output",
			o:       o,
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\nb\n"), termOut("t2", "terminal_output_delta", "c"), finishedInTerminal("t2")},
			want: "$ go test ./..." +
				"\r\x1b[2K$ go test ./...\n  │ a\n  │ b" +
				"\r\x1b[2A\x1b[J$ go test ./...\n  │ a\n  │ b\n  │ c" +
				"\r\x1b[3A\x1b[J$ go test ./... ✓\n  │ a\n  │ b\n  │ c\n",
		},
		{
			name:    "erased when other output comes",
			o:       o,
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\n"), acp.UpdateAgentMessageText("hi"), finishedInTerminal("t2")},
			want: "$ go test ./..." +
				"\r\x1b[2K$ go test ./...\n  │ a" +
				"\r\x1b[1A\x1b[J$ go test ./...\nhi\n$ go test ./... ✓\n  │ a\n",
		},
		{
			name:    "the last 5 lines, cut to the width",
			o:       o,
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "1\n2\n3\n4\n5\n6 is a long line of output\n")},
			want: "$ go test ./..." +
				"\r\x1b[2K$ go test ./...\n  │ 2\n  │ 3\n  │ 4\n  │ 5\n  │ 6 is a long li…" +
				// The turn ends with the command still running.
				"\r\x1b[5A\x1b[J$ go test ./...\n",
		},
		{
			name:    "not a terminal: nothing live",
			o:       Options{Verbose: true, Width: width(20)},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\n$ go test ./... ✓\n  │ a\n",
		},
		{
			name:    "not verbose: nothing live",
			o:       Options{Rewrite: true, Width: width(20)},
			updates: []acp.SessionUpdate{goTest, termOut("t2", "terminal_output_delta", "a\n"), finishedInTerminal("t2")},
			want:    "$ go test ./...\r\x1b[2K$ go test ./... ✓\n",
		},
		{
			name:    "a running tool call further up is not drawn",
			o:       o,
			updates: []acp.SessionUpdate{goTest, readMain, termOut("t2", "terminal_output_delta", "a\n"), status("t1", acp.ToolCallStatusCompleted), finishedInTerminal("t2")},
			want:    "$ go test ./...\n⚙ Read main.go\r\x1b[2K⚙ Read main.go ✓\n$ go test ./... ✓\n  │ a\n",
		},
		{
			name:    "the status line is cut to the width while it is open",
			o:       o,
			updates: []acp.SessionUpdate{acp.StartToolCall("t4", "go test -run TestSomethingLong ./...", acp.WithStartKind(acp.ToolKindExecute)), finishedInTerminal("t4")},
			want:    "$ go test -run Tes…\r\x1b[2K$ go test -run TestSomethingLong ./... ✓\n",
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

func TestFit(t *testing.T) {
	tests := []struct {
		in   string
		w    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"eleven chars", 10, "eleven ch…"},
		{"a\tb", 10, "a       b"},
		{"a\tb", 5, "a   …"},
		{"日本語テキスト", 7, "日本語…"},
		{"日本語", 6, "日本語"},
		{"x", 0, ""},
	}
	for _, tt := range tests {
		if got := fit(tt.in, tt.w); got != tt.want {
			t.Errorf("fit(%q, %d) = %q, want %q", tt.in, tt.w, got, tt.want)
		}
	}
}

// TestLiveOutputCutTail: the live view cleans only the end of the output;
// the line its start falls in is not shown.
func TestLiveOutputCutTail(t *testing.T) {
	var b strings.Builder
	r := New(&b, Options{Verbose: true, Rewrite: true, Width: width(40)})
	r.Update(goTest)
	r.Update(termOut("t2", "terminal_output_delta", strings.Repeat("x", 2*liveTailBytes)+"\nlast\n"))
	got := b.String()
	if !strings.HasSuffix(got, "\n  │ last") || strings.Contains(got, "│ xxx") {
		t.Errorf("got %q", got)
	}
}
