package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
)

// Paths are the files of one shell session.
type Paths struct {
	Session string
	Socket  string // the daemon's unix socket
	State   string // State as JSON
	Dir     string // the runtime directory holding both
}

var sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)

// maxSocketPath is the room for a unix socket path: sun_path is 108 bytes
// on Linux and 104 on macOS, including the terminating NUL.
const maxSocketPath = 103

// SessionPaths returns the paths of session in the runtime directory,
// creating the directory if needed.
func SessionPaths(session string) (Paths, error) {
	if !sessionRe.MatchString(session) {
		return Paths{}, fmt.Errorf("invalid ECDY_SESSION %q: use up to 64 letters, digits, '.', '_' and '-'", session)
	}
	dir, err := RuntimeDir()
	if err != nil {
		return Paths{}, err
	}
	p := Paths{
		Session: session,
		Dir:     dir,
		Socket:  filepath.Join(dir, session+".sock"),
		State:   filepath.Join(dir, session+".state"),
	}
	if len(p.Socket) > maxSocketPath {
		return Paths{}, fmt.Errorf("socket path %s is too long for a unix socket", p.Socket)
	}
	return p, nil
}

// RuntimeDir returns $XDG_RUNTIME_DIR/ecdy, or $TMPDIR/ecdy-<uid> when
// XDG_RUNTIME_DIR is not set (macOS), creating it with mode 0700. An existing
// directory must be a directory owned by the user with no group or other
// permissions (AGENTS.md, invariant 5).
func RuntimeDir() (string, error) {
	var dir string
	if base := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(base) {
		dir = filepath.Join(base, "ecdy")
	} else {
		dir = filepath.Join(os.TempDir(), "ecdy-"+strconv.Itoa(os.Getuid()))
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("runtime directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("runtime directory: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.IsDir():
		return "", fmt.Errorf("runtime directory %s is not a directory", dir)
	case !ok || int(st.Uid) != os.Getuid():
		return "", fmt.Errorf("runtime directory %s is not owned by you", dir)
	case info.Mode().Perm()&0o077 != 0:
		return "", fmt.Errorf("runtime directory %s is accessible by others (mode %v); it must be 0700", dir, info.Mode().Perm())
	}
	return dir, nil
}

// State is what `ecdy use` and `ecdy new` change in a shell session.
type State struct {
	// Agent is the session's agent; empty means the config's default_agent.
	Agent string `json:"agent,omitempty"`
	// Generation is increased by `ecdy new`: an agent whose session was
	// started for another generation starts a new one.
	Generation int `json:"generation,omitempty"`
}

// LoadState reads the state at path; a missing file is the zero State.
func LoadState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read session state: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("session state %s: %w", path, err)
	}
	return s, nil
}

// UpdateState applies change to the state at path and writes it atomically.
func UpdateState(path string, change func(*State)) (State, error) {
	s, err := LoadState(path)
	if err != nil {
		return s, err
	}
	change(&s)
	data, err := json.Marshal(s)
	if err != nil {
		return s, fmt.Errorf("encode session state: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return s, fmt.Errorf("write session state: %w", err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return s, fmt.Errorf("write session state: %w", err)
	}
	return s, nil
}
