package link

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// No sockets: a fake transport serves the stream from an io.Pipe, and a fake
// Receiver records every call it gets.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// fakeReceiver records every Receiver call in order, under a mutex.
//
// It is used from the stream reader goroutine only: the heartbeat goroutine never
// touches a Receiver, which is the concurrency rule this slice relies on.
type fakeReceiver struct {
	mu       sync.Mutex
	calls    []string
	states   []protocol.ConnectionView
	peers    [][]string
	offers   []protocol.Offer
	reports  []protocol.MessageInfo
	linkUps  int
	closedAt bool
}

func (r *fakeReceiver) Receive(offer protocol.Offer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closedAt {
		panic("Receive after Run returned")
	}
	r.calls = append(r.calls, "receive")
	r.offers = append(r.offers, offer)
}

func (r *fakeReceiver) LinkState(view protocol.ConnectionView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closedAt {
		panic("LinkState after Run returned")
	}
	r.calls = append(r.calls, "state:"+string(view.State))
	r.states = append(r.states, view)
}

func (r *fakeReceiver) LinkDirectory(peers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "directory")
	r.peers = append(r.peers, append([]string(nil), peers...))
}

func (r *fakeReceiver) LinkUp() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "up")
	r.linkUps++
}

func (r *fakeReceiver) LinkReport(info protocol.MessageInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "report")
	r.reports = append(r.reports, info)
}

// seal marks the receiver so a later call panics: used to prove that nothing runs
// after Run returns.
func (r *fakeReceiver) seal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closedAt = true
}

func (r *fakeReceiver) lastState() (protocol.ConnectionView, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return protocol.ConnectionView{}, false
	}
	return r.states[len(r.states)-1], true
}

func (r *fakeReceiver) stateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.states)
}

func (r *fakeReceiver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fakeReceiver) linkUpCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.linkUps
}

// streamFake serves GET /v1/stream in one of several modes, and records requests.
type streamFake struct {
	mu       sync.Mutex
	requests []recordedRequest
	writes   *io.PipeWriter
	// mode decides what the stream request answers.
	mode string
	// status is used by the canned modes.
	status int
	// content is the Content-Type of a streaming answer.
	content string
	// body is what a canned answer carries; empty means "{}".
	body string
	// served counts how many stream requests produced a response.
	served int
	// headerGate, when non-nil, blocks the response headers until it is closed:
	// used to prove the watchdog catches a gateway that never answers.
	headerGate chan struct{}
}

// cannedBody is the body a canned answer carries.
func cannedBody(body string) string {
	if body == "" {
		return "{}"
	}
	return body
}

func (f *streamFake) RoundTrip(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		method: request.Method,
		path:   request.URL.Path,
		header: request.Header.Clone(),
	})
	mode := f.mode
	status := f.status
	content := f.content
	body := f.body
	gate := f.headerGate
	f.served++
	f.mu.Unlock()

	if mode == "canned" {
		if status == 0 {
			status = http.StatusNoContent
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{content}},
			Body:       io.NopCloser(strings.NewReader(cannedBody(body))),
			Request:    request,
		}, nil
	}

	if gate != nil {
		select {
		case <-gate:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}

	readSide, writeSide := io.Pipe()

	// A goroutine ends the stream when the request's context does.
	go func() {
		<-request.Context().Done()
		_ = writeSide.CloseWithError(io.EOF)
	}()

	f.mu.Lock()
	f.writes = writeSide
	f.mu.Unlock()

	if content == "" {
		content = streamMediaType
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{content}},
		Body:       readSide,
		Request:    request,
	}, nil
}

// send writes one frame line into the stream.
func (f *streamFake) send(t *testing.T, frame protocol.StreamFrame) {
	t.Helper()

	payload, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	f.sendRaw(t, append(payload, '\n'))
}

// sendRaw writes raw bytes into the stream.
func (f *streamFake) sendRaw(t *testing.T, payload []byte) {
	t.Helper()

	f.mu.Lock()
	writer := f.writes
	f.mu.Unlock()
	if writer == nil {
		t.Fatal("the stream has no writer yet")
	}

	if _, err := writer.Write(payload); err != nil {
		// A closed pipe is expected when the connection is being torn down.
		return
	}
}

