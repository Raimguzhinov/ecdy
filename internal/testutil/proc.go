package testutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

// killSession kills every process left in session sid (a PTY's: the
// process started under it leads it) and waits until they are gone. The
// kernel's SIGHUP on the leader's death reaches the terminal's foreground
// process group only; what an interactive shell started in the background
// (atuin's precmd runs `(atuin history end ... &)`) lives on, keeps the
// PTY open and may write into the test's directories while they are
// removed. Linux only (/proc); elsewhere it does nothing.
func killSession(tb testing.TB, sid int) {
	deadline := time.Now().Add(DefaultTimeout)
	for {
		left := sessionProcs(sid)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			tb.Errorf("processes of session %d still running: %v", sid, left)
			return
		}
		for _, pid := range left {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sessionProcs returns the processes of session sid that are not zombies.
func sessionProcs(sid int) []int {
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	var pids []int
	for _, p := range stats {
		stat, err := os.ReadFile(p)
		if err != nil {
			continue // gone meanwhile
		}
		// pid (comm) state ppid pgrp session ...; comm may contain spaces.
		f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(f) < 4 || f[0] == "Z" || f[3] != strconv.Itoa(sid) {
			continue
		}
		if pid, err := strconv.Atoi(filepath.Base(filepath.Dir(p))); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}
