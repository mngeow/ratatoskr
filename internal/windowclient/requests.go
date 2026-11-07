package windowclient

// Requests, their answers, and the router methods the TUI calls.
//
// One writer owns the connection, so every request is queued in call order. Each request gets a
// unique id, is registered before it is written, and is answered strictly by id and expected
// type. A request whose outcome is unknown (a timed-out input) ends the connection, because
// resending it could type twice; a timed-out query only retires its id.
//
// UNTESTED: see doc.go.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// inputTimeout bounds one input operation's wait for its ack.
	inputTimeout = 3 * time.Second // UNTESTED guess
	// queryTimeout bounds one query; the daemon's own budget is 10 s.
	queryTimeout = 12 * time.Second // UNTESTED guess
	// queryLimit is the local concurrency cap, matching the daemon's.
	queryLimit = 8
)

// pendingCall is one request waiting for its answer.
type pendingCall struct {
	want  wire.Type
	reply chan callReply
}

// callReply is one answer.
type callReply struct {
	value any
	err   error
}

// errBusy is what a full pending table or a full query semaphore answers.
var errBusy = errors.New("windowclient: too many requests are already in flight")

// register adds one pending request, or reports that the table is full.
func (c *Client) register(id uint64, want wire.Type) (*pendingCall, error) {
	call := &pendingCall{want: want, reply: make(chan callReply, 1)}

	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.closed.Load() {
		return nil, c.Err()
	}
	if len(c.pending) >= maxPending {
		return nil, errBusy
	}
	c.pending[id] = call
	return call, nil
}

// drop removes one pending request, if it is still there.
func (c *Client) drop(id uint64) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	delete(c.pending, id)
}

// failPending fails everything waiting.
func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	calls := make([]*pendingCall, 0, len(c.pending))
	for id, call := range c.pending {
		calls = append(calls, call)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()

	for _, call := range calls {
		select {
		case call.reply <- callReply{err: err}:
		default:
		}
	}
}

// deliver hands one answer to its request.
//
// An id nobody waits for is a protocol violation unless that id was retired after a query
// timeout: a late answer to a query the caller gave up on is expected, and is dropped.
func (c *Client) deliver(id uint64, want wire.Type, value any, answerErr error) {
	c.pendingMu.Lock()
	call, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	retired := false
	if !ok {
		if _, wasRetired := c.retired[id]; wasRetired {
			retired = true
			delete(c.retired, id)
		}
	}
	c.pendingMu.Unlock()

	if !ok {
		if !retired {
			c.fail(fmt.Sprintf("the daemon answered an unknown request id %d", id))
		}
		return
	}
	if call.want != want {
		call.reply <- callReply{err: errors.New("windowclient: wrong response type")}
		c.fail(fmt.Sprintf("the daemon answered request %d with %q", id, want))
		return
	}

	select {
	case call.reply <- callReply{value: value, err: answerErr}:
	default:
	}
}

// retire remembers an id whose answer is no longer wanted.
func (c *Client) retire(id uint64) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	delete(c.pending, id)
	if len(c.retired) >= retiredLimit {
		for stale := range c.retired {
			delete(c.retired, stale)
			break
		}
	}
	c.retired[id] = struct{}{}
}

// request sends one request and waits for its answer.
//
// On a timeout the id is retired (queries) or the connection is failed (input), because an
// unknown input outcome must never be retried.
func (c *Client) request(ctx context.Context, typ wire.Type, body any, want wire.Type, timeout time.Duration) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.Err(); err != nil {
		return nil, err
	}

	id := c.nextID.Add(1)
	call, err := c.register(id, want)
	if err != nil {
		return nil, err
	}

	if err := c.enqueue(outFrame{typ: typ, id: id, body: body}); err != nil {
		c.drop(id)
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case reply := <-call.reply:
		return reply.value, reply.err
	case <-timer.C:
		if want == wire.TypeResult {
			// A query is safe to abandon: a late answer is ignored.
			c.retire(id)
			return nil, errors.New("windowclient: the query timed out")
		}
		// An input whose ack never came may or may not have been applied. Retrying could type
		// twice, so the window ends instead.
		c.fail("an input operation was not acknowledged")
		return nil, errors.New("windowclient: an input operation was not acknowledged")
	case <-c.done:
		return nil, c.Err()
	case <-ctx.Done():
		if want == wire.TypeResult {
			c.retire(id)
		} else {
			c.fail("an input operation was cancelled before acknowledgement")
		}
		return nil, ctx.Err()
	}
}

