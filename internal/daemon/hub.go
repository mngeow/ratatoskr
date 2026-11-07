package daemon

// The hub: every window, the router's events, and the messages that come from the gate.
//
// It is the ONLY consumer of router.Events(), and it never blocks on a window: a window whose
// reliable queue is full is dropped, and the others still get the message. Nothing here holds
// a daemon lock while a client is written to - the windows write on their own goroutines.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No window has been attached.

import (
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// pendingEvery is how often the daemon checks whether the count of messages that would
	// be lost on quit changed. UNTESTED guess.
	pendingEvery = 500 * time.Millisecond // UNTESTED guess

	// byeGrace is how long a stopping daemon waits, for all windows together, for each
	// window's writer to send its bye and close.
	byeGrace = 1 * time.Second // UNTESTED guess
)

// hub owns the windows.
type hub struct {
	gate *gate
	log  *logger

	// pending is how the hub asks the router for the count. It is a field so a test needs no
	// router.
	pending func() int

	// router is what a window's void requests reach: the draft flag, a submit, deliver-held.
	router windowRouter

	// network is the network service, when one is running. Every hook below is nil-safe, so a hub
	// built without one (a test, or an early slice) behaves exactly as before.
	network *networkService

	mu      sync.Mutex
	windows map[string]*window
	// panes is the latest session-level pane state per agent, from the watcher or from a
	// gate change. The hub copies it per window and fills in you_hold.
	panes map[protocol.AgentID]wire.PaneState
	// lastPending is the last count broadcast, so an unchanged count is not resent.
	lastPending int
	havePending bool
	closing     bool
}

// newHub builds an empty hub.
func newHub(log *logger, gate *gate) *hub {
	h := &hub{
		log:     log,
		gate:    gate,
		windows: make(map[string]*window, 2),
		panes:   make(map[protocol.AgentID]wire.PaneState, 2),
	}

	// The gate tells the windows about holder changes and asks for a republish, both
	// outside its own lock.
	gate.onHolderChange = h.announceOwnership
	gate.onSessionChanged = h.republishPane
	return h
}

// run is the events fan-out and the pending ticker. It is the only reader of events.
//
// UNTESTED.
func (h *hub) run(events <-chan protocol.Event) {
	ticker := time.NewTicker(pendingEvery)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			h.fanOut(event)

		case <-ticker.C:
			h.publishPending()
		}
	}
}

// fanOut gives one router event to every window's reliable queue.
func (h *hub) fanOut(event protocol.Event) {
	h.mu.Lock()
	windows := h.snapshotWindowsLocked()
	h.mu.Unlock()

	if event.Agent != nil {
		// One agent changed state: stamp it for the report and ask for one soon. Both are cheap
		// and take no new lock while hub.mu is held.
		if h.network != nil {
			h.network.observe([]protocol.AgentView{*event.Agent}, time.Now())
			h.network.kick()
		}
	}

	for _, w := range windows {
		w.push(wire.TypeEvent, 0, event)
	}
}

// publishPending asks the router for the count and pushes it when it changed.
//
// Nothing is read while no window is attached, which is why a daemon with nobody watching
// costs nothing here.
func (h *hub) publishPending() {
	h.mu.Lock()
	if len(h.windows) == 0 {
		h.mu.Unlock()
		return
	}
	read := h.pending
	h.mu.Unlock()

	if read == nil {
		return
	}
	count := read()

	h.mu.Lock()
	changed := !h.havePending || count != h.lastPending
	h.lastPending = count
	h.havePending = true
	h.mu.Unlock()

	if !changed {
		return
	}

	h.broadcast(wire.TypePending, 0, wire.Pending{Count: count})
}

// register adds a window. The window's ID is unique: a collision is a programming error, so
// the newcomer replaces the older entry and the older one is dropped.
//
// UNTESTED.
func (h *hub) register(w *window) {
	h.mu.Lock()
	if h.closing {
		// A window that connected while the daemon was stopping is not let in.
		h.mu.Unlock()
		w.drop("the daemon is stopping")
		return
	}
	previous := h.windows[w.id]
	h.windows[w.id] = w
	panes := make([]protocol.AgentID, 0, len(h.panes))
	for agent := range h.panes {
		panes = append(panes, agent)
	}
	h.mu.Unlock()

	if previous != nil {
		h.log.logf("window %s replaced an earlier window with the same id", w.id)
		previous.drop("the same window id attached again")
	}

	// The newcomer needs a frame for every session it will show.
	for _, agent := range panes {
		h.republishPane(agent)
	}
}

// unregister removes a window and releases the input it held.
//
// UNTESTED.
func (h *hub) unregister(w *window) {
	h.mu.Lock()
	if h.windows[w.id] == w {
		delete(h.windows, w.id)
	}
	h.mu.Unlock()

	// A window leaving releases its holds; nothing takes them automatically.
	h.gate.releaseAll(w.id)
}

