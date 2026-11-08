//go:build !unix

package card

// A draft command is not supported on this platform: the process-group handling above is the only way
// this package guarantees that a killed drafter leaves nothing behind, and a platform without it is
// refused rather than run unsafely.

import (
	"errors"
	"os/exec"
)

// draftSupported reports whether Draft can run here.
func draftSupported() bool { return false }

// setProcessGroup does nothing here.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup cannot be done here.
func killProcessGroup(cmd *exec.Cmd) error {
	return errors.New("card: the draft command is not supported on this platform")
}
