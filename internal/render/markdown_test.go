package render

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// sgr makes the styles in rendered markdown readable: ESC [ 0;1 m → <0;1>.
func sgr(s string) string {
	return sgrRe.ReplaceAllStringFunc(s, func(m string) string { return "<" + m[2:len(m)-1] + ">" })
}

// renderMD feeds the pieces one after another and flushes at the end.
func renderMD(pieces ...string) string {
	var m markdown
	var b strings.Builder
	for _, p := range pieces {
		b.WriteString(m.feed(p))
	}
	b.WriteString(m.flush())
	return b.String()
}

var markdownCases = []struct {
	name, in, want string
}{
	{"plain", "hello world\n", "hello world\n"},
	{"empty line", "a\n\nb\n", "a\n\nb\n"},
	{"unfinished line", "hello", "hello"},
	{"heading 1", "# Title\n", "<0;1;35>Title<0>\n"},
	{"heading 3", "### Sub *it*\n", "<0;1>Sub <0;1;3>it<0>\n"},
	{"heading indented", "  ## Two\n", "<0;1;35>Two<0>\n"},
	{"heading empty", "#\n", "\n"},
	{"not a heading: no space", "#hashtag\n", "#hashtag\n"},
	{"not a heading: seven", "####### x\n", "####### x\n"},
	{"bold", "a **b** c\n", "a <0;1>b<0> c\n"},
	{"bold underscores", "a __b__ c\n", "a <0;1>b<0> c\n"},
	{"italic", "a *b* c\n", "a <0;3>b<0> c\n"},
	{"italic underscore", "a _b_ c\n", "a <0;3>b<0> c\n"},
	{"bold italic", "***x***\n", "<0;1;3>x<0>\n"},
	{"bold inside italic", "*a **b** c*\n", "<0;3>a <0;1;3>b<0;3> c<0>\n"},
	{"snake_case stays", "use snake_case_name here\n", "use snake_case_name here\n"},
	{"lone star", "a * b\n", "a * b\n"},
	{"stars between digits", "2*3*4\n", "2<0;3>3<0>4\n"},
	{"unclosed bold ends at the line end", "**open\nnext\n", "<0;1>open<0>\nnext\n"},
	{"long run literal", "a **** b\n", "a **** b\n"},
	{"code span", "run `go test` now\n", "run <0;36>go test<0> now\n"},
	{"code span keeps markup", "`a *b* c`\n", "<0;36>a *b* c<0>\n"},
	{"double backtick span", "``a ` b``\n", "<0;36>a ` b<0>\n"},
	{"unclosed code span", "`abc\n", "<0;36>abc<0>\n"},
	{"escape", `\*not\* \_x\_ \\ \q` + "\n", `*not* _x_ \ \q` + "\n"},
	{"trailing backslash", "a\\\n", "a\\\n"},
	{"link", "see [docs](https://x.dev) now\n", "see <0;4>docs<0> <0;2>(https://x.dev)<0> now\n"},
	{"link same as url", "[https://x.dev](https://x.dev)\n", "<0;4>https://x.dev<0>\n"},
	{"brackets without link", "a [b] c\n", "a [b] c\n"},
	{"unclosed bracket", "a [b c\n", "a [b c\n"},
	{"bracket then paren later", "[a] (b)\n", "[a] (b)\n"},
	{"link text with emphasis in brackets", "[**x**] y\n", "[<0;1>x<0>] y\n"},
	{"unclosed link url", "[a](b\n", "[a](b\n"},
	{"bullet", "- one\n- two\n", "• one\n• two\n"},
	{"bullet star and plus", "* one\n+ two\n", "• one\n• two\n"},
	{"nested bullet", "- a\n  - b\n", "• a\n  • b\n"},
	{"bullet with bold", "- **k**: v\n", "• <0;1>k<0>: v\n"},
	{"task list", "- [ ] todo\n- [x] done\n", "• ☐ todo\n• ☑ done\n"},
	{"ordered", "1. one\n10) ten\n", "1. one\n10) ten\n"},
	{"digits only", "2024 was\n", "2024 was\n"},
	{"dash without space", "-flag\n", "-flag\n"},
	{"star emphasis at line start", "*it* yes\n", "<0;3>it<0> yes\n"},
	{"quote", "> said *so*\n", "<0;2>│ <0>said <0;3>so<0>\n"},
	{"quote without space", ">x\n", "<0;2>│ <0>x\n"},
	{"rule dashes", "---\n", "<0;2>────────────────────<0>\n"},
	{"rule stars spaced", "* * *\n", "<0;2>────────────────────<0>\n"},
	{"rule underscores", "___\n", "<0;2>────────────────────<0>\n"},
	{"two dashes not a rule", "--\n", "--\n"},
	{"fence", "```go\nx := *p\n```\nafter\n", "<0;2>```go<0>\n<0;36>x := *p<0>\n<0;2>```<0>\nafter\n"},
	{"fence tilde", "~~~\n# not heading\n~~~\n", "<0;2>~~~<0>\n<0;36># not heading<0>\n<0;2>~~~<0>\n"},
	{"fence closing needs same length", "````\n```\n````\n", "<0;2>````<0>\n<0;36>```<0>\n<0;2>````<0>\n"},
	{"fence closing with trailing spaces", "```\na\n```  \nb\n", "<0;2>```<0>\n<0;36>a<0>\n<0;2>```  <0>\nb\n"},
	{"fence closing with text is content", "```\n``` x\n```\n", "<0;2>```<0>\n<0;36>``` x<0>\n<0;2>```<0>\n"},
	{"fence empty line", "```\n\n```\n", "<0;2>```<0>\n\n<0;2>```<0>\n"},
	{"backtick fence with backtick info is inline", "```a`b\n", "<0;36>a`b<0>\n"},
	{"unclosed fence", "```\ncode", "<0;2>```<0>\n<0;36>code<0>"},
	{"table is plain", "| a | *b* |\n", "| a | <0;3>b<0> |\n"},
	{"cyrillic", "это **важно**, да\n", "это <0;1>важно<0>, да\n"},
	{"tabs kept", "a\tb\n", "a\tb\n"},
	{"escape sequences removed", "a\x1b[31mred\x1b[0m\x1b]0;title\x07b\n", "aredb\n"},
	{"a newline ends an escape sequence", "a\x1b\nb\x1b[3\nc\x1b]0;t\nd\n", "a\nb\nc\nd\n"},
	{"controls removed", "a\x00b\x07c\x7fd\u009be\r\n", "abcde\n"},
	{"invalid utf-8", "a\xffb\n", "a�b\n"},
}

