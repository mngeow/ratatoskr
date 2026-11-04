//go:build !darwin && !linux

package store

import (
	"errors"
	"os"
)

// Windows is deferred (design decision 4).
func acquireLock(string) (*os.File, error) {
	return nil, errors.New("store: this platform is not supported")
}
