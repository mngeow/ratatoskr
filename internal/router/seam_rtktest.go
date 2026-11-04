//go:build rtktest

package router

import (
	"os"
	"strings"
)

// Kill points for acceptance check 10 (design section 12): the process is killed
// with SIGKILL-equivalent force at exactly one named moment, chosen by the
// RTK_TEST_KILL_AT environment variable, so the next start can be checked for the
// matching interrupted reason with nothing resent. A message that is merely held
// needs no point: a test harness kills the process from outside.
//
// Untested: under GR-3 nothing here has been run.
const (
	// KillAfterDelivering: just after "delivering" committed, before any write.
	// Expect interrupted / write_unknown at the next start.
	KillAfterDelivering = "afterDelivering"
	// KillAfterPasteWrite: the PTY write returned, "pasted" not yet committed.
	// Expect interrupted / write_unknown.
	KillAfterPasteWrite = "afterPasteWrite"
	// KillAfterPasted: just after "pasted" committed. Expect interrupted /
	// pasted_unconfirmed.
	KillAfterPasted = "afterPasted"

	killEnv = "RTK_TEST_KILL_AT"
)

func killPoint(point string) {
	if strings.TrimSpace(os.Getenv(killEnv)) != point {
		return
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		os.Exit(137)
	}
	_ = process.Kill() // SIGKILL on Unix; no deferred function runs
	select {}          // do not continue while the kill is delivered
}
