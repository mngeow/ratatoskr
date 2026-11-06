package daemon

// The input gate: which window may type into which session.
//
// There is exactly one gate, and every input operation asks it before doing anything else.
// A viewer's keys are refused here, in the daemon, not merely hidden by the window.
//
// The gate owns the one revision counter per session. It is bumped when the holder changes
// and when a session ends, and never for a drafting change: a window applies the holder
// fields only at a revision at least as high as the highest it has applied, so an equal
// revision carrying a new Drafting still applies.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No window has typed anything.

import (
	"sync"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// gateState is one session's ownership. Every field is read and written under the gate's
// mutex, which is never held while a client is written to: the gate collects what changed
// and the hub, outside the lock, tells the windows.
type gateState struct {
	name     string
	holder   string
	rev      uint64
	drafting bool
	ended    bool
	exitCode *int
}

// gate is every session's ownership.
type gate struct {
	mu       sync.Mutex
	sessions map[protocol.AgentID]*gateState

	// onHolderChange tells every window that the holder of one session changed. It runs
	// after the gate's mutex is released.
	onHolderChange func(agent protocol.AgentID, ownership wire.Ownership)
	// onSessionChanged asks the hub to republish that session's pane state, which carries
	// you_hold and drafting. It runs after the mutex is released.
	onSessionChanged func(agent protocol.AgentID)
}

// newGate builds an empty gate.
func newGate() *gate {
	return &gate{sessions: make(map[protocol.AgentID]*gateState, 2)}
}

// addSession records a new session. It starts unheld and unended, at revision 1.
//
// UNTESTED.
func (g *gate) addSession(agent protocol.AgentID, name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessions[agent] = &gateState{name: name, rev: 1}
}

// removeSession forgets a session after its end has been announced.
//
// UNTESTED.
func (g *gate) removeSession(agent protocol.AgentID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.sessions, agent)
}

// has reports whether that agent is a current session.
func (g *gate) has(agent protocol.AgentID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.sessions[agent]
	return ok
}

// holder is the window that holds input for one session, or "".
//
// UNTESTED.
func (g *gate) holder(agent protocol.AgentID) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.sessions[agent]
	if !ok {
		return ""
	}
	return state.holder
}

// snapshot is one session's ownership as a pane state needs it.
//
// UNTESTED.
func (g *gate) snapshot(agent protocol.AgentID) (holder string, rev uint64, drafting, ended bool, exitCode *int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.sessions[agent]
	if !ok {
		return "", 0, false, true, nil
	}
	return state.holder, state.rev, state.drafting, state.ended, state.exitCode
}

// admit is the one rule every input operation applies.
//
// It returns nil when windowID may type into that session, and the refusal otherwise:
// session_ended when the session is over, not_holder when another window holds it.
//
// UNTESTED.
func (g *gate) admit(agent protocol.AgentID, windowID string) *wire.Error {
	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok := g.sessions[agent]
	if !ok {
		return &wire.Error{Code: wire.CodeNoSuchSession, Message: "no session by that id is running"}
	}
	if state.ended {
		return &wire.Error{Code: wire.CodeSessionEnded, Message: "that session has ended"}
	}
	if state.holder != windowID {
		return &wire.Error{Code: wire.CodeNotHolder, Message: "another window holds input for that session; request it first"}
	}
	return nil
}

