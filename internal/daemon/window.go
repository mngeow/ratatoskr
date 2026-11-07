package daemon

// One attached window: its outbound state, its single writer, and its reader.
//
// The writer goroutine is the ONLY writer on the connection. Reliable messages go first, then
// the coalesced latest pane state, each write under a deadline; a failed write closes the
// window. Nothing else in the daemon ever writes to a window, which is what keeps a slow or
// wedged client from blocking a pane, the router, or another client.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No frame has crossed a socket.

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// windowQueue is the reliable queue's capacity. A window that cannot keep up is dropped
	// rather than allowed to hold memory or block the daemon.
	windowQueue = 256 // UNTESTED guess

	// requestQueue is one (window, session) worker's queue. Overflowing it drops the window.
	requestQueue = 64 // UNTESTED guess
)

// envelope is one queued ready-to-send message.
type envelope struct {
	typ  wire.Type
	id   uint64
	body any
}

// window is one attached window connection.
type window struct {
	id   string
	conn net.Conn
	hub  *hub
	log  *logger

	queue chan envelope
	// deferred holds reliable messages that arrived before the attached answer was ready.
	// They are sent after it, so attached is always the window's first reliable message.
	deferred []envelope
	paused   bool

	wake chan struct{}
	done chan struct{}

	mu        sync.Mutex
	coalesced map[protocol.AgentID]wire.PaneState
	closed    bool
	closeOnce sync.Once

	// sessions is the fixed set this window may address, keyed by the session's stable agent
	// id. It never gains a member, so a replacement is unreachable from this window.
	sessions map[protocol.AgentID]sessionView
	workers  map[protocol.AgentID]chan wire.Envelope

	// queries counts the read goroutines in flight, so one window cannot flood the store.
	queryMu sync.Mutex
	queries int

	// networkRelease is set the first time this window asks for the network answer: from then on
	// the daemon fetches from the gateway until the window closes. It is nil while the window has
	// asked nothing, which is what keeps an idle window from fetching.
	networkRelease func()
}

// newWindow builds a window for one connection. Nothing is sent until start.
func newWindow(id string, conn net.Conn, h *hub, log *logger) *window {
	return &window{
		id:        id,
		conn:      conn,
		hub:       h,
		log:       log,
		queue:     make(chan envelope, windowQueue),
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		coalesced: make(map[protocol.AgentID]wire.PaneState, 2),
		sessions:  make(map[protocol.AgentID]sessionView, 2),
		workers:   make(map[protocol.AgentID]chan wire.Envelope, 2),
		paused:    true,
	}
}

// push queues one reliable message. It never blocks: a full queue drops the window.
//
// Before the attached answer exists, messages are held and sent after it, so the window's
// first reliable message is always attached.
//
// UNTESTED.
func (w *window) push(typ wire.Type, id uint64, body any) {
	message := envelope{typ: typ, id: id, body: body}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if w.paused {
		// One slot is kept for the attached answer, which goes first.
		if len(w.deferred) >= windowQueue-1 {
			w.mu.Unlock()
			w.drop("its queue overflowed before it was attached")
			return
		}
		w.deferred = append(w.deferred, message)
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	select {
	case w.queue <- message:
	default:
		w.drop("its reliable queue overflowed")
	}
}

// pushBye queues the bye for a window that is about to be closed. It is best effort and never
// waits: a window whose queue is full is closed without it.
func (w *window) pushBye(body wire.Bye) {
	select {
	case w.queue <- envelope{typ: wire.TypeBye, body: body}:
	default:
	}
}

// setPane replaces one session's coalesced state and wakes the writer.
//
// Coalescing is the point: intermediate frames are dropped, and a window that reads slowly
// still sees the latest complete state of every pane.
//
// UNTESTED.
func (w *window) setPane(state wire.PaneState) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.coalesced[state.Agent] = state
	w.mu.Unlock()

	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// startWriter starts the single writer goroutine.
func (w *window) startWriter() {
	go w.writeLoop()
}

// writeLoop is the only writer on the connection.
func (w *window) writeLoop() {
	for {
		// Reliable messages first, so an event is never overtaken by a burst of frames.
		select {
		case message := <-w.queue:
			if !w.write(message) {
				return
			}
			continue
		default:
		}

		select {
		case <-w.done:
			return
		case message := <-w.queue:
			if !w.write(message) {
				return
			}
		case <-w.wake:
			if !w.writePanes() {
				return
			}
		}
	}
}

// write sends one message under the deadline. It reports whether the window survives.
func (w *window) write(message envelope) bool {
	if err := w.conn.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
		w.drop("its connection could not be given a write deadline")
		return false
	}
	if err := wire.Write(w.conn, message.typ, message.id, message.body); err != nil {
		w.drop("a write failed")
		return false
	}
	if message.typ == wire.TypeBye {
		// The bye is the last thing a window hears: once it is written the window is done.
		w.closeWith("")
		return false
	}
	return true
}

