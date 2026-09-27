package testutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Alive reports whether process pid exists and is not a zombie. A zombie
// counts as gone: a process that is not the caller's child is reaped by
// init, and one that is has nothing left running. Zombies are detected on
// Linux only (/proc).
func Alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	// pid (comm) state ...; comm may contain spaces.
	f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	return len(f) == 0 || f[0] != "Z"
}
