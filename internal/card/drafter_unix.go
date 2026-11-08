//go:build unix

package card

// The process-group side of a draft: the command gets its own group, so a kill reaches everything it
// started and nothing else.

import (
	"errors"
	"os/exec"
	"syscall"
)

// draftSupported reports whether Draft can run here.
func draftSupported() bool { return true }

// setProcessGroup puts the command in a new process group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the command's whole group.
//
// The negative pid is what makes it a group kill. A process that has already exited is not an error here:
// the kill is a "make sure it is gone" step at the end of a timeout, a cancellation or an over-long output.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("card: the draft command is not running")
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return err
	}
	return nil
}
