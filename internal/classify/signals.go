package classify

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// scorer counts natural-language signals in the arguments of a line.
type scorer struct {
	line  string
	toks  []token
	sh    shape
	fsys  fs.StatFS
	stats int
	// comments: '#' at the start of a word starts a comment.
	comments bool

	parsed   bool
	file     *syntax.File
	parseErr error
	segs     []segment
}

// segment is a part of the line scored on its own, with its own word count.
type segment struct {
	toks []token
	sh   shape
	end  int // byte offset in the line just past the segment
}

func newScorer(line string, toks []token, sh shape, fsys fs.StatFS, comments bool) *scorer {
	return &scorer{line: line, toks: toks, sh: sh, fsys: fsys, comments: comments}
}

// exists reports whether name is a file in the current directory. The number
// of Stat calls is bounded; past the bound words are assumed not to be
// files, which can only move the verdict towards Ask.
func (s *scorer) exists(name string) bool {
	if s.fsys == nil || !fs.ValidPath(name) || s.stats >= maxStatCalls {
		return false
	}
	s.stats++
	_, err := s.fsys.Stat(name)
	return err == nil
}

func (s *scorer) parse() error {
	if !s.parsed {
		s.parsed = true
		// LangZsh is experimental in mvdan.cc/sh (see its docs for
		// syntax.LangZsh), which is one more reason a parse error is only a
		// weak signal.
		p := syntax.NewParser(syntax.Variant(syntax.LangZsh))
		s.file, s.parseErr = p.Parse(strings.NewReader(s.line), "")
	}
	return s.parseErr
}

// parseOK reports whether the line is complete, valid zsh syntax.
func (s *scorer) parseOK() bool { return s.parse() == nil }

// score returns the total signal weight, the part of it that comes only
// from counting word-like arguments, and a description of each signal.
func (s *scorer) score() (score, countOnly int, signals []string) {
	add := func(w int, format string, args ...any) {
		score += w
		signals = append(signals, fmt.Sprintf(format, args...))
	}

	seen := make(map[string]bool)
	wordy := 0
	nonASCII := false
	segs := s.segments()
	for _, seg := range segs {
		n := 0
		for _, i := range seg.sh.args {
			t := seg.toks[i]
			core, ok := s.word(t)
			if !ok {
				continue
			}
			n++
			if t.contraction {
				add(weightStrong, "contraction %q", core)
			}
			lw := strings.ToLower(core)
			if !seen[lw] {
				seen[lw] = true
				switch {
				case strongStopWords[lw]:
					add(weightStrong, "stop word %q", lw)
				case weakStopWords[lw]:
					add(weightWeak, "weak stop word %q", lw)
				}
			}
			if !nonASCII && hasNonASCIILetter(core) {
				nonASCII = true
				add(weightStrong, "non-ASCII word %q", core)
			}
		}
		wordy = max(wordy, n)
	}
	if wordy >= wordyArgs {
		countOnly += weightWeak
		add(weightWeak, "%d word-like arguments", wordy)
	}
	if wordy >= veryWordyArgs {
		countOnly += weightWeak
		add(weightWeak, "at least %d word-like arguments", veryWordyArgs)
	}
	last := slices.MaxFunc(segs, func(a, b segment) int { return a.end - b.end })
	if last.end == len(s.line) && s.trailingQuestion(last) {
		add(weightStrong, "trailing question mark")
	}
	if err := s.parse(); err != nil && !syntax.IsIncomplete(err) {
		add(weightWeak, "does not parse as zsh")
	}
	return score, countOnly, signals
}

// segments splits the line into the parts that are scored separately. A
// single line, a block that does not parse, or a block with a word starting
// with '#' when the shell has no comments (the parser would drop the words
// after it), is one segment, so its word-like arguments are counted together. A multi-line block that parses
// has one segment per simple command (see simpleCommands); comments, heredoc
// bodies, for/select word lists and command substitutions belong to none.
func (s *scorer) segments() []segment {
	if s.segs == nil {
		s.segs = s.split()
	}
	return s.segs
}

func (s *scorer) split() []segment {
	whole := []segment{{toks: s.toks, sh: s.sh, end: len(s.line)}}
	if !strings.Contains(s.line, "\n") || !s.comments && hasHashWord(s.toks) || !s.parseOK() {
		return whole
	}
	var segs []segment
	for _, r := range simpleCommands(s.file) {
		if r[0] > r[1] || r[1] > len(s.line) {
			return whole
		}
		toks := lex(s.line[r[0]:r[1]])
		segs = append(segs, segment{toks: toks, sh: analyze(toks), end: r[1]})
	}
	if len(segs) == 0 {
		return whole
	}
	return segs
}

func hasHashWord(toks []token) bool {
	return slices.ContainsFunc(toks, func(t token) bool { return !t.op && strings.HasPrefix(t.text, "#") })
}

// simpleCommands returns the byte ranges of the simple commands in f, outside
// command and process substitutions.
func simpleCommands(f *syntax.File) [][2]int {
	var ranges [][2]int
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false
		case *syntax.CallExpr, *syntax.DeclClause, *syntax.LetClause:
			ranges = append(ranges, [2]int{int(n.Pos().Offset()), int(n.End().Offset())})
		}
		return true
	})
	return ranges
}

// dangerous reports whether any simple command in the line is destructive.
func (s *scorer) dangerous() bool {
	for _, seg := range s.segments() {
		if isDangerous(seg.toks, seg.sh) {
			return true
		}
	}
	return false
}

// word returns the token stripped of sentence punctuation if it is a
// word-like argument: a plain word made of letters that is not a flag, path,
// glob, number or existing file.
func (s *scorer) word(t token) (string, bool) {
	if t.op || !t.plain {
		return "", false
	}
	core := strings.TrimRight(t.text, ".,!?:;")
	if !isWordLike(core) {
		return "", false
	}
	if s.exists(t.text) || core != t.text && s.exists(core) {
		return "", false
	}
	return core, true
}

// trailingQuestion reports a '?' ending the line after a word: "why?" or
// "what is this ?". A lone "?" after a command ("ls ?") is a glob.
func (s *scorer) trailingQuestion(seg segment) bool {
	n := len(seg.toks)
	if n == 0 {
		return false
	}
	last := seg.toks[n-1]
	if last.op || !last.plain || !strings.HasSuffix(last.text, "?") {
		return false
	}
	if last.text != "?" {
		core := strings.TrimRight(last.text, "?")
		return isWordLike(core) && !s.exists(last.text)
	}
	if n < 2 || len(seg.sh.args) == 0 || seg.sh.args[len(seg.sh.args)-1] != n-1 {
		return false
	}
	// The word before the "?" must itself be an argument.
	for _, i := range seg.sh.args {
		if i == n-2 {
			_, ok := s.word(seg.toks[i])
			return ok
		}
	}
	return false
}

func isWordLike(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	if !unicode.IsLetter(r) || strings.HasSuffix(s, "-") {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && r != '-' && r != '\'' {
			return false
		}
	}
	return true
}

func hasNonASCIILetter(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
