//go:build linux && (386 || amd64 || arm || arm64 || loong64 || riscv64 || s390x)

package shell_test

import (
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

const zombieSupported = true

// TCXONC, TCOOFF and TCOON from <asm-generic/ioctls.h> and
// <asm-generic/termbits.h>; package syscall does not define them.
const tcxonc, tcooff, tcoon = 0x540A, 0, 1

// The main goroutine must run on the main thread: its exit is what
// zombieClassifier is about.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "zombie-classifier" {
		runtime.LockOSThread()
	}
}

// zombieClassifier runs `ecdy classify` with args[1:] (args[0] is ecdy) and
// answers like it, then ends its main thread only: /proc shows the process
// as a zombie, but the kernel tells the parent (SIGCHLD) only when the last
// thread is gone. Until then, the remaining thread holds the terminal's
// output (so that a write of zsh's waits, and a signal makes it fail
// instead of landing between writes) and sends zsh SIGCHLD every 2 ms.
func zombieClassifier(args []string) {
	// The main thread exits holding a P: another one must be left (one CPU),
	// and a stop-the-world would wait for it.
	runtime.GOMAXPROCS(2)
	debug.SetGCPercent(-1)
	out, err := exec.Command(args[0], args[1:]...).Output()
	if err != nil {
		os.Exit(1)
	}
	// The classifier runs in the background: tcflow from there sends SIGTTOU.
	signal.Ignore(syscall.SIGTTOU)
	tty, err := syscall.Open("/dev/tty", syscall.O_WRONLY, 0)
	if err != nil {
		os.Exit(1)
	}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(tty), tcxonc, tcooff)
	_, _ = os.Stdout.Write(out)
	_ = syscall.Close(1)
	ppid := os.Getppid()
	started := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		close(started)
		for range 15 {
			time.Sleep(2 * time.Millisecond)
			_ = syscall.Kill(ppid, syscall.SIGCHLD)
		}
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(tty), tcxonc, tcoon)
		_, _, _ = syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 0, 0, 0)
	}()
	<-started
	_, _, _ = syscall.RawSyscall(syscall.SYS_EXIT, 0, 0, 0)
}
