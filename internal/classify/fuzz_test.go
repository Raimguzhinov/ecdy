package classify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzClassify(f *testing.F) {
	for _, c := range loadGolden(f) {
		f.Add(c.input, int(c.firstKind))
	}
	f.Add("echo $(( 1 + ${#a[@]} ))'\"`", int(KindBuiltin))
	f.Add("x=(((", int(KindNone))
	f.Add("2>", int(KindNone))
	fsys := goldenFS()
	f.Fuzz(func(t *testing.T, line string, kind int) {
		k := Kind(kind % (int(KindHashed) + 1))
		if k < 0 {
			k = -k
		}
		res := Classify(Input{Line: line, FirstKind: k, FS: fsys})
		switch res.Verdict {
		case Cmd, Prompt, Ask:
		default:
			t.Fatalf("invalid verdict %d", res.Verdict)
		}
		if res.Reason == "" {
			t.Fatal("empty reason")
		}
		if res.Verdict == Prompt && k != KindNone && !strings.HasPrefix(res.Reason, "explicit") {
			t.Fatalf("known first word (%s) classified as prompt: %q", k, line)
		}
		if utf8.ValidString(line) && res.Correction != "" && !utf8.ValidString(res.Correction) {
			t.Fatalf("invalid UTF-8 correction %q", res.Correction)
		}
		_ = FirstWord(line)
	})
}