// input sends one input request and returns the daemon's written count.
func (c *Client) input(typ wire.Type, body any, fallbackWritten int) (int, error) {
	value, err := c.request(context.Background(), typ, body, wire.TypeAck, inputTimeout)
	if err != nil {
		return 0, err
	}

	ack, ok := value.(wire.Ack)
	if !ok {
		return 0, errors.New("windowclient: the daemon's answer was not an acknowledgement")
	}
	if !ack.OK {
		if ack.Error != nil {
			return ack.Written, inputRefused{
				code:    string(ack.Error.Code),
				message: ack.Error.Message,
				written: ack.Written,
			}
		}
		return ack.Written, errors.New("windowclient: the daemon refused the input")
	}
	if ack.Written > 0 {
		return ack.Written, nil
	}
	return fallbackWritten, nil
}

// resize sends one resize request.
func (c *Client) resize(agent protocol.AgentID, width, height int, request bool) error {
	_, err := c.input(wire.TypeResize, wire.Resize{Agent: agent, Width: width, Height: height, Request: request}, 0)
	return err
}

// takeInput asks for ownership and returns the authoritative answer.
func (c *Client) takeInput(ctx context.Context, agent protocol.AgentID) (wire.Ownership, error) {
	value, err := c.request(ctx, wire.TypeTakeInput, wire.SessionRef{Agent: agent}, wire.TypeAck, inputTimeout)
	if err != nil {
		return wire.Ownership{}, err
	}

	ack, ok := value.(wire.Ack)
	if !ok {
		return wire.Ownership{}, errors.New("windowclient: the daemon's answer was not an acknowledgement")
	}
	if !ack.OK {
		if ack.Error != nil {
			return wire.Ownership{}, inputRefused{code: string(ack.Error.Code), message: ack.Error.Message}
		}
		return wire.Ownership{}, errors.New("windowclient: the daemon refused the ownership request")
	}
	if ack.Ownership == nil {
		return wire.Ownership{}, errors.New("windowclient: the daemon granted ownership without saying so")
	}
	if ack.Ownership.Agent != agent || !ack.Ownership.YouHold {
		c.fail("the daemon returned invalid ownership")
		return wire.Ownership{}, c.Err()
	}
	return *ack.Ownership, nil
}

