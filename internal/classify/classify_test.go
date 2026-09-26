package classify

import (
	"slices"
	"testing"
)

func TestResultDetails(t *testing.T) {
	fsys := goldenFS()
	tests := []struct {
		name  string
		in    Input
		want  Result
		check func(t *testing.T, got Result)
	}{
		{
			name: "prefix is stripped from the prompt",
			in:   Input{Line: "  ? why is CI red  ", FirstKind: KindNone},
			want: Result{Verdict: Prompt, Prompt: "why is CI red"},
		},
		{
			name: "custom prefix",
			in:   Input{Line: ",, explain", FirstKind: KindNone, Config: Config{PromptPrefixes: []string{",,"}}},
			want: Result{Verdict: Prompt, Prompt: "explain"},
		},
		{
			name: "prefixes can be disabled",
			in:   Input{Line: "?", FirstKind: KindCommand, Config: Config{PromptPrefixes: []string{}}},
			want: Result{Verdict: Cmd},
		},
		{
			name: "typo suggestion and correction",
			in:   Input{Line: "gti push origin", FirstKind: KindNone},
			want: Result{Verdict: Ask, Prompt: "gti push origin", Suggestion: "git", Correction: "git push origin"},
		},
		{
			name: "typo after a precommand keeps the rest of the line",
			in:   Input{Line: "sudo sytemctl restart nginx", FirstKind: KindNone},
			want: Result{Verdict: Ask, Prompt: "sudo sytemctl restart nginx", Suggestion: "systemctl",
				Correction: "sudo systemctl restart nginx"},
		},
		{
			name: "extra commands are typo candidates",
			in:   Input{Line: "zelij attach", FirstKind: KindNone, Config: Config{ExtraCommands: []string{"zellij"}}},
			want: Result{Verdict: Ask, Prompt: "zelij attach", Suggestion: "zellij", Correction: "zellij attach"},
		},
		{
			name: "dangerous flag is set on ask",
			in:   Input{Line: "rm everything in tmp except configs", FirstKind: KindCommand, FS: fsys},
			want: Result{Verdict: Ask, Prompt: "rm everything in tmp except configs", Dangerous: true},
		},
		{
			name: "dangerous flag is reported on cmd too",
			in:   Input{Line: "rm -rf build", FirstKind: KindCommand, FS: fsys},
			want: Result{Verdict: Cmd, Dangerous: true},
		},
		{
			name: "nil FS means no file exists",
			in:   Input{Line: "rm a", FirstKind: KindCommand},
			want: Result{Verdict: Ask, Prompt: "rm a", Dangerous: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.in)
			if got.Verdict != tt.want.Verdict || got.Prompt != tt.want.Prompt ||
				got.Suggestion != tt.want.Suggestion || got.Correction != tt.want.Correction ||
				got.Dangerous != tt.want.Dangerous {
				t.Errorf("Classify(%q) =\n\t%+v\nwant\n\t%+v", tt.in.Line, got, tt.want)
			}
			if got.Reason == "" {
				t.Error("empty Reason")
			}
		})
	}
}

// Invariant 1 (AGENTS.md section 2): only the user may turn a line whose
// first word the shell knows into a prompt, so such lines are never Prompt.
func TestKnownFirstWordIsNeverPrompt(t *testing.T) {
	for _, c := range loadGolden(t) {
		if c.firstKind == KindNone {
			continue
		}
		for _, k := range []Kind{KindAlias, KindFunction, KindBuiltin, KindCommand, KindReserved, KindHashed} {
			in := Input{Line: c.input, FirstKind: k, FS: goldenFS(), Config: Config{PromptPrefixes: []string{}}}
			if got := Classify(in); got.Verdict == Prompt {
				t.Errorf("Classify(%q, %s) = prompt", c.input, k)
			}
		}
	}
}

