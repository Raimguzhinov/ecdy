package classify

import "strings"

// token is a shell word or operator produced by lex.
//
// lex is deliberately not a full shell parser: it only needs to find command
// positions, redirection targets and the unquoted words the natural-language
// heuristics look at. Syntax validity is checked separately with
// mvdan.cc/sh/v3/syntax.
type token struct {
	text  string // raw source text
	start int    // byte offset in the line
	end   int
	op    bool // control or redirection operator
	// plain is true when the word has no quoting, escapes or expansions, so
	// text is exactly the literal value the shell would see.
	plain bool
	// contraction is set when the word contains an unmatched apostrophe
	// between two letters ("isn't", "repo's"): in a shell that opens a quote,
	// in English it is a contraction or a possessive.
	contraction bool
}

// Operators, longest first so that prefix matching picks the right one.
var operators = []string{
	"&>>", "<<<", "<<-",
	"||", "|&", "&&", "&>", ";;", "<<", "<>", "<&", ">>", ">&", ">|",
	"|", "&", ";", "(", ")", "<", ">",
}

// lex splits line into tokens. It never fails: unterminated quotes and
// substitutions simply extend to the end of the line.
func lex(line string) []token {
	var toks []token
	i := 0
	for i < len(line) {
		switch line[i] {
		case ' ', '\t', '\r':
			i++
			continue
		case '\n':
			toks = append(toks, token{text: ";", start: i, end: i + 1, op: true})
			i++
			continue
		}
		if op := matchOperator(line[i:]); op != "" {
			toks = append(toks, token{text: op, start: i, end: i + len(op), op: true})
			i += len(op)
			continue
		}
		// A file descriptor number glued to a redirection ("2>", "2>&1")
		// belongs to the operator, not to the argument list.
		if j := skipDigits(line, i); j > i && j < len(line) && (line[j] == '<' || line[j] == '>') {
			op := matchOperator(line[j:])
			toks = append(toks, token{text: line[i : j+len(op)], start: i, end: j + len(op), op: true})
			i = j + len(op)
			continue
		}
		t := lexWord(line, i)
		toks = append(toks, t)
		i = t.end
	}
	return toks
}

func matchOperator(s string) string {
	for _, op := range operators {
		if strings.HasPrefix(s, op) {
			return op
		}
	}
	return ""
}

func skipDigits(s string, i int) int {
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

func isWordBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '|', '&', ';', '(', ')', '<', '>':
		return true
	}
	return false
}

func lexWord(line string, start int) token {
	t := token{start: start, plain: true}
	i := start
	for i < len(line) {
		if isWordBreak(line[i]) {
			// NAME=(...) is an array assignment, not a subshell.
			if line[i] == '(' && isAssignment(line[start:i]+"x") && line[i-1] == '=' {
				t.plain = false
				i = skipBalanced(line, i, '(', ')')
				continue
			}
			break
		}
		switch c := line[i]; c {
		case '\\':
			t.plain = false
			i += 2
		case '\'':
			if j := strings.IndexByte(line[i+1:], '\''); j >= 0 {
				t.plain = false
				i += j + 2
				continue
			}
			// Unmatched: keep it as a literal apostrophe.
			if i > start && isASCIILetter(line[i-1]) && i+1 < len(line) && isASCIILetter(line[i+1]) {
				t.contraction = true
			} else {
				t.plain = false
			}
			i++
		case '"':
			t.plain = false
			i = skipDoubleQuoted(line, i+1)
		case '`':
			t.plain = false
			if j := strings.IndexByte(line[i+1:], '`'); j >= 0 {
				i += j + 2
			} else {
				i = len(line)
			}
		case '$':
			t.plain = false
			i = skipDollar(line, i)
		default:
			i++
		}
	}
	if i > len(line) {
		i = len(line)
	}
	t.end = i
	t.text = line[start:i]
	return t
}

// skipDoubleQuoted returns the index just past the closing quote of a
// double-quoted string whose body starts at i.
func skipDoubleQuoted(line string, i int) int {
	for i < len(line) {
		switch line[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i + 1
		case '$':
			i = skipDollar(line, i)
			continue
		case '`':
			if j := strings.IndexByte(line[i+1:], '`'); j >= 0 {
				i += j + 2
				continue
			}
			return len(line)
		}
		i++
	}
	return len(line)
}

// skipDollar skips an expansion starting at line[i] == '$'.
func skipDollar(line string, i int) int {
	i++
	if i >= len(line) {
		return i
	}
	switch line[i] {
	case '(':
		return skipBalanced(line, i, '(', ')')
	case '{':
		return skipBalanced(line, i, '{', '}')
	case '[':
		return skipBalanced(line, i, '[', ']')
	case '\'':
		// $'...' with backslash escapes.
		for j := i + 1; j < len(line); j++ {
			switch line[j] {
			case '\\':
				j++
			case '\'':
				return j + 1
			}
		}
		return len(line)
	}
	return i
}

// skipBalanced skips from line[i] == open to the matching close, honoring
// nested pairs and quotes. Unbalanced input extends to the end of the line.
func skipBalanced(line string, i int, open, closing byte) int {
	depth := 0
	for i < len(line) {
		switch c := line[i]; c {
		case '\\':
			i += 2
			continue
		case '\'':
			if j := strings.IndexByte(line[i+1:], '\''); j >= 0 {
				i += j + 2
				continue
			}
		case '"':
			i = skipDoubleQuoted(line, i+1)
			continue
		case open:
			depth++
		case closing:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return len(line)
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
