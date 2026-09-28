package sessionlog

import (
	"strings"
	"testing"
)

func BenchmarkRedact(b *testing.B) {
	cases := loadRedactCases(b)
	var lines []string
	for _, c := range cases {
		lines = append(lines, c.input)
	}
	block := strings.Join(lines, "\n")
	b.SetBytes(int64(len(block)))
	for b.Loop() {
		Redact(block)
	}
}

// BenchmarkRedactPathological feeds inputs that make a backtracking or
// quadratic implementation slow: one long line with many partial matches.
func BenchmarkRedactPathological(b *testing.B) {
	for _, in := range []string{
		strings.Repeat("curl -u ", 20000),
		strings.Repeat("TOKEN='", 20000),
		strings.Repeat("-----BEGIN PRIVATE KEY-----", 5000),
		strings.Repeat("--token ", 20000),
		strings.Repeat(`"key":"`, 20000),
	} {
		b.Run(in[:8], func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			for b.Loop() {
				Redact(in)
			}
		})
	}
}
