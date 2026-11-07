package windowclient

// The client: the handshake, the two pumps, and the lifecycle.
//
// Dial speaks exactly the sequence the daemon expects - hello, hello_ack, attach, attached - and
// then starts one writer and one reader. The writer is the only writer on the connection; the
// reader never waits on a request or a consumer.
//
// UNTESTED: see doc.go.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// helloTimeout bounds the hello handshake.
	helloTimeout = 5 * time.Second // UNTESTED guess
	// attachTimeout bounds the whole attach exchange, because the daemon starts the requested
	// sessions under its own 30 s budget inside it.
	attachTimeout = 40 * time.Second // UNTESTED guess
	// writeTimeout bounds one frame write.
	writeTimeout = 5 * time.Second // UNTESTED guess
	// writeQueue is how many frames may wait for the writer.
	writeQueue = 128 // UNTESTED guess
	// maxPending bounds the requests waiting for an answer.
	maxPending = 128 // UNTESTED guess
	// eventQueue bounds both event channels: overflow fails the window rather than blocking.
	eventQueue = 256 // UNTESTED guess
	// retiredLimit bounds the ids remembered after a query timed out, so a late answer is
	// ignored rather than treated as a protocol violation.
	retiredLimit = 64 // UNTESTED guess
)

// Client is one attached window.
type Client struct {
	conn     net.Conn
	build    string
	windowID string
	attached wire.Attached

	out  chan outFrame
	done chan struct{}
	// channelsClosed is closed once both pumps have stopped and the channels are closed.
	channelsClosed chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool

	state *stateCache
	panes *paneStore

	events       chan protocol.Event
	windowEvents chan WindowEvent

	nextID atomic.Uint64

	pendingMu sync.Mutex
	pending   map[uint64]*pendingCall
	retired   map[uint64]struct{}

	querySem chan struct{}

	resyncMu       sync.Mutex
	resyncInFlight bool
	resyncBuffer   []protocol.Event

	errMu sync.Mutex
	err   error

	wg                 sync.WaitGroup
	pubMu              sync.RWMutex
	publicationsClosed bool
}

// outFrame is one queued message for the writer.
type outFrame struct {
	typ  wire.Type
	id   uint64
	body any
}

// WindowEvent is one state change the UI must react to beyond a frame.
//
// A frame update needs no event: the UI polls the cached state. These are the changes that need
// an explicit signal.
type WindowEvent struct {
	Kind  string
	Agent protocol.AgentID
	// Error is set for KindOperationError (nil when a latch was cleared) and
	// KindDisconnected.
	Error error
	// Ownership is the authoritative ownership for KindOwnership.
	Ownership wire.Ownership
}

// Event kinds.
const (
	// KindOwnership: the holder of a session changed.
	KindOwnership = "ownership"
	// KindSessionEnded: a session ended; the latch is permanent.
	KindSessionEnded = "session_ended"
	// KindDisconnected: the window lost its connection. Nothing reconnects.
	KindDisconnected = "disconnected"
	// KindOperationError: a latched failure was set, or cleared when Error is nil.
	KindOperationError = "operation_error"
)

// Dial attaches one window to a daemon's socket.
//
// The caller's context owns the window lifetime. The internal attach deadline
// applies only to bootstrap and is cleared before the pumps start.
//
// A zero-slot answer is returned as it is: whether that is guidance to print or an error is the
// caller's decision.
//
// UNTESTED.
func Dial(ctx context.Context, socketPath, build string, attach wire.Attach) (*Client, error) {
	handshakeCtx, cancel := context.WithTimeout(ctx, attachTimeout)
	defer cancel()
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(handshakeCtx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("windowclient: connect to the daemon: %w", err)
	}

	client := &Client{
		conn:           conn,
		build:          build,
		out:            make(chan outFrame, writeQueue),
		done:           make(chan struct{}),
		channelsClosed: make(chan struct{}),
		events:         make(chan protocol.Event, eventQueue),
		windowEvents:   make(chan WindowEvent, eventQueue),
		pending:        make(map[uint64]*pendingCall, 8),
		retired:        make(map[uint64]struct{}, 8),
		querySem:       make(chan struct{}, queryLimit),
	}

	// The hello and the attach are bounded together, and the caller's context may shorten it.

	if err := client.handshake(handshakeCtx, socketPath, build, attach); err != nil {
		_ = conn.Close()
		return nil, err
	}

	client.state = newStateCache(client.attached)
	client.panes = newPaneStore(client, client.attached)

	client.wg.Add(2)
	go client.readLoop()
	go client.writeLoop()
	go client.closeChannelsWhenDone()
	go func() {
		select {
		case <-ctx.Done():
			client.fail("the window context ended: " + ctx.Err().Error())
		case <-client.done:
		}
	}()

	return client, nil
}

