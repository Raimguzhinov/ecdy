package classify

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// goldenFS is the directory the golden cases run in; keep it in sync with
// the header of testdata/cases.tsv.
func goldenFS() fstest.MapFS {
	dir := &fstest.MapFile{Mode: 0o755 | os.ModeDir}
	return fstest.MapFS{
		"configs":   {},
		"tmp":       dir,
		"main.go":   {},
		"go.mod":    {},
		"README.md": {},
		"Makefile":  {},
		"docs":      dir,
		"Загрузки":  dir,
		"notes":     {},
		"a":         {},
	}
}

type goldenCase struct {
	line      int
	input     string
	firstKind Kind
	want      Verdict
	comment   string
}

func loadGolden(tb testing.TB) []goldenCase {
	tb.Helper()
	data, err := os.ReadFile("testdata/cases.tsv")
	if err != nil {
		tb.Fatal(err)
	}

	var cases []goldenCase
	for n, text := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		n++
		if strings.TrimSpace(text) == "" && !strings.Contains(text, "\t") || strings.HasPrefix(text, "#") {
			continue
		}
		cols := strings.Split(text, "\t")
		if len(cols) < 3 || len(cols) > 4 {
			tb.Fatalf("cases.tsv:%d: want 3 or 4 tab-separated columns, got %d", n, len(cols))
		}
		kind, err := ParseKind(cols[1])
		if err != nil {
			tb.Fatalf("cases.tsv:%d: %v", n, err)
		}
		want, err := ParseVerdict(cols[2])
		if err != nil {
			tb.Fatalf("cases.tsv:%d: %v", n, err)
		}
		c := goldenCase{line: n, input: cols[0], firstKind: kind, want: want}
		if len(cols) == 4 {
			c.comment = cols[3]
		}
		cases = append(cases, c)
	}
	return cases
}

func TestGolden(t *testing.T) {
	cases := loadGolden(t)
	if len(cases) < 200 {
		t.Errorf("cases.tsv has %d cases, AGENTS.md requires at least 200", len(cases))
	}
	fsys := goldenFS()
	for _, c := range cases {
		got := Classify(Input{Line: c.input, FirstKind: c.firstKind, FS: fsys})
		if got.Verdict != c.want {
			t.Errorf("cases.tsv:%d: Classify(%q, %s) = %s, want %s (%s)\n\treason: %s, score %d, signals %q",
				c.line, c.input, c.firstKind, got.Verdict, c.want, c.comment, got.Reason, got.Score, got.Signals)
		}
	}
}
