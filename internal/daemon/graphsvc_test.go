package daemon

// Untested: under GR-3 nothing here has been run. The link is a fake; no gateway, no socket.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// fakeGraphLink answers FetchGraph from a script.
type fakeGraphLink struct {
	mu      sync.Mutex
	fetches []link.GraphFetch
	err     error
	calls   int
}

func (f *fakeGraphLink) FetchGraph(ctx context.Context, since uint64, wait time.Duration) (link.GraphFetch, error) {
	f.mu.Lock()
	f.calls++
	err := f.err
	var fetch link.GraphFetch
	if len(f.fetches) > 0 {
		fetch = f.fetches[0]
		if len(f.fetches) > 1 {
			f.fetches = f.fetches[1:]
		}
	} else {
		fetch = link.GraphFetch{Unchanged: true}
	}
	f.mu.Unlock()

	if err != nil {
		return link.GraphFetch{}, err
	}
	if fetch.Unchanged {
		select {
		case <-ctx.Done():
			return link.GraphFetch{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return fetch, nil
}

func (f *fakeGraphLink) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newGraphFixture(t *testing.T, fake *fakeGraphLink) (*graphService, *fakeStates) {
	t.Helper()
	states := &fakeStates{}
	states.set(nil, &protocol.ConnectionView{State: protocol.ConnConnected})

	service := newGraphService(fake, states, &logger{out: io.Discard}, func() string { return "laptop" })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.start(ctx)
	return service, states
}

func consumersOf(service *graphService) (int, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.consumers, service.fetchRunning
}

// TestGraphConsumerLifecycle: the first consumer starts the loop, the last stops it, release is
// idempotent.
func TestGraphConsumerLifecycle(t *testing.T) {
	service, _ := newGraphFixture(t, &fakeGraphLink{})

	release := service.acquire()
	second := service.acquire()
	if count, running := consumersOf(service); count != 2 || !running {
		t.Fatalf("consumers = %d, running = %t", count, running)
	}

	release()
	release()
	if count, running := consumersOf(service); count != 1 || !running {
		t.Fatalf("after one release: consumers = %d, running = %t", count, running)
	}
	second()
	if count, running := consumersOf(service); count != 0 || running {
		t.Fatalf("after all releases: consumers = %d, running = %t", count, running)
	}
}

// TestGraphViewBeforeAnyFetch: identity needs no gateway, the snapshot is present and null, and the
// state is connected with a healthy link.
func TestGraphViewBeforeAnyFetch(t *testing.T) {
	service, _ := newGraphFixture(t, &fakeGraphLink{})

	view, version := service.view()
	if view.Local.Host != "laptop" || view.Snapshot != nil || view.Schema != protocol.GraphSchema {
		t.Fatalf("view = %+v", view)
	}
	if view.Gateway.State != protocol.GatewayConnected {
		t.Fatalf("state = %q", view.Gateway.State)
	}
	if version == 0 {
		t.Fatal("version = 0")
	}
}

// TestGraphNotConfiguredWithoutAHost: no pairing host means not_configured.
func TestGraphNotConfiguredWithoutAHost(t *testing.T) {
	states := &fakeStates{}
	states.set(nil, &protocol.ConnectionView{State: protocol.ConnConnected})
	service := newGraphService(&fakeGraphLink{}, states, &logger{out: io.Discard}, func() string { return "" })

	view, _ := service.view()
	if view.Gateway.State != protocol.GatewayNotConfigured {
		t.Fatalf("state = %q", view.Gateway.State)
	}
}

// TestGraphInstallKeepsASnapshotAndTheFirstWaitReturnsIt: the loop fetches immediately and the
// first-wait caller gets the body.
func TestGraphInstallKeepsASnapshotAndTheFirstWaitReturnsIt(t *testing.T) {
	fake := &fakeGraphLink{fetches: []link.GraphFetch{{Snapshot: &protocol.GraphSnapshot{Schema: 1, Revision: 4}}}}
	service, _ := newGraphFixture(t, fake)
	release := service.acquire()
	defer release()

	view, _ := service.answer(context.Background(), wire.GraphRequest{FirstWaitMS: 2000})
	if view.Snapshot == nil || view.Snapshot.Revision != 4 || view.FetchedAt == "" {
		t.Fatalf("view = %+v", view)
	}
}

// TestGraphNotFoundIsGatewayTooOld: a 404 is its own state, sticky until the link is seen down and
// back, and a first-wait caller is not made to wait for it.
func TestGraphNotFoundIsGatewayTooOld(t *testing.T) {
	fake := &fakeGraphLink{err: link.ErrGraphUnsupported}
	service, states := newGraphFixture(t, fake)
	release := service.acquire()
	defer release()

	started := time.Now()
	view, _ := service.answer(context.Background(), wire.GraphRequest{FirstWaitMS: 3000})
	if view.Gateway.State != protocol.GatewayTooOld {
		t.Fatalf("state = %q", view.Gateway.State)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("a too-old gateway made the caller wait out the first wait")
	}

	// The link goes down and comes back: the flag is released and the loop asks again.
	callsBefore := fake.callCount()
	states.set(nil, &protocol.ConnectionView{State: protocol.ConnReconnecting})
	time.Sleep(3 * fetchIdlePoll)
	states.set(nil, &protocol.ConnectionView{State: protocol.ConnConnected})
	deadline := time.Now().Add(3 * time.Second)
	for fake.callCount() == callsBefore && time.Now().Before(deadline) {
		time.Sleep(fetchIdlePoll)
	}
	if fake.callCount() == callsBefore {
		t.Fatal("the loop never asked again after a reconnect")
	}
}

// TestGraphSchemaTooNewIsNeverInstalled: newer data is unavailable, not best-effort.
func TestGraphSchemaTooNewIsNeverInstalled(t *testing.T) {
	service, _ := newGraphFixture(t, &fakeGraphLink{err: link.ErrGraphSchemaTooNew})
	release := service.acquire()
	defer release()

	view, _ := service.answer(context.Background(), wire.GraphRequest{FirstWaitMS: 2000})
	if view.Snapshot != nil || view.Gateway.State != protocol.GatewayTooOld {
		t.Fatalf("view = %+v", view)
	}
	if view.Gateway.Error == "" {
		t.Fatal("no explanation for a newer schema")
	}
}

// TestGraphFailedFetchKeepsTheSnapshotButSaysUnreachable: the kept body is never presented as current.
func TestGraphFailedFetchKeepsTheSnapshotButSaysUnreachable(t *testing.T) {
	fake := &fakeGraphLink{fetches: []link.GraphFetch{{Snapshot: &protocol.GraphSnapshot{Schema: 1, Revision: 2}}}}
	service, _ := newGraphFixture(t, fake)
	release := service.acquire()
	defer release()

	if view, _ := service.answer(context.Background(), wire.GraphRequest{FirstWaitMS: 2000}); view.Snapshot == nil {
		t.Fatal("no snapshot installed")
	}

	fake.mu.Lock()
	fake.err = context.DeadlineExceeded
	fake.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, _ := service.view()
		if view.Gateway.State == protocol.GatewayUnreachable {
			if view.Snapshot == nil || view.FetchedAt == "" {
				t.Fatalf("the kept snapshot was dropped: %+v", view)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a failing fetch never made the gateway unreachable")
}

// TestGraphSinceWaitReturnsOnChange: an up-to-date caller waits and is woken by a change.
func TestGraphSinceWaitReturnsOnChange(t *testing.T) {
	service, _ := newGraphFixture(t, &fakeGraphLink{})
	_, version := service.view()

	done := make(chan uint64, 1)
	go func() {
		_, current := service.answer(context.Background(), wire.GraphRequest{Since: version, WaitMS: 3000})
		done <- current
	}()

	time.Sleep(50 * time.Millisecond)
	service.install(&protocol.GraphSnapshot{Schema: 1, Revision: 9})

	select {
	case current := <-done:
		if current == version {
			t.Fatal("the wait returned the same version after a change")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the wait was not woken")
	}
}

// TestAttachMakesTheWindowAGraphConsumer: like the network consumer, from attach to close.
func TestAttachMakesTheWindowAGraphConsumer(t *testing.T) {
	fixture := newAttachFixture(t)
	service, _ := newGraphFixture(t, &fakeGraphLink{})
	fixture.hub.graph = service

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	if !fixture.server.handleAttach(w, wire.Attach{Width: 80, Height: 24}) {
		t.Fatal("attach failed")
	}
	if count, _ := consumersOf(service); count != 1 {
		t.Fatalf("consumers = %d after attach, want 1", count)
	}
	w.closeWith("test")
	if count, _ := consumersOf(service); count != 0 {
		t.Fatalf("consumers = %d after close, want 0", count)
	}

	// A refused attach acquires nothing.
	refused := newWindow("w2", fixture.conn, fixture.hub, &logger{out: io.Discard})
	if fixture.server.handleAttach(refused, wire.Attach{Width: 0, Height: 24}) {
		t.Fatal("a bad size was accepted")
	}
	if count, _ := consumersOf(service); count != 0 {
		t.Fatalf("a refused attach left %d consumers", count)
	}
}
