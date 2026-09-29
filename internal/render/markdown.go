package render

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// markdown renders an agent's markdown for a terminal as it streams in
// (docs/adr/0006-ux.md). It is a subset of CommonMark
// (https://spec.commonmark.org/0.31.2/): headings, emphasis, code spans,
// fenced code blocks, bullet and task lists, block quotes, thematic breaks,
// links and backslash escapes. Everything else (tables, HTML, setext
// headings, indented code) is printed as it is.
//
// Text is printed as soon as its rendering is certain; what is not certain
// yet (the first characters of a line, a delimiter at the end of a chunk, a
// link's text up to its closing parenthesis) is held back, never past the
// end of the line. Every decision looks only at the current line and at
// whether it has ended, so the output does not depend on how the text is
// split into chunks. Styles end at every line end.
//
// The agent's escape sequences and control characters are removed: they
// must not drive the user's terminal.
type markdown struct {
	san sanitizer

	line    []byte // the current line so far, sanitized, without its newline
	pos     int    // bytes of line already rendered
	decided bool   // the line's block kind is known
	fence   string // the opening fence of the code block we are in, or ""

	// Styles of the current line.
	block  string // SGR parameters of the whole line (heading, code block, …)
	bold   bool
	italic bool
	code   int  // backticks of the open code span, 0 outside one
	prev   rune // the last character rendered, for emphasis rules
	cur    string

	out strings.Builder
}

// SGR parameters (ECMA-48, 8.3.117).
const (
	sgrBold      = "1"
	sgrDim       = "2"
	sgrItalic    = "3"
	sgrUnderline = "4"
	sgrCode      = "36"   // cyan
	sgrHeading   = "1;35" // bold magenta, levels 1 and 2
	ruleText     = "────────────────────"
)

// feed renders s and returns what can be printed now.
func (m *markdown) feed(s string) string {
	for _, c := range []byte(m.san.feed(s)) {
		if c == '\n' {
			m.render(true)
			m.style("")
			m.out.WriteByte('\n')
			m.newLine()
			continue
		}
		m.line = append(m.line, c)
	}
	m.render(false)
	return m.take()
}

// flush ends the current line where it is, as if it were complete, without
// printing a newline: the text is interrupted by a tool call, a notice or a
// dialog. A code block stays open.
func (m *markdown) flush() string {
	m.line = append(m.line, m.san.end()...)
	m.render(true)
	m.style("")
	m.newLine()
	return m.take()
}

func (m *markdown) take() string {
	s := m.out.String()
	m.out.Reset()
	return s
}

func (m *markdown) newLine() {
	m.line, m.pos, m.decided = m.line[:0], 0, false
	m.block, m.bold, m.italic, m.code, m.prev = "", false, false, 0, ' '
}

// style switches the terminal to the given SGR parameters (after a reset),
// if they are not the current ones.
func (m *markdown) style(p string) {
	if p == m.cur {
		return
	}
	m.cur = p
	if p == "" {
		m.out.WriteString("\x1b[0m")
		return
	}
	m.out.WriteString("\x1b[0;" + p + "m")
}

// inline is the style of inline text at this point of the line.
func (m *markdown) inline() string {
	p := m.block
	if m.code > 0 {
		return join(p, sgrCode)
	}
	if m.bold {
		p = join(p, sgrBold)
	}
	if m.italic {
		p = join(p, sgrItalic)
	}
	return p
}

// join adds the SGR parameter b to the list a, unless it is there already
// (bold in a heading).
func join(a, b string) string {
	if a == "" {
		return b
	}
	for _, p := range strings.Split(a, ";") {
		if p == b {
			return a
		}
	}
	return a + ";" + b
}

func (m *markdown) emit(style, s string) {
	if s == "" {
		return
	}
	m.style(style)
	m.out.WriteString(s)
	if r, _ := utf8.DecodeLastRuneInString(s); r != utf8.RuneError {
		m.prev = r
	}
}

// render renders as much of the line as is certain. eol: the line is
// complete, so a lookahead that runs out of text sees its end.
func (m *markdown) render(eol bool) {
	if !m.decided {
		if !m.decide(eol) {
			return
		}
		m.decided = true
	}
	for m.pos < len(m.line) {
		n := m.step(eol)
		if n == 0 {
			return // not certain yet
		}
		m.pos += n
	}
}

