package router

import (
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them.

// allStates includes every state the router knows plus one it does not, so the
// default branches are exercised.
var allStates = []protocol.AgentState{
	protocol.AgentIdle,
	protocol.AgentBusy,
	protocol.AgentNeedsYou,
	protocol.AgentStarting,
	protocol.AgentUnknown,
	protocol.AgentExited,
	protocol.AgentState("not-a-state"),
}

func stateName(s protocol.AgentState) string {
	if s == "" {
		return "empty"
	}
	return string(s)
}

// TestGateHasExactlyOnePassingCombination walks every combination of state,
// drafting, in-flight and pane availability and asserts that only an idle agent
// with a pane, no draft and nothing in flight may be delivered to.
func TestGateHasExactlyOnePassingCombination(t *testing.T) {
	passes := 0
	for _, state := range allStates {
		for _, drafting := range []bool{false, true} {
			for _, inFlight := range []bool{false, true} {
				for _, hasPane := range []bool{false, true} {
					in := gateInput{state: state, drafting: drafting, inFlight: inFlight, hasPane: hasPane}
					ok, hold := gate(in)

					switch {
					case hasPane && !inFlight && state == protocol.AgentIdle && !drafting:
						passes++
						if !ok {
							t.Fatalf("gate(%+v) held (%s) but should pass", in, hold)
						}
						if hold != "" {
							t.Fatalf("gate(%+v) passed with hold reason %q", in, hold)
						}
						continue
					default:
						if ok {
							t.Fatalf("gate(%+v) passed but should hold", in)
						}
					}

					want := expectedHold(state, drafting, inFlight, hasPane)
					if hold != want {
						t.Fatalf("gate(%+v) hold = %q, want %q", in, hold, want)
					}
				}
			}
		}
	}

	if passes != 1 {
		t.Fatalf("%d combinations passed, want exactly 1", passes)
	}
}

// expectedHold restates the precedence the brief specifies, independently of
// gate's implementation: no pane first, then something in flight, then state,
// and a draft only matters while idle.
func expectedHold(state protocol.AgentState, drafting, inFlight, hasPane bool) string {
	if !hasPane {
		return holdNoPane
	}
	if inFlight {
		return holdPrevious
	}
	switch state {
	case protocol.AgentIdle:
		if drafting {
			return holdDrafting
		}
		return ""
	case protocol.AgentStarting:
		return holdStarting
	case protocol.AgentBusy:
		return holdBusy
	case protocol.AgentNeedsYou:
		return holdNeedsYou
	case protocol.AgentExited:
		return holdExited
	default:
		// Both AgentUnknown and any state the router does not know.
		return holdUnknown
	}
}

// TestGateRefusesNeedsYou is the invariant-2 guard: a permission prompt is never
// overridden and never delivered into.
func TestGateRefusesNeedsYou(t *testing.T) {
	in := gateInput{state: protocol.AgentNeedsYou, hasPane: true}
	if ok, hold := gate(in); ok || hold != holdNeedsYou {
		t.Fatalf("gate(%+v) = (%v, %q), want (false, %q)", in, ok, hold, holdNeedsYou)
	}
	if overrideOK(in) {
		t.Fatal("overrideOK permitted a delivery while a permission prompt was open")
	}
}

func TestGateHoldReasonValues(t *testing.T) {
	// The strings reach the UI and, for the first transition, the store, so they
	// are pinned here.
	tests := []struct {
		got  string
		want string
	}{
		{holdStarting, "starting"},
		{holdBusy, "busy"},
		{holdNeedsYou, "needs_you"},
		{holdUnknown, "unknown"},
		{holdExited, "exited"},
		{holdDrafting, "drafting"},
		{holdPrevious, "previous_message"},
		{holdNoPane, "no_pane"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Fatalf("hold reason = %q, want %q", tc.got, tc.want)
		}
	}
}

// TestOverrideOKRule walks every combination and asserts the v0.10 rule: F4
// passes for unknown, and for idle over a draft; never for needs_you, busy,
// starting, exited or an unrecognised state; never with a message in flight or
// without a pane.
func TestOverrideOKRule(t *testing.T) {
	passes := 0
	for _, state := range allStates {
		for _, drafting := range []bool{false, true} {
			for _, inFlight := range []bool{false, true} {
				for _, hasPane := range []bool{false, true} {
					in := gateInput{state: state, drafting: drafting, inFlight: inFlight, hasPane: hasPane}
					want := hasPane && !inFlight &&
						(state == protocol.AgentUnknown || (state == protocol.AgentIdle && drafting))
					got := overrideOK(in)
					if got != want {
						t.Fatalf("overrideOK(%+v) = %v, want %v", in, got, want)
					}
					if got {
						passes++
					}
				}
			}
		}
	}
	// unknown: drafting x2 = 2 combinations; idle+drafting: 1.
	if passes != 3 {
		t.Fatalf("%d combinations override, want 3", passes)
	}
}

func TestOverrideOKRefusesNeedsYouAndBusyEvenWhenDrafting(t *testing.T) {
	for _, state := range []protocol.AgentState{protocol.AgentNeedsYou, protocol.AgentBusy} {
		in := gateInput{state: state, drafting: true, hasPane: true}
		if overrideOK(in) {
			t.Fatalf("overrideOK passed for %s", state)
		}
	}
}

// TestNextStateEveryPair checks every state and hook kind, including the pairs
// the brief leaves unchanged.
func TestNextStateEveryPair(t *testing.T) {
	kinds := []protocol.HookKind{
		protocol.HookSessionStart,
		protocol.HookUserPromptSubmit,
		protocol.HookStop,
		protocol.HookPermissionRequest,
		protocol.HookPostToolUse,
		protocol.HookKind("NotAHook"),
	}

	for _, state := range allStates {
		for _, kind := range kinds {
			want := expectedNextState(state, kind)
			if got := nextState(state, kind); got != want {
				t.Fatalf("nextState(%s, %s) = %s, want %s", stateName(state), kind, stateName(got), stateName(want))
			}
		}
	}
}

func expectedNextState(cur protocol.AgentState, k protocol.HookKind) protocol.AgentState {
	if cur == protocol.AgentExited {
		return protocol.AgentExited
	}
	switch k {
	case protocol.HookSessionStart:
		if cur == protocol.AgentStarting || cur == protocol.AgentUnknown {
			return protocol.AgentIdle
		}
		return cur
	case protocol.HookUserPromptSubmit, protocol.HookPostToolUse:
		return protocol.AgentBusy
	case protocol.HookStop:
		return protocol.AgentIdle
	case protocol.HookPermissionRequest:
		return protocol.AgentNeedsYou
	default:
		return cur
	}
}

// TestNextStateExitedIsSticky states the property on its own: no hook can revive
// a pane that has gone.
func TestNextStateExitedIsSticky(t *testing.T) {
	kinds := []protocol.HookKind{
		protocol.HookSessionStart,
		protocol.HookUserPromptSubmit,
		protocol.HookStop,
		protocol.HookPermissionRequest,
		protocol.HookPostToolUse,
		protocol.HookKind("NotAHook"),
	}
	for _, kind := range kinds {
		if got := nextState(protocol.AgentExited, kind); got != protocol.AgentExited {
			t.Fatalf("nextState(exited, %s) = %s, want exited", kind, stateName(got))
		}
	}
}

// TestSessionStartLifts pins the one harness difference: Codex reports its first
// turn with a SessionStart, so that event must not look like readiness. Anything
// that is not exactly "codex" keeps the old behaviour, including an empty or
// differently-cased harness name.
func TestSessionStartLifts(t *testing.T) {
	tests := []struct {
		harness string
		want    bool
	}{
		{harness: "claude", want: true},
		{harness: harnessCodex, want: false},
		{harness: "", want: true},
		{harness: "Codex", want: true},
		{harness: "codex-cli", want: true},
		{harness: "cursor", want: true},
	}

	for _, tc := range tests {
		if got := sessionStartLifts(tc.harness); got != tc.want {
			t.Fatalf("sessionStartLifts(%q) = %v, want %v", tc.harness, got, tc.want)
		}
	}
}