// dispatch handles one message from the daemon.
func (c *Client) dispatch(envelope wire.Envelope) {
	switch envelope.Type {
	case wire.TypePane:
		var state wire.PaneState
		if err := wire.Decode(envelope, &state); err != nil {
			c.fail("the daemon sent a pane state this build could not read")
			return
		}
		c.panes.apply(state)

	case wire.TypeEvent:
		var event protocol.Event
		if err := wire.Decode(envelope, &event); err != nil {
			c.fail("the daemon sent an event this build could not read")
			return
		}
		c.handleEvent(event)

	case wire.TypeOwnership:
		var ownership wire.Ownership
		if err := wire.Decode(envelope, &ownership); err != nil {
			c.fail("the daemon sent an ownership change this build could not read")
			return
		}
		c.panes.applyOwnership(ownership)
		c.notify(WindowEvent{Kind: KindOwnership, Agent: ownership.Agent, Ownership: ownership})

	case wire.TypeSessionEnded:
		var ended wire.SessionEnded
		if err := wire.Decode(envelope, &ended); err != nil {
			c.fail("the daemon sent a session end this build could not read")
			return
		}
		if pane := c.panes.Pane(ended.Agent); pane != nil {
			pane.markEnded(ended.Rev, ended.ExitCode)
		}

	case wire.TypePending:
		var pending wire.Pending
		if err := wire.Decode(envelope, &pending); err != nil {
			c.fail("the daemon sent a pending count this build could not read")
			return
		}
		c.state.setPending(pending.Count)

	case wire.TypeAck:
		var ack wire.Ack
		if err := wire.Decode(envelope, &ack); err != nil {
			c.fail("the daemon sent an acknowledgement this build could not read")
			return
		}
		c.deliver(envelope.ID, wire.TypeAck, ack, nil)

	case wire.TypeResult:
		var result wire.Result
		if err := wire.Decode(envelope, &result); err != nil {
			c.fail("the daemon sent a result this build could not read")
			return
		}
		c.deliver(envelope.ID, wire.TypeResult, result, nil)

	case wire.TypeBye:
		var bye wire.Bye
		_ = wire.Decode(envelope, &bye)
		reason := bye.Message
		if reason == "" {
			reason = string(bye.Code)
		}
		c.fail("the daemon is stopping: " + reason)

	default:
		// A message this build does not know cannot be interpreted, and guessing would be worse.
		c.fail(fmt.Sprintf("the daemon sent %q, which this build does not know", envelope.Type))
	}
}

// handleEvent applies one protocol event, and schedules the snapshot a Resync asks for.
//
// A Resync's own fields are applied first and then buffered with the deltas that follow, so the
// snapshot it returns is brought up to date before it is published.
func (c *Client) handleEvent(event protocol.Event) {
	if !event.Resync {
		c.resyncMu.Lock()
		buffering := c.resyncInFlight
		if buffering {
			if len(c.resyncBuffer) >= eventQueue {
				c.resyncMu.Unlock()
				c.fail("resync event buffer overflowed")
				return
			}
			c.resyncBuffer = append(c.resyncBuffer, event)
		}

		if buffering {
			c.resyncMu.Unlock()
			return
		}
		c.state.merge(event)
		c.publishEvent(event)
		c.resyncMu.Unlock()
		return
	}

	c.resyncMu.Lock()
	if c.resyncInFlight {
		// A second Resync while one is running is just another delta.
		if len(c.resyncBuffer) >= eventQueue {
			c.resyncMu.Unlock()
			c.fail("resync event buffer overflowed")
			return
		}
		c.resyncBuffer = append(c.resyncBuffer, event)
		c.resyncMu.Unlock()
		return
	}
	c.resyncInFlight = true
	c.resyncBuffer = append(c.resyncBuffer[:0], event)
	c.resyncMu.Unlock()

	// Applied now so the buffer's first entry is part of the state being refreshed, and delivered
	// only after the snapshot that absorbs it.
	c.state.merge(event)

	c.wg.Add(1)
	go c.resync()
}

// resync asks for a snapshot, applies the deltas that arrived meanwhile, and publishes the Resync.
func (c *Client) resync() {
	defer c.wg.Done()
	result, err := c.query(wire.Query{Kind: wire.QuerySnapshot})

	c.resyncMu.Lock()
	defer c.resyncMu.Unlock()
	buffered := c.resyncBuffer
	c.resyncBuffer = nil
	c.resyncInFlight = false

	if err != nil {
		// A snapshot that could not be fetched must not be published as authoritative, and the
		// deltas that arrived meanwhile cannot be applied to a state nobody has.
		c.fail("a resync snapshot could not be fetched")
		return
	}
	if result.Snapshot == nil {
		c.fail("the daemon answered a resync without a snapshot")
		return
	}

	c.state.install(*result.Snapshot)
	for _, event := range buffered {
		c.state.merge(event)
	}

	// All deltas are already in the cache; one Resync makes consumers read that
	// final picture rather than applying an old delta after the fresh snapshot.
	c.publishEvent(protocol.Event{Resync: true})
}

