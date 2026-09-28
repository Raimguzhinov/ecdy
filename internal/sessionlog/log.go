package sessionlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Record is one command of a shell session (docs/adr/0004-session-context.md).
type Record struct {
	Line  string        `json:"line"`  // as typed, redacted
	Cwd   string        `json:"cwd"`   // where it started
	Exit  int           `json:"exit"`  // exit status
	Start time.Time     `json:"start"` // when it started
	Dur   time.Duration `json:"dur"`   // how long it ran, in nanoseconds
}

// End is when the command finished.
func (r Record) End() time.Time { return r.Start.Add(r.Dur) }

const (
	defaultMaxSize = 256 << 10 // trim the log when it grows past this
	defaultKeep    = 200       // records kept by trimming
	// defaultLockTimeout bounds the wait for another writer: a recorder
	// stuck behind a hung one gives up instead of piling up.
	defaultLockTimeout = 2 * time.Second
	pruneAge           = 7 * 24 * time.Hour
)

// Log is the command log of one shell session.
type Log struct {
	path, lockPath string
	maxSize        int64
	keep           int
	lockTimeout    time.Duration
}

// Dir returns $XDG_STATE_HOME/ecdy/sessions, or
// ~/.local/state/ecdy/sessions when XDG_STATE_HOME is unset or relative.
func Dir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ecdy", "sessions"), nil
}

// Open returns the log of session in dir. Nothing is created until Append.
func Open(dir, session string) (*Log, error) {
	if session == "" || session == "." || session == ".." || strings.HasPrefix(session, ".") ||
		strings.ContainsAny(session, "/\x00") {
		return nil, fmt.Errorf("invalid session id %q", session)
	}
	return &Log{
		path:        filepath.Join(dir, session+".jsonl"),
		lockPath:    filepath.Join(dir, session+".lock"),
		maxSize:     defaultMaxSize,
		keep:        defaultKeep,
		lockTimeout: defaultLockTimeout,
	}, nil
}

// Path is the log file.
func (l *Log) Path() string { return l.path }

// Append redacts r and adds it to the log, creating the directory (0700)
// and the file (0600) if needed. A new log file prunes the logs of other
// sessions not written for a week. Concurrent Appends, from any process,
// are serialised by a lock file.
func (l *Log) Append(r Record) error {
	r.Line, r.Cwd = Redact(r.Line), Redact(r.Cwd)
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("session log: %w", err)
	}
	unlock, err := l.lock()
	if err != nil {
		return err
	}
	defer unlock()

	_, err = os.Lstat(l.path)
	created := errors.Is(err, fs.ErrNotExist)
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("session log: %w", err)
	}
	_, err = f.Write(data)
	var size int64
	if info, serr := f.Stat(); serr == nil {
		size = info.Size()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("session log: %w", err)
	}
	if size > l.maxSize {
		if err := l.trim(); err != nil {
			return err
		}
	}
	if created {
		prune(dir, l.path, time.Now().Add(-pruneAge))
	}
	return nil
}

// lock takes an exclusive flock on the log's lock file, waiting at most
// l.lockTimeout for another writer.
func (l *Log) lock() (func(), error) {
	f, err := os.OpenFile(l.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session log lock: %w", err)
	}
	deadline := time.Now().Add(l.lockTimeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) || time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("session log lock: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// trim rewrites the log with its last l.keep records, fewer if they do
// not fit in half of l.maxSize. Called with the lock held.
func (l *Log) trim() error {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return fmt.Errorf("session log: %w", err)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}
	lines = lines[max(0, len(lines)-l.keep):]
	for size := len(bytes.Join(lines, nil)); len(lines) > 1 && int64(size) > l.maxSize/2; {
		size -= len(lines[0])
		lines = lines[1:]
	}
	f, err := os.CreateTemp(filepath.Dir(l.path), filepath.Base(l.path)+".*")
	if err != nil {
		return fmt.Errorf("session log: %w", err)
	}
	_, err = f.Write(bytes.Join(lines, nil))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), l.path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return fmt.Errorf("session log: %w", err)
	}
	return nil
}

// Read returns the log's records in the order the commands ended. A missing
// log has no records; lines that do not decode (a torn or foreign line) are
// skipped.
func (l *Log) Read() ([]Record, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session log: %w", err)
	}
	defer func() { _ = f.Close() }()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			recs = append(recs, r)
		}
	}
	if err := sc.Err(); err != nil {
		return recs, fmt.Errorf("session log: %w", err)
	}
	slices.SortStableFunc(recs, func(a, b Record) int { return a.End().Compare(b.End()) })
	return recs, nil
}

// Remove deletes the log and its lock file; a missing log is not an error.
func (l *Log) Remove() error {
	var errs []error
	for _, p := range []string{l.path, l.lockPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// prune removes the logs in dir, other than keep, last written before
// cutoff, with their lock files. Errors are ignored: pruning is best effort.
func prune(dir, keep string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		if !strings.HasSuffix(name, ".jsonl") || path == keep || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(path)
		_ = os.Remove(strings.TrimSuffix(path, ".jsonl") + ".lock")
	}
}