// decide recognizes the block the line belongs to and renders its prefix.
// It returns false while the line so far does not tell.
func (m *markdown) decide(eol bool) bool {
	l := m.line
	ind := 0
	for ind < len(l) && l[ind] == ' ' {
		ind++
	}
	if ind == len(l) && !eol {
		return false
	}
	if m.fence != "" {
		return m.decideInFence(ind, eol)
	}
	if ind == len(l) {
		m.emit("", string(l)) // a blank line
		m.pos = len(l)
		return true
	}
	c := l[ind]
	if ind <= 3 {
		switch c {
		case '`', '~':
			n := runLen(l, ind)
			if ind+n == len(l) && !eol {
				return false
			}
			if n >= 3 {
				// An opening fence; its info string may not contain a
				// backtick, so the whole line is needed.
				if !eol {
					return false
				}
				if c == '~' || !strings.ContainsRune(string(l[ind+n:]), '`') {
					m.fence = string(l[ind : ind+n])
					m.emit(sgrDim, string(l))
					m.pos = len(l)
					return true
				}
			}
		case '#':
			n := runLen(l, ind)
			if ind+n == len(l) && !eol {
				return false
			}
			if n <= 6 && (ind+n == len(l) || l[ind+n] == ' ') {
				m.block = sgrBold
				if n <= 2 {
					m.block = sgrHeading
				}
				m.pos = min(ind+n+1, len(l))
				return true
			}
		case '>':
			if ind+1 == len(l) && !eol {
				return false
			}
			m.emit(sgrDim, "│ ")
			m.pos = ind + 1
			if m.pos < len(l) && l[m.pos] == ' ' {
				m.pos++
			}
			m.prev = ' '
			return true
		case '-', '*', '_':
			// A thematic break: three or more of the same character,
			// spaces between them allowed, nothing else.
			count, other := 0, false
			for _, b := range l[ind:] {
				switch b {
				case c:
					count++
				case ' ':
				default:
					other = true
				}
			}
			if !other && !eol {
				return false
			}
			if !other && count >= 3 {
				m.emit(sgrDim, ruleText)
				m.pos = len(l)
				return true
			}
		}
	}
	if c == '-' || c == '*' || c == '+' {
		if ind+1 == len(l) && !eol {
			return false
		}
		if ind+1 < len(l) && l[ind+1] == ' ' {
			rest := string(l[ind+2:])
			if !eol && maybeTask(rest) {
				return false
			}
			m.emit("", string(l[:ind])+"• ")
			m.pos = ind + 2
			switch {
			case strings.HasPrefix(rest, "[ ] "):
				m.emit("", "☐ ")
				m.pos += 4
			case strings.HasPrefix(rest, "[x] "), strings.HasPrefix(rest, "[X] "):
				m.emit("", "☑ ")
				m.pos += 4
			}
			m.prev = ' '
			return true
		}
	}
	// Anything else, ordered list items included, is a paragraph.
	m.pos = 0
	return true
}

// maybeTask reports whether s may still become a task's checkbox.
func maybeTask(s string) bool {
	for _, box := range []string{"[ ] ", "[x] ", "[X] "} {
		if len(s) < len(box) && strings.HasPrefix(box, s) {
			return true
		}
	}
	return false
}

// decideInFence tells a closing fence from a line of code.
func (m *markdown) decideInFence(ind int, eol bool) bool {
	l := m.line
	if ind <= 3 {
		n := 0
		for ind+n < len(l) && l[ind+n] == m.fence[0] {
			n++
		}
		if n >= len(m.fence) && strings.TrimRight(string(l[ind+n:]), " ") == "" {
			if !eol {
				return false // spaces may still be followed by text
			}
			m.fence = ""
			m.emit(sgrDim, string(l))
			m.pos = len(l)
			return true
		}
		if ind+n == len(l) && !eol {
			return false // a prefix of a fence
		}
	}
	m.block = sgrCode
	return true
}

// runLen is the length of the run of l[i]'s character starting at i.
func runLen(l []byte, i int) int {
	n := 0
	for i+n < len(l) && l[i+n] == l[i] {
		n++
	}
	return n
}

