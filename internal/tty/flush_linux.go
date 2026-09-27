//go:build linux && (386 || amd64 || arm || arm64 || loong64 || riscv64 || s390x)

package tty

import "syscall"

// tcflsh is TCFLSH from <asm-generic/ioctls.h>; package syscall does not
// define it on every architecture listed in the build constraint.
const tcflsh = 0x540B

// flushInput discards input typed but not yet read: tcflush(fd, TCIFLUSH).
func flushInput(fd int) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflsh, syscall.TCIFLUSH)
}
