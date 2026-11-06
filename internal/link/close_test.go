package link

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// No sockets: the stream and the unregister request are served by a fake transport.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// closeFake serves the stream and records the DELETE /v1/sessions requests.
type closeFake struct {
	streamFake

	mu         sync.Mutex
	deletes    int
	deleteFail bool
}

func (f *closeFake) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodDelete && request.URL.Path == "/v1/sessions" {
		f.mu.Lock()
		f.deletes++
		fail := f.deleteFail
		f.mu.Unlock()

		if fail {
			return nil, errors.New("dial failed")
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	}
	return f.streamFake.RoundTrip(request)
}

func (f *closeFake) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

// TestRunStopsOnContextAndCallsNothingAfter: after Run returns, the receiver is
// sealed, so any later call panics.
func TestRunStopsOnContextAndCallsNothingAfter(t *testing.T) {
	transport := &streamFake{mode: "stream"}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	ctx, cancel := context.WithCancel(context.Background())
	recorder.setCancel(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx, recv)
	}()

	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })
	transport.send(t, directoryFrame("claude@alpha"))
	waitFor(t, "a connected state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnConnected
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	// Nothing may call the receiver afterwards.
	recv.seal()
	time.Sleep(50 * time.Millisecond)
}

// TestCloseCancelsRunAndUnregistersOnce: one DELETE, and a second Close does
// nothing.
func TestCloseCancelsRunAndUnregistersOnce(t *testing.T) {
	transport := &closeFake{streamFake: streamFake{mode: "stream"}}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })

	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	stop()

	if got := transport.deleteCount(); got != 1 {
		t.Fatalf("%d DELETEs, want 1", got)
	}

	// A second Close does nothing and returns nil.
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := transport.deleteCount(); got != 1 {
		t.Fatalf("%d DELETEs after the second Close, want 1", got)
	}
}

// TestCloseWithoutRunOnlyUnregisters: no stream was ever started.
func TestCloseWithoutRunOnlyUnregisters(t *testing.T) {
	transport := &closeFake{streamFake: streamFake{mode: "stream"}}
	client, _ := runClient(t, transport)

	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := transport.deleteCount(); got != 1 {
		t.Fatalf("%d DELETEs, want 1", got)
	}
}

// TestCloseReturnsTheUnregisterError.
func TestCloseReturnsTheUnregisterError(t *testing.T) {
	transport := &closeFake{streamFake: streamFake{mode: "stream"}, deleteFail: true}
	client, _ := runClient(t, transport)

	if err := client.Close(context.Background()); err == nil {
		t.Fatal("Close hid a failed unregister")
	}
}

// TestSecondRunReturnsAtOnceAndMakesNoRequest.
func TestSecondRunReturnsAtOnceAndMakesNoRequest(t *testing.T) {
	transport := &streamFake{mode: "stream"}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })
	before := transport.requestCount()

	// A second Run returns immediately without opening a stream.
	second := &fakeReceiver{}
	client.Run(context.Background(), second)
	if got := transport.requestCount(); got != before {
		t.Fatalf("%d requests after a second Run, want %d", got, before)
	}
	if got := second.callCount(); got != 0 {
		t.Fatalf("the second run reported %d states", got)
	}
}

// TestNilReceiverReturnsAtOnce.
func TestNilReceiverReturnsAtOnce(t *testing.T) {
	transport := &streamFake{mode: "stream"}
	client, _ := runClient(t, transport)

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(context.Background(), nil)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run with a nil Receiver did not return")
	}
	if got := transport.requestCount(); got != 0 {
		t.Fatalf("%d requests, want 0", got)
	}
}

// TestHeartbeatRefusalEndsTheConnection: a 401 from a heartbeat is an unpaired host.
func TestHeartbeatRefusalEndsTheConnection(t *testing.T) {
	transport := &refusingHeartbeat{streamFake: streamFake{mode: "stream"}}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })
	transport.send(t, directoryFrame("claude@alpha"))
	waitFor(t, "a connected state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnConnected
	})

	waitFor(t, "a refused state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnRefused
	})
	view, _ := recv.lastState()
	if !strings.Contains(view.Err, "refused this host") {
		t.Fatalf("reason = %q, want the refusal text", view.Err)
	}
}

// refusingHeartbeat answers heartbeats with 401 and the stream normally.
type refusingHeartbeat struct {
	streamFake
}

func (f *refusingHeartbeat) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == protocol.HeartbeatPath {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"code":"unauthorized","message":"the credential was not accepted"}`)),
			Request: request,
		}, nil
	}
	return f.streamFake.RoundTrip(request)
}

// TestHeartbeatTransportFailureChangesNothing: a failed heartbeat is not a dead
// connection; the watchdog is the only judge.
func TestHeartbeatTransportFailureChangesNothing(t *testing.T) {
	transport := &brokenHeartbeat{streamFake: streamFake{mode: "stream"}}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })
	transport.send(t, directoryFrame("claude@alpha"))
	waitFor(t, "a connected state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnConnected
	})

	// Keep the stream alive with pings while the heartbeats fail.
	for i := 0; i < 6; i++ {
		transport.send(t, protocol.StreamFrame{Type: protocol.FramePing})
		time.Sleep(15 * time.Millisecond)
	}

	view, _ := recv.lastState()
	if view.State != protocol.ConnConnected {
		t.Fatalf("state = %q, want connected: a failed heartbeat must not drop the stream", view.State)
	}
	if count := recv.linkUpCount(); count != 1 {
		t.Fatalf("%d LinkUp calls, want 1", count)
	}
}

// brokenHeartbeat fails every heartbeat at the transport.
type brokenHeartbeat struct {
	streamFake
}

func (f *brokenHeartbeat) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == protocol.HeartbeatPath {
		return nil, errors.New("dial failed")
	}
	return f.streamFake.RoundTrip(request)
}
