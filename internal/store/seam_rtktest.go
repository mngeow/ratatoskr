//go:build rtktest

package store

import (
	"errors"
	"sync/atomic"
)

var failNextCommit atomic.Bool

// FailNextCommit makes the next write transaction fail just before commit, as a
// full disk would (acceptance check 12). Untested (GR-3).
func FailNextCommit() { failNextCommit.Store(true) }

func testCommitFault() error {
	if failNextCommit.CompareAndSwap(true, false) {
		return errors.New("rtktest: injected commit failure")
	}
	return nil
}
