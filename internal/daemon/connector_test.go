package daemon

// Untested: under GR-3 nothing here has been run. The connector's link and sleep are fakes,
// so no gateway is contacted and no time passes.

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// fakeConnectorLink answers connect attempts in order.
type fakeConnectorLink struct {
	mu       sync.Mutex
	failures int
	attempts int
}

func (f *fakeConnectorLink) Connect(context.Context) (protocol.ConnectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.attempts <= f.failures {
		return protocol.ConnectResponse{}, errors.New("connection refused")
	}
	return protocol.ConnectResponse{Host: "mac"}, nil
}

func (f *fakeConnectorLink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// fakePending records the retry calls.
type fakePending struct {
	mu    sync.Mutex
	calls int
}

func (f *fakePending) retryPendingEnd(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
}

func (f *fakePending) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestConnectorBackoffAndOneConnect: the failures back off 1s, 2s, 4s, then the cap, and
// the connect happens once.
func TestConnectorBackoffAndOneConnect(t *testing.T) {
	link := &fakeConnectorLink{failures: 3}
	connector := newConnector(link, &logger{out: io.Discard})

	var waits []time.Duration
	connector.sleep = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return true
	}

	ctx, cancel := context.WithCancel(context.Background())
	pending := &fakePending{}
	connector.setPending(pending)

	// The run blocks in its retry loop after connecting, so it runs in a goroutine and the
	// test cancels once the connect has happened.
	done := make(chan struct{})
	go func() {
		connector.run(ctx)
		close(done)
	}()

	waitFor(t, "the connect", func() bool { return connector.Connected() })
	cancel()
	<-done

	want := []time.Duration{connectBackoffMin, 2 * connectBackoffMin, 4 * connectBackoffMin}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
	if link.count() != 4 {
		t.Fatalf("connect attempts = %d, want 4 (three failures and one success)", link.count())
	}
	if connector.LastError() != "" {
		t.Fatalf("last error = %q, want empty after a success", connector.LastError())
	}
	if pending.count() < 1 {
		t.Fatalf("the pending ends were retried %d times, want at least once after the connect", pending.count())
	}
}

// TestConnectorBackoffCap: the wait stops doubling at the cap.
func TestConnectorBackoffCap(t *testing.T) {
	link := &fakeConnectorLink{failures: 10}
	connector := newConnector(link, &logger{out: io.Discard})

	var waits []time.Duration
	connector.sleep = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		// Stop after enough waits to see the cap.
		return len(waits) < 8
	}

	connector.run(context.Background())

	if len(waits) < 6 {
		t.Fatalf("waits = %v, want at least six", waits)
	}
	if waits[len(waits)-1] != connectBackoffMax {
		t.Fatalf("last wait = %v, want the cap %v", waits[len(waits)-1], connectBackoffMax)
	}
	for _, wait := range waits {
		if wait > connectBackoffMax {
			t.Fatalf("a wait of %v exceeds the cap %v", wait, connectBackoffMax)
		}
	}
	if connector.Connected() {
		t.Fatal("the connector reported a connection it never made")
	}
	if connector.LastError() == "" {
		t.Fatal("the last error was not recorded")
	}
}

// TestConnectorStopsWithTheContext: a cancelled context ends the run without another
// attempt.
func TestConnectorStopsWithTheContext(t *testing.T) {
	link := &fakeConnectorLink{failures: 100}
	connector := newConnector(link, &logger{out: io.Discard})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	connector.run(ctx)

	if link.count() != 0 {
		t.Fatalf("connect attempts = %d, want 0 for an already-cancelled context", link.count())
	}
}