// writePanes sends every coalesced pane state that is still waiting.
func (w *window) writePanes() bool {
	w.mu.Lock()
	pending := make([]wire.PaneState, 0, len(w.coalesced))
	for agent, state := range w.coalesced {
		pending = append(pending, state)
		delete(w.coalesced, agent)
	}
	w.mu.Unlock()

	for _, state := range pending {
		if !w.write(envelope{typ: wire.TypePane, body: state}) {
			return false
		}
	}
	return true
}

// sendAttached ends the paused phase: the attached answer goes first, then everything that
// arrived while it was being built, and only then does the writer start.
//
// All of it is queued while holding the window's mutex. push takes the same mutex to read
// `paused`, so no other message can be queued between the attached answer and the held ones:
// attached is the window's first reliable message, and the held ones keep their order. The
// queue is empty and its writer has not started, so these sends have room (one slot more than
// the held messages can fill); a full queue drops the window.
//
// UNTESTED.
func (w *window) sendAttached(attached wire.Attached) bool {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false
	}
	deferred := w.deferred
	w.deferred = nil
	w.paused = false

	overflow := false
	select {
	case w.queue <- envelope{typ: wire.TypeAttached, body: attached}:
	default:
		overflow = true
	}
	for _, message := range deferred {
		if overflow {
			break
		}
		select {
		case w.queue <- message:
		default:
			overflow = true
		}
	}
	w.mu.Unlock()

	if overflow {
		w.drop("its reliable queue overflowed while it attached")
		return false
	}
	return true
}

// closeWith closes the connection once and stops the writer.
//
// UNTESTED.
func (w *window) closeWith(reason string) {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()

		close(w.done)

		w.mu.Lock()
		release := w.networkRelease
		w.networkRelease = nil
		w.mu.Unlock()
		if release != nil {
			// Idempotent: the network service's release runs once however often it is called.
			release()
		}

		_ = w.conn.Close()
		if w.hub != nil {
			w.hub.unregister(w)
		}
		if reason != "" {
			w.log.logf("window %s closed: %s", w.id, reason)
		}
	})
}

// drop closes the window because this client could not keep up or could not be written to.
func (w *window) drop(reason string) {
	w.closeWith(reason)
}

// isClosed reports whether the window is done.
func (w *window) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// addSession records one session this window may address, and starts its FIFO worker.
//
// The worker holds the session's agent id for its whole life, so a request can never reach a
// replacement session that later takes the same name.
//
// UNTESTED.
func (w *window) addSession(view sessionView) {
	w.mu.Lock()
	if _, exists := w.sessions[view.Agent]; exists {
		w.mu.Unlock()
		return
	}
	w.sessions[view.Agent] = view
	requests := make(chan wire.Envelope, requestQueue)
	w.workers[view.Agent] = requests
	w.mu.Unlock()

	go w.runWorker(view, requests)
}

