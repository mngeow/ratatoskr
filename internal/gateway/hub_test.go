package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// testStream builds a stream whose cancellation a test can observe.
func testStream(host string, queue int) (*stream, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	return &stream{
		host:   host,
		frames: make(chan protocol.StreamFrame, queue),
		cancel: cancel,
		done:   make(chan struct{}),
	}, ctx
}

// waitCancelled reports whether a stream's context ended within a second.
func waitCancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(time.Second):
		return false
	}
}

func TestHubAttachDetachIdentity(t *testing.T) {
	h := newHub()

	if h.open("laptop") {
		t.Fatal("an empty hub reports an open stream")
	}

	first, _ := testStream("laptop", 1)
	if previous := h.attach("laptop", first); previous != nil {
		t.Fatal("the first attach replaced something")
	}
	if !h.open("laptop") {
		t.Fatal("an attached stream is not open")
	}

	second, _ := testStream("laptop", 1)
	if previous := h.attach("laptop", second); previous != first {
		t.Fatal("attach did not report the stream it replaced")
	}

	// The old handler finishing must not evict the new stream.
	if h.detach("laptop", first) {
		t.Fatal("detach removed a stream that was no longer current")
	}
	if !h.open("laptop") {
		t.Fatal("detaching the old stream removed the new one")
	}

	if !h.detach("laptop", second) {
		t.Fatal("detach did not report removing the current stream")
	}
	if h.open("laptop") {
		t.Fatal("the stream is still open after detach")
	}
	if h.detach("laptop", second) {
		t.Fatal("a second detach reported a removal")
	}
}

func TestHubHosts(t *testing.T) {
	h := newHub()

	if len(h.hosts()) != 0 {
		t.Fatal("an empty hub lists hosts")
	}

	laptop, _ := testStream("laptop", 1)
	desktop, _ := testStream("desktop", 1)
	h.attach("laptop", laptop)
	h.attach("desktop", desktop)

	hosts := h.hosts()
	if len(hosts) != 2 {
		t.Fatalf("hosts = %v, want two", hosts)
	}
	seen := map[string]bool{}
	for _, host := range hosts {
		seen[host] = true
	}
	if !seen["laptop"] || !seen["desktop"] {
		t.Fatalf("hosts = %v, want laptop and desktop", hosts)
	}
}

func TestHubEnqueue(t *testing.T) {
	h := newHub()

	if err := h.enqueue("laptop", protocol.StreamFrame{}); !errors.Is(err, errNoStream) {
		t.Fatalf("enqueue with no stream = %v, want errNoStream", err)
	}

	st, _ := testStream("laptop", 1)
	h.attach("laptop", st)

	if err := h.enqueue("laptop", protocol.StreamFrame{Type: protocol.FramePing}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// The queue is one deep and nobody is reading it.
	if err := h.enqueue("laptop", protocol.StreamFrame{Type: protocol.FramePing}); !errors.Is(err, errQueueFull) {
		t.Fatalf("enqueue past capacity = %v, want errQueueFull", err)
	}

	select {
	case frame := <-st.frames:
		if frame.Type != protocol.FramePing {
			t.Fatalf("queued frame = %+v", frame)
		}
	default:
		t.Fatal("the first frame was not queued")
	}
}

func TestHubCancelAndCloseAll(t *testing.T) {
	h := newHub()

	laptop, laptopCtx := testStream("laptop", 1)
	desktop, desktopCtx := testStream("desktop", 1)
	h.attach("laptop", laptop)
	h.attach("desktop", desktop)

	h.cancel("laptop")
	if !waitCancelled(laptopCtx) {
		t.Fatal("cancel did not end the stream's context")
	}

	// closeAll cancels the rest and leaves the map for the handlers to clean.
	h.closeAll()
	if !waitCancelled(desktopCtx) {
		t.Fatal("closeAll did not end the stream's context")
	}
	if len(h.hosts()) != 2 {
		t.Fatal("closeAll removed entries itself; handlers own detach")
	}

	// Cancelling a host that has no stream is harmless.
	h.cancel("nobody")
}