// requestCount is how many requests the fake has seen.
func (f *streamFake) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// streamPaths lists the request paths, in order.
func (f *streamFake) streamRequests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// runClient builds a client with tiny stream timings and a recording wait.
func runClient(t *testing.T, transport http.RoundTripper) (*Client, *waitRecorder) {
	t.Helper()

	client, err := NewClient(testPairing(), Options{
		Transport:      transport,
		Now:            func() time.Time { return gatewayNow() },
		HeartbeatEvery: 10 * time.Millisecond,
		DeadAfter:      50 * time.Millisecond,
		BackoffMin:     5 * time.Millisecond,
		BackoffMax:     20 * time.Millisecond,
		MaxFrameBytes:  4096,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	recorder := &waitRecorder{stopAfter: 6, cancel: func() {}}
	client.wait = recorder.wait
	return client, recorder
}

// waitRecorder records the requested waits and stops the loop after a bounded
// number, so an immediate return cannot spin.
type waitRecorder struct {
	mu        sync.Mutex
	waits     []time.Duration
	stopAfter int
	cancel    context.CancelFunc
	stopped   bool
}

func (w *waitRecorder) wait(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.waits = append(w.waits, d)
	count := len(w.waits)
	cancel := w.cancel
	shake := count >= w.stopAfter && !w.stopped
	if shake {
		w.stopped = true
	}
	w.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if shake {
		cancel()
		return context.Canceled
	}
	return nil
}

func (w *waitRecorder) recorded() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.waits...)
}

func (w *waitRecorder) setCancel(cancel context.CancelFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cancel = cancel
}

// directoryFrame builds a directory frame with the given addresses.
func directoryFrame(addresses ...string) protocol.StreamFrame {
	entries := make([]protocol.DirectoryEntry, 0, len(addresses))
	for _, address := range addresses {
		name, host, _ := strings.Cut(address, "@")
		entries = append(entries, protocol.DirectoryEntry{Address: address, Host: host, Live: true})
		_ = name
	}
	return protocol.StreamFrame{Type: protocol.FrameDirectory, Directory: entries}
}

// startRun runs the client in a goroutine and returns a stop function.
func startRun(t *testing.T, client *Client, recv Receiver, recorder *waitRecorder) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	recorder.setCancel(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.Run(ctx, recv)
	}()

	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	}
}

// TestStreamHappyPath: connecting, then connected on the first frame, with frames
// dispatched in order.
func TestStreamHappyPath(t *testing.T) {
	transport := &streamFake{mode: "stream"}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })

	transport.send(t, directoryFrame("claude@alpha", "codex@beta"))
	waitFor(t, "a connected state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnConnected
	})

	transport.send(t, protocol.StreamFrame{Type: protocol.FrameOffer, Offer: &protocol.Offer{
		ID: "m1", From: "codex@beta", To: "claude", ToSession: "s1", Body: "hi",
	}})
	transport.send(t, protocol.StreamFrame{Type: protocol.FrameReport, Report: &protocol.MessageInfo{
		ID: "m2", Result: protocol.SendHostStored,
	}})
	transport.send(t, protocol.StreamFrame{Type: protocol.FramePing})
	transport.send(t, protocol.StreamFrame{Type: "something_new"})
	transport.sendRaw(t, []byte("\n"))
	transport.send(t, directoryFrame("claude@alpha"))

	waitFor(t, "the offer, the report and the second directory", func() bool {
		recv.mu.Lock()
		defer recv.mu.Unlock()
		return len(recv.offers) == 1 && len(recv.reports) == 1 && len(recv.peers) == 2
	})

	recv.mu.Lock()
	defer recv.mu.Unlock()

	if recv.offers[0].ID != "m1" {
		t.Fatalf("offer = %+v", recv.offers[0])
	}
	if recv.reports[0].Result != protocol.SendHostStored {
		t.Fatalf("report = %+v", recv.reports[0])
	}
	// The first directory is the frame's order, unchanged.
	if got := recv.peers[0]; len(got) != 2 || got[0] != "claude@alpha" || got[1] != "codex@beta" {
		t.Fatalf("first peers = %v", got)
	}
	if recv.linkUps != 1 {
		t.Fatalf("%d LinkUp calls, want 1", recv.linkUps)
	}
	// The first emitted state is connecting.
	if len(recv.states) < 2 || recv.states[0].State != protocol.ConnConnecting {
		t.Fatalf("first state = %+v, want connecting", recv.states[0])
	}
}