// handshake performs the hello and the attach.
func (c *Client) handshake(ctx context.Context, socketPath, build string, attach wire.Attach) error {
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}

	helloCtx, cancelHello := context.WithTimeout(ctx, helloTimeout)
	defer cancelHello()
	if deadline, ok := helloCtx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}

	if err := wire.Write(c.conn, wire.TypeHello, 0, wire.Hello{
		Build: build,
		Role:  wire.RoleWindow,
		PID:   os.Getpid(),
	}); err != nil {
		return fmt.Errorf("windowclient: send the hello: %w", err)
	}

	envelope, err := wire.Read(c.conn)
	if err != nil {
		return fmt.Errorf("windowclient: read the hello answer: %w", err)
	}
	switch envelope.Type {
	case wire.TypeHelloAck:
		var ack wire.HelloAck
		if err := wire.Decode(envelope, &ack); err != nil {
			return err
		}
		c.windowID = ack.WindowID
		if ack.Build != build {
			return errors.New("windowclient: daemon build does not match window")
		}
		if c.windowID == "" {
			return errors.New("windowclient: the daemon did not name this window")
		}
	case wire.TypeRefused:
		var refusal wire.Refusal
		if err := wire.Decode(envelope, &refusal); err != nil {
			return err
		}
		// The prefix stays exactly as it was, and the code travels inside the wrap so a caller can
		// tell an expected-session refusal from any other.
		return fmt.Errorf("windowclient: the daemon refused this window: %w",
			&RefusalError{Code: refusal.Code, Message: refusal.Message})
	default:
		return fmt.Errorf("windowclient: unexpected %q in answer to the hello", envelope.Type)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}

	if attach.Width < 1 || attach.Height < 1 {
		return errors.New("windowclient: the attach size must be at least 1x1")
	}
	if err := wire.Write(c.conn, wire.TypeAttach, 0, attach); err != nil {
		return fmt.Errorf("windowclient: send the attach: %w", err)
	}

	envelope, err = wire.Read(c.conn)
	if err != nil {
		return fmt.Errorf("windowclient: read the attach answer: %w", err)
	}
	switch envelope.Type {
	case wire.TypeAttached:
		var attached wire.Attached
		if err := wire.Decode(envelope, &attached); err != nil {
			return err
		}
		if attached.WindowID != c.windowID {
			return errors.New("windowclient: attached window identity mismatch")
		}
		seen := make(map[protocol.AgentID]bool)
		if len(attached.Slots) > 2 {
			return errors.New("windowclient: too many attached sessions")
		}
		for _, slot := range attached.Slots {
			if slot.Agent == "" || seen[slot.Agent] || slot.Pane.Agent != slot.Agent {
				return errors.New("windowclient: invalid attached session identity")
			}
			seen[slot.Agent] = true
		}
		c.attached = attached
		return c.conn.SetDeadline(time.Time{})
	case wire.TypeRefused:
		var refusal wire.Refusal
		if err := wire.Decode(envelope, &refusal); err != nil {
			return err
		}
		return fmt.Errorf("windowclient: the daemon refused the attach: %w",
			&RefusalError{Code: refusal.Code, Message: refusal.Message})
	default:
		return fmt.Errorf("windowclient: unexpected %q in answer to the attach", envelope.Type)
	}
}

// Attached is the bootstrap the daemon answered with, by value.
func (c *Client) Attached() wire.Attached {
	attached := c.attached
	attached.Slots = append([]wire.Slot(nil), c.attached.Slots...)
	for i := range attached.Slots {
		attached.Slots[i].Pane = copyPaneState(attached.Slots[i].Pane)
	}
	attached.StartErrors = append([]wire.StartError(nil), c.attached.StartErrors...)
	attached.Snapshot = copySnapshot(c.attached.Snapshot)
	return attached
}

// Pane returns one session's pane, or nil for an agent this window does not show.
func (c *Client) Pane(agent protocol.AgentID) *Pane { return c.panes.Pane(agent) }

func (c *Client) TakeInput(ctx context.Context, agent protocol.AgentID) error {
	p := c.Pane(agent)
	if p == nil {
		return &wire.Error{Code: wire.CodeNoSuchSession}
	}
	return p.TakeInput(ctx)
}

