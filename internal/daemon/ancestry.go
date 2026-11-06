package daemon

// The parent-chain check: is the peer a descendant of a harness this daemon started?
//
// THIS IS NOT A SECURITY BOUNDARY. A process that double-forks is reparented and gets
// past it, and credentials reflect connect time, so PID reuse, reparenting and passing a
// descriptor defeat it. It stops the accidental case - a harness or a tool it launched
// reaching for the daemon - and it stops an agent running `rtk attach` from inside a pane.
// Ancestry results are NEVER used to signal or kill anything.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No process has been walked.

import "fmt"

const (
	// maxAncestryDepth bounds the walk, so a pathological chain cannot spin.
	maxAncestryDepth = 64
)

// harnessPIDs is the set of harness processes this daemon started.
//
// The daemon owns this interface, so the session manager (slice 2) can supply a live set
// without either side importing the other. It must be read fresh for every connection:
// sessions start and end while the daemon runs.
type harnessPIDs interface {
	HarnessPIDs() map[int]struct{}
}

// descendsFrom walks from pid up through its parents, looking for a member of set.
//
// found is true only when an ancestor really is in the set. complete is false when a step
// could not be read (the process vanished, hidepid, a parse failure): an incomplete walk
// proves nothing, and the caller then allows the connection and logs that it could not
// check. Every discovery is logged by the caller, never here.
//
// parentOf is injected so the walk can be tested without processes.
//
// UNTESTED.
func descendsFrom(pid int, set map[int]struct{}, parentOf func(int) (int, error)) (found bool, complete bool) {
	if len(set) == 0 || pid <= 1 {
		return false, true
	}

	visited := make(map[int]struct{}, 8)
	current := pid
	for depth := 0; depth < maxAncestryDepth; depth++ {
		if _, seen := visited[current]; seen {
			// A cycle in the chain cannot be walked any further.
			return false, false
		}
		visited[current] = struct{}{}

		parent, err := parentOf(current)
		if err != nil {
			return false, false
		}
		if parent <= 1 {
			return false, true
		}
		if _, isHarness := set[parent]; isHarness {
			return true, true
		}
		current = parent
	}
	// The bound was reached without deciding: the chain is longer than any real one.
	return false, false
}

// ancestryCheck is the policy: what a found ancestor and an incomplete walk each mean.
//
// found: refuse, with a code and message a user can act on.
// not found, complete: allow.
// not found, incomplete: allow, and say so in the log, because a check that could not run
// must not silently look like a check that passed.
//
// UNTESTED.
func ancestryCheck(peer peerInfo, harnesses harnessPIDs) (refuse bool, note string) {
	if !peer.pidKnown {
		return false, "the peer PID is unknown, so the ancestry check was skipped"
	}
	var set map[int]struct{}
	if harnesses != nil {
		set = harnesses.HarnessPIDs()
	}
	if len(set) == 0 {
		// No harness has been started, so nothing can be a descendant of one.
		return false, ""
	}

	found, complete := descendsFrom(peer.PID, set, parentOf)
	switch {
	case found:
		return true, fmt.Sprintf("the peer (pid %d) is a descendant of a harness this daemon started", peer.PID)
	case !complete:
		return false, fmt.Sprintf("the ancestry of the peer (pid %d) could not be checked, so it was allowed", peer.PID)
	}
	return false, ""
}