// step renders the text at m.pos and returns how many bytes it consumed,
// or 0 if it needs more of the line to tell.
func (m *markdown) step(eol bool) int {
	l := m.line[m.pos:]
	if m.block == sgrCode && m.fence != "" {
		// A code block line: no inline markup.
		m.emit(sgrCode, string(l))
		return len(l)
	}
	c := l[0]
	if m.code > 0 {
		if c == '`' {
			n := runLen(l, 0)
			if n == len(l) && !eol {
				return 0
			}
			if n == m.code {
				m.code = 0
				return n
			}
			m.emit(m.inline(), string(l[:n]))
			return n
		}
		return m.char(l)
	}
	switch c {
	case '\\':
		if len(l) == 1 {
			if !eol {
				return 0
			}
			m.emit(m.inline(), "\\")
			return 1
		}
		if l[1] < utf8.RuneSelf && isPunct(rune(l[1])) { // ASCII punctuation only (section 2.4)
			m.emit(m.inline(), string(l[1]))
			return 2
		}
		m.emit(m.inline(), "\\")
		return 1
	case '`':
		// A code span needs a closing run of as many backticks on the line;
		// without one the backticks are printed as they are.
		n := runLen(l, 0)
		closed, known := closer(l, n, eol, func(k int, _, _ rune) bool { return k == n })
		switch {
		case !known:
			return 0
		case closed:
			m.code = n
		default:
			m.emit(m.inline(), string(l[:n]))
		}
		return n
	case '*', '_':
		return m.delim(l, eol)
	case '[':
		return m.link(l, eol)
	}
	return m.char(l)
}

// char renders one character.
func (m *markdown) char(l []byte) int {
	_, size := utf8.DecodeRune(l)
	m.emit(m.inline(), string(l[:size]))
	return size
}

// delim handles a run of * or _: it opens or closes emphasis by the
// CommonMark flanking rules (section 6.2), or is printed as it is.
func (m *markdown) delim(l []byte, eol bool) int {
	n := runLen(l, 0)
	if n == len(l) && !eol {
		return 0
	}
	next := ' '
	if n < len(l) {
		r, _ := utf8.DecodeRune(l[n:])
		next = r
	}
	prev := m.prev
	if prev == 0 {
		prev = ' ' // the start of the text
	}
	canOpen, canClose := flanking(l[0], prev, next)
	if canOpen && (n == 1 && !m.italic || n == 2 && !m.bold || n == 3 && !m.bold && !m.italic) {
		// An opener counts only if a run of the same length can close it
		// later on the line; an unmatched one is literal (section 6.2).
		// Until that is known, the rest of the line is held back.
		closed, known := closer(l, n, eol, func(k int, prev, next rune) bool {
			return k == n && m.canClose(l[0], prev, next)
		})
		if !known {
			return 0
		}
		canOpen = closed
	}
	toggle := func(on *bool) bool {
		switch {
		case *on && canClose:
			*on = false
		case !*on && canOpen:
			*on = true
		default:
			return false
		}
		return true
	}
	ok := false
	switch n {
	case 1:
		ok = toggle(&m.italic)
	case 2:
		ok = toggle(&m.bold)
	case 3:
		b, i := m.bold, m.italic
		if toggle(&m.bold) && toggle(&m.italic) {
			ok = true
		} else {
			m.bold, m.italic = b, i
		}
	}
	if !ok {
		m.emit(m.inline(), string(l[:n]))
		return n
	}
	m.prev = rune(l[0])
	return n
}

// flanking tells whether a run of delimiter c between prev and next can
// open and close emphasis (CommonMark, section 6.2).
func flanking(c byte, prev, next rune) (canOpen, canClose bool) {
	left := !unicode.IsSpace(next) && (!isPunct(next) || unicode.IsSpace(prev) || isPunct(prev))
	right := !unicode.IsSpace(prev) && (!isPunct(prev) || unicode.IsSpace(next) || isPunct(next))
	if c == '_' {
		return left && (!right || isPunct(prev)), right && (!left || isPunct(next))
	}
	return left, right
}

func (m *markdown) canClose(c byte, prev, next rune) bool {
	_, ok := flanking(c, prev, next)
	return ok
}