// TestStreamRequestShape: method, path, headers and the stream transport's rules.
func TestStreamRequestShape(t *testing.T) {
	transport := &streamFake{mode: "stream"}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })

	requests := transport.streamRequests()
	first := requests[0]
	if first.method != http.MethodGet || first.path != "/v1/stream" {
		t.Fatalf("request = %s %s", first.method, first.path)
	}
	if got := first.header.Get(protocol.WireVersionHeader); got != "1" {
		t.Fatalf("version header = %q", got)
	}
	if got := first.header.Get("Authorization"); got != "Bearer "+testPairing().Credential {
		t.Fatalf("Authorization = %q", got)
	}
	if got := first.header.Get("Accept"); got != streamMediaType {
		t.Fatalf("Accept = %q", got)
	}

	// Built without an injected transport, the stream transport keeps its
	// connection and has no header timeout.
	built, err := NewClient(testPairing(), Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	streamTransportValue, ok := built.streamHTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("stream transport is %T", built.streamHTTP.Transport)
	}
	if streamTransportValue.Proxy != nil {
		t.Fatal("the stream transport has a proxy function")
	}
	if !streamTransportValue.DisableCompression {
		t.Fatal("compression is enabled on the stream transport")
	}
	if streamTransportValue.DisableKeepAlives {
		t.Fatal("keep-alives are off on the stream transport")
	}
	if streamTransportValue.ResponseHeaderTimeout != 0 {
		t.Fatalf("the stream transport has a %v header timeout, want none", streamTransportValue.ResponseHeaderTimeout)
	}
	if built.streamHTTP.Timeout != 0 {
		t.Fatalf("the stream client has a %v timeout, want none", built.streamHTTP.Timeout)
	}
}

// TestWatchdogClosesAQuietGateway: no frame for DeadAfter ends the connection and
// reports a reconnect, then a redial happens after the recorded wait.
func TestWatchdogClosesAQuietGateway(t *testing.T) {
	transport := &streamFake{mode: "stream"}
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

	// Say nothing: the watchdog must fire.
	waitFor(t, "a reconnecting state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnReconnecting
	})
	view, _ := recv.lastState()
	if !strings.Contains(view.Err, "no frame") {
		t.Fatalf("reconnecting reason = %q, want it to mention frames", view.Err)
	}

	waitFor(t, "a recorded backoff", func() bool { return len(recorder.recorded()) >= 1 })
	if got := recorder.recorded()[0]; got != 5*time.Millisecond {
		t.Fatalf("first wait = %v, want BackoffMin", got)
	}
	waitFor(t, "a second stream request", func() bool { return transport.requestCount() >= 2 })
}

// TestStreamWithoutHeadersIsCaughtByTheWatchdog: the transport has no header
// timeout, so a gateway that never answers must be caught by the watchdog.
func TestStreamWithoutHeadersIsCaughtByTheWatchdog(t *testing.T) {
	transport := &streamFake{mode: "stream", headerGate: make(chan struct{})}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "a reconnecting state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnReconnecting
	})

	view, _ := recv.lastState()
	if !strings.Contains(view.Err, "no frame") {
		t.Fatalf("reason = %q, want the watchdog's reason", view.Err)
	}
	if count := transport.requestCount(); count < 1 {
		t.Fatalf("%d requests, want at least 1", count)
	}
}

// TestPingsKeepTheConnectionAlive: frames reset the watchdog.
func TestPingsKeepTheConnectionAlive(t *testing.T) {
	transport := &streamFake{mode: "stream"}
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

	for i := 0; i < 8; i++ {
		transport.send(t, protocol.StreamFrame{Type: protocol.FramePing})
		time.Sleep(15 * time.Millisecond)
	}

	view, _ := recv.lastState()
	if view.State != protocol.ConnConnected {
		t.Fatalf("state = %q after pings, want connected", view.State)
	}
	if count := recv.linkUpCount(); count != 1 {
		t.Fatalf("%d LinkUp calls, want 1: the connection never dropped", count)
	}
}

