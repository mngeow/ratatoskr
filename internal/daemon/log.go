package daemon

// The daemon's log: one helper, so the rule that nothing secret is ever logged has a
// single place to hold.
//
// What may be logged: start and stop, the end reason, the socket path, link state
// changes, session start and end (names and exit codes), refused connections (reason
// and peer UID/PID), and errors. What may never be logged: a message body, a
// credential, a token, or terminal contents.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No log line has been written.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	// logName is the daemon's log inside daemon/.
	logName = "daemon.log"

	// logFileMode is the log's mode. The umask is repaired with Fchmod, because
	// O_CREATE's mode applies only to a file that did not exist.
	logFileMode = 0o600
)

// logger writes the daemon's log.
//
// In --foreground mode it writes to stderr; in detached mode the child's stderr IS the
// log file, so both modes write to the same place and the file is never opened twice.
// That keeps logf the only writer.
type logger struct {
	mu         sync.Mutex
	out        io.Writer
	complained bool
}

// logf writes one timestamped line.
//
// A write failure is swallowed after a single note to stderr: a full disk must not take
// the daemon down, and the daemon's own log is not a place it can complain about its
// own log.
func (l *logger) logf(format string, args ...any) {
	if l == nil || l.out == nil {
		return
	}
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := io.WriteString(l.out, line); err != nil && !l.complained {
		l.complained = true
		fmt.Fprintf(os.Stderr, "rtk: cannot write the daemon log: %v\n", err)
	}
}

// openLogFile opens daemon/daemon.log for appending.
//
// The file is unbounded: there is no rotation in this work. Renaming or unlinking it does
// not move the daemon's writes, because the descriptor was opened first. A symlink, or
// anything that is not a regular file, is refused rather than followed.
//
// UNTESTED.
func openLogFile(dir string) (*os.File, error) {
	path := logPath(dir)

	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("daemon: %s must be a regular file, not a symlink", path)
	case err == nil && !info.Mode().IsRegular():
		return nil, fmt.Errorf("daemon: %s must be a regular file", path)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("daemon: stat %s: %w", path, err)
	}

	file, err := os.OpenFile(path, logFlags, logFileMode)
	if err != nil {
		return nil, fmt.Errorf("daemon: open %s: %w", path, err)
	}
	// O_CREATE's mode is subject to the umask and only applies to a new file, so the
	// mode is set explicitly.
	if err := file.Chmod(logFileMode); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("daemon: chmod %s: %w", path, err)
	}
	return file, nil
}

// stderrIsFile reports whether this process's stderr is the file at path.
//
// It is how the daemon knows its log path is real: `rtk daemon` starts the child with stderr
// redirected to daemon.log, and a foreground run has a terminal there. os.SameFile compares the
// device and inode, so an old log file left from an earlier run does not count.
func stderrIsFile(path string) bool {
	return sameFile(os.Stderr, path)
}

// sameFile reports whether an open file is the file at path.
func sameFile(open *os.File, path string) bool {
	if open == nil {
		return false
	}
	openInfo, err := open.Stat()
	if err != nil {
		return false
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(openInfo, pathInfo)
}
