//go:build darwin

package daemon

// macOS ancestry: kern.proc.pid.

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// parentOf returns the parent PID of pid.
func parentOf(pid int) (int, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, fmt.Errorf("daemon: read the process table: %w", err)
	}
	if info == nil {
		return 0, fmt.Errorf("daemon: the process table has no entry for pid %d", pid)
	}
	ppid := int(info.Eproc.Ppid)
	if ppid < 0 {
		return 0, fmt.Errorf("daemon: the process table has no usable parent for pid %d", pid)
	}
	return ppid, nil
}