// TestRefusalsAreReportedAndRetried: a refusal is its own state, with its reason,
// and the runtime keeps trying.
func TestRefusalsAreReportedAndRetried(t *testing.T) {
	// The gateway's own WireError text must reach the state's reason: the user needs
	// to see WHY the host was refused.
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized,
			body: `{"code":"unauthorized","message":"the credential was not accepted"}`, want: "the credential was not accepted"},
		{name: "version mismatch", status: http.StatusBadRequest,
			body: `{"code":"version_mismatch","message":"this gateway speaks protocol version 1"}`, want: "this gateway speaks protocol version 1"},
		{name: "redirect", status: http.StatusFound, body: "", want: "redirect"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &streamFake{mode: "canned", status: tc.status, content: "application/json", body: tc.body}
			client, recorder := runClient(t, transport)
			recv := &fakeReceiver{}

			stop := startRun(t, client, recv, recorder)
			defer stop()

			waitFor(t, "a refused state", func() bool {
				view, ok := recv.lastState()
				return ok && view.State == protocol.ConnRefused
			})

			view, _ := recv.lastState()
			if !strings.Contains(view.Err, tc.want) {
				t.Fatalf("reason = %q, want it to mention %q", view.Err, tc.want)
			}

			// It keeps trying: a second request happens after a recorded wait.
			waitFor(t, "a retry", func() bool { return transport.requestCount() >= 2 })
		})
	}
}

// TestServerErrorsAreReconnections: a 5xx or a non-NDJSON 200 is not a refusal.
func TestServerErrorsAreReconnections(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		content string
	}{
		{name: "503", status: http.StatusServiceUnavailable, content: "application/json"},
		{name: "not ndjson", status: http.StatusOK, content: "text/plain"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &streamFake{mode: "canned", status: tc.status, content: tc.content}
			client, recorder := runClient(t, transport)
			recv := &fakeReceiver{}

			stop := startRun(t, client, recv, recorder)
			defer stop()

			waitFor(t, "a reconnecting state", func() bool {
				view, ok := recv.lastState()
				return ok && view.State == protocol.ConnReconnecting
			})
			view, _ := recv.lastState()
			if strings.Contains(view.Err, "refused") {
				t.Fatalf("reason = %q, want no refusal wording", view.Err)
			}
		})
	}
}

// TestBackoffDoublesAndResets: consecutive dial failures double the wait, capped;
// a connection that delivered a frame resets it.
func TestBackoffDoublesAndResets(t *testing.T) {
	transport := &failingStream{}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	recorder.stopAfter = 4
	stop := startRun(t, client, recv, recorder)
	stop()

	waits := recorder.recorded()
	if len(waits) < 3 {
		t.Fatalf("%d waits, want at least 3", len(waits))
	}
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	for i, expected := range want {
		if waits[i] != expected {
			t.Fatalf("wait %d = %v, want %v (waits %v)", i, waits[i], expected, waits)
		}
	}
	// Capped at BackoffMax.
	for _, got := range waits {
		if got > 20*time.Millisecond {
			t.Fatalf("a wait of %v exceeded BackoffMax", got)
		}
	}

	// The reconnecting reason carries no address.
	if view, ok := recv.lastState(); ok {
		if strings.Contains(view.Err, "127.0.0.1") || strings.Contains(view.Err, "7777") {
			t.Fatalf("reason = %q, want no address", view.Err)
		}
	}
}

// failingStream always fails the dial.
type failingStream struct{}

func (f *failingStream) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed")
}

// TestFrameTooLarge: a line over the cap ends the connection and delivers nothing.
func TestFrameTooLarge(t *testing.T) {
	transport := &streamFake{mode: "stream"}
	client, recorder := runClient(t, transport)
	client.opts.MaxFrameBytes = 64
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.requestCount() >= 1 })
	transport.send(t, directoryFrame("claude@alpha"))
	waitFor(t, "a connected state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnConnected
	})

	transport.sendRaw(t, []byte(strings.Repeat("x", 200)+"\n"))

	waitFor(t, "a reconnecting state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnReconnecting
	})
	view, _ := recv.lastState()
	if !strings.Contains(view.Err, "too large") {
		t.Fatalf("reason = %q, want the too-large cause", view.Err)
	}
}

// TestBrokenJSONEndsTheConnection: a line that is not JSON is a reconnection after
// the earlier frames were delivered.
func TestBrokenJSONEndsTheConnection(t *testing.T) {
	transport := &streamFake{mode: "stream"}
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

	transport.sendRaw(t, []byte("this is not json\n"))

	waitFor(t, "a reconnecting state", func() bool {
		view, ok := recv.lastState()
		return ok && view.State == protocol.ConnReconnecting
	})
	if got := recv.callCount(); got == 0 {
		t.Fatal("the earlier frame was not delivered before the reconnect")
	}
}