// takeInput moves the holder to windowID.
//
// It is the only operation that is not gated by holding. The previous holder is told, and
// so is every other window, so a viewer learns that it is a viewer.
//
// UNTESTED.
func (g *gate) takeInput(agent protocol.AgentID, windowID string) (wire.Ownership, *wire.Error) {
	g.mu.Lock()
	state, ok := g.sessions[agent]
	if !ok {
		g.mu.Unlock()
		return wire.Ownership{}, &wire.Error{Code: wire.CodeNoSuchSession, Message: "no session by that id is running"}
	}
	if state.ended {
		g.mu.Unlock()
		return wire.Ownership{}, &wire.Error{Code: wire.CodeSessionEnded, Message: "that session has ended"}
	}

	previous := state.holder
	state.holder = windowID
	state.rev++
	ownership := wire.Ownership{
		Agent:   agent,
		Rev:     state.rev,
		Holder:  windowID,
		YouHold: true,
	}
	rev := state.rev
	g.mu.Unlock()

	if g.onHolderChange != nil && previous != windowID {
		// Every window hears the new holder; each fills in its own you_hold.
		announcement := wire.Ownership{Agent: agent, Rev: rev, Holder: windowID}
		g.onHolderChange(agent, announcement)
	}
	if previous != windowID && g.onSessionChanged != nil {
		g.onSessionChanged(agent)
	}
	return ownership, nil
}

// assign gives an unheld session to an attaching window. It is what makes a lone window
// need no keypress per session, and it bumps the revision like any holder change.
//
// It reports whether it assigned the session.
//
// UNTESTED.
func (g *gate) assign(agent protocol.AgentID, windowID string) bool {
	g.mu.Lock()
	state, ok := g.sessions[agent]
	if !ok || state.ended || state.holder != "" {
		g.mu.Unlock()
		return false
	}
	state.holder = windowID
	state.rev++
	rev := state.rev
	g.mu.Unlock()

	if g.onHolderChange != nil {
		g.onHolderChange(agent, wire.Ownership{Agent: agent, Rev: rev, Holder: windowID})
	}
	defer func() {
		if g.onSessionChanged != nil {
			g.onSessionChanged(agent)
		}
	}()
	return true
}

// releaseAll drops every hold windowID had, and nothing takes them. It runs when a window
// closes: a window leaving touches nothing at the gateway or in the store.
//
// UNTESTED.
func (g *gate) releaseAll(windowID string) {
	type released struct {
		agent protocol.AgentID
		rev   uint64
	}

	g.mu.Lock()
	var freed []released
	for agent, state := range g.sessions {
		if state.holder != windowID {
			continue
		}
		state.holder = ""
		state.rev++
		freed = append(freed, released{agent: agent, rev: state.rev})
	}
	g.mu.Unlock()

	for _, item := range freed {
		if g.onHolderChange != nil {
			// Holder is empty: every window, including the one that just left, learns that
			// nobody holds it. A closing window simply will not read it.
			g.onHolderChange(item.agent, wire.Ownership{Agent: item.agent, Rev: item.rev})
		}
		if g.onSessionChanged != nil {
			g.onSessionChanged(item.agent)
		}
	}
}

// setDrafting records the holder's draft flag. It does NOT bump the revision: a drafting
// change rides the pane state, which a window applies at an equal revision.
//
// UNTESTED.
func (g *gate) setDrafting(agent protocol.AgentID, drafting bool) {
	g.mu.Lock()
	state, ok := g.sessions[agent]
	if !ok || state.ended || state.drafting == drafting {
		g.mu.Unlock()
		return
	}
	state.drafting = drafting
	g.mu.Unlock()

	if g.onSessionChanged != nil {
		g.onSessionChanged(agent)
	}
}

// end latches a session as over: no holder, ended true, a fresh revision, and a refusal
// for anything that still asks.
//
// UNTESTED.
func (g *gate) end(agent protocol.AgentID, exitCode *int) (rev uint64, ok bool) {
	g.mu.Lock()
	state, ok := g.sessions[agent]
	if !ok {
		g.mu.Unlock()
		return 0, false
	}
	wasEnded := state.ended
	state.holder = ""
	state.ended = true
	state.drafting = false
	state.exitCode = exitCode
	state.rev++
	rev = state.rev
	g.mu.Unlock()

	if wasEnded {
		return rev, true
	}
	if g.onHolderChange != nil {
		g.onHolderChange(agent, wire.Ownership{Agent: agent, Rev: rev})
	}
	if g.onSessionChanged != nil {
		g.onSessionChanged(agent)
	}
	return rev, true
}