// query sends one read and returns its result.
func (c *Client) query(q wire.Query) (wire.Result, error) {
	return c.queryContext(context.Background(), q)
}

// queryContext is query with the caller's context, which a network wait needs: the daemon may hold
// the answer for its own budget, and the UI must be able to give up without ending the connection.
//
// A cancelled query retires its id, so a late answer is ignored rather than treated as a protocol
// violation.
//
// UNTESTED.
func (c *Client) queryContext(ctx context.Context, q wire.Query) (wire.Result, error) {
	if err := ctx.Err(); err != nil {
		return wire.Result{}, err
	}
	if err := c.Err(); err != nil {
		return wire.Result{}, err
	}

	select {
	case c.querySem <- struct{}{}:
		defer func() { <-c.querySem }()
	default:
		return wire.Result{}, errBusy
	}

	value, err := c.request(ctx, wire.TypeQuery, q, wire.TypeResult, queryTimeout)
	if err != nil {
		return wire.Result{}, err
	}
	result, ok := value.(wire.Result)
	if !ok {
		return wire.Result{}, errors.New("windowclient: the daemon's answer was not a result")
	}
	if result.Error != nil {
		return result, &wireError{code: string(result.Error.Code), message: result.Error.Message}
	}
	return result, nil
}

// routerSurface is tui.Router plus the optional outbound reader, copied here so the method set is
// checked without importing the tui package.
type routerSurface interface {
	UserDraft(agent protocol.AgentID, drafting bool)
	UserSubmitted(agent protocol.AgentID)
	DeliverHeld(agent protocol.AgentID)
	Snapshot() protocol.Snapshot
	History(q protocol.HistoryQuery) ([]protocol.MessageView, error)
	Runs(limit int) ([]protocol.RunView, error)
	MessageDetail(id string) (protocol.MessageDetail, error)
	PendingOnQuit() int

	Sends(limit int) ([]protocol.SendView, error)
	SendDetail(id string) (protocol.SendDetail, error)
}

// Snapshot is the cached state. It never touches the socket.
func (c *Client) Snapshot() protocol.Snapshot { return c.state.Snap() }

var _ routerSurface = (*Client)(nil)

// PendingOnQuit is the cached count of messages that would be lost on quit.
func (c *Client) PendingOnQuit() int { return c.state.Pending() }

// History reads the message history.
func (c *Client) History(q protocol.HistoryQuery) ([]protocol.MessageView, error) {
	result, err := c.query(wire.Query{Kind: wire.QueryHistory, History: &q})
	if err != nil {
		return nil, err
	}
	return result.History, nil
}

// Runs reads the run log.
func (c *Client) Runs(limit int) ([]protocol.RunView, error) {
	result, err := c.query(wire.Query{Kind: wire.QueryRuns, Limit: limit})
	if err != nil {
		return nil, err
	}
	return result.Runs, nil
}

// MessageDetail reads one message in full.
func (c *Client) MessageDetail(id string) (protocol.MessageDetail, error) {
	result, err := c.query(wire.Query{Kind: wire.QueryMessageDetail, ID: id})
	if err != nil {
		return protocol.MessageDetail{}, err
	}
	if result.MessageDetail == nil {
		return protocol.MessageDetail{}, errors.New("windowclient: the daemon sent no message detail")
	}
	return *result.MessageDetail, nil
}

// Sends reads the outbound list, which is the optional outboundReader seam.
func (c *Client) Sends(limit int) ([]protocol.SendView, error) {
	result, err := c.query(wire.Query{Kind: wire.QuerySends, Limit: limit})
	if err != nil {
		return nil, err
	}
	return result.Sends, nil
}

