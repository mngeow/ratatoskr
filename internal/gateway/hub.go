package gateway

// The hub: which hosts have an open stream, and the bounded queue each one
// writes from.
//
// It holds no Store, no HTTP and no ledger, and it never decides liveness: it only
// answers whether a stream exists. That answer is the streamOpen half of
// HostLive, and nothing else in this package may infer liveness from it.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No stream has been attached
// and no frame has been queued.

import (
	"context"
	"errors"
	"sync"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

var (
	// errNoStream reports a push to a host that has no open stream.
	errNoStream = errors.New("gateway: no stream for that host")
	// errQueueFull reports a push to a stream that is not keeping up.
	errQueueFull = errors.New("gateway: the stream queue is full")
)

// stream is one open stream for one host.
//
// frames is the bounded queue other goroutines push into; the handler goroutine is
// the only writer to the connection. done is closed when the handler returns, so a
// test can wait for teardown. Streams end by cancelling their context; the frames
// channel is never closed, so a sender can never panic on it.
type stream struct {
	host   string
	frames chan protocol.StreamFrame
	cancel context.CancelFunc
	done   chan struct{}
}

// hub is the set of open streams, keyed by host.
type hub struct {
	mu      sync.Mutex
	streams map[string]*stream
	// acks holds one waiter per in-flight message. It only wakes; it never
	// carries a decision, which the database holds.
	acks map[string]chan struct{}
}

// newHub builds an empty hub.
func newHub() *hub {
	return &hub{
		streams: make(map[string]*stream),
		acks:    make(map[string]chan struct{}),
	}
}

// open reports whether a host has an open stream. It is the streamOpen half of
// HostLive and nothing more.
func (h *hub) open(host string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, exists := h.streams[host]
	return exists
}

// attach installs a stream for a host and returns the one it replaced.
//
// The caller cancels the returned stream AFTER this call returns, so no context is
// cancelled while the hub lock is held.
func (h *hub) attach(host string, s *stream) (previous *stream) {
	h.mu.Lock()
	defer h.mu.Unlock()
	previous = h.streams[host]
	h.streams[host] = s
	return previous
}

// detach removes a stream, but only if the hub still holds this exact one: an old
// handler finishing must not evict the stream that replaced it.
//
// It returns whether it removed anything, which is how the handler knows that its
// host has just stopped being attached and the directory should be pushed. The
// brief's signature had no return; the bool is what makes the caller's decision
// correct rather than racy.
func (h *hub) detach(host string, s *stream) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, exists := h.streams[host]
	if !exists || current != s {
		return false
	}
	delete(h.streams, host)
	return true
}

// enqueue pushes one frame without blocking.
//
// The channel is captured under the lock and the send happens outside it, so a
// slow reader never holds the hub for anyone else.
func (h *hub) enqueue(host string, frame protocol.StreamFrame) error {
	h.mu.Lock()
	s, exists := h.streams[host]
	var frames chan protocol.StreamFrame
	if exists {
		frames = s.frames
	}
	h.mu.Unlock()

	if !exists {
		return errNoStream
	}
	select {
	case frames <- frame:
		return nil
	default:
		return errQueueFull
	}
}

// expectAck registers a wake-up for one in-flight message and returns it with the
// function that removes it.
//
// Registering the same id twice replaces the earlier waiter, so the caller's
// forget must only delete when the map still holds its own channel - otherwise a
// finished handler would delete its replacement's registration.
//
// UNTESTED.
func (h *hub) expectAck(id string) (wake <-chan struct{}, forget func()) {
	channel := make(chan struct{}, 1)

	h.mu.Lock()
	if h.acks == nil {
		h.acks = make(map[string]chan struct{})
	}
	h.acks[id] = channel
	h.mu.Unlock()

	forget = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if current, exists := h.acks[id]; exists && current == channel {
			delete(h.acks, id)
		}
	}
	return channel, forget
}

// wake tells a waiting sender that the message's result has been decided.
//
// It never blocks: the waiter is already reading the database, and a missing
// waiter means the sender has stopped waiting.
//
// UNTESTED.
func (h *hub) wake(id string) {
	h.mu.Lock()
	channel, exists := h.acks[id]
	h.mu.Unlock()

	if !exists {
		return
	}
	select {
	case channel <- struct{}{}:
	default:
	}
}

// hosts returns the hosts that currently have a stream.
func (h *hub) hosts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	names := make([]string, 0, len(h.streams))
	for host := range h.streams {
		names = append(names, host)
	}
	return names
}

// cancel ends one host's stream, if it has one. It is used when a stream's queue
// is full: a host that is not reading its frames cannot be kept.
func (h *hub) cancel(host string) {
	h.mu.Lock()
	s, exists := h.streams[host]
	h.mu.Unlock()

	if exists {
		s.cancel()
	}
}

// closeAll cancels every attached stream.
//
// It leaves the map alone: each handler detaches itself as it returns, which is
// why shutting down does not race a handler's own cleanup.
func (h *hub) closeAll() {
	h.mu.Lock()
	streams := make([]*stream, 0, len(h.streams))
	for _, s := range h.streams {
		streams = append(streams, s)
	}
	h.mu.Unlock()

	for _, s := range streams {
		s.cancel()
	}
}
