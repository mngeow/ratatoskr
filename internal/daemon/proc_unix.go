//go:build unix

package daemon

import (
	"errors"
	"syscall"
)

// processExists reports whether a process with this pid is still there.
//
// Signal 0 does the existence and permission checks and delivers nothing: this never sends a
// real signal to anything. EPERM means the process exists (it is not ours to signal), so it
// counts as there. A pid that was reused by another process after the daemon exited reads as
// "still there", which can only make the stop report "still finishing" and never a false
// "stopped".
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
