//go:build !unix

package daemon

// processExists on a platform this daemon does not support: it cannot tell, so it says the
// process is there, and a stop reports "still finishing" rather than a false "stopped".
func processExists(pid int) bool { return pid > 0 }