// SendDetail reads one outbound record in full.
func (c *Client) SendDetail(id string) (protocol.SendDetail, error) {
	result, err := c.query(wire.Query{Kind: wire.QuerySendDetail, ID: id})
	if err != nil {
		return protocol.SendDetail{}, err
	}
	if result.SendDetail == nil {
		return protocol.SendDetail{}, errors.New("windowclient: the daemon sent no send detail")
	}
	return *result.SendDetail, nil
}

// UserDraft sets the draft flag for one session, waiting for its bounded ack.
//
// It runs from a TUI command, so the wait is bounded; a failure latches, and later input does not
// proceed past the latch.
func (c *Client) UserDraft(agent protocol.AgentID, drafting bool) {
	pane := c.panes.Pane(agent)
	if pane == nil {
		c.notify(WindowEvent{Kind: KindOperationError, Agent: agent, Error: errors.New("windowclient: no such session")})
		return
	}
	if err := c.applyVoid(pane, wire.TypeDraft, wire.Draft{Agent: agent, Drafting: drafting}); err != nil {
		pane.latch(err, true)
	}
}

// UserSubmitted reports that the user submitted input.
func (c *Client) UserSubmitted(agent protocol.AgentID) {
	pane := c.panes.Pane(agent)
	if pane == nil {
		c.notify(WindowEvent{Kind: KindOperationError, Agent: agent, Error: errors.New("windowclient: no such session")})
		return
	}
	if err := c.applyVoid(pane, wire.TypeSubmitted, wire.SessionRef{Agent: agent}); err != nil {
		pane.latch(err, true)
	}
}

// DeliverHeld asks for the held messages to be delivered.
func (c *Client) DeliverHeld(agent protocol.AgentID) {
	pane := c.panes.Pane(agent)
	if pane == nil {
		c.notify(WindowEvent{Kind: KindOperationError, Agent: agent, Error: errors.New("windowclient: no such session")})
		return
	}
	if err := c.applyVoid(pane, wire.TypeDeliverHeld, wire.SessionRef{Agent: agent}); err != nil {
		pane.latch(err, true)
	}
}

// applyVoid sends one router mutation and waits for its ack.
//
// The latch is checked first: a latched failure must not be bypassed by a mutation that would
// otherwise look like it worked.
func (c *Client) applyVoid(pane *Pane, typ wire.Type, body any) error {
	if err := pane.checkInputable(); err != nil {
		return err
	}
	_, err := c.input(typ, body, 0)
	return err
}

// wireError is a refusal from the daemon with its code kept.
type wireError struct {
	code    string
	message string
}

// Error renders the refusal.
func (e *wireError) Error() string {
	if e.message == "" {
		return "windowclient: the daemon refused the request (" + e.code + ")"
	}
	return "windowclient: " + e.message
}

// Code is the wire code the daemon used.
func (e *wireError) Code() string { return e.code }

// inputRefused is a refused input operation.
type inputRefused struct {
	code    string
	message string
	written int
}

// Error renders the refusal.
func (e inputRefused) Error() string {
	if e.message == "" {
		return "windowclient: the daemon refused the input (" + e.code + ")"
	}
	return "windowclient: " + e.message
}

// Code is the wire code the daemon used.
func (e inputRefused) Code() string { return e.code }

// BytesWritten reports whether some of the input reached the child. The TUI shows that
// distinction, because a partial paste is not the same as a refused one.
func (e inputRefused) BytesWritten() bool { return e.written > 0 }

// ByteCount is how many bytes reached the child.
func (e inputRefused) ByteCount() int { return e.written }

// operationError is a latched failure for one session.
type operationError struct {
	cause       error
	recoverable bool
}

// Error renders the latched cause.
func (e *operationError) Error() string { return e.cause.Error() }

// Unwrap exposes the cause.
func (e *operationError) Unwrap() error { return e.cause }

// endedError is the local refusal for an ended session.
func endedError() error { return errors.New("windowclient: that session has ended") }

// notHolderError is the local refusal for a viewer.
func notHolderError() error {
	return errors.New("windowclient: another window holds input for that session")
}
