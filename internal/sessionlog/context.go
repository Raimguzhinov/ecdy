package sessionlog

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxContextBytes bounds the context block (AGENTS.md, section 5).
	MaxContextBytes = 4 << 10
	// DefaultCommands is how many commands the block lists by default.
	DefaultCommands = 20
	// maxCommandBytes bounds one command in the block.
	maxCommandBytes = 512
)

// ContextOptions is what BuildContext needs.
type ContextOptions struct {
	Cwd    string // the prompt's directory
	Branch string // its git branch, if any (GitBranch)
	// Records is the session's log in end order (Log.Read). Only the ones
	// that ended after Since are listed, at most Commands of them.
	Records  []Record
	Since    time.Time
	Commands int
}

// BuildContext returns the block sent to the agent before a prompt and the
// end time of the newest record after Since (Since if there is none): the
// next turn of the same conversation passes it as Since. Everything in the
// block is redacted before it is cut, and the block is at most
// MaxContextBytes; the oldest commands are dropped to fit.
func BuildContext(o ContextOptions) (text string, last time.Time) {
	last = o.Since
	var recent []Record
	for _, r := range o.Records {
		if r.End().After(o.Since) {
			recent = append(recent, r)
			if r.End().After(last) {
				last = r.End()
			}
		}
	}
	recent = recent[max(0, len(recent)-max(o.Commands, 0)):]

	var head strings.Builder
	head.WriteString("Context from the user's shell, added by ecdy (secrets redacted, command output not captured):\n")
	fmt.Fprintf(&head, "cwd: %s\n", cut(Redact(o.Cwd), maxCommandBytes))
	if o.Branch != "" {
		fmt.Fprintf(&head, "git branch: %s\n", cut(Redact(o.Branch), maxCommandBytes))
	}
	if len(recent) > 0 {
		if o.Since.IsZero() {
			head.WriteString("Recent commands, oldest first:\n")
		} else {
			head.WriteString("Commands since the previous prompt, oldest first:\n")
		}
	}

	lines := make([]string, len(recent))
	size := head.Len()
	for i, r := range recent {
		lines[i] = commandLine(r, o.Cwd)
		size += len(lines[i])
	}
	for len(lines) > 0 && size > MaxContextBytes {
		size -= len(lines[0])
		lines = lines[1:]
	}
	// The head is bounded too: its cwd and branch are cut to maxCommandBytes.
	return head.String() + strings.Join(lines, ""), last
}

// commandLine formats one record: "[exit 2, 1.3s, in /dir] make test\n",
// the directory only when it is not cwd, continuation lines indented.
func commandLine(r Record, cwd string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[exit %d, %s", r.Exit, duration(r.Dur))
	if r.Cwd != "" && r.Cwd != cwd {
		fmt.Fprintf(&b, ", in %s", cut(Redact(r.Cwd), maxCommandBytes/2))
	}
	b.WriteString("] ")
	line := cut(Redact(strings.TrimRight(r.Line, "\n")), maxCommandBytes)
	b.WriteString(strings.ReplaceAll(line, "\n", "\n    "))
	b.WriteByte('\n')
	return b.String()
}

// duration formats d as "0.1s" up to a minute, as "2m3s" above.
func duration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// cut shortens s to at most n bytes at a rune boundary, marking the cut
// with "…" (which fits in n).
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "…"
	s = s[:n-len(mark)]
	for len(s) > 0 && !utf8.ValidString(s[len(s)-min(len(s), utf8.UTFMax):]) {
		s = s[:len(s)-1]
	}
	return s + mark
}

// GitBranch returns the git branch checked out in dir or the repository
// above it, "detached at <hash>" for a detached HEAD, or "" outside a
// repository. It reads .git/HEAD (following a worktree's "gitdir:" file)
// instead of running git.
func GitBranch(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		gitDir := filepath.Join(d, ".git")
		if info, err := os.Stat(gitDir); err == nil {
			if !info.IsDir() {
				data, err := readSmall(gitDir)
				rest, ok := strings.CutPrefix(strings.TrimSpace(data), "gitdir: ")
				if err != nil || !ok {
					return ""
				}
				if !filepath.IsAbs(rest) {
					rest = filepath.Join(d, rest)
				}
				gitDir = rest
			}
			head, err := readSmall(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return ""
			}
			head = strings.TrimSpace(head)
			if ref, ok := strings.CutPrefix(head, "ref: "); ok {
				return strings.TrimPrefix(ref, "refs/heads/")
			}
			if len(head) >= 7 {
				return "detached at " + head[:7]
			}
			return ""
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

// readSmall reads at most 4 KiB of path: HEAD and .git files are tiny, and
// a huge or special file there must not stall a prompt.
func readSmall(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	return string(bytes.ToValidUTF8(data, nil)), nil
}
