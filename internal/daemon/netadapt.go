package daemon

// The small adapters that let the network service read the manager, the router and the gate without
// the service knowing any of their concrete types.
//
// They are deliberately tiny, and none of them changes an existing signature: the service takes
// interfaces, and these methods are what the daemon's own values happen to satisfy.
//
// UNTESTED: see netsvc.go.

import (
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// LocalSessions is every running session as the network service needs it.
//
// It is the manager's view, so it names only what a report may carry: the local agent id, the
// gateway's session id when there is one, the harness version and when it started.
//
// UNTESTED.
func (m *sessionManager) LocalSessions() []localSession {
	running := m.Running()

	out := make([]localSession, 0, len(running))
	for _, view := range running {
		out = append(out, localSession{
			Name:           view.Name,
			Agent:          view.Agent,
			Gateway:        m.gatewaySession(view.Name),
			HarnessVersion: m.harnessVersionOf(view.Agent),
			StartedAt:      view.StartedAt,
		})
	}
	return out
}

// gatewaySession is the gateway's session id for one running session, or "".
func (m *sessionManager) gatewaySession(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.sessions[name]
	if !ok || entry.state != stateRunning {
		return ""
	}
	return entry.gateway
}

// harnessVersionOf is one session's harness version, when the router recorded it.
func (m *sessionManager) harnessVersionOf(agent protocol.AgentID) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.versions[agent]
}

// Holder reports who holds input for one session, and whether the gate knows it at all.
//
// The window id is used only to tell "someone holds it" from "nobody does": it never leaves the
// daemon, and it is never part of a report.
//
// UNTESTED.
func (g *gate) Holder(agent protocol.AgentID) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok := g.sessions[agent]
	if !ok {
		return "", false
	}
	return state.holder, true
}