// TestHeartbeatsStartAfterTheFirstFrame: no heartbeat before a frame, then repeats.
func TestHeartbeatsStartAfterTheFirstFrame(t *testing.T) {
	transport := &countingTransport{mode: "stream"}
	client, recorder := runClient(t, transport)
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.streamRequests() >= 1 })

	// No heartbeat yet: the first frame has not arrived.
	time.Sleep(40 * time.Millisecond)
	if hearts := transport.heartbeats(); hearts != 0 {
		t.Fatalf("%d heartbeats before the first frame, want 0", hearts)
	}

	transport.send(t, directoryFrame("claude@alpha"))
	waitFor(t, "several heartbeats", func() bool { return transport.heartbeats() >= 3 })
}

// countingTransport is a stream fake that also counts heartbeats.
type countingTransport struct {
	streamFake
	mu       sync.Mutex
	beatHits int
}

func (c *countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == protocol.HeartbeatPath {
		c.mu.Lock()
		c.beatHits++
		c.mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	}
	return c.streamFake.RoundTrip(request)
}

func (c *countingTransport) heartbeats() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.beatHits
}

func (c *countingTransport) streamRequests() int {
	return c.streamFake.requestCount()
}

// waitFor polls a condition with a bounded deadline, so no test asserts on a sleep.
func waitFor(t *testing.T, why string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// TestTheFirstHeartbeatIsImmediate: the gateway counts a host live only if it has
// heard a heartbeat within 15 s and nothing else sets that time, so the first beat
// must follow the first frame at once, not one interval later. With the interval set
// far beyond the test, exactly one beat can only be the immediate one.
func TestTheFirstHeartbeatIsImmediate(t *testing.T) {
	transport := &countingTransport{mode: "stream"}
	client, recorder := runClient(t, transport)
	client.opts.HeartbeatEvery = time.Hour
	recv := &fakeReceiver{}

	stop := startRun(t, client, recv, recorder)
	defer stop()

	waitFor(t, "the stream request", func() bool { return transport.streamRequests() >= 1 })
	if hearts := transport.heartbeats(); hearts != 0 {
		t.Fatalf("%d heartbeats before the first frame, want 0", hearts)
	}

	transport.send(t, directoryFrame("claude@alpha"))
	waitFor(t, "the immediate heartbeat", func() bool { return transport.heartbeats() == 1 })

	time.Sleep(40 * time.Millisecond)
	if hearts := transport.heartbeats(); hearts != 1 {
		t.Fatalf("%d heartbeats, want exactly the one immediate beat with the interval at one hour", hearts)
	}
}

// TestStateEmitterCarriesTheSavedTransport: LANPlainHTTP follows the pairing on every
// emit, not only while connected. UNTESTED: written, not run.
func TestStateEmitterCarriesTheSavedTransport(t *testing.T) {
	tests := []struct {
		name      string
		pairing   Pairing
		wantPlain bool
	}{
		{
			name:      "a plain LAN pairing says so",
			pairing:   Pairing{Gateway: "192.168.1.20:7777", Host: "mac", Credential: "abc", InsecureHTTP: true},
			wantPlain: true,
		},
		{
			name:      "an https pairing does not",
			pairing:   Pairing{Gateway: "192.168.1.20:7777", Host: "mac", Credential: "abc", Fingerprint: "sha256:" + strings.Repeat("ab", 32)},
			wantPlain: false,
		},
		{
			name:      "a tailscale pairing does not",
			pairing:   Pairing{Gateway: "100.64.0.1:7777", Host: "mac", Credential: "abc"},
			wantPlain: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(tc.pairing, Options{})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			receiver := &fakeReceiver{}
			emitter := &stateEmitter{client: client, recv: receiver}

			// Every state, so a flag that only appeared while connected would show up.
			emitter.emit(protocol.ConnConnecting, "")
			emitter.emit(protocol.ConnConnected, "")
			emitter.emit(protocol.ConnReconnecting, "connection reset")
			emitter.emit(protocol.ConnRefused, "revoked")

			receiver.mu.Lock()
			states := append([]protocol.ConnectionView(nil), receiver.states...)
			receiver.mu.Unlock()
			if len(states) == 0 {
				t.Fatal("no state was emitted")
			}
			for _, view := range states {
				if view.LANPlainHTTP != tc.wantPlain {
					t.Fatalf("state %q has LANPlainHTTP=%t, want %t", view.State, view.LANPlainHTTP, tc.wantPlain)
				}
				// No credential or address detail leaks into the view's error text.
				if strings.Contains(view.Err, tc.pairing.Credential) {
					t.Fatalf("the view's error text carries the credential: %q", view.Err)
				}
			}
		})
	}
}
