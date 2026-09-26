package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func runClassify(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"classify"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func TestClassifyCmd(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--first-kind=command", "--", "git status"}, "cmd\n"},
		{[]string{"--first-kind=command", "--", "rm everything in tmp except configs"}, "ask\n"},
		{[]string{"--first-kind=none", "--", "explain this error"}, "prompt\n"},
		{[]string{"--shell=zsh", "--first-kind=none", "--", "gti", "status"}, "ask\n"},
		{[]string{"--first-kind=command", "--cwd", dir, "--", "rm", "-rf", "--", "the"}, "ask\n"},
		{[]string{"--first-kind=none"}, "cmd\n"},
	}
	for _, tt := range tests {
		got, err := runClassify(t, tt.args...)
		if err != nil {
			t.Errorf("classify %q: %v", tt.args, err)
			continue
		}
		if got != tt.want {
			t.Errorf("classify %q = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestClassifyCmdCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "the"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := runClassify(t, "--first-kind=command", "--cwd", dir, "--", "ls the")
	if err != nil || got != "cmd\n" {
		t.Fatalf("classify with existing file \"the\" = %q, %v; want cmd", got, err)
	}
}

func TestClassifyCmdJSON(t *testing.T) {
	got, err := runClassify(t, "--json", "--first-kind=none", "--", "gti status")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Verdict    string   `json:"verdict"`
		Reason     string   `json:"reason"`
		Suggestion string   `json:"suggestion"`
		Correction string   `json:"correction"`
		Signals    []string `json:"signals"`
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	if res.Verdict != "ask" || res.Reason == "" || res.Suggestion != "git" || res.Correction != "git status" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestClassifyCmdNul(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--format=nul", "--first-kind=command", "--", "git status"}, "cmd\x00\x00\x00\x00" + "0\x00"},
		{[]string{"--format=nul", "--first-kind=none", "--", "? why\tnot"}, "prompt\x00why\tnot\x00\x00\x00" + "0\x00"},
		{[]string{"--format=nul", "--first-kind=none", "--", "gti status"}, "ask\x00gti status\x00git\x00git status\x00" + "0\x00"},
		{
			[]string{"--format=nul", "--first-kind=command", "--", "rm everything in tmp except configs"},
			"ask\x00rm everything in tmp except configs\x00\x00\x00" + "1\x00",
		},
	}
	for _, tt := range tests {
		got, err := runClassify(t, tt.args...)
		if err != nil {
			t.Errorf("classify %q: %v", tt.args, err)
			continue
		}
		if got != tt.want {
			t.Errorf("classify %q = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestClassifyCmdErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--format=yaml", "--", "ls"},
		{"--shell=bash", "--", "ls"},
		{"--first-kind=file", "--", "ls"},
	} {
		if _, err := runClassify(t, args...); err == nil {
			t.Errorf("classify %q: expected error", args)
		}
	}
}

func TestGuessKind(t *testing.T) {
	tests := map[string]string{
		"":                          "none",
		"for":                       "reserved",
		"cd":                        "builtin",
		"sh":                        "command",
		"surely-not-a-command-ecdy": "none",
	}
	for word, want := range tests {
		if got := guessKind(word).String(); got != want {
			t.Errorf("guessKind(%q) = %s, want %s", word, got, want)
		}
	}
}

// coldStartRuns is how many times the cold-start measurement execs the binary.
const coldStartRuns = 300

// coldStartBudget is the p99 target from AGENTS.md section 4.
const coldStartBudget = 15 * time.Millisecond

// measureColdStart builds ecdy and runs `ecdy classify` as a fresh process
// coldStartRuns times, the way the zsh plugin does on every Enter.
func measureColdStart(tb testing.TB) (p50, p99 time.Duration) {
	tb.Helper()
	bin := filepath.Join(tb.TempDir(), "ecdy")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		tb.Fatalf("go build: %v\n%s", err, out)
	}
	lines := []string{
		"rm everything in tmp except configs",
		"git status",
		"ps aux | grep nginx | awk '{print $2}'",
		"почему не работает сборка",
	}
	durs := make([]time.Duration, 0, coldStartRuns)
	for i := range coldStartRuns {
		cmd := exec.Command(bin, "classify", "--shell=zsh", "--first-kind=command", "--", lines[i%len(lines)])
		start := time.Now()
		out, err := cmd.Output()
		d := time.Since(start)
		if err != nil {
			tb.Fatalf("ecdy classify: %v", err)
		}
		if v := strings.TrimSpace(string(out)); v != "cmd" && v != "ask" {
			tb.Fatalf("unexpected verdict %q", v)
		}
		durs = append(durs, d)
	}
	slices.Sort(durs)
	return durs[len(durs)/2], durs[len(durs)*99/100]
}

// TestClassifyColdStart checks the p99 budget. It is opt-in because it
// builds the binary and measures wall time: ECDY_COLDSTART=1 go test ./cmd/ecdy -run ColdStart -v
func TestClassifyColdStart(t *testing.T) {
	if os.Getenv("ECDY_COLDSTART") == "" {
		t.Skip("set ECDY_COLDSTART=1 to measure the cold-start budget")
	}
	p50, p99 := measureColdStart(t)
	t.Logf("ecdy classify cold start over %d runs: p50 %v, p99 %v (budget %v)", coldStartRuns, p50, p99, coldStartBudget)
	if p99 > coldStartBudget {
		t.Fatalf("p99 %v exceeds the %v budget", p99, coldStartBudget)
	}
}

func BenchmarkClassifyColdStart(b *testing.B) {
	for b.Loop() {
		p50, p99 := measureColdStart(b)
		b.ReportMetric(float64(p50.Microseconds())/1000, "p50-ms")
		b.ReportMetric(float64(p99.Microseconds())/1000, "p99-ms")
	}
}
