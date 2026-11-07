package daemon

// The pane watcher: one goroutine per session, turning a pane into the complete wire.PaneState a
// window renders.
//
// It publishes only when something changed, and the hub coalesces whatever it publishes, so a
// window that reads slowly still sees the latest complete state rather than a backlog of frames.
// Nothing here waits on a window: the hub's publish copies into a window's slot and returns.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No pane has been watched.

import (
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// watcherEvery is how often one pane is compared. It is fast enough that typing feels
	// immediate and slow enough that a burst of output coalesces.
	watcherEvery = 30 * time.Millisecond // UNTESTED guess
)

// paneSnapshot is the comparable part of a pane: what the watcher compares to decide whether
// anything changed.
type paneSnapshot struct {
	frame pane.Frame
	cols  int
	rows  int
	mouse uint32
	ready bool
	err   string
}

// paneWatcher watches one session.
type paneWatcher struct {
	view sessionView
	hub  *hub
	stop chan struct{}
	once sync.Once
}

// newPaneWatcher builds a watcher; start runs it.
func newPaneWatcher(view sessionView, h *hub) *paneWatcher {
	return &paneWatcher{view: view, hub: h, stop: make(chan struct{})}
}

// start begins watching, and returns the watcher.
func (p *paneWatcher) start() *paneWatcher {
	go p.run()
	return p
}

// shut stops the watcher. It is safe to call twice.
func (p *paneWatcher) shut() {
	p.once.Do(func() { close(p.stop) })
}

// run compares the pane on every tick and publishes the states that changed.
func (p *paneWatcher) run() {
	ticker := time.NewTicker(watcherEvery)
	defer ticker.Stop()

	var last paneSnapshot
	haveLast := false

	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			current, ok := p.snapshot()
			if !ok {
				return
			}
			if haveLast && current == last {
				continue
			}
			last = current
			haveLast = true
			p.hub.publishPane(p.state(current))
		}
	}
}

// snapshot reads the pane's comparable state.
func (p *paneWatcher) snapshot() (paneSnapshot, bool) {
	if p.view.Pane == nil {
		return paneSnapshot{}, false
	}

	errText := ""
	if err := p.view.Pane.Err(); err != nil {
		errText = sanitizeMessage(err.Error())
	}
	cols, rows := p.view.Pane.Size()

	return paneSnapshot{
		frame: p.view.Pane.Snapshot(),
		cols:  cols,
		rows:  rows,
		mouse: p.view.Pane.MouseModeBits(),
		ready: p.view.Pane.InputReady(),
		err:   errText,
	}, true
}

// state builds the complete pane state. YouHold is deliberately absent: it is per window, and
// the hub fills it in.
//
// UNTESTED.
func (p *paneWatcher) state(snapshot paneSnapshot) wire.PaneState {
	return wire.PaneState{
		Agent:      p.view.Agent,
		Frame:      snapshot.frame,
		Cols:       snapshot.cols,
		Rows:       snapshot.rows,
		Mouse:      snapshot.mouse,
		InputReady: snapshot.ready,
		Err:        snapshot.err,
	}
}

// endedPaneState is the final state of a session that ended. The ended path publishes it
// directly, because the watcher may already have stopped.
//
// UNTESTED.
func endedPaneState(h *hub, view sessionView, exitCode *int) wire.PaneState {
	state := wire.PaneState{Agent: view.Agent, Ended: true, ExitCode: exitCode}
	if view.Pane != nil {
		state.Frame = view.Pane.Snapshot()
		state.Cols, state.Rows = view.Pane.Size()
		state.Mouse = view.Pane.MouseModeBits()
	}
	return state
}

// sessionObserverFor is the manager's observer: it starts a watcher per session, announces an
// exit and an end, and keeps the gate in step.
//
// It must be idempotent: an exit runs sessionExited and, moments later, sessionEnded for the
// same session.
type paneObserver struct {
	hub *hub
	log *logger
	// network is the network service, when one is running: a session starting, exiting or ending
	// changes what this host reports, so it is asked for a report. Nil-safe.
	network  *networkService
	mu       sync.Mutex
	watchers map[protocol.AgentID]*paneWatcher
}

// newPaneObserver builds the observer.
func newPaneObserver(h *hub, log *logger) *paneObserver {
	return &paneObserver{hub: h, log: log, watchers: make(map[protocol.AgentID]*paneWatcher, 2)}
}

// sessionStarted opens the gate for the session and starts its watcher.
//
// UNTESTED.
func (o *paneObserver) sessionStarted(view sessionView) {
	o.hub.gate.addSession(view.Agent, view.Name)

	o.mu.Lock()
	previous := o.watchers[view.Agent]
	watcher := newPaneWatcher(view, o.hub).start()
	o.watchers[view.Agent] = watcher
	o.mu.Unlock()

	if previous != nil {
		previous.shut()
	}
	if o.network != nil {
		o.network.kick()
	}
	o.log.logf("watching the %s session (%s)", view.Name, view.Agent)
}

// sessionExited records the exit code and latches the session as ended, so no window can type
// into it any more.
//
// UNTESTED.
func (o *paneObserver) sessionExited(view sessionView, exitCode *int) {
	rev, ok := o.hub.gate.end(view.Agent, exitCode)
	if !ok {
		return
	}

	o.mu.Lock()
	watcher := o.watchers[view.Agent]
	o.mu.Unlock()
	if watcher != nil {
		watcher.shut()
	}

	// The latched state is published directly: the watcher may already be gone, and a window
	// must see the end whatever the order of frames.
	o.hub.publishPane(endedPaneState(o.hub, view, exitCode))
	o.hub.announceSessionEnded(view.Agent, rev, exitCode)
	if o.network != nil {
		o.network.kick()
	}
	o.log.logf("the %s session exited (%s)", view.Name, view.Agent)
}

// sessionEnded finishes the bookkeeping: the gate forgets the session, so a late request gets
// no_such_session rather than an ended one.
//
// UNTESTED.
func (o *paneObserver) sessionEnded(view sessionView) {
	o.mu.Lock()
	watcher := o.watchers[view.Agent]
	delete(o.watchers, view.Agent)
	o.mu.Unlock()

	if watcher != nil {
		watcher.shut()
	}
	o.hub.gate.removeSession(view.Agent)
	if o.network != nil {
		// The session is gone, so its state_at stamp must go with it.
		o.network.forget(view.Agent)
		o.network.kick()
	}
	o.log.logf("the %s session is gone (%s)", view.Name, view.Agent)
}
