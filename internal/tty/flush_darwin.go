package tty

import (
	"syscall"
	"unsafe"
)

// flushInput discards input typed but not yet read: ioctl(fd, TIOCFLUSH,
// &FREAD), which is what tcflush(fd, TCIFLUSH) does on macOS.
func flushInput(fd int) {
	const fread = 1 // FREAD from <sys/fcntl.h>
	which := int32(fread)
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCFLUSH, uintptr(unsafe.Pointer(&which)))
}
