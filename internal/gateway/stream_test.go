package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// Nothing binds a socket: the stream is driven through server.Handler() with a
// recording ResponseWriter and a cancellable request context.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// streamRecorder is a ResponseWriter a stream can write into.
//
// It implements Flush and SetWriteDeadline itself, so http.ResponseController finds
// them without needing Unwrap - which is also what proves the wrapper passes the
// controller through to the real writer in production.
type streamRecorder struct {
	mu           sync.Mutex
	header       http.Header
	status       int
	body         bytes.Buffer
	flushes      int
	lastDeadline time.Time
	deadlines    int
	failDeadline bool
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: make(http.Header)}
}

func (r *streamRecorder) Header() http.Header { return r.header }

func (r *streamRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = status
	}
}

func (r *streamRecorder) Write(payload []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(payload)
}

func (r *streamRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
}

func (r *streamRecorder) SetWriteDeadline(deadline time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failDeadline {
		return errors.New("streamRecorder: deadlines unsupported")
	}
	r.lastDeadline = deadline
	r.deadlines++
	return nil
}

// frames decodes every complete NDJSON line written so far.
func (r *streamRecorder) frames(t *testing.T) []protocol.StreamFrame {
	t.Helper()

	r.mu.Lock()
	content := r.body.String()
	flushes := r.flushes
	deadlines := r.deadlines
	r.mu.Unlock()

	if content != "" && !strings.HasSuffix(content, "\n") {
		t.Fatalf("the stream has a partial line: %q", content)
	}

	frames := make([]protocol.StreamFrame, 0)
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		if line == "" {
			continue
		}
		var frame protocol.StreamFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("a line is not one JSON object: %v (%q)", err, line)
		}
		frames = append(frames, frame)
	}

	if len(frames) > 0 && deadlines < len(frames) {
		t.Fatalf("%d frames were written with only %d deadlines set", len(frames), deadlines)
	}
	if len(frames) > 0 && flushes < len(frames) {
		t.Fatalf("%d frames were written with only %d flushes", len(frames), flushes)
	}
	return frames
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

// streamFixture is one server with a paired host and a running stream.
type streamFixture struct {
	server     *Server
	store      *Store
	credential string
	recorder   *streamRecorder
	cancel     context.CancelFunc
	finished   chan struct{}
}

// startStream opens a stream through the handler and waits for the hub.
func startStream(t *testing.T, opts Options) *streamFixture {
	t.Helper()

	store, _ := openStore(t, false)
	credential := pairHostFrom(t, store, "laptop", gatewayNow)
	if opts.Now == nil {
		opts.Now = func() time.Time { return gatewayNow }
	}
	server := NewServer(store, opts)

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
	request.Header.Set(protocol.WireVersionHeader, version())
	request.Header.Set("Authorization", "Bearer "+credential)

	recorder := newStreamRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		server.Handler().ServeHTTP(recorder, request)
	}()

	fixture := &streamFixture{
		server:     server,
		store:      store,
		credential: credential,
		recorder:   recorder,
		cancel:     cancel,
		finished:   finished,
	}

	waitFor(t, "the stream to attach", func() bool { return server.hub.open("laptop") })
	return fixture
}

// stop ends the stream and waits for its handler.
func (f *streamFixture) stop(t *testing.T) {
	t.Helper()

	f.cancel()
	select {
	case <-f.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream handler did not return after cancellation")
	}
}

func TestStreamRequiresVersionAndCredential(t *testing.T) {
	server, _, credential := newGatewayServer(t, Options{})

	// No version header.
	request := httptest.NewRequest(http.MethodGet, "/v1/stream", nil)
	request.Header.Set("Authorization", "Bearer "+credential)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing version = %d, want 400", recorder.Code)
	}
	if code := wireCode(t, recorder); code != protocol.WireErrVersion {
		t.Fatalf("code = %q, want %q", code, protocol.WireErrVersion)
	}

	// No credential.
	request = httptest.NewRequest(http.MethodGet, "/v1/stream", nil)
	request.Header.Set(protocol.WireVersionHeader, version())
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing credential = %d, want 401", recorder.Code)
	}
}

