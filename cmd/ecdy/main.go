// Command ecdy is a smart layer on top of zsh: it decides whether the typed
// line is a shell command or a prompt, and sends prompts to an ACP agent.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "ecdy:", err)
		os.Exit(1)
	}
}
