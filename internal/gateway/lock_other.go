//go:build !darwin && !linux

package gateway

import (
	"errors"
	"os"
)

// acquireLock is unavailable here: the gateway's lock is a flock, and Windows is
// deferred for the runtime as well. The error is returned rather than ignored, so
// no platform can accidentally run without the lock.
//
// UNTESTED.
func acquireLock(string) (*os.File, error) {
	return nil, errors.New("gateway: this platform has no gateway lock implementation")
}
