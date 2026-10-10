//go:build !(linux && (386 || amd64 || arm || arm64 || loong64 || riscv64 || s390x))

package shell_test

import "os"

const zombieSupported = false

func zombieClassifier([]string) { os.Exit(1) }