// count is how many windows are attached, for `rtk daemon status`.
//
// UNTESTED.
func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.windows)
}

// broadcast pushes one reliable message to every window.
func (h *hub) broadcast(typ wire.Type, id uint64, body any) {
	h.mu.Lock()
	windows := h.snapshotWindowsLocked()
	h.mu.Unlock()

	for _, w := range windows {
		w.push(typ, id, body)
	}
}

// snapshotWindowsLocked copies the window list. The caller holds h.mu.
func (h *hub) snapshotWindowsLocked() []*window {
	out := make([]*window, 0, len(h.windows))
	for _, w := range h.windows {
		out = append(out, w)
	}
	return out
}

// announceOwnership tells every window that one session's holder changed.
//
// Each window fills in its own you_hold, so the same message serves the new holder, the old
// one and every viewer.
func (h *hub) announceOwnership(agent protocol.AgentID, ownership wire.Ownership) {
	if h.network != nil {
		// Input ownership is part of what a host reports, so a holder change asks for a report.
		h.network.kick()
	}
	h.mu.Lock()
	windows := h.snapshotWindowsLocked()
	h.mu.Unlock()

	for _, w := range windows {
		message := ownership
		message.YouHold = ownership.Holder != "" && ownership.Holder == w.id
		w.push(wire.TypeOwnership, 0, message)
	}
}

// publishPane records one session's latest pane state and gives each window its own copy.
//
// The watcher supplies everything except you_hold; the holder fields are taken from the gate
// here, so a coalesced frame can never carry a stale holder.
//
// UNTESTED.
func (h *hub) publishPane(state wire.PaneState) {
	agent := state.Agent

	// The gate is read under the hub's mutex, so two publishes for one session are ordered:
	// the one that stores last also read the gate last, and a stale holder or drafting flag
	// can never overwrite a newer one in a window's coalesced slot. The gate never calls the
	// hub while holding its own mutex, so this order (hub, then gate) cannot invert.
	h.mu.Lock()
	holder, rev, drafting, ended, exitCode := h.gate.snapshot(agent)
	state.Holder = holder
	state.Rev = rev
	state.Drafting = drafting
	if ended {
		state.Ended = true
		state.ExitCode = exitCode
	}

	h.panes[agent] = state
	windows := h.snapshotWindowsLocked()
	for _, w := range windows {
		copied := state
		copied.YouHold = holder != "" && holder == w.id
		w.setPane(copied)
	}
	h.mu.Unlock()
}

// republishPane re-sends the last known state of one session, with the gate's current holder
// facts. It is what makes a holder change or a drafting change visible.
//
// UNTESTED.
func (h *hub) republishPane(agent protocol.AgentID) {
	h.mu.Lock()
	state, ok := h.panes[agent]
	h.mu.Unlock()

	if !ok {
		// Nothing has been watched yet; the watcher's first tick will publish.
		return
	}
	h.publishPane(state)
}

// announceSessionEnded sends the reliable end notice, then republishes the latched pane state
// so a window that applies messages out of order still ends up correct.
//
// UNTESTED.
func (h *hub) announceSessionEnded(agent protocol.AgentID, rev uint64, exitCode *int) {
	h.broadcast(wire.TypeSessionEnded, 0, wire.SessionEnded{Agent: agent, Rev: rev, ExitCode: exitCode, Reason: endedReason})
	h.republishPane(agent)
}

// closeAll tells every window that the daemon is stopping and closes them.
//
// The bye is best effort: a window whose queue is full is closed without it. Every hold is
// released, so a later attach cannot inherit a stale holder.
//
// UNTESTED.
func (h *hub) closeAll(reason string) {
	h.mu.Lock()
	h.closing = true
	windows := h.snapshotWindowsLocked()
	h.windows = make(map[string]*window, 2)
	h.mu.Unlock()

	// The bye goes to every window at once and each window's own writer closes it after
	// writing it, so one stuck window cannot delay the others. A window that has not finished
	// within one shared grace is closed anyway.
	for _, w := range windows {
		w.pushBye(wire.Bye{Code: wire.CodeShuttingDown, Message: reason})
	}
	grace := time.NewTimer(byeGrace)
	defer grace.Stop()
	expired := false
	for _, w := range windows {
		if !expired {
			select {
			case <-w.done:
			case <-grace.C:
				// The timer fires once; every window after this one is closed at once.
				expired = true
			}
		}
		if expired {
			w.closeWith(reason)
		}
		h.gate.releaseAll(w.id)
	}
}

// endedReason is what a window shows a session ended for.
const endedReason = "the session ended"