func (c *Client) OperationError(agent protocol.AgentID) error {
	p := c.Pane(agent)
	if p == nil {
		return &wire.Error{Code: wire.CodeNoSuchSession}
	}
	return p.OperationError()
}

// Events is the protocol event stream, for the model's deltas.
func (c *Client) Events() <-chan protocol.Event { return c.events }

// WindowEvents is the stream of ownership, ended, disconnect and error changes.
func (c *Client) WindowEvents() <-chan WindowEvent { return c.windowEvents }

// Err is why this window is unusable, or nil.
//
// It is authoritative: a caller that missed the disconnect event still sees the reason here.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// notify publishes one window event without ever blocking the caller.
//
// A full queue fails the window: a client that cannot drain its own events has a problem the
// daemon must not inherit, and the read pump must never wait on a consumer.
func (c *Client) notify(event WindowEvent) {
	c.pubMu.RLock()
	if c.publicationsClosed {
		c.pubMu.RUnlock()
		return
	}
	overflow := false
	select {
	case c.windowEvents <- event:
	default:
		overflow = true
	}
	c.pubMu.RUnlock()
	if overflow {
		c.fail("the window event queue overflowed")
	}
}

// publishEvent publishes one protocol event without blocking.
func (c *Client) publishEvent(event protocol.Event) {
	c.pubMu.RLock()
	if c.publicationsClosed {
		c.pubMu.RUnlock()
		return
	}
	overflow := false
	select {
	case c.events <- event:
	default:
		overflow = true
	}
	c.pubMu.RUnlock()
	if overflow {
		c.fail("the event queue overflowed")
	}
}

// Close closes the connection and every channel. It is idempotent and never stops a session or
// the daemon.
//
// UNTESTED.
func (c *Client) Close() error {
	c.fail("the window closed")
	<-c.channelsClosed
	return nil
}

// fail ends the connection once, with a reason a user can read.
//
// It fails every pending request, so nothing waits for an answer that will not come, and the
// channels are closed by the closer goroutine once both pumps have stopped.
func (c *Client) fail(reason string) {
	c.closeOnce.Do(func() {
		c.errMu.Lock()
		if c.err == nil {
			c.err = errors.New("windowclient: " + reason)
		}
		reason = c.err.Error()
		c.errMu.Unlock()
		c.closed.Store(true)

		close(c.done)
		_ = c.conn.Close()
		c.failPending(errors.New(reason))

		// The disconnect is discoverable through Err above even if this cannot enqueue.
		c.pubMu.RLock()
		if !c.publicationsClosed {
			select {
			case c.windowEvents <- WindowEvent{Kind: KindDisconnected, Error: errors.New(reason)}:
			default:
			}
		}
		c.pubMu.RUnlock()
	})
}

// closeChannelsWhenDone closes both channels exactly once, after every publisher has stopped.
func (c *Client) closeChannelsWhenDone() {
	<-c.done
	c.wg.Wait()
	c.pubMu.Lock()
	c.publicationsClosed = true
	close(c.events)
	close(c.windowEvents)
	c.pubMu.Unlock()
	close(c.channelsClosed)
}

// readLoop is the only reader. It never waits on a request or a consumer.
func (c *Client) readLoop() {
	defer c.wg.Done()
	defer c.fail("the daemon connection closed")

	for {
		envelope, err := wire.Read(c.conn)
		if err != nil {
			if !c.closed.Load() {
				c.fail(fmt.Sprintf("reading from the daemon failed: %v", err))
			}
			return
		}
		c.dispatch(envelope)
		if c.closed.Load() {
			return
		}
	}
}

// writeLoop is the only writer.
func (c *Client) writeLoop() {
	defer c.wg.Done()

	for {
		select {
		case <-c.done:
			return
		case frame := <-c.out:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				c.fail("the connection could not be given a write deadline")
				return
			}
			if err := wire.Write(c.conn, frame.typ, frame.id, frame.body); err != nil {
				c.fail(fmt.Sprintf("writing to the daemon failed: %v", err))
				return
			}
		}
	}
}

// enqueue queues one frame for the writer.
func (c *Client) enqueue(frame outFrame) error {
	if err := c.Err(); err != nil {
		return err
	}
	select {
	case c.out <- frame:
		return nil
	default:
		c.fail("the write queue overflowed")
		return errors.New("windowclient: the write queue is full")
	}
}
