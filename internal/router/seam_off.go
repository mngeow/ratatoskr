//go:build !rtktest

package router

// killPoint is the rtktest seam for acceptance check 10. Outside the rtktest
// build it does nothing, so the release binary has no way to kill itself.
func killPoint(string) {}
