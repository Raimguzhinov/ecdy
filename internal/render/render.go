// Package render prints an agent's turn to the terminal as it streams in:
// message text as is or as rendered markdown, each tool call as one status
// line, ecdy's own notices dimmed.
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
	// Markdown renders the agent's message text (docs/adr/0006-ux.md). For
	// terminals with colors.
	Markdown bool
	// Width returns the terminal's width. With Rewrite and Verbose, it lets
	// a running command's output be drawn live under its status line, and
	// the open status line is cut to it.
	Width func() int
	// Spacing separates blocks (message text, a run of tool calls, a plan,
	// a notice) with a blank line, and Pad ends the output with one, so that
	// the reply stands apart from the prompts around it. For terminals.
	Spacing bool
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
	md    *markdown      // with Options.Markdown
	block string         // the kind of the last block printed, "" before any
	nl    int            // how many newlines the output ends with

	// The open status line of last as printed, and how many rows of live
	// output are drawn under it.
	lastLine string
	live     int
}

type tool struct {
	title  string
	kind   acp.ToolKind
	status acp.ToolCallStatus
	output []acp.ToolCallContent
	term   termBuf // command output from _meta, verbose only
}

// New returns a Renderer writing to w.
func New(w io.Writer, o Options) *Renderer {
	r := &Renderer{o: o, w: w, col0: true, tools: map[acp.ToolCallId]*tool{}}
	if o.Markdown {
		r.md = &markdown{}
	}
	return r
}

// liveLines is how many lines of a running command's output are drawn.
const liveLines = 5

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
	if t := strings.TrimRight(s, "\n"); t == "" {
		r.nl += len(s)
	} else {
		r.nl = len(s) - len(t)
	}
}

// start ends the current line and, with Spacing, leaves a blank line if a
// block of another kind was printed before.
func (r *Renderer) start(kind string) {
	r.line()
	if r.o.Spacing && r.block != "" && r.block != kind && r.nl < 2 {
		r.write("\n")
	}
	r.block = kind
}

// line starts a new line if the cursor is not at the start of one. It first
// ends the markdown line in progress and erases live output: something else
// is about to be printed.
func (r *Renderer) line() {
	if r.md != nil {
		r.write(r.md.flush())
	}
	if r.live > 0 {
		r.write(r.erase() + r.lastLine)
		r.live = 0
	}
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
		r.start(mode)
	}
	r.mode, r.last = mode, ""
	if mode == "text" && r.md != nil {
		r.write(r.md.feed(c.Text.Text))
		return
	}
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
	if r.o.Verbose && t.term.addMeta(u.Meta) && r.liveOn() && r.last == u.ToolCallId && !done(t.status) {
		r.drawLive(t)
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
		r.write(r.erase())
		r.live = 0
	} else {
		r.start("tool")
	}
	icon := "⚙"
	if t.kind == acp.ToolKindExecute {
		icon = "$"
	}
	title := oneLine(t.title)
	if r.o.Rewrite && r.o.Width != nil && !done(t.status) {
		// An open line must fit in one row to be redrawn in place.
		title = fit(icon+" "+title, r.o.Width()-1)[len(icon)+1:]
	}
	s := r.style(dim, icon+" ") + title
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
			r.output(t)
		}
		return
	}
	r.write(s)
	r.last, r.lastLine = id, s
	if r.liveOn() && len(t.term.b) > 0 {
		r.drawLive(t)
	}
}

func (r *Renderer) liveOn() bool { return r.o.Verbose && r.o.Rewrite && r.o.Width != nil }

// erase returns what moves the cursor to the start of the open status line
// and clears it along with the live output under it.
func (r *Renderer) erase() string {
	if r.live == 0 {
		return "\r\x1b[2K"
	}
	return fmt.Sprintf("\r\x1b[%dA\x1b[J", r.live)
}

// drawLive redraws the open status line of the last tool call with the last
// lines of its command output under it. Each line is cut to the terminal
// width, so that it takes exactly one row and erase can count them.
func (r *Renderer) drawLive(t *tool) {
	lines := t.term.tail(liveLines)
	if len(lines) == 0 && r.live == 0 {
		return
	}
	var b strings.Builder
	b.WriteString(r.erase() + r.lastLine)
	w := r.o.Width() - 1
	for _, l := range lines {
		b.WriteString("\n" + r.style(dim, fit("  │ "+l, w)))
	}
	r.write(b.String())
	r.live = len(lines)
}

// output prints the text of a finished tool call, indented (verbose only):
// the first lines of its content, then the last lines of the command output
// sent in _meta. A terminal content block only refers to that output.
func (r *Renderer) output(t *tool) {
	const maxLines = 20
	var lines []string
	for _, c := range t.output {
		switch {
		case c.Content != nil && c.Content.Content.Text != nil:
			lines = append(lines, strings.Split(strings.TrimRight(clean(c.Content.Content.Text.Text), "\n"), "\n")...)
		case c.Diff != nil:
			lines = append(lines, "edit "+clean(c.Diff.Path))
		}
	}
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], fmt.Sprintf("… %d more lines", len(lines)-maxLines))
	}
	lines = append(lines, t.term.lines(maxLines)...)
	for _, l := range lines {
		r.write(r.style(dim, "  │ "+l) + "\n")
	}
}

func (r *Renderer) plan(entries []acp.PlanEntry) {
	r.start("plan")
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
	r.start("notice")
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

// Pad ends the output with a blank line (Spacing only), so that the next
// prompt does not stick to the reply. Nothing if nothing was printed.
func (r *Renderer) Pad() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.line()
	if r.o.Spacing && r.block != "" && r.nl < 2 {
		r.write("\n")
	}
}

// oneLine returns the first line of s, marking anything cut off.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