func TestDangerousCommands(t *testing.T) {
	dangerous := []string{
		"rm x", "/bin/rm x", `\rm x`, "sudo rm x", "dd if=a of=b", "mkfs.ext4 /dev/x", "mkfs -t ext4 x",
		"kill 1", "pkill x", "killall x", "chmod -R 755 x", "chmod -Rv 755 x", "chmod --recursive 755 x",
		"chown -R u x", "git reset --hard", "git -C repo reset --hard", "git clean -fd", "git push --force",
		"git push -f", "git push --force-with-lease", "git push origin +main", "shutdown now", "reboot",
		"truncate -s0 x", "find . -delete", "find . -exec rm {} +", "ls; rm x", "ls | xargs rm", "xargs -I {} -n1 rm {}",
	}
	safe := []string{
		"ls", "chmod 755 x", "chown u x", "git reset --soft HEAD~1", "git reset", "git push",
		"git push origin main", "find . -name x", "echo rm", "git -c rm=1 status", "xargs echo",
	}
	for _, line := range dangerous {
		toks := lex(line)
		if !isDangerous(toks, analyze(toks)) {
			t.Errorf("isDangerous(%q) = false", line)
		}
	}
	for _, line := range safe {
		toks := lex(line)
		if isDangerous(toks, analyze(toks)) {
			t.Errorf("isDangerous(%q) = true", line)
		}
	}
}

func TestFirstWord(t *testing.T) {
	tests := map[string]string{
		"":                         "",
		"ls -la":                   "ls",
		"FOO=1 BAR=2 make":         "make",
		"sudo -u root rm -rf /":    "rm",
		"env -i PATH=/bin sh":      "sh",
		"noglob nocorrect git log": "git",
		"FOO=1":                    "",
		`"my cmd" arg`:             "my cmd",
		"(cd x && make)":           "",
		"  time make build":        "make",
		"arr=(a b c) cmd":          "cmd",
	}
	for line, want := range tests {
		if got := FirstWord(line); got != want {
			t.Errorf("FirstWord(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestLex(t *testing.T) {
	tests := []struct {
		line  string
		texts []string
	}{
		{"ls -la", []string{"ls", "-la"}},
		{"a|b&&c||d;e&", []string{"a", "|", "b", "&&", "c", "||", "d", ";", "e", "&"}},
		{"make 2>&1 >out", []string{"make", "2>&", "1", ">", "out"}},
		{`echo "a b" 'c d' $(x y) ${z} $'q r'`, []string{"echo", `"a b"`, "'c d'", "$(x y)", "${z}", "$'q r'"}},
		{"echo `a b` c", []string{"echo", "`a b`", "c"}},
		{"a\nb", []string{"a", ";", "b"}},
		{`echo a\ b`, []string{"echo", `a\ b`}},
		{"isn't it", []string{"isn't", "it"}},
		{`echo "unterminated`, []string{"echo", `"unterminated`}},
		{"arr=(a b) c", []string{"arr=(a b)", "c"}},
	}
	for _, tt := range tests {
		var got []string
		for _, tok := range lex(tt.line) {
			got = append(got, tok.text)
		}
		if !slices.Equal(got, tt.texts) {
			t.Errorf("lex(%q) = %q, want %q", tt.line, got, tt.texts)
		}
	}
}

func TestOSADistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"git", "git", 0},
		{"gti", "git", 1},
		{"got", "git", 1},
		{"gitt", "git", 1},
		{"gi", "git", 1},
		{"sl", "ls", 1},
		{"abc", "ca", 2},
		{"docker", "dcoker", 1},
		{"make", "mkae", 1}, //nolint:misspell // an intentional typo
		{"kubectl", "k", 2},
	}
	for _, tt := range tests {
		if got := min(osaDistance(tt.a, tt.b), 2); got != tt.want {
			t.Errorf("osaDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	for _, v := range []Verdict{Cmd, Prompt, Ask} {
		got, err := ParseVerdict(v.String())
		if err != nil || got != v {
			t.Errorf("ParseVerdict(%q) = %v, %v", v, got, err)
		}
	}
	for k := KindNone; k <= KindHashed; k++ {
		got, err := ParseKind(k.String())
		if err != nil || got != k {
			t.Errorf("ParseKind(%q) = %v, %v", k, got, err)
		}
	}
	if _, err := ParseKind("file"); err == nil {
		t.Error("ParseKind(file) succeeded")
	}
	if _, err := ParseVerdict("maybe"); err == nil {
		t.Error("ParseVerdict(maybe) succeeded")
	}
}
