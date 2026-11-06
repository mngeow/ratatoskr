// Package gateway is the cross-machine half of Ratatoskr: the store, the pairing
// rules, the listen-address check and the admin commands.
//
// It is a separate component with its own SQLite database. `rtk gateway
// --listen` is the only process that holds its lock; the admin commands run as
// separate processes beside it and open the database without the lock, which WAL
// mode and a busy timeout make safe.
//
// The package imports only the standard library and the SQLite driver. It must
// not import store, router, server, agent, pane, tui or app: those are one
// machine's runtime, and this is the network between machines.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied, including cross-compiles.
// No database has been opened and no listener has been bound.
package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// dbName is the gateway database inside its own directory.
	dbName = "gateway.db"
	// lockName is the gateway's process lock, distinct from the runtime's.
	lockName = "gateway.lock"

	dirMode  = 0o700
	fileMode = 0o600
)

// PrepareDir returns <stateDir>/gateway, creating both directories at 0700 and
// repairing the mode of either one if it already exists.
//
// The state directory itself may be a symlink to a directory - that is a
// legitimate user setup - so it is checked with os.Stat. Everything this package
// owns inside it is checked with os.Lstat and must be a real directory, not a
// link: a symlinked gateway directory could point the database somewhere else
// entirely.
//
// UNTESTED.
func PrepareDir(stateDir string) (string, error) {
	if stateDir == "" {
		return "", errors.New("gateway: empty state directory")
	}

	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("gateway: resolve state directory: %w", err)
	}

	// os.Stat follows a symlink here on purpose: the user may have pointed their
	// state directory at a real directory elsewhere.
	info, err := os.Stat(absolute)
	switch {
	case err == nil && !info.IsDir():
		return "", fmt.Errorf("gateway: %s must be a directory", absolute)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("gateway: stat %s: %w", absolute, err)
	}

	if err := os.MkdirAll(absolute, dirMode); err != nil {
		return "", fmt.Errorf("gateway: create %s: %w", absolute, err)
	}
	if err := os.Chmod(absolute, dirMode); err != nil {
		return "", fmt.Errorf("gateway: chmod %s: %w", absolute, err)
	}

	dir := filepath.Join(absolute, "gateway")
	if err := prepareOwnedDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// prepareOwnedDir creates the gateway's own directory, refusing a symlink or a
// non-directory, and repairs its mode.
func prepareOwnedDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("gateway: %s must be a real directory", dir)
	case err == nil && !info.IsDir():
		return fmt.Errorf("gateway: %s must be a real directory", dir)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("gateway: stat %s: %w", dir, err)
	}

	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("gateway: create %s: %w", dir, err)
	}
	// MkdirAll is subject to umask and skips an existing directory, so the mode
	// is set explicitly.
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("gateway: chmod %s: %w", dir, err)
	}
	return nil
}

// prepareOwnedFile creates a file this package owns at 0600, refusing a symlink
// or a non-regular file at its path. The file is created empty if it is missing;
// an existing file is never truncated.
func prepareOwnedFile(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("gateway: %s must be a real file", path)
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("gateway: %s must be a real file", path)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("gateway: stat %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return fmt.Errorf("gateway: create %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("gateway: close %s: %w", path, err)
	}
	// O_CREATE's mode applies only to a new file.
	if err := os.Chmod(path, fileMode); err != nil {
		return fmt.Errorf("gateway: chmod %s: %w", path, err)
	}
	return nil
}

// tightenFiles restricts the database and its existing sidecars to 0600.
//
// It does not prove the mode of a sidecar SQLite creates later, and it changes
// no process-wide umask on purpose: a umask would be inherited by the harness
// children this process starts, and that would change local behaviour. Nothing
// is deleted or truncated here - a live sidecar belongs to SQLite.
//
// UNTESTED.
func tightenFiles(dir string) error {
	for _, name := range []string{dbName, dbName + "-wal", dbName + "-shm"} {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("gateway: stat %s: %w", path, err)
		}
		if err := os.Chmod(path, fileMode); err != nil {
			return fmt.Errorf("gateway: chmod %s: %w", path, err)
		}
	}
	return nil
}
