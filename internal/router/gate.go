package router

import "github.com/mngeow/ratatoskr/internal/protocol"

// The reasons a message can be held. They are plain strings because they reach
// the UI and, for the first transition, the store.
const (
	holdStarting = "starting"
	holdBusy     = "busy"
	holdNeedsYou = "needs_you"
	holdUnknown  = "unknown"
	holdExited   = "exited"
	holdDrafting = "drafting"
	// holdPrevious is the reason used while a message is still in flight. The
	// value is "previous_message", not "previous_in_flight".
	holdPrevious = "previous_message"
	holdNoPane   = "no_pane"
)

// harnessCodex is the one harness whose SessionStart does not mean idle.
//
// The name is duplicated here as a string because the router must not import
// internal/agent (that package builds launch specs; the router must stay usable
// without it). internal/agent only ever produces exactly "claude" or "codex", so
// an exact comparison is enough; anything else keeps the old behaviour.
const harnessCodex = "codex"

// sessionStartLifts reports whether a SessionStart means the harness is idle.
//
// Claude announces the launch with it, so it lifts starting or unknown to idle.
// Codex announces its first turn with it: it only sends a hook once a turn
// begins, so a SessionStart means work is starting, not that the agent is ready
// to receive a message. A Codex agent therefore stays starting or unknown until
// its first Stop. Any harness other than exactly "codex" keeps the old rule.
//
// UNTESTED.
func sessionStartLifts(harness string) bool {
	return harness != harnessCodex
}

// gateInput is everything the delivery gate looks at. It is a value, not a
// pointer, so a caller cannot hand the gate a view that changes under it.
type gateInput struct {
	state    protocol.AgentState
	drafting bool // the user has typed but not submitted in that pane
	inFlight bool // a message is already being delivered to that agent
	hasPane  bool // the agent has a live pane to write to
}

// gate decides whether a queued message may be delivered now.
//
// Exactly one case passes: an idle agent, with a pane, nothing in flight and no
// user draft. Everything else holds, with the reason the UI shows. The checks
// run cheapest-and-safest first: no pane, then something already in flight, then
// the agent's state, and a draft only matters once the state is idle.
//
// An unrecognised state holds as unknown. Every branch holds by default, so a
// state added later cannot accidentally start delivering messages.
//
// UNTESTED.
func gate(in gateInput) (ok bool, hold string) {
	if !in.hasPane {
		return false, holdNoPane
	}
	if in.inFlight {
		return false, holdPrevious
	}

	switch in.state {
	case protocol.AgentIdle:
		if in.drafting {
			return false, holdDrafting
		}
		return true, ""
	case protocol.AgentStarting:
		return false, holdStarting
	case protocol.AgentBusy:
		return false, holdBusy
	case protocol.AgentNeedsYou:
		return false, holdNeedsYou
	case protocol.AgentUnknown:
		return false, holdUnknown
	case protocol.AgentExited:
		return false, holdExited
	default:
		return false, holdUnknown
	}
}

// overrideOK reports whether the user's explicit deliver-anyway (F4) applies.
//
// It applies in two situations: an agent whose state is unknown (its hooks are
// missing or untrusted, so the router can never see it idle), and an idle agent
// whose pane holds a draft (the user chose to send over their own draft; design
// v0.10). Never needs_you (invariant 2: answering a permission prompt is the
// human's job and a delivered message would look like an answer), and never busy.
// An unrecognised state is refused. Nothing is overridden while another message
// is in flight, or without a pane.
//
// UNTESTED.
func overrideOK(in gateInput) bool {
	if !in.hasPane || in.inFlight {
		return false
	}
	switch in.state {
	case protocol.AgentUnknown:
		return true
	case protocol.AgentIdle:
		return in.drafting
	}
	return false
}

// nextState applies one hook event to an agent's state.
//
// An exited agent stays exited: a late hook from a harness that has already gone
// away must not resurrect it. Otherwise the event decides the state, and a
// session start lifts a starting or unknown agent to idle. An unrecognised kind
// leaves the state alone.
//
// UNTESTED.
func nextState(cur protocol.AgentState, k protocol.HookKind) protocol.AgentState {
	if cur == protocol.AgentExited {
		return protocol.AgentExited
	}

	switch k {
	case protocol.HookSessionStart:
		switch cur {
		case protocol.AgentStarting, protocol.AgentUnknown:
			return protocol.AgentIdle
		default:
			return cur
		}
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
