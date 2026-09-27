package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// RunOptions configure the daemon process of one shell session.
type RunOptions struct {
	Paths Paths
	// ShellPid is the shell the session belongs to: the daemon stops once it
	// is gone. 0 disables the check.
	ShellPid int
	// ShellPoll is how often the shell is checked.
	ShellPoll time.Duration
	// IdleTimeout stops the daemon when no client has been connected for
	// that long; 0 stops it as soon as the last client disconnects.
	IdleTimeout time.Duration
	// Ready, if set, receives one line once the daemon listens ("ok"),
	// finds another daemon listening ("running") or fails ("error: ...");
	// then it is closed.
	Ready io.WriteCloser
	// Server configures the server; OnConn is set by Run.
	Server Options
}

// Ready lines.
const (
	readyOK      = "ok"
	readyRunning = "running"
)

// Run listens on the session's socket and serves clients until ctx is done,
// a client sends stop, the shell is gone or the idle timeout passes. Then it
// stops the agents and removes the socket. If another daemon of the session
// is listening, Run returns nil at once.
func Run(ctx context.Context, o RunOptions) (err error) {
	log := o.Server.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ready := func(line string) {
		if o.Ready != nil {
			_, _ = io.WriteString(o.Ready, line+"\n")
			_ = o.Ready.Close()
			o.Ready = nil
		}
	}
	defer func() {
		if err != nil {
			ready("error: " + err.Error())
		}
	}()

	l, err := listen(o.Paths)
	if errors.Is(err, errRunning) {
		ready(readyRunning)
		return nil
	}
	if err != nil {
		return err
	}
	socketID, err := fileID(o.Paths.Socket)
	if err != nil {
		_ = l.Close()
		return err
	}

	idle := newIdleTimer(o.IdleTimeout)
	o.Server.OnConn = idle.add
	s := NewServer(o.Server)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.ServeConn(c)
		}
	}()
	log.Info("daemon started", "session", o.Paths.Session, "shell_pid", o.ShellPid, "idle_timeout", o.IdleTimeout)
	ready(readyOK)

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	var reason string
	select {
	case <-ctx.Done():
		reason = "signal"
	case <-s.StopRequested():
		reason = "stop requested"
	case <-idle.fired:
		reason = "idle"
	case <-shellGone(watchCtx, o.ShellPid, o.ShellPoll):
		reason = "shell gone"
	}
	log.Info("daemon stopping", "reason", reason)
	// Unlink the socket only while it is still ours: a daemon started after
	// ours was declared dead may have bound the same path.
	if id, err := fileID(o.Paths.Socket); err == nil && id == socketID {
		_ = os.Remove(o.Paths.Socket)
	}
	_ = l.Close()
	s.Close()
	s.Wait()
	if reason == "shell gone" {
		_ = os.Remove(o.Paths.State)
	}
	log.Info("daemon stopped")
	return nil
}

var errRunning = errors.New("a daemon is already running for this session")

// listen binds the session's socket. An exclusive lock on the runtime
// directory makes checking for a live daemon and binding one step, so two
// daemons starting at once cannot both listen.
func listen(p Paths) (*net.UnixListener, error) {
	dir, err := os.Open(p.Dir)
	if err != nil {
		return nil, fmt.Errorf("lock runtime directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock runtime directory: %w", err)
	}
	defer func() { _ = syscall.Flock(int(dir.Fd()), syscall.LOCK_UN) }()

	if c, err := net.Dial("unix", p.Socket); err == nil {
		_ = c.Close()
		return nil, errRunning
	}
	// A socket nobody listens on is left by a daemon that was killed.
	if err := os.Remove(p.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: p.Socket, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	// Run removes the socket itself, and only if it is still its own.
	l.SetUnlinkOnClose(false)
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		_ = l.Close()
		_ = os.Remove(p.Socket)
		return nil, fmt.Errorf("listen: %w", err)
	}
	return l, nil
}

type fileKey struct{ dev, ino uint64 }

func fileID(path string) (fileKey, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return fileKey{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return fileKey{uint64(st.Dev), uint64(st.Ino)}, nil //nolint:unconvert // Dev is int32 on macOS
}

// shellGone returns a channel closed once process pid no longer exists.
func shellGone(ctx context.Context, pid int, poll time.Duration) <-chan struct{} {
	ch := make(chan struct{})
	if pid <= 0 {
		return ch
	}
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	go func() {
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			// ESRCH: gone. EPERM would mean it exists under another user.
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				close(ch)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return ch
}

// idleTimer fires after timeout without connections.
type idleTimer struct {
	timeout time.Duration
	fired   chan struct{}

	mu     sync.Mutex
	active int
	timer  *time.Timer
	once   sync.Once
}

// firstClient is how long a new daemon waits for its first client even with
// a shorter idle timeout: the `ecdy ask` that started it connects only once
// it is ready.
const firstClient = 10 * time.Second

func newIdleTimer(timeout time.Duration) *idleTimer {
	t := &idleTimer{timeout: timeout, fired: make(chan struct{})}
	t.timer = time.AfterFunc(max(timeout, firstClient), t.fire)
	return t
}

func (t *idleTimer) fire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == 0 {
		t.once.Do(func() { close(t.fired) })
	}
}

func (t *idleTimer) add(delta int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active += delta
	t.timer.Stop()
	if t.active == 0 {
		t.timer.Reset(t.timeout)
	}
}
