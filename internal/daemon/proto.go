// Package daemon keeps a shell session's ACP agents alive between prompts
// (docs/adr/0003-session-daemon.md).
//
// A Server owns the agents and their ACP sessions. `ecdy ask` talks to it
// over a connection: a unix socket to the session's daemon, or an in-memory
// pipe to a Server of its own for one prompt. The server sends what the agent
// says; the client renders it, asks the user for permissions on its terminal
// and sends back the answers.
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// ProtocolVersion changes whenever Msg changes incompatibly. A daemon of
// another version is restarted by the client.
const ProtocolVersion = 1

// Message types. One connection carries one request: prompt, status or stop.
const (
	// Client to server.
	tPrompt = "prompt" // Prompt, Agent, Cwd, Verbose, Version
	tCancel = "cancel" // cancel the turn: session/cancel
	tKill   = "kill"   // kill the turn's agent now
	tAnswer = "answer" // ID, Outcome or Interrupted: a permission answer
	tStatus = "status"
	tStop   = "stop"

	// Server to client.
	tUpdate     = "update"     // Update
	tPermission = "permission" // ID, Permission: ask the user
	tWithdraw   = "withdraw"   // ID: the permission request is no longer needed
	tStderr     = "stderr"     // Text: the agent's stderr (verbose turns)
	tNotice     = "notice"     // Text: a line of ecdy's own
	tDone       = "done"       // StopReason: the turn ended
	tError      = "error"      // Code, Text: the request failed
	tOK         = "ok"         // Status for status; nothing for stop
)

// Error codes.
const (
	CodeVersion = "version" // the daemon speaks another protocol version
	CodeBusy    = "busy"    // the agent is in another turn
)

// Msg is one message in either direction, one JSON object per line. Only
// the fields of its type are set.
type Msg struct {
	T string `json:"t"`

	Version int    `json:"v,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Verbose bool   `json:"verbose,omitempty"`

	ID          int                           `json:"id,omitempty"`
	Permission  *acp.RequestPermissionRequest `json:"permission,omitempty"`
	Outcome     *acp.RequestPermissionOutcome `json:"outcome,omitempty"`
	Interrupted bool                          `json:"interrupted,omitempty"`

	Update     *acp.SessionUpdate `json:"update,omitempty"`
	Text       string             `json:"text,omitempty"`
	Code       string             `json:"code,omitempty"`
	StopReason acp.StopReason     `json:"stop_reason,omitempty"`
	Status     *Status            `json:"status,omitempty"`
}

// Status describes a running daemon.
type Status struct {
	Pid    int           `json:"pid"`
	Agent  string        `json:"agent"` // the session's agent
	Agents []AgentStatus `json:"agents"`
}

// AgentStatus describes one running agent.
type AgentStatus struct {
	Name    string `json:"name"`
	Pid     int    `json:"pid"`
	Session string `json:"session"`
	Cwd     string `json:"cwd"`
	Busy    bool   `json:"busy"`
}

// Error is an error reported by the server.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// maxMsg bounds one message. The agent's updates are bounded by the SDK the
// same way (10 MiB per line).
const maxMsg = 16 << 20

// wire sends and receives Msgs on a connection. send is safe for concurrent
// use; recv is not.
type wire struct {
	c  net.Conn
	r  *bufio.Reader
	mu sync.Mutex
}

func newWire(c net.Conn) *wire { return &wire{c: c, r: bufio.NewReaderSize(c, 64<<10)} }

func (w *wire) send(m Msg) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode %s: %w", m.T, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.c.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("send %s: %w", m.T, err)
	}
	return nil
}

var errTooLong = errors.New("message too long")

func (w *wire) recv() (Msg, error) {
	var line []byte
	for {
		chunk, err := w.r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxMsg {
			return Msg{}, errTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				err = io.ErrUnexpectedEOF
			}
			return Msg{}, err
		}
		break
	}
	var m Msg
	if err := json.Unmarshal(line, &m); err != nil {
		return Msg{}, fmt.Errorf("decode message: %w", err)
	}
	return m, nil
}
