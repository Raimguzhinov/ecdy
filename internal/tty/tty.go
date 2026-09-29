// Package tty holds single-key dialogs on the controlling terminal: the
// permission dialog of `ecdy ask`.
package tty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"golang.org/x/term"
)

// ErrInterrupted is returned when the user presses Ctrl+C in a dialog (raw
// mode turns it into a key instead of SIGINT).
var ErrInterrupted = errors.New("interrupted")

// Keys reads key presses.
type Keys interface {
	// ReadKey returns the bytes of one key press; an escape sequence
	// (an arrow key) arrives as one read.
	ReadKey(ctx context.Context) ([]byte, error)
}

// Terminal is the controlling terminal in raw mode.
type Terminal struct {
	f     *os.File
	state *term.State
	keys  chan keyRead
	done  chan struct{} // closed by Close
}

type keyRead struct {
	b   []byte
	err error
}

// Open opens /dev/tty, discards pending input (keys typed before the dialog
// appeared must not answer it) and switches to raw mode.
func Open() (*Terminal, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open terminal: %w", err)
	}
	var state *term.State
	err = control(f, func(fd int) error {
		flushInput(fd)
		var err error
		state, err = term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("raw mode: %w", err)
		}
		return nil
	})
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	t := &Terminal{f: f, state: state, keys: make(chan keyRead), done: make(chan struct{})}
	go t.read()
	return t, nil
}

func (t *Terminal) read() {
	for {
		buf := make([]byte, 16)
		n, err := t.f.Read(buf)
		select {
		case t.keys <- keyRead{buf[:n], err}:
		case <-t.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// ReadKey implements Keys.
func (t *Terminal) ReadKey(ctx context.Context) ([]byte, error) {
	select {
	case k := <-t.keys:
		if k.err != nil {
			return nil, fmt.Errorf("read terminal: %w", k.err)
		}
		return k.b, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("read terminal: %w", ctx.Err())
	}
}

// Write writes to the terminal. In raw mode "\n" does not return the
// carriage: use "\r\n".
func (t *Terminal) Write(p []byte) (int, error) {
	n, err := t.f.Write(p)
	if err != nil {
		return n, fmt.Errorf("write terminal: %w", err)
	}
	return n, nil
}

// Close restores the terminal mode and closes it. Closing the file also ends
// a pending Read of the reader goroutine: Go polls terminals, as long as the
// file stays non-blocking (see control).
func (t *Terminal) Close() error {
	close(t.done)
	err := control(t.f, func(fd int) error { return term.Restore(fd, t.state) })
	_ = t.f.Close()
	if err != nil {
		return fmt.Errorf("restore terminal: %w", err)
	}
	return nil
}

// control runs fn with f's descriptor. f.Fd() would do, but it switches
// the descriptor to blocking mode (https://pkg.go.dev/os#File.Fd): Close
// then no longer ends a pending Read, and the reader goroutine of a closed
// dialog swallows the first key of the next one.
func control(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("terminal descriptor: %w", err)
	}
	var fnErr error
	if err := rc.Control(func(fd uintptr) { fnErr = fn(int(fd)) }); err != nil {
		return fmt.Errorf("terminal descriptor: %w", err)
	}
	return fnErr
}

// Style of the dialog.
type Style struct {
	Color bool
}

// Permission shows a permission request and waits for a key:
//
//	1–9  the option with that number
//	y    the first "allow once" option
//	n    the first "reject once" option
//	Esc  the first reject option
//	^C   ErrInterrupted: cancel the turn
//
// Enter and other keys are ignored: nothing is allowed by default. If the
// keys fail, the request is rejected.
func Permission(ctx context.Context, keys Keys, w io.Writer, req acp.RequestPermissionRequest, st Style) (acp.RequestPermissionOutcome, error) {
	title := "the agent wants to run a tool"
	if req.ToolCall.Title != nil && strings.TrimSpace(*req.ToolCall.Title) != "" {
		title = *req.ToolCall.Title
	}
	var b strings.Builder
	b.WriteString(paint(st, "\x1b[1;33m", "⚠ Allow?") + " " + crlf(title) + "\r\n")
	for _, l := range req.ToolCall.Locations {
		b.WriteString("  " + paint(st, "\x1b[2m", l.Path) + "\r\n")
	}
	b.WriteString(" ")
	for i, o := range req.Options {
		if i < 9 {
			fmt.Fprintf(&b, " %s %s", paint(st, "\x1b[1m", fmt.Sprint(i+1)), o.Name)
		}
	}
	b.WriteString(paint(st, "\x1b[2m", "  · Esc reject · ^C cancel") + "\r\n")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return reject(req, err)
	}

	for {
		key, err := keys.ReadKey(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return acp.RequestPermissionOutcome{}, fmt.Errorf("permission dialog: %w", ctx.Err())
			}
			return reject(req, err)
		}
		i := choose(req.Options, key)
		switch {
		case i == interrupt:
			_, _ = io.WriteString(w, paint(st, "\x1b[2m", "  → cancelled")+"\r\n")
			return acp.RequestPermissionOutcome{}, ErrInterrupted
		case i >= 0:
			o := req.Options[i]
			_, _ = io.WriteString(w, paint(st, "\x1b[2m", "  → "+o.Name)+"\r\n")
			return selected(o), nil
		}
	}
}

const (
	none      = -1
	interrupt = -2
)

// choose maps what one read returned to an option index, none or
// interrupt. Keys typed quickly arrive in one read: the first key that
// decides wins. An escape sequence (arrow keys, function keys: ESC followed
// by more bytes) is skipped.
func choose(opts []acp.PermissionOption, keys []byte) int {
	for i, k := range keys {
		if k == 0x1b && i+1 < len(keys) {
			return none
		}
		if c := chooseKey(opts, k); c != none {
			return c
		}
	}
	return none
}

func chooseKey(opts []acp.PermissionOption, k byte) int {
	switch {
	case k == 0x03: // Ctrl+C
		return interrupt
	case k >= '1' && k <= '9':
		if i := int(k - '1'); i < len(opts) {
			return i
		}
	case k == 'y' || k == 'Y':
		return find(opts, acp.PermissionOptionKindAllowOnce)
	case k == 'n' || k == 'N':
		return find(opts, acp.PermissionOptionKindRejectOnce)
	case k == 0x1b: // Esc
		return find(opts, acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways)
	}
	return none
}

func find(opts []acp.PermissionOption, kinds ...acp.PermissionOptionKind) int {
	for _, k := range kinds {
		for i, o := range opts {
			if o.Kind == k {
				return i
			}
		}
	}
	return none
}

// reject answers with the first reject option when the dialog cannot be
// shown or read; without one it fails.
func reject(req acp.RequestPermissionRequest, cause error) (acp.RequestPermissionOutcome, error) {
	if i := find(req.Options, acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways); i >= 0 {
		return selected(req.Options[i]), nil
	}
	return acp.RequestPermissionOutcome{}, fmt.Errorf("permission dialog: %w", cause)
}

// Reject is the answer when there is no terminal to ask on.
func Reject(req acp.RequestPermissionRequest) (acp.RequestPermissionOutcome, error) {
	return reject(req, errors.New("no terminal"))
}

func selected(o acp.PermissionOption) acp.RequestPermissionOutcome {
	return acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: o.OptionId}}
}

func paint(st Style, code, s string) string {
	if !st.Color {
		return s
	}
	return code + s + "\x1b[0m"
}

// crlf makes a multi-line title print correctly in raw mode.
func crlf(s string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\r\n  ")
}
