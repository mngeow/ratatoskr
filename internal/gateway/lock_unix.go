//go:build darwin || linux

package gateway

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// acquireLock takes an exclusive, non-blocking flock on the gateway lock file.
//
// The kernel releases it when the process exits, however it exits, which is why
// the gateway process does not need a shutdown hook to be safe. Only the process
// that runs `rtk gateway --listen` takes this lock; the admin commands open the
// database without it.
//
// UNTESTED.
func acquireLock(path string) (*os.File, error) {
	// Refuse a symlink or a non-regular file before opening: the lock protects
	// one database, and a link could aim it elsewhere.
	if err := prepareOwnedFile(path); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return nil, fmt.Errorf("gateway: open lock file: %w", err)
	}
	if err := file.Chmod(fileMode); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("gateway: chmod lock file: %w", err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrGatewayRunning
		}
		return nil, fmt.Errorf("gateway: lock: %w", err)
	}
	return file, nil
}