// acquireNetwork registers this window as a consumer of the network fetch loop, once.
//
// A window that never asks for the network answer never fetches anything, which is the contract's
// rule and the reason this is lazy.
//
// UNTESTED.
func (w *window) acquireNetwork() {
	if w.hub == nil || w.hub.network == nil {
		return
	}

	w.mu.Lock()
	// A query that was in flight when the window closed must not acquire after closeWith has
	// already released: nothing would ever release it, and the daemon would fetch for ever.
	if w.closed || w.networkRelease != nil {
		w.mu.Unlock()
		return
	}
	release := w.hub.network.acquire()
	w.networkRelease = release
	w.mu.Unlock()
}

// session returns one addressable session.
func (w *window) session(agent protocol.AgentID) (sessionView, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	view, ok := w.sessions[agent]
	return view, ok
}

// enqueue puts one request on its session's queue, in arrival order.
func (w *window) enqueue(agent protocol.AgentID, envelope wire.Envelope) bool {
	w.mu.Lock()
	requests, ok := w.workers[agent]
	w.mu.Unlock()
	if !ok {
		return false
	}

	select {
	case requests <- envelope:
		return true
	default:
		w.drop("its request queue overflowed")
		return false
	}
}

// serve reads requests until the connection ends.
//
// After attach there is no read deadline: a window may sit idle for hours. A frame the wire
// layer refuses closes the window; a request that merely makes no sense is answered.
//
// UNTESTED.
func (w *window) serve() {
	defer w.closeWith("its reader stopped")

	for {
		envelope, err := wire.Read(w.conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !w.isClosed() {
				w.log.logf("window %s: reading failed: %v", w.id, err)
			}
			return
		}
		w.dispatch(envelope)
		if w.isClosed() {
			return
		}
	}
}

// dispatch routes one request.
func (w *window) dispatch(envelope wire.Envelope) {
	switch envelope.Type {
	case wire.TypeDetach:
		// The daemon keeps every session; only this window goes away.
		w.closeWith("it detached")
		return

	case wire.TypeAttach:
		// A second attach would leave the connection's state ambiguous.
		w.ack(envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeBadRequest, Message: "this window is already attached"}})
		w.closeWith("it attached twice")
		return

	case wire.TypeQuery:
		w.startQuery(envelope)
		return

	case wire.TypeKey, wire.TypePaste, wire.TypeMouse, wire.TypeResize,
		wire.TypeDraft, wire.TypeSubmitted, wire.TypeDeliverHeld, wire.TypeTakeInput:
		w.route(envelope)
		return
	}

	// An unknown type is answered when the request carries an id, because that is the only
	// way a client can match an answer to it.
	if envelope.ID == 0 {
		w.closeWith("it sent a message this build does not know, without an id")
		return
	}
	w.ack(envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that request is not supported by this build"}})
}

// route sends one input request to its session's worker.
//
// Only the agent id is decoded here, so routing does not depend on the rest of the body; the
// worker decodes again, in order, with the op.
func (w *window) route(envelope wire.Envelope) {
	var ref wire.SessionRef
	if err := json.Unmarshal(envelope.Body, &ref); err != nil || ref.Agent == "" {
		w.ack(envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that request names no session"}})
		return
	}

	view, ok := w.session(ref.Agent)
	if !ok {
		// An agent this window does not show, including one that ended and was replaced.
		w.ack(envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeNoSuchSession, Message: "no session by that id is running in this window"}})
		return
	}

	if !w.enqueue(view.Agent, envelope) {
		return
	}
}

// ack queues one acknowledgement on the reliable path.
func (w *window) ack(id uint64, ack wire.Ack) {
	w.push(wire.TypeAck, id, ack)
}

// result queues one query answer on the reliable path.
func (w *window) result(id uint64, result wire.Result) {
	w.push(wire.TypeResult, id, result)
}
