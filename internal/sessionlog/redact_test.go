package sessionlog

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

type redactCase struct {
	line    int
	input   string
	want    string
	comment string
}

func loadRedactCases(tb testing.TB) []redactCase {
	tb.Helper()
	data, err := os.ReadFile("testdata/redact.tsv")
	if err != nil {
		tb.Fatal(err)
	}

	var cases []redactCase
	for n, text := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		n++
		if strings.TrimSpace(text) == "" && !strings.Contains(text, "\t") || strings.HasPrefix(text, "#") {
			continue
		}
		cols := strings.Split(text, "\t")
		if len(cols) < 2 || len(cols) > 3 {
			tb.Fatalf("redact.tsv:%d: want 2 or 3 tab-separated columns, got %d", n, len(cols))
		}
		c := redactCase{line: n, input: cols[0], want: cols[1]}
		if c.want == "=" {
			c.want = c.input
		}
		if len(cols) == 3 {
			c.comment = cols[2]
		}
		cases = append(cases, c)
	}
	return cases
}

func TestRedactGolden(t *testing.T) {
	cases := loadRedactCases(t)
	if len(cases) < 100 {
		t.Errorf("redact.tsv has %d cases, want at least 100", len(cases))
	}
	for _, c := range cases {
		if got := Redact(c.input); got != c.want {
			t.Errorf("redact.tsv:%d: Redact(%q)\n\tgot  %q\n\twant %q (%s)", c.line, c.input, got, c.want, c.comment)
		}
	}
}

func TestRedactMultiline(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{
			name: "one secret per line",
			input: "export GITHUB_TOKEN=abc123\n" +
				"ls\n" +
				"curl -H 'Authorization: Bearer xyz' https://x\n",
			want: "export GITHUB_TOKEN=[REDACTED]\n" +
				"ls\n" +
				"curl -H 'Authorization: Bearer [REDACTED]' https://x\n",
		},
		{
			name:  "bare header ends at the end of its line",
			input: "Authorization: Bearer abc123\nnext line\n",
			want:  "Authorization: Bearer [REDACTED]\nnext line\n",
		},
		{
			name:  "unclosed quote ends at the end of its line",
			input: "export TOKEN='abc\necho visible\n",
			want:  "export TOKEN='[REDACTED]\necho visible\n",
		},
		{
			name: "private key block",
			input: "cat <<EOF > id\n" +
				"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
				"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
				"QyNTUxOQAAACDc3Ryb25nIGZha2Uga2V5IGZvciB0ZXN0cyBvbmx5AAAAAAAAAAAAAAAA\n" +
				"-----END OPENSSH PRIVATE KEY-----\n" +
				"EOF\n",
			want: "cat <<EOF > id\n" +
				"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
				"[REDACTED]\n" +
				"-----END OPENSSH PRIVATE KEY-----\n" +
				"EOF\n",
		},
		{
			name: "RSA private key block",
			input: "-----BEGIN RSA PRIVATE KEY-----\n" +
				"MIIEowIBAAKCAQEAfakefakefake\n" +
				"-----END RSA PRIVATE KEY-----",
			want: "-----BEGIN RSA PRIVATE KEY-----\n" +
				"[REDACTED]\n" +
				"-----END RSA PRIVATE KEY-----",
		},
		{
			name: "truncated private key: to the end",
			input: "-----BEGIN PRIVATE KEY-----\n" +
				"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n" +
				"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
			want: "-----BEGIN PRIVATE KEY-----\n" +
				"[REDACTED]",
		},
		{
			name: "public key and certificate are kept",
			input: "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----\n" +
				"-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUQ\n-----END CERTIFICATE-----\n",
			want: "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----\n" +
				"-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUQ\n-----END CERTIFICATE-----\n",
		},
		{
			name:  "flag value is not taken from the next line",
			input: "mytool --token\nls\n",
			want:  "mytool --token\nls\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.input); got != c.want {
				t.Errorf("Redact(%q)\n\tgot  %q\n\twant %q", c.input, got, c.want)
			}
		})
	}
}

func FuzzRedact(f *testing.F) {
	for _, c := range loadRedactCases(f) {
		f.Add(c.input)
	}
	f.Add("-----BEGIN PRIVATE KEY-----\nabc")
	f.Add("TOKEN=\"abc\\")
	f.Add("curl -u :@")
	f.Fuzz(func(t *testing.T, s string) {
		got := Redact(s)
		if again := Redact(got); again != got {
			t.Fatalf("not idempotent:\n\tinput %q\n\tonce  %q\n\ttwice %q", s, got, again)
		}
		if utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("Redact(%q) = %q: invalid UTF-8", s, got)
		}
		if strings.Count(got, "\n") != strings.Count(s, "\n") && !strings.Contains(s, "PRIVATE KEY-----") {
			t.Fatalf("Redact(%q) = %q: lines were joined or split", s, got)
		}
	})
}
