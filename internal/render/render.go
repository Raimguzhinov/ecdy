// Package render prints an agent's turn to the terminal as it streams in:
// message text as is, each tool call as one status line, ecdy's own notices
// dimmed.
package render

import (
	"fmt"
	"io"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// Options select the output style.
type Options struct {
	// Color enables ANSI colors and styles.
	Color bool
	// Rewrite lets a tool call's status line be redrawn in place while it is
	// the last line printed. Only for terminals.
	Rewrite bool
	// Verbose also prints thoughts, plans and the output of finished tool
	// calls.
	Verbose bool
}

// Renderer is safe for concurrent use.
type Renderer struct {
	o Options

	mu    sync.Mutex
	w     io.Writer
	col0  bool // the cursor is at the start of a line
	tools map[acp.ToolCallId]*tool
	last  acp.ToolCallId // the tool call whose status line was printed last, if nothing followed it
	mode  string         // "", "text" or "thought": what the current line is
}

type tool struct {
	title  string
	kind   acp.ToolKind
	status acp.ToolCallStatus
	output []acp.ToolCallContent
}

// New returns a Renderer writing to w.
func New(w io.Writer, o Options) *Renderer {
	return &Renderer{o: o, w: w, col0: true, tools: map[acp.ToolCallId]*tool{}}
}

const (
	dim   = "\x1b[2m"
	bold  = "\x1b[1m"
	red   = "\x1b[31m"
	green = "\x1b[32m"
	reset = "\x1b[0m"
)

func (r *Renderer) style(s, text string) string {
	if !r.o.Color || text == "" {
		return text
	}
	return s + text + reset
}

// write prints s and keeps track of the cursor column.
func (r *Renderer) write(s string) {
	if s == "" {
		return
	}
	_, _ = io.WriteString(r.w, s)
	r.col0 = strings.HasSuffix(s, "\n")
}

// line starts a new line if the cursor is not at the start of one.
func (r *Renderer) line() {
	if !r.col0 {
		r.write("\n")
	}
}

// Update renders one session/update.
func (r *Renderer) Update(u acp.SessionUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case u.AgentMessageChunk != nil:
		r.text("text", u.AgentMessageChunk.Content, "")
	case u.AgentThoughtChunk != nil:
		if r.o.Verbose {
			r.text("thought", u.AgentThoughtChunk.Content, dim)
		}
	case u.ToolCall != nil:
		tc := u.ToolCall
		t := &tool{title: tc.Title, kind: tc.Kind, status: tc.Status, output: tc.Content}
		r.tools[tc.ToolCallId] = t
		r.toolLine(tc.ToolCallId, t)
	case u.ToolCallUpdate != nil:
		r.toolUpdate(u.ToolCallUpdate)
	case u.Plan != nil:
		if r.o.Verbose {
			r.plan(u.Plan.Entries)
		}
	}
}

func (r *Renderer) text(mode string, c acp.ContentBlock, style string) {
	if c.Text == nil || c.Text.Text == "" {
		return
	}
	if r.mode != mode {
		r.line()
	}
	r.mode, r.last = mode, ""
	if style != "" && r.o.Color {
		// Style each piece: a chunk may be followed by a dialog or a notice.
		r.write(style + c.Text.Text + reset)
		return
	}
	r.write(c.Text.Text)
}

func (r *Renderer) toolUpdate(u *acp.SessionToolCallUpdate) {
	t := r.tools[u.ToolCallId]
	if t == nil {
		// An update without a tool_call first: allowed by the protocol.
		t = &tool{status: acp.ToolCallStatusPending}
		r.tools[u.ToolCallId] = t
	}
	prev := *t
	if u.Title != nil {
		t.title = *u.Title
	}
	if u.Kind != nil {
		t.kind = *u.Kind
	}
	if u.Status != nil {
		t.status = *u.Status
	}
	if u.Content != nil {
		t.output = u.Content
	}
	if t.title == prev.title && t.status == prev.status && prev.title != "" {
		return
	}
	if !done(t.status) && r.last != u.ToolCallId && prev.title != "" {
		// A title change of a tool call further up: wait for its end.
		return
	}
	r.toolLine(u.ToolCallId, t)
}

func done(s acp.ToolCallStatus) bool {
	return s == acp.ToolCallStatusCompleted || s == acp.ToolCallStatusFailed
}

// toolLine prints the status line of a tool call, over the previous one if
// that is the last line and the output is a terminal.
func (r *Renderer) toolLine(id acp.ToolCallId, t *tool) {
	if r.o.Rewrite && r.last == id {
		r.write("\r\x1b[2K")
	} else {
		r.line()
	}
	icon := "⚙"
	if t.kind == acp.ToolKindExecute {
		icon = "$"
	}
	s := r.style(dim, icon+" ") + oneLine(t.title)
	switch t.status {
	case acp.ToolCallStatusCompleted:
		s += " " + r.style(green, "✓")
	case acp.ToolCallStatusFailed:
		s += " " + r.style(red, "✗")
	}
	r.mode = ""
	if !r.o.Rewrite || done(t.status) {
		// The line is final: end it. On a terminal a running tool call's
		// line stays open, so that it can be redrawn.
		r.write(s + "\n")
		r.last = ""
		if r.o.Verbose && done(t.status) {
			r.output(t.output)
		}
		return
	}
	r.write(s)
	r.last = id
}

// output prints the text of a finished tool call, indented (verbose only).
func (r *Renderer) output(content []acp.ToolCallContent) {
	const maxLines = 20
	var lines []string
	for _, c := range content {
		switch {
		case c.Content != nil && c.Content.Content.Text != nil:
			lines = append(lines, strings.Split(strings.TrimRight(c.Content.Content.Text.Text, "\n"), "\n")...)
		case c.Diff != nil:
			lines = append(lines, "edit "+c.Diff.Path)
		case c.Terminal != nil:
			lines = append(lines, "terminal "+c.Terminal.TerminalId)
		}
	}
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("… %d more lines", len(lines)-maxLines))
	}
	for _, l := range lines {
		r.write(r.style(dim, "  │ "+l) + "\n")
	}
}

func (r *Renderer) plan(entries []acp.PlanEntry) {
	r.line()
	r.mode, r.last = "", ""
	r.write(r.style(dim, "Plan:") + "\n")
	for _, e := range entries {
		mark := "○"
		switch e.Status {
		case acp.PlanEntryStatusInProgress:
			mark = "◐"
		case acp.PlanEntryStatusCompleted:
			mark = "●"
		}
		r.write(r.style(dim, "  "+mark+" "+oneLine(e.Content)) + "\n")
	}
}

// Notice prints a line of ecdy's own (not the agent's), dimmed.
func (r *Renderer) Notice(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.line()
	r.mode, r.last = "", ""
	r.write(r.style(dim, fmt.Sprintf(format, args...)) + "\n")
}

// Pause ends the current line and blocks all output until resume is called,
// so that a dialog can use the terminal.
func (r *Renderer) Pause() (resume func()) {
	r.mu.Lock()
	r.line()
	r.mode, r.last = "", ""
	return r.mu.Unlock
}

// Finish ends the last line.
func (r *Renderer) Finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.line()
}

// oneLine returns the first line of s, marking anything cut off.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
