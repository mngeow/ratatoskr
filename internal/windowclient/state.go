package windowclient

// The cached state a window holds: the snapshot, the pending count, and one pane state per
// session, merged by the revision and latch rules in daemon-tui-brief.md.
//
// Every getter reads an immutable copy or an atomic, so nothing here takes a lock that IO
// holds, and nothing here talks to the socket.
//
// UNTESTED: see doc.go.

import (
	"sort"
	"sync"
	"sync/atomic"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// stateCache is the immutable snapshot plus the merge helpers.
//
// Two kinds of change exist: a full snapshot (from attached or a snapshot query) and a delta
// (a protocol.Event). Both are applied the same way, so a delta that arrives twice changes
// nothing and a snapshot never loses a newer delta's effect.
type stateCache struct {
	snapshot atomic.Pointer[protocol.Snapshot]
	pending  atomic.Int64
}

// newStateCache builds a cache from the attached answer.
func newStateCache(attached wire.Attached) *stateCache {
	cache := &stateCache{}
	snapshot := copySnapshot(attached.Snapshot)
	cache.snapshot.Store(&snapshot)
	cache.pending.Store(int64(attached.Pending))
	return cache
}

// Snap returns the current snapshot by value, so a caller cannot change the shared copy.
func (c *stateCache) Snap() protocol.Snapshot {
	if snapshot := c.snapshot.Load(); snapshot != nil {
		return copySnapshot(*snapshot)
	}
	return protocol.Snapshot{}
}

// install replaces the snapshot with a merged copy and returns it.
func (c *stateCache) install(snapshot protocol.Snapshot) protocol.Snapshot {
	copied := copySnapshot(snapshot)
	c.snapshot.Store(&copied)
	return copied
}

// merge applies one delta to the current snapshot and installs the result.
//
// Agents, messages and sends are replaced by ID; the budget replaces only when the delta carries
// one; the banner and the connection view follow the same rule the local model uses.
func (c *stateCache) merge(event protocol.Event) protocol.Snapshot {
	current := c.Snap()

	if event.Agent != nil {
		current.Agents = replaceAgent(current.Agents, *event.Agent)
	}
	if event.Message != nil {
		current.Messages = replaceMessage(current.Messages, *event.Message)
	}
	if event.Send != nil {
		current.Sends = replaceSend(current.Sends, *event.Send)
	}
	if event.Budget != nil {
		current.Budget = *event.Budget
	}
	if event.Banner != "" {
		current.Banner = event.Banner
	}
	if event.Connection != nil {
		connection := *event.Connection
		current.Connection = &connection
	}

	return c.install(current)
}

// setPending records a pending count.
func (c *stateCache) setPending(count int) { c.pending.Store(int64(count)) }

// Pending is the cached count of messages that would be lost on quit.
func (c *stateCache) Pending() int { return int(c.pending.Load()) }

// copySnapshot deep-copies everything a caller could mutate.
func copySnapshot(snapshot protocol.Snapshot) protocol.Snapshot {
	out := snapshot

	out.Agents = make([]protocol.AgentView, len(snapshot.Agents))
	for i, agent := range snapshot.Agents {
		copied := agent
		if agent.ExitCode != nil {
			code := *agent.ExitCode
			copied.ExitCode = &code
		}
		out.Agents[i] = copied
	}

	out.Messages = append([]protocol.MessageView(nil), snapshot.Messages...)
	out.Sends = append([]protocol.SendView(nil), snapshot.Sends...)

	if snapshot.Connection != nil {
		connection := *snapshot.Connection
		connection.Peers = append([]string(nil), snapshot.Connection.Peers...)
		out.Connection = &connection
	}
	return out
}

// replaceAgent puts one agent in place, by ID, keeping a stable order.
func replaceAgent(agents []protocol.AgentView, agent protocol.AgentView) []protocol.AgentView {
	for i := range agents {
		if agents[i].ID == agent.ID {
			agents[i] = agent
			return agents
		}
	}
	agents = append(agents, agent)
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	return agents
}

// replaceMessage puts one message in place, by ID.
func replaceMessage(messages []protocol.MessageView, message protocol.MessageView) []protocol.MessageView {
	for i := range messages {
		if messages[i].ID == message.ID {
			messages[i] = message
			return messages
		}
	}
	messages = append(messages, message)
	if len(messages) > 200 {
		messages = append([]protocol.MessageView(nil), messages[len(messages)-200:]...)
	}
	return messages
}

// replaceSend puts one outbound record in place, by ID.
func replaceSend(sends []protocol.SendView, send protocol.SendView) []protocol.SendView {
	for i := range sends {
		if sends[i].ID == send.ID {
			sends[i] = send
			return sends
		}
	}
	sends = append(sends, send)
	if len(sends) > 200 {
		sends = append([]protocol.SendView(nil), sends[len(sends)-200:]...)
	}
	return sends
}

// paneStore is every session's pane, by stable agent ID.
//
// A replacement session has a new ID, so an old pane can never be handed a new session's input.
type paneStore struct {
	mu    sync.RWMutex
	panes map[protocol.AgentID]*Pane
}

// newPaneStore builds the store from the attached answer.
func newPaneStore(client *Client, attached wire.Attached) *paneStore {
	store := &paneStore{panes: make(map[protocol.AgentID]*Pane, len(attached.Slots))}
	for _, slot := range attached.Slots {
		store.panes[slot.Agent] = newPane(client, slot)
	}
	return store
}

// Pane returns one session's pane, or nil for an agent this window does not show.
func (s *paneStore) Pane(agent protocol.AgentID) *Pane {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.panes[agent]
}

// agents lists the panes, for an ownership or end message that names an unknown session.
func (s *paneStore) all() []*Pane {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Pane, 0, len(s.panes))
	for _, pane := range s.panes {
		out = append(out, pane)
	}
	return out
}

// apply installs one complete pane state, merging it by the contract's rules.
func (s *paneStore) apply(state wire.PaneState) {
	if pane := s.Pane(state.Agent); pane != nil {
		pane.apply(state)
	}
}

// applyOwnership installs an ownership change.
//
// It is authoritative for who holds input, but it never resurrects an ended session and it never
// clears the ended latch.
func (s *paneStore) applyOwnership(ownership wire.Ownership) {
	if pane := s.Pane(ownership.Agent); pane != nil {
		pane.applyOwnership(ownership)
	}
}

// ended lists the panes whose session has ended, for the daemon's status shapes.
func (s *paneStore) ended() []protocol.AgentID {
	var out []protocol.AgentID
	for _, pane := range s.all() {
		if pane.Ended() {
			out = append(out, pane.agent)
		}
	}
	return out
}