// TestStreamStartsWithDirectoryThenPings covers the framing: a directory first, one
// flush and one deadline per frame, and a ping on the ticker.
func TestStreamStartsWithDirectoryThenPings(t *testing.T) {
	fixture := startStream(t, Options{
		PingEvery:     10 * time.Millisecond,
		WriteDeadline: 50 * time.Millisecond,
		QueueSize:     2,
	})
	defer fixture.stop(t)

	// The first frame must be the directory.
	waitFor(t, "a directory frame", func() bool { return len(fixture.recorder.frames(t)) > 0 })
	frames := fixture.recorder.frames(t)
	if frames[0].Type != protocol.FrameDirectory {
		t.Fatalf("first frame = %q, want %q", frames[0].Type, protocol.FrameDirectory)
	}

	// And a ping arrives.
	waitFor(t, "a ping frame", func() bool {
		for _, frame := range fixture.recorder.frames(t) {
			if frame.Type == protocol.FramePing {
				return true
			}
		}
		return false
	})

	// Every write carried a deadline and was flushed.
	fixture.recorder.mu.Lock()
	deadlines := fixture.recorder.deadlines
	flushes := fixture.recorder.flushes
	state := fixture.recorder.status
	fixture.recorder.mu.Unlock()
	if state != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", state)
	}
	if deadlines == 0 || flushes < deadlines {
		t.Fatalf("deadlines %d, flushes %d", deadlines, flushes)
	}
	if contentType := fixture.recorder.Header().Get("Content-Type"); contentType != streamContentType {
		t.Fatalf("content type = %q, want %q", contentType, streamContentType)
	}
}

// TestSecondStreamReplacesTheFirst: one stream per host, and the old handler ends.
func TestSecondStreamReplacesTheFirst(t *testing.T) {
	fixture := startStream(t, Options{PingEvery: time.Hour})
	defer fixture.cancel()

	server, store, credential := fixture.server, fixture.store, fixture.credential

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
	request.Header.Set(protocol.WireVersionHeader, version())
	request.Header.Set("Authorization", "Bearer "+credential)
	second := newStreamRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		server.Handler().ServeHTTP(second, request)
	}()

	// The first handler returns because the second replaced it.
	select {
	case <-fixture.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced stream did not end")
	}

	// Its detach must not have removed the new stream from the hub.
	if !server.hub.open("laptop") {
		t.Fatal("the new stream is not attached")
	}

	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the second stream did not end")
	}
	if server.hub.open("laptop") {
		t.Fatal("the hub still holds a stream after both ended")
	}
	_ = store
}

// TestUnpairEndsTheStreamAtTheNextTick is the contract's one-ping-interval rule.
func TestUnpairEndsTheStreamAtTheNextTick(t *testing.T) {
	fixture := startStream(t, Options{PingEvery: 10 * time.Millisecond})
	defer fixture.cancel()

	waitFor(t, "the first frame", func() bool { return len(fixture.recorder.frames(t)) > 0 })

	if err := fixture.store.Unpair(context.Background(), "laptop", gatewayNow); err != nil {
		t.Fatalf("Unpair: %v", err)
	}

	select {
	case <-fixture.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not end after the host was unpaired")
	}

	// Nothing was written after the unpair: the recheck happens before the write.
	frames := fixture.recorder.frames(t)
	last := frames[len(frames)-1]
	if last.Type == protocol.FramePing {
		t.Fatalf("a ping was written after the host was unpaired: %+v", last)
	}
}

// TestStreamEndsWhenDeadlinesUnsupported: continuing without deadlines is refused.
func TestStreamEndsWhenDeadlinesUnsupported(t *testing.T) {
	store, _ := openStore(t, false)
	credential := pairHostFrom(t, store, "laptop", gatewayNow)
	server := NewServer(store, Options{Now: func() time.Time { return gatewayNow }})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
	request.Header.Set(protocol.WireVersionHeader, version())
	request.Header.Set("Authorization", "Bearer "+credential)

	recorder := newStreamRecorder()
	recorder.failDeadline = true
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		server.Handler().ServeHTTP(recorder, request)
	}()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("a stream whose deadlines are unsupported kept running")
	}
	if server.hub.open("laptop") {
		t.Fatal("a failed stream is still attached")
	}
}

