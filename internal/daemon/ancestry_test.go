package daemon

// Untested: under GR-3 nothing here has been run. The walk is driven by injected
// functions, so no real process table is read.

import (
	"errors"
	"testing"
)

// TestDescendsFrom: found, not found, incomplete, the depth bound, and the pid stop.
func TestDescendsFrom(t *testing.T) {
	// chain builds a parentOf from a fixed pid -> parent map.
	chain := func(parents map[int]int, failing map[int]error) func(int) (int, error) {
		return func(pid int) (int, error) {
			if err, ok := failing[pid]; ok {
				return 0, err
			}
			parent, ok := parents[pid]
			if !ok {
				return 0, errors.New("no such process")
			}
			return parent, nil
		}
	}

	tests := []struct {
		name         string
		pid          int
		set          map[int]struct{}
		parentOf     func(int) (int, error)
		wantFound    bool
		wantComplete bool
	}{
		{
			name:         "an ancestor is a harness",
			pid:          30,
			set:          map[int]struct{}{10: {}},
			parentOf:     chain(map[int]int{30: 20, 20: 10, 10: 1}, nil),
			wantFound:    true,
			wantComplete: true,
		},
		{
			name:         "the pid itself is in the set is not checked",
			pid:          30,
			set:          map[int]struct{}{30: {}},
			parentOf:     chain(map[int]int{30: 20, 20: 1}, nil),
			wantFound:    false,
			wantComplete: true,
		},
		{
			name:         "nothing in the chain",
			pid:          30,
			set:          map[int]struct{}{99: {}},
			parentOf:     chain(map[int]int{30: 20, 20: 1}, nil),
			wantFound:    false,
			wantComplete: true,
		},
		{
			name:         "a failed step is incomplete",
			pid:          30,
			set:          map[int]struct{}{10: {}},
			parentOf:     chain(map[int]int{30: 20}, map[int]error{20: errors.New("gone")}),
			wantFound:    false,
			wantComplete: false,
		},
		{
			name:         "a cycle is incomplete",
			pid:          30,
			set:          map[int]struct{}{99: {}},
			parentOf:     chain(map[int]int{30: 20, 20: 30}, nil),
			wantFound:    false,
			wantComplete: false,
		},
		{
			name:         "pid 1 is the stop",
			pid:          1,
			set:          map[int]struct{}{1: {}},
			parentOf:     chain(nil, nil),
			wantFound:    false,
			wantComplete: true,
		},
		{
			name:         "an empty set proves nothing and is complete",
			pid:          30,
			set:          nil,
			parentOf:     chain(nil, nil),
			wantFound:    false,
			wantComplete: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			found, complete := descendsFrom(tc.pid, tc.set, tc.parentOf)
			if found != tc.wantFound || complete != tc.wantComplete {
				t.Fatalf("descendsFrom = (%t, %t), want (%t, %t)", found, complete, tc.wantFound, tc.wantComplete)
			}
		})
	}
}

// TestDescendsFromDepthBound: a chain longer than the bound is incomplete, not found.
func TestDescendsFromDepthBound(t *testing.T) {
	// Every pid's parent is the next one up, so the walk never reaches 1.
	parentOf := func(pid int) (int, error) { return pid + 1, nil }
	set := map[int]struct{}{100000: {}}

	found, complete := descendsFrom(2, set, parentOf)
	if found || complete {
		t.Fatalf("descendsFrom = (%t, %t), want (false, false)", found, complete)
	}
}

// stubHarnesses is a fixed harnessPIDs.
type stubHarnesses map[int]struct{}

// HarnessPIDs returns the fixed set.
func (s stubHarnesses) HarnessPIDs() map[int]struct{} { return s }

// TestAncestryCheck: the policy for each outcome, including the fail-open log line.
func TestAncestryCheck(t *testing.T) {
	t.Run("a known descendant is refused", func(t *testing.T) {
		refused, note := ancestryCheck(peerInfo{UID: 1, PID: 30, pidKnown: true}, stubHarnesses{10: {}})
		// 30's parent is unknown here, so the walk is incomplete and the peer is allowed:
		// this also proves an incomplete walk never refuses.
		if refused {
			t.Fatalf("an incomplete walk refused the peer: %q", note)
		}
		if note == "" {
			t.Fatal("an incomplete walk said nothing")
		}
	})

	t.Run("no harness means no refusal", func(t *testing.T) {
		refused, note := ancestryCheck(peerInfo{UID: 1, PID: 30, pidKnown: true}, stubHarnesses{})
		if refused || note != "" {
			t.Fatalf("ancestryCheck = (%t, %q)", refused, note)
		}
	})

	t.Run("an unknown PID is skipped and logged", func(t *testing.T) {
		refused, note := ancestryCheck(peerInfo{UID: 1, pidKnown: false}, stubHarnesses{10: {}})
		if refused {
			t.Fatal("an unknown PID refused the peer")
		}
		if note == "" {
			t.Fatal("an unknown PID said nothing")
		}
	})

	t.Run("a nil harness source is not an error", func(t *testing.T) {
		refused, note := ancestryCheck(peerInfo{UID: 1, PID: 30, pidKnown: true}, nil)
		if refused || note != "" {
			t.Fatalf("ancestryCheck = (%t, %q)", refused, note)
		}
	})
}
