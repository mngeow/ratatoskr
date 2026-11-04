//go:build !rtktest

package store

// testCommitFault is the rtktest seam for acceptance check 12. Outside the
// rtktest build it does nothing.
func testCommitFault() error { return nil }
