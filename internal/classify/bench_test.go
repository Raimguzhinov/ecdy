package classify

import "testing"

func BenchmarkClassify(b *testing.B) {
	cases := loadGolden(b)
	fsys := goldenFS()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		c := cases[i%len(cases)]
		Classify(Input{Line: c.input, FirstKind: c.firstKind, FS: fsys})
	}
}

func BenchmarkClassifyCanonical(b *testing.B) {
	in := Input{Line: "rm everything in tmp except configs", FirstKind: KindCommand, FS: goldenFS()}
	b.ReportAllocs()
	for b.Loop() {
		Classify(in)
	}
}
