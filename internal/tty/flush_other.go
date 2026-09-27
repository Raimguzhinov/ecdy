//go:build !darwin && !(linux && (386 || amd64 || arm || arm64 || loong64 || riscv64 || s390x))

package tty

// flushInput is not implemented here: keys typed ahead of a dialog may
// answer it.
func flushInput(int) {}
