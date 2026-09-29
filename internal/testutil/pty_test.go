package testutil

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

// A process the shell left running in the background (atuin's precmd
// does: `(atuin history end ... &)`) is killed with the shell: otherwise it
// may still write into the test's $HOME while TempDir's cleanup removes
// it ("directory not empty").
func TestCloseKillsSession(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	var pid int
	t.Run("term", func(t *testing.T) {
		// Interactive: with job control the subshell gets its own process
		// group, which is not the terminal's foreground group once the
		// subshell exits, so the kernel's SIGHUP on the shell's death
		// misses it (a non-interactive sh would not show the problem).
		term := StartTerm(t, exec.Command(zsh, "-f", "-i"))
		term.Send("setopt no_bang_hist\r(sleep 60 & print -r -- bg $!)\r")
		m := regexp.MustCompile(`bg (\d+)`).FindStringSubmatch(term.ExpectRe(regexp.MustCompile(`bg \d+`)))
		pid, _ = strconv.Atoi(m[1])
	})
	if pid == 0 {
		t.Fatal("no pid")
	}
	if Alive(pid) {
		_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
		t.Errorf("background process %d outlived the terminal", pid)
	}
}
