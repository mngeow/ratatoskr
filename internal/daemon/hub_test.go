package daemon

// Untested: under GR-3 nothing here has been run. The hub is driven with fake windows, so no
// socket is bound and no frame is read.

import (
	"io"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// TestHubFanOutReachesEveryWindow: one event goes to every registered window.
func TestHubFanOutReachesEveryWindow(t *testing.T) {
	h := newHub(&logger{out: io.Discard}, newGate())

	first := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	second := newWindow("w2", &recordingConn{}, h, &logger{out: io.Discard})
	first.paused, second.paused = false, false
	h.register(first)
	h.register(second)

	h.fanOut(protocol.Event{Resync: true})

	for _, w := range []*window{first, second} {
		if len(w.queue) != 1 {
			t.Fatalf("%s: queue = %d, want 1", w.id, len(w.queue))
		}
		message := <-w.queue
		if message.typ != wire.TypeEvent {
			t.Fatalf("%s: message = %q, want event", w.id, message.typ)
		}
	}
}

// TestHubDropsAFullWindowAndKeepsTheOthers: a window that cannot keep up is dropped, and the
// rest still get the message.
func TestHubDropsAFullWindowAndKeepsTheOthers(t *testing.T) {
	h := newHub(&logger{out: io.Discard}, newGate())

	full := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	full.paused = false
	h.register(full)
	// Fill the reliable queue without a writer consuming it.
	for i := 0; i < windowQueue; i++ {
		full.push(wire.TypeEvent, 0, protocol.Event{})
	}

	healthy := newWindow("w2", &recordingConn{}, h, &logger{out: io.Discard})
	healthy.paused = false
	h.register(healthy)

	h.fanOut(protocol.Event{Resync: true})

	if !full.isClosed() {
		t.Fatal("the full window was not dropped")
	}
	if len(healthy.queue) != 1 {
		t.Fatalf("the healthy window got %d messages, want 1", len(healthy.queue))
	}
}

// TestHubCountAndUnregister: the count tracks the registry, and an unregistered window releases
// what it held.
func TestHubCountAndUnregister(t *testing.T) {
	gate := newGate()
	gate.addSession("a1", "claude")
	h := newHub(&logger{out: io.Discard}, gate)

	w := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	h.register(w)
	if h.count() != 1 {
		t.Fatalf("count = %d, want 1", h.count())
	}

	if _, err := gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	h.unregister(w)
	if h.count() != 0 {
		t.Fatalf("count = %d, want 0", h.count())
	}
	if holder := gate.holder("a1"); holder != "" {
		t.Fatalf("the session is still held by %q", holder)
	}
}

// TestHubOwnershipIsPerWindow: the same broadcast means you_hold to one window only.
func TestHubOwnershipIsPerWindow(t *testing.T) {
	h := newHub(&logger{out: io.Discard}, newGate())
	first := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	second := newWindow("w2", &recordingConn{}, h, &logger{out: io.Discard})
	first.paused, second.paused = false, false
	h.register(first)
	h.register(second)

	h.announceOwnership("a1", wire.Ownership{Agent: "a1", Rev: 2, Holder: "w1"})

	firstMessage := <-first.queue
	secondMessage := <-second.queue
	firstOwnership := firstMessage.body.(wire.Ownership)
	secondOwnership := secondMessage.body.(wire.Ownership)
	if !firstOwnership.YouHold || secondOwnership.YouHold {
		t.Fatalf("you_hold: first %t, second %t", firstOwnership.YouHold, secondOwnership.YouHold)
	}
}

// TestHubPaneStateIsPerWindow: the coalesced state carries you_hold per window and the holder
// from the gate.
func TestHubPaneStateIsPerWindow(t *testing.T) {
	gate := newGate()
	gate.addSession("a1", "claude")
	h := newHub(&logger{out: io.Discard}, gate)

	holder := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	viewer := newWindow("w2", &recordingConn{}, h, &logger{out: io.Discard})
	h.register(holder)
	h.register(viewer)

	if _, err := gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	h.publishPane(wire.PaneState{Agent: "a1", Cols: 80, Rows: 24})

	holderState := holder.coalesced["a1"]
	viewerState := viewer.coalesced["a1"]
	if !holderState.YouHold || viewerState.YouHold {
		t.Fatalf("you_hold: holder %t, viewer %t", holderState.YouHold, viewerState.YouHold)
	}
	if holderState.Holder != "w1" || viewerState.Holder != "w1" {
		t.Fatalf("holder = %q / %q", holderState.Holder, viewerState.Holder)
	}
	if holderState.Rev == 0 {
		t.Fatal("the state carries no revision")
	}
}

// TestHubCloseAllSaysByeAndReleases: every window gets a bye and is closed, and no hold
// survives.
func TestHubCloseAllSaysByeAndReleases(t *testing.T) {
	gate := newGate()
	gate.addSession("a1", "claude")
	h := newHub(&logger{out: io.Discard}, gate)

	w := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	w.paused = false
	h.register(w)
	if _, err := gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	h.closeAll("the daemon is stopping")

	if !w.isClosed() {
		t.Fatal("the window was not closed")
	}
	if holder := gate.holder("a1"); holder != "" {
		t.Fatalf("a hold survived the shutdown: %q", holder)
	}

	// The bye was queued before the close.
	sawBye := false
	for len(w.queue) > 0 {
		message := <-w.queue
		if message.typ == wire.TypeBye {
			sawBye = true
			if bye := message.body.(wire.Bye); bye.Code != wire.CodeShuttingDown {
				t.Fatalf("bye code = %q", bye.Code)
			}
		}
	}
	if !sawBye {
		t.Fatal("no bye was sent")
	}
	if h.count() != 0 {
		t.Fatalf("the registry still holds %d windows", h.count())
	}
}

// TestHubPendingOnlyWhenChanged: the count is pushed once per change, and nothing is read while
// no window is attached.
func TestHubPendingOnlyWhenChanged(t *testing.T) {
	h := newHub(&logger{out: io.Discard}, newGate())

	reads := 0
	value := 3
	h.pending = func() int {
		reads++
		return value
	}

	h.publishPending()
	if reads != 0 {
		t.Fatalf("%d reads with no windows, want 0", reads)
	}

	w := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	w.paused = false
	h.register(w)

	h.publishPending()
	if reads != 1 {
		t.Fatalf("reads = %d, want 1", reads)
	}
	if len(w.queue) != 1 {
		t.Fatalf("queue = %d, want the first count", len(w.queue))
	}
	<-w.queue

	// The same value is not pushed again.
	h.publishPending()
	if len(w.queue) != 0 {
		t.Fatal("an unchanged count was pushed again")
	}

	value = 4
	h.publishPending()
	if len(w.queue) != 1 {
		t.Fatalf("queue = %d, want the changed count", len(w.queue))
	}
}