func TestMarkdown(t *testing.T) {
	for _, tt := range markdownCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := sgr(renderMD(tt.in)); got != tt.want {
				t.Errorf("render(%q)\ngot  %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// The output must not depend on how the agent splits its text: every case
// split in two at every byte, and in many random pieces.
func TestMarkdownChunks(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, tt := range markdownCases {
		want := renderMD(tt.in)
		for i := 1; i < len(tt.in); i++ {
			if got := renderMD(tt.in[:i], tt.in[i:]); got != want {
				t.Fatalf("%s: split at %d (%q | %q)\ngot  %q\nwant %q", tt.name, i, tt.in[:i], tt.in[i:], sgr(got), sgr(want))
			}
		}
		for range 50 {
			var pieces []string
			for s := tt.in; s != ""; {
				n := 1 + rng.IntN(4)
				n = min(n, len(s))
				pieces = append(pieces, s[:n])
				s = s[n:]
			}
			if got := renderMD(pieces...); got != want {
				t.Fatalf("%s: pieces %q\ngot  %q\nwant %q", tt.name, pieces, sgr(got), sgr(want))
			}
		}
	}
}

// A flush in the middle of a line (a tool call or a dialog interrupting the
// text) ends the line's styles and prints what was held back.
func TestMarkdownFlush(t *testing.T) {
	var m markdown
	got := m.feed("**bo") + "|" + m.flush() + "|" + m.feed("ld** x\n")
	if want := "<0;1>bo|<0>|ld** x\n"; sgr(got) != want {
		t.Errorf("got  %q\nwant %q", sgr(got), want)
	}
	m = markdown{}
	got = m.feed("## Hea") + "|" + m.flush()
	if want := "<0;1;35>Hea|<0>"; sgr(got) != want {
		t.Errorf("got  %q\nwant %q", sgr(got), want)
	}
	// A character cut at the end is not lost.
	m = markdown{}
	if got := m.feed("a\xd0") + m.flush(); got != "a\ufffd" {
		t.Errorf("got %q", got)
	}
	// A code block goes on after the interruption.
	m = markdown{}
	got = m.feed("```\na") + m.flush() + m.feed("\n# b\n```\n")
	if want := "<0;2>```<0>\n<0;36>a<0>\n<0;36># b<0>\n<0;2>```<0>\n"; sgr(got) != want {
		t.Errorf("got  %q\nwant %q", sgr(got), want)
	}
}

// Held-back text is bounded by the line: nothing is held across a newline.
func TestMarkdownHoldsOneLineAtMost(t *testing.T) {
	var m markdown
	for _, in := range []string{"[a very long link text", "```go", "***", "# ", "- [ ", "  ", "12", "\\", "**"} {
		m = markdown{}
		if out := m.feed(in + "\n"); !strings.HasSuffix(out, "\n") {
			t.Errorf("%q: the line was not finished: %q", in, out)
		}
	}
}

var sgrRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func FuzzMarkdown(f *testing.F) {
	for _, tt := range markdownCases {
		f.Add(tt.in, uint8(3))
	}
	f.Fuzz(func(t *testing.T, in string, cut uint8) {
		whole := renderMD(in)
		i := int(cut) % (len(in) + 1)
		if split := renderMD(in[:i], in[i:]); split != whole {
			t.Fatalf("split at %d: %q != %q", i, split, whole)
		}
		if !utf8.ValidString(whole) {
			t.Fatalf("invalid UTF-8: %q", whole)
		}
		// Only SGR sequences of ecdy's own, no controls from the input.
		plain := sgrRe.ReplaceAllString(whole, "")
		for _, r := range plain {
			if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f || r >= 0x80 && r < 0xa0 {
				t.Fatalf("control %U in %q", r, whole)
			}
		}
		if strings.Count(whole, "\n") != strings.Count(in, "\n") {
			t.Fatalf("line count changed: %q → %q", in, whole)
		}
	})
}