// TestPushDirectoryReachesAttachedStreams: a registration pushes a directory frame
// to every attached stream.
func TestPushDirectoryReachesAttachedStreams(t *testing.T) {
	fixture := startStream(t, Options{PingEvery: time.Hour})
	defer fixture.stop(t)

	waitFor(t, "the initial directory", func() bool { return len(fixture.recorder.frames(t)) > 0 })

	// A registration goes through the same handler the runtime uses.
	body, err := json.Marshal(protocol.SessionsRequest{Agents: []string{"claude", "codex"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	recorder := call(fixture.server, http.MethodPost, "/v1/sessions", version(), fixture.credential, string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("registration = %d: %s", recorder.Code, recorder.Body.String())
	}

	waitFor(t, "a pushed directory with two sessions", func() bool {
		frames := fixture.recorder.frames(t)
		for _, frame := range frames[1:] {
			if frame.Type == protocol.FrameDirectory && len(frame.Directory) == 2 {
				return true
			}
		}
		return false
	})
}

// TestPushDirectoryCancelsAFullQueue: a host that is not reading loses its stream.
func TestPushDirectoryCancelsAFullQueue(t *testing.T) {
	store, _ := openStore(t, false)
	server := NewServer(store, Options{Now: func() time.Time { return gatewayNow }})

	// A stream nobody drains, so its queue fills on the first push.
	stalled, stalledCtx := testStream("laptop", 1)
	server.hub.attach("laptop", stalled)

	server.pushDirectory()
	if !waitCancelled(stalledCtx) {
		t.Fatal("a full queue did not cancel the stream")
	}
}

// TestShutdownEndsStreams: Shutdown cancels streams before closing the server, and
// a server that never served does not panic.
func TestShutdownEndsStreams(t *testing.T) {
	store, _ := openStore(t, false)
	server := NewServer(store, Options{Now: func() time.Time { return gatewayNow }})

	attached, attachedCtx := testStream("laptop", 1)
	server.hub.attach("laptop", attached)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The http server was never started; Shutdown must still be safe.
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown on a server that never served: %v", err)
	}
	if !waitCancelled(attachedCtx) {
		t.Fatal("Shutdown did not cancel the attached stream")
	}
}

// TestLastHeartbeat covers the three answers the stream and the admin view need.
func TestLastHeartbeat(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	heartbeat, paired, err := store.LastHeartbeat(ctx, "laptop")
	if err != nil {
		t.Fatalf("LastHeartbeat for an unpaired host: %v", err)
	}
	if paired || !heartbeat.IsZero() {
		t.Fatalf("unpaired host = (%v, %v), want a zero time and false", heartbeat, paired)
	}

	pairHostFrom(t, store, "laptop", gatewayNow)

	heartbeat, paired, err = store.LastHeartbeat(ctx, "laptop")
	if err != nil {
		t.Fatalf("LastHeartbeat: %v", err)
	}
	if !paired || !heartbeat.IsZero() {
		t.Fatalf("a host that never sent a heartbeat = (%v, %v), want zero and true", heartbeat, paired)
	}

	if err := store.TouchHeartbeat(ctx, "laptop", gatewayNow); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	heartbeat, paired, err = store.LastHeartbeat(ctx, "laptop")
	if err != nil {
		t.Fatalf("LastHeartbeat: %v", err)
	}
	if !paired || !heartbeat.Equal(gatewayNow) {
		t.Fatalf("heartbeat = (%v, %v), want %v and true", heartbeat, paired, gatewayNow)
	}

	if err := store.Unpair(ctx, "laptop", gatewayNow); err != nil {
		t.Fatalf("Unpair: %v", err)
	}
	heartbeat, paired, err = store.LastHeartbeat(ctx, "laptop")
	if err != nil {
		t.Fatalf("LastHeartbeat after unpair: %v", err)
	}
	if paired || !heartbeat.IsZero() {
		t.Fatalf("after unpair = (%v, %v), want zero and false", heartbeat, paired)
	}
}
