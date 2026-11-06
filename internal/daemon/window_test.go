package daemon

// Untested: under GR-3 nothing here has been run. The outbound state is driven directly; the
// writer is replaced by a recorder, so no socket is used.

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// recordingConn counts what a window wrote, without a socket.
type recordingConn struct {
	mu     sync.Mutex
	writes []wire.Envelope
	// failAfter makes every write from then on fail.
	failAfter int
	wrote     int
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wrote++
	if c.failAfter > 0 && c.wrote > c.failAfter {
		return 0, io.ErrClosedPipe
	}
	envelope, err := testDecodeEnvelope(p)
	if err != nil {
		return 0, err
	}
	c.writes = append(c.writes, envelope)
	return len(p), nil
}

func (c *recordingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *recordingConn) Close() error                     { return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *recordingConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

// testAddr is a net.Addr for the recorder.
type testAddr string

func (a testAddr) Network() string { return "unix" }
func (a testAddr) String() string  { return string(a) }

// TestWindowCoalescesPaneStates: only the latest complete state per session is kept.
func TestWindowCoalescesPaneStates(t *testing.T) {
	w := newTestWindow(t)

	for i := 1; i <= 5; i++ {
		w.setPane(wire.PaneState{Agent: "a1", Rev: uint64(i), Cols: 80 + i})
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.coalesced) != 1 {
		t.Fatalf("coalesced = %d states, want 1", len(w.coalesced))
	}
	state := w.coalesced["a1"]
	if state.Cols != 85 {
		t.Fatalf("kept Cols = %d, want the latest 85", state.Cols)
	}
}

// TestWindowReliableOverflowDropsTheWindow: a window that cannot keep up is closed, not blocked
// on.
func TestWindowReliableOverflowDropsTheWindow(t *testing.T) {
	w := newTestWindow(t)
	w.paused = false

	for i := 0; i < windowQueue+10; i++ {
		w.push(wire.TypeEvent, 0, protocol.Event{})
	}

	if !w.isClosed() {
		t.Fatal("an overflowing window was not closed")
	}
}

// TestWindowDeferredUntilAttached: reliable messages that arrive before the attached answer are
// held and sent after it.
func TestWindowDeferredUntilAttached(t *testing.T) {
	w := newTestWindow(t)

	w.push(wire.TypeEvent, 0, protocol.Event{})
	w.push(wire.TypePending, 0, wire.Pending{Count: 2})

	w.mu.Lock()
	deferred := len(w.deferred)
	w.mu.Unlock()
	if deferred != 2 {
		t.Fatalf("deferred = %d, want 2", deferred)
	}

	if !w.sendAttached(wire.Attached{WindowID: w.id}) {
		t.Fatal("sendAttached failed")
	}

	w.mu.Lock()
	paused := w.paused
	deferred = len(w.deferred)
	w.mu.Unlock()
	if paused || deferred != 0 {
		t.Fatalf("paused = %t, deferred = %d; want false and 0", paused, deferred)
	}

	// The queue holds attached first, then the deferred messages in order.
	first := <-w.queue
	if first.typ != wire.TypeAttached {
		t.Fatalf("first queued message = %q, want attached", first.typ)
	}
	second := <-w.queue
	if second.typ != wire.TypeEvent {
		t.Fatalf("second queued message = %q, want event", second.typ)
	}
}

// TestWindowWriterSendsReliableBeforePanes: an event queued behind a waiting frame still goes
// first.
func TestWindowWriterSendsReliableBeforePanes(t *testing.T) {
	conn := &recordingConn{}
	w := newWindow("w1", conn, nil, &logger{out: io.Discard})
	w.paused = false

	w.setPane(wire.PaneState{Agent: "a1", Rev: 1, Frame: pane.Frame{Content: "first"}})
	w.push(wire.TypeEvent, 0, protocol.Event{Resync: true})

	w.writeLoopWake()

	waitFor(t, "the two writes", func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.writes) >= 2
	})

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.writes[0].Type != wire.TypeEvent {
		t.Fatalf("first write = %q, want the reliable event first", conn.writes[0].Type)
	}
	if conn.writes[1].Type != wire.TypePane {
		t.Fatalf("second write = %q, want the coalesced frame", conn.writes[1].Type)
	}
}

// TestWindowWriteFailureClosesIt: a failed write closes the window and writes nothing else.
func TestWindowWriteFailureClosesIt(t *testing.T) {
	conn := &recordingConn{failAfter: 1}
	w := newWindow("w1", conn, nil, &logger{out: io.Discard})
	w.paused = false

	w.push(wire.TypeEvent, 0, protocol.Event{})
	w.push(wire.TypeEvent, 0, protocol.Event{})
	w.writeLoopWake()

	waitFor(t, "the window to close", w.isClosed)
	if !w.isClosed() {
		t.Fatal("a failed write did not close the window")
	}
}

// testDecodeEnvelope reads back what wire.Write produced, so a recorded write can be inspected.
func testDecodeEnvelope(p []byte) (wire.Envelope, error) {
	return wire.Read(bytes.NewReader(p))
}

// newTestWindow builds a window with no hub and a recorder for a connection.
func newTestWindow(t *testing.T) *window {
	t.Helper()
	return newWindow("w1", &recordingConn{}, nil, &logger{out: io.Discard})
}

// writeLoopWake does one pass of the writer's work without starting it: the tests here want to
// observe the writes, not race a goroutine.
func (w *window) writeLoopWake() {
	select {
	case message := <-w.queue:
		_ = w.write(message)
	default:
	}
	_ = w.writePanes()
}
