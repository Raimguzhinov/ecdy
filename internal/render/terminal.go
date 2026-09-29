package render

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Command output in _meta (ADR 0005). codex-acp and pi-acp do not put a
// command's output in the tool call's content: they send a "terminal" content
// block and the output in tool_call_update._meta, as
// {"terminal_output_delta" or "terminal_output": {"terminal_id", "data"}},
// data being the next piece to append in both. This is Zed's convention, not
// the ACP schema; it needs no client capability.
var termOutputKeys = []string{"terminal_output_delta", "terminal_output"}

// maxTermBytes bounds the output kept per tool call: only the last lines are
// shown.
const maxTermBytes = 64 << 10

// termBuf keeps the tail of a command's raw output. It is cleaned only when
// shown, since an escape sequence may be split across pieces.
type termBuf struct {
	b       []byte
	dropped int  // lines whose end was dropped
	cut     bool // b starts inside a line
}

func (t *termBuf) add(data string) {
	t.b = append(t.b, data...)
	if over := len(t.b) - maxTermBytes; over > 0 {
		t.dropped += strings.Count(string(t.b[:over]), "\n")
		t.b = append(t.b[:0], t.b[over:]...)
		t.cut = true
	}
}

// addMeta appends the command output that meta carries, if any.
func (t *termBuf) addMeta(meta map[string]any) {
	for _, k := range termOutputKeys {
		if v, ok := meta[k].(map[string]any); ok {
			if data, ok := v["data"].(string); ok {
				t.add(data)
			}
		}
	}
}

// lines returns the last n lines of the output, cleaned, preceded by a
// "… N earlier lines" line if some are left out.
func (t *termBuf) lines(n int) []string {
	if len(t.b) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(clean(string(t.b)), "\n"), "\n")
	earlier := t.dropped
	if t.cut {
		// The first line lost its beginning, maybe in the middle of a
		// character or an escape sequence.
		lines = lines[1:]
		earlier++
	}
	if len(lines) > n {
		earlier += len(lines) - n
		lines = lines[len(lines)-n:]
	}
	if earlier > 0 {
		lines = append([]string{fmt.Sprintf("… %d earlier lines", earlier)}, lines...)
	}
	return lines
}

// clean makes a program's terminal output safe to print as plain lines:
// escape sequences (CSI, OSC, DCS and the like, two-byte ones) and control
// characters other than tab and newline are removed, invalid UTF-8 is
// replaced, and a carriage return keeps only what follows it on its line, as
// a terminal would show a progress bar. The output must not be able to move
// the cursor, change the title or clear the user's screen.
// Sequences: ECMA-48 (https://ecma-international.org/publications-and-standards/standards/ecma-48/),
// section 5.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			// C0, DEL and C1 controls (U+009B is a CSI to some terminals).
		default:
			b.WriteRune(r)
		}
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// skipEscape returns the index after the escape sequence starting at s[i]
// (an ESC); an unfinished sequence runs to the end of s.
func skipEscape(s string, i int) int {
	i++
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameters and intermediates, then a final byte 0x40–0x7e.
		for i++; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return i
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: up to BEL or ST (ESC \).
		for i++; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return i
	default:
		// Intermediate bytes (0x20–0x2f), then one final byte.
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) {
			i++
		}
		return i
	}
}
