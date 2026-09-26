package classify

import (
	"fmt"
	"io/fs"
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

	parsed   bool
	parseErr error
}

func newScorer(line string, toks []token, sh shape, fsys fs.StatFS) *scorer {
	return &scorer{line: line, toks: toks, sh: sh, fsys: fsys}
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
		_, s.parseErr = p.Parse(strings.NewReader(s.line), "")
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
	for _, i := range s.sh.args {
		t := s.toks[i]
		core, ok := s.word(t)
		if !ok {
			continue
		}
		wordy++
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
	if wordy >= wordyArgs {
		countOnly += weightWeak
		add(weightWeak, "%d word-like arguments", wordy)
	}
	if wordy >= veryWordyArgs {
		countOnly += weightWeak
		add(weightWeak, "at least %d word-like arguments", veryWordyArgs)
	}
	if s.trailingQuestion() {
		add(weightStrong, "trailing question mark")
	}
	if err := s.parse(); err != nil && !syntax.IsIncomplete(err) {
		add(weightWeak, "does not parse as zsh")
	}
	return score, countOnly, signals
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
func (s *scorer) trailingQuestion() bool {
	n := len(s.toks)
	last := s.toks[n-1]
	if last.op || !last.plain || !strings.HasSuffix(last.text, "?") {
		return false
	}
	if last.text != "?" {
		core := strings.TrimRight(last.text, "?")
		return isWordLike(core) && !s.exists(last.text)
	}
	if n < 2 || len(s.sh.args) == 0 || s.sh.args[len(s.sh.args)-1] != n-1 {
		return false
	}
	// The word before the "?" must itself be an argument.
	for _, i := range s.sh.args {
		if i == n-2 {
			_, ok := s.word(s.toks[i])
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