// closer looks for a run of l[0]'s character after the run of n at the start
// of l that match accepts, given the run's length and the characters around
// it. known is false while the line so far does not tell.
func closer(l []byte, n int, eol bool, match func(k int, prev, next rune) bool) (found, known bool) {
	c := l[0]
	prev := rune(c)
	for i := n; i < len(l); {
		if l[i] == '\\' && c != '`' && i+1 < len(l) {
			prev = rune(l[i+1])
			i += 2
			continue
		}
		if l[i] == c {
			k := runLen(l, i)
			if i+k == len(l) && !eol {
				return false, false
			}
			next := ' '
			if i+k < len(l) {
				next, _ = utf8.DecodeRune(l[i+k:])
			}
			if match(k, prev, next) {
				return true, true
			}
			prev = rune(c)
			i += k
			continue
		}
		r, size := utf8.DecodeRune(l[i:])
		prev = r
		i += size
	}
	return false, eol
}

func isPunct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

// link renders [text](url) as the text underlined and the URL dimmed after
// it. A bracket that turns out not to start a link is printed as it is and
// the text after it is rendered as usual.
func (m *markdown) link(l []byte, eol bool) int {
	s := string(l)
	end := strings.IndexByte(s, ']')
	if end < 0 || end+1 == len(s) {
		if !eol {
			return 0
		}
		return m.literal("[")
	}
	if s[end+1] != '(' {
		return m.literal("[")
	}
	close := strings.IndexByte(s[end+2:], ')')
	if close < 0 {
		if !eol {
			return 0
		}
		return m.literal("[")
	}
	text, url := s[1:end], s[end+2:end+2+close]
	base := m.inline()
	m.emit(join(base, sgrUnderline), text)
	if url != "" && url != text {
		m.emit(base, " ")
		m.emit(join(base, sgrDim), "("+url+")")
	}
	return end + 2 + close + 1
}

func (m *markdown) literal(s string) int {
	m.emit(m.inline(), s)
	return len(s)
}

// sanitizer removes escape sequences and control characters other than tab
// and newline from a stream, and replaces invalid UTF-8, keeping its state
// across chunks: a sequence or a character may be split between them. The
// sequences are the ones clean removes (ECMA-48, section 5).
type sanitizer struct {
	state   int    // one of the esc* states
	partial []byte // the start of a character cut by the chunk's end
}

const (
	escNone     = iota
	escStart    // after ESC
	escCSI      // ESC [ …
	escString   // OSC, DCS, SOS, PM, APC: up to BEL or ST
	escStringST // ESC inside a string: ST if followed by a backslash
	escInter    // intermediate bytes, then one final byte
)

// end returns what is left of a character cut at the end of the stream.
func (s *sanitizer) end() string {
	if len(s.partial) == 0 {
		return ""
	}
	s.partial = s.partial[:0]
	return string(utf8.RuneError)
}

func (s *sanitizer) feed(in string) string {
	if len(s.partial) > 0 {
		in = string(s.partial) + in
		s.partial = s.partial[:0]
	}
	var b strings.Builder
	for i := 0; i < len(in); {
		c := in[i]
		if c == '\n' {
			s.state = escNone // see skipEscape
		}
		switch s.state {
		case escStart:
			i++
			switch {
			case c == '[':
				s.state = escCSI
			case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
				s.state = escString
			case c >= 0x20 && c <= 0x2f:
				s.state = escInter
			default:
				s.state = escNone
			}
			continue
		case escCSI:
			i++
			if c >= 0x40 && c <= 0x7e {
				s.state = escNone
			}
			continue
		case escString:
			i++
			switch c {
			case 0x07:
				s.state = escNone
			case 0x1b:
				s.state = escStringST
			}
			continue
		case escStringST:
			i++
			if c == '\\' {
				s.state = escNone
			} else if c != 0x1b {
				s.state = escString
			}
			continue
		case escInter:
			i++
			if c < 0x20 || c > 0x2f {
				s.state = escNone
			}
			continue
		}
		if c == 0x1b {
			s.state = escStart
			i++
			continue
		}
		if !utf8.FullRuneInString(in[i:]) {
			s.partial = append(s.partial, in[i:]...)
			break
		}
		r, size := utf8.DecodeRuneInString(in[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
