//go:build !linux && !darwin

package daemon

// Ancestry on a platform this build does not support: the walk cannot run, so the caller
// sees an incomplete check and allows the connection, logging that it could not look.

import "errors"

// parentOf always fails, so descendsFrom reports an incomplete walk.
func parentOf(pid int) (int, error) {
	return 0, errors.New("daemon: the process table is not readable on this platform")
}
