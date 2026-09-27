// Command ecdy is a smart layer on top of zsh: it decides whether the typed
// line is a shell command or a prompt, and sends prompts to an ACP agent.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func main() {
	err := newRootCmd().ExecuteContext(context.Background())
	var ec *exitCodeError
	switch {
	case err == nil:
	case errors.As(err, &ec):
		if ec.err != nil {
			fmt.Fprintln(os.Stderr, "ecdy:", ec.err)
		}
		os.Exit(ec.code)
	default:
		fmt.Fprintln(os.Stderr, "ecdy:", err)
		os.Exit(1)
	}
}

// exitCodeError makes ecdy exit with code, printing err if it is not nil.
type exitCodeError struct {
	code int
	err  error
}

func exitCode(code int, err error) error { return &exitCodeError{code, err} }

func (e *exitCodeError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func (e *exitCodeError) Unwrap() error { return e.err }
