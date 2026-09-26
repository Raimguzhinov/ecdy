// Package classify decides whether a line typed in the shell is a command, a
// prompt for the agent, or ambiguous enough that the user has to choose.
//
// Classify is a pure function: the only I/O it does is bounded Stat calls on
// the injected file system. The rule cascade is described in AGENTS.md,
// section 4; the scoring of natural-language signals in
// docs/adr/0001-classifier-signal-scoring.md.
package classify

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Verdict is the outcome of classification.
type Verdict int

const (
	// Cmd: let the shell run the line as usual.
	Cmd Verdict = iota
	// Prompt: send the line to the agent.
	Prompt
	// Ask: never run the line without asking the user first.
	Ask
)

var verdictNames = [...]string{Cmd: "cmd", Prompt: "prompt", Ask: "ask"}

func (v Verdict) String() string {
	if v >= 0 && int(v) < len(verdictNames) {
		return verdictNames[v]
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}

// MarshalText implements encoding.TextMarshaler.
func (v Verdict) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

// ParseVerdict parses the output of Verdict.String.
func ParseVerdict(s string) (Verdict, error) {
	for v, name := range verdictNames {
		if s == name {
			return Verdict(v), nil
		}
	}
	return 0, fmt.Errorf("unknown verdict %q", s)
}

// Kind is what the shell knows about the first word of the line, as printed
// by zsh's `whence -w`.
type Kind int

const (
	KindNone Kind = iota // not found
	KindAlias
	KindFunction
	KindBuiltin
	KindCommand
	KindReserved
	KindHashed
)

var kindNames = [...]string{
	KindNone:     "none",
	KindAlias:    "alias",
	KindFunction: "function",
	KindBuiltin:  "builtin",
	KindCommand:  "command",
	KindReserved: "reserved",
	KindHashed:   "hashed",
}

func (k Kind) String() string {
	if k >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// ErrUnknownKind is returned by ParseKind for names `whence -w` never prints.
var ErrUnknownKind = errors.New("unknown first-word kind")

// ParseKind parses a kind name as printed by `whence -w`.
func ParseKind(s string) (Kind, error) {
	for k, name := range kindNames {
		if s == name {
			return Kind(k), nil
		}
	}
	return 0, fmt.Errorf("%w %q", ErrUnknownKind, s)
}

// Config holds the user-tunable parts of the classifier.
type Config struct {
	// PromptPrefixes force a line to be a prompt. nil means DefaultPromptPrefixes;
	// an empty non-nil slice disables prefixes.
	PromptPrefixes []string
	// ExtraCommands are added to the built-in typo-suggestion candidates.
	ExtraCommands []string
}

// DefaultPromptPrefixes are used when Config.PromptPrefixes is nil.
var DefaultPromptPrefixes = []string{"?"}

// Input is everything Classify looks at.
type Input struct {
	Line string
	// FirstKind is the kind of the first command word (after assignments
	// and precommand modifiers), computed by the shell.
	FirstKind Kind
	// FS is rooted at the current directory and is used to tell file
	// names from words. nil means "no file exists".
	FS     fs.StatFS
	Config Config
}

// Result is the verdict plus everything the shell integration and --json
// need to explain it.
type Result struct {
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason"`
	// Prompt is the text for the agent: the line with any explicit prefix
	// stripped. Set for Prompt and Ask.
	Prompt string `json:"prompt,omitempty"`
	// Suggestion is the command the first word is probably a typo of, and
	// Correction the whole line with that fix applied.
	Suggestion string `json:"suggestion,omitempty"`
	Correction string `json:"correction,omitempty"`
	// Dangerous means the line runs a destructive command; the Ask dialog
	// must default to sending it to the agent.
	Dangerous bool     `json:"dangerous,omitempty"`
	Score     int      `json:"score"`
	Signals   []string `json:"signals,omitempty"`
}

// Signal weights and thresholds; see ADR 0001.
const (
	weightStrong      = 2 // strong stop word, non-ASCII word, trailing '?', contraction
	weightWeak        = 1 // weak stop word, parse error
	wordyArgs         = 3 // this many word-like arguments add weightWeak
	veryWordyArgs     = 6 // this many add weightWeak once more
	askThreshold      = 2 // a known command with this score is Ask
	dangerThreshold   = 1 // a dangerous command with this score, not counting the word count, is Ask
	maxStatCalls      = 16
	minTypoLen        = 2
	transpositionOnly = 2 // words this short only match by swapping two letters
)

// Classify applies the rule cascade from AGENTS.md section 4.
func Classify(in Input) Result {
	line := strings.TrimSpace(in.Line)

	// Rule 1: empty line.
	if line == "" {
		return Result{Verdict: Cmd, Reason: "empty line"}
	}

	// Rule 2: explicit prefix.
	prefixes := in.Config.PromptPrefixes
	if prefixes == nil {
		prefixes = DefaultPromptPrefixes
	}
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(line, p) {
			return Result{
				Verdict: Prompt,
				Reason:  fmt.Sprintf("explicit prompt prefix %q", p),
				Prompt:  strings.TrimSpace(line[len(p):]),
			}
		}
	}

	toks := lex(line)
	sh := analyze(toks)
	sc := newScorer(line, toks, sh, in.FS)
	score, countOnly, signals := sc.score()

	first := -1
	if len(sh.cmdWords) > 0 && !toks[0].op {
		first = sh.cmdWords[0]
	}

	// Only assignments (FOO=1) or only redirections: the shell handles it.
	if first < 0 && !toks[0].op {
		return Result{Verdict: Cmd, Reason: "no command word", Score: score, Signals: signals}
	}

	// Rule 3: the first word is unknown to the shell and is a plain name.
	if first >= 0 && in.FirstKind == KindNone && isName(toks[first]) {
		t := toks[first]
		word := strings.TrimRight(t.text, "?")
		if score < askThreshold {
			if sugg, exact := suggest(word, in.Config.ExtraCommands); exact {
				return Result{
					Verdict: Cmd,
					Reason:  fmt.Sprintf("%q is a known command name that is not installed", word),
					Score:   score, Signals: signals,
				}
			} else if sugg != "" {
				return Result{
					Verdict:    Ask,
					Reason:     fmt.Sprintf("unknown command %q looks like a typo of %q", word, sugg),
					Prompt:     line,
					Suggestion: sugg,
					Correction: line[:t.start] + sugg + line[t.start+len(word):],
					Score:      score, Signals: signals,
				}
			}
		}
		return Result{
			Verdict: Prompt,
			Reason:  fmt.Sprintf("first word %q is not a command", word),
			Prompt:  line,
			Score:   score, Signals: signals,
		}
	}

	// Rule 4 and 5: the first word is known (or is a path, an expansion or
	// shell syntax). Only the user may turn such a line into a prompt.
	if in.FirstKind == KindReserved && sc.parseOK() {
		return Result{Verdict: Cmd, Reason: "valid shell syntax", Score: score, Signals: signals}
	}
	dangerous := isDangerous(toks, sh)
	res := Result{Score: score, Signals: signals, Dangerous: dangerous}
	switch {
	case dangerous && score-countOnly >= dangerThreshold:
		res.Verdict = Ask
		res.Reason = "dangerous command with natural-language signals"
		res.Prompt = line
	case score >= askThreshold:
		res.Verdict = Ask
		res.Reason = "known command with natural-language signals"
		res.Prompt = line
	case score > 0:
		res.Verdict = Cmd
		res.Reason = "weak natural-language signals"
	default:
		res.Verdict = Cmd
		res.Reason = "no natural-language signals"
	}
	return res
}

// isName reports whether t can be looked up as a command name: a plain word
// that is not a path, an expansion or a glob.
func isName(t token) bool {
	if t.op || !t.plain || t.text == "" {
		return false
	}
	name := strings.TrimRight(t.text, "?") // "why?" is a word, not a glob
	if name == "" {
		return false
	}
	switch name[0] {
	case '.', '/', '~', '-', '=':
		return false
	}
	return !strings.ContainsAny(name, "/*?[]{}=")
}

// shape is the command structure of a token list.
type shape struct {
	cmdWords []int // indexes of command words
	args     []int // indexes of argument words (not redirection targets)
	// segEnd[i] is the index one past the last token of the simple command
	// whose command word is cmdWords[i].
	segEnd []int
}

func analyze(toks []token) shape {
	var sh shape
	expectCmd := true
	redirTarget := false
	pre := ""          // precommand whose options are being skipped
	skipValue := false // the next word is the value of a precommand option
	for i, t := range toks {
		if t.op {
			if strings.ContainsAny(t.text, "<>") {
				redirTarget = true
				continue
			}
			sh.closeSegment(i)
			expectCmd, pre, skipValue = true, "", false
			continue
		}
		if redirTarget {
			redirTarget = false
			continue
		}
		if !expectCmd {
			sh.args = append(sh.args, i)
			continue
		}
		switch {
		case skipValue:
			skipValue = false
		case isAssignment(t.text):
		case pre != "" && strings.HasPrefix(t.text, "-") && t.text != "-":
			skipValue = precommandArgFlags[pre][t.text]
		case precommands[t.text]:
			pre = t.text
		default:
			sh.cmdWords = append(sh.cmdWords, i)
			expectCmd, pre = false, ""
		}
	}
	sh.closeSegment(len(toks))
	return sh
}

func (sh *shape) closeSegment(end int) {
	if len(sh.cmdWords) > len(sh.segEnd) {
		sh.segEnd = append(sh.segEnd, end)
	}
}

// isAssignment matches NAME=..., NAME+=... and NAME[sub]=...
func isAssignment(s string) bool {
	i := 0
	for i < len(s) && (s[i] == '_' || isASCIILetter(s[i]) || i > 0 && s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 || i == len(s) {
		return false
	}
	if s[i] == '[' {
		j := strings.IndexByte(s[i:], ']')
		if j < 0 {
			return false
		}
		i += j + 1
	}
	if i < len(s) && s[i] == '+' {
		i++
	}
	return i < len(s) && s[i] == '='
}

// unquote strips quotes and backslashes; good enough for recognizing a
// command name like \rm or "rm".
func unquote(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\\', '\'', '"':
			return -1
		}
		return r
	}, s)
}

func cmdName(t token) string {
	return path.Base(unquote(t.text))
}

// FirstWord returns the command word whose kind Input.FirstKind describes:
// the first word after assignments and precommand modifiers, with quotes
// removed. It is empty if the line has no command word.
func FirstWord(line string) string {
	toks := lex(strings.TrimSpace(line))
	sh := analyze(toks)
	if len(sh.cmdWords) == 0 || toks[0].op {
		return ""
	}
	return unquote(toks[sh.cmdWords[0]].text)
}
