package daemon

// The daemon's Unix socket: where it lives, what may happen to a path that is already
// there, and the one function that removes a socket.
//
// There is no TCP listener anywhere in this package.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No socket has been bound.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	// dirName is the daemon's own directory inside the state directory.
	dirName = "daemon"

	// socketName is the socket inside that directory.
	socketName = "daemon.sock"

	// socketMode is the socket's mode: only this user may connect. The directory's
	// 0700 already excludes everyone else, so the window between bind and chmod is not
	// reachable by another user.
	socketMode = 0o600

	// dirMode is the daemon directory's mode.
	dirMode = 0o700

	// maxSocketPath is the shortest Unix socket path limit of the platforms this
	// supports (104 bytes on macOS, 108 on Linux), applied on both so one path works
	// everywhere. Refusing rather than hashing is deliberate: a hashed path would be
	// impossible to find by hand.
	//
	// The default state directory adds roughly 40 bytes to the home path, so a long user
	// name or a custom XDG_STATE_HOME can reach this limit.
	maxSocketPath = 104
)

// daemonDir is <stateDir>/daemon.
func daemonDir(stateDir string) string { return filepath.Join(stateDir, dirName) }

// socketPath is <stateDir>/daemon/daemon.sock.
func socketPath(stateDir string) string { return filepath.Join(daemonDir(stateDir), socketName) }

// logPath is <stateDir>/daemon/daemon.log.
func logPath(dir string) string { return filepath.Join(dir, logName) }

// checkSocketPath refuses a path that cannot be a Unix socket.
//
// The three refusals are one check so a caller cannot bind a path the kernel would
// reject with an unexplained error: a path at or over the platform limit, a NUL byte, or
// (on Linux) a leading '@', which names an abstract socket rather than a file.
//
// UNTESTED.
func checkSocketPath(path string) error {
	if len(path) >= maxSocketPath {
		return fmt.Errorf(
			"daemon: socket path %s is %d bytes, and a Unix socket path must be under %d bytes: set the state directory to a shorter path",
			path, len(path), maxSocketPath,
		)
	}
	for i := 0; i < len(path); i++ {
		if path[i] == 0 {
			return fmt.Errorf("daemon: socket path %s contains a NUL byte", printable(path))
		}
	}
	if strings.HasPrefix(path, "@") {
		return fmt.Errorf("daemon: socket path %s would be an abstract socket, not a file", path)
	}
	return nil
}

// printable makes a path safe to put in an error: a NUL cannot be printed.
func printable(path string) string {
	out := make([]byte, 0, len(path))
	for i := 0; i < len(path); i++ {
		if path[i] == 0 {
			out = append(out, []byte("\\0")...)
			continue
		}
		out = append(out, path[i])
	}
	return string(out)
}

// prepareDir creates or repairs <stateDir>/daemon at 0700.
//
// It refuses a symlink or a non-directory, so the socket and the log always live in a
// directory this user owns and nobody else can enter.
//
// UNTESTED.
func prepareDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("daemon: %s must be a real directory", dir)
	case err == nil && !info.IsDir():
		return fmt.Errorf("daemon: %s must be a real directory", dir)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("daemon: stat %s: %w", dir, err)
	}

	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("daemon: create %s: %w", dir, err)
	}
	// MkdirAll is subject to umask and skips an existing directory, so the mode is set
	// explicitly.
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("daemon: chmod %s: %w", dir, err)
	}
	return nil
}

// removeStaleSocket is the ONLY place that removes a socket path.
//
// It must be called with the store lock held: a live daemon would hold that lock, so a
// socket found here is a leftover from a daemon that died. An absent path is nothing to
// do; a socket is removed; anything else - a symlink, a regular file, a directory - is
// refused with an error naming the path, and nothing is removed. That distinction is
// what keeps a user's file safe from a typo about where the daemon lives.
//
// UNTESTED.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("daemon: stat %s: %w", path, err)
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("daemon: %s is a symlink, not a socket: refusing to remove it", path)
	case info.Mode()&os.ModeSocket == 0:
		return fmt.Errorf("daemon: %s is not a socket: refusing to remove it", path)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("daemon: remove the stale socket %s: %w", path, err)
	}
	return nil
}

// listenSocket binds the daemon's socket and restricts it to this user.
//
// The path is checked first, so a path the kernel would refuse fails with an explanation
// rather than a bare syscall error. Go's *net.UnixListener unlinks its socket when it is
// closed, which is why shutdown closes the listener before the store lock is released.
//
// UNTESTED.
func listenSocket(stateDir string) (net.Listener, string, error) {
	dir := daemonDir(stateDir)
	if err := prepareDir(dir); err != nil {
		return nil, "", err
	}

	path := socketPath(stateDir)
	if err := checkSocketPath(path); err != nil {
		return nil, "", err
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, "", err
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", fmt.Errorf("daemon: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, socketMode); err != nil {
		_ = listener.Close()
		return nil, "", fmt.Errorf("daemon: chmod %s: %w", path, err)
	}
	return listener, path, nil
}
