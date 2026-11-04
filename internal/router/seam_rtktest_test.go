//go:build rtktest

package router

import "testing"

// Written under GR-3 and never run. The kill itself cannot be unit tested; this
// only pins that an unset or different point leaves the process alone.
func TestKillPointDoesNothingUnlessItsPointIsSelected(t *testing.T) {
	t.Setenv(killEnv, "")
	killPoint(KillAfterDelivering)

	t.Setenv(killEnv, KillAfterPasted)
	killPoint(KillAfterDelivering)
	killPoint(KillAfterPasteWrite)
}
