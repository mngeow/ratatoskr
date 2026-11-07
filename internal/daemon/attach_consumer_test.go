package daemon

// Untested: under GR-3 nothing here has been run. The attach fixture is driven with fakes, so no
// socket, no gateway and no harness is involved.

import (
	"context"
	"io"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

// consumers reads the network service's consumer count under its mutex.
func consumers(service *networkService) int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.consumers
}

// withService gives the attach fixture's hub a network service, built by the existing constructor.
func withService(t *testing.T, fixture *attachFixture) *networkService {
	t.Helper()
	service := newNetsvcFixture(t).service
	fixture.hub.network = service
	return service
}

// TestAttachMakesTheWindowAConsumer: a successful attach is one consumer, and closing it is none.
func TestAttachMakesTheWindowAConsumer(t *testing.T) {
	fixture := newAttachFixture(t)
	service := withService(t, fixture)

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	if !fixture.server.handleAttach(w, wire.Attach{Dir: t.TempDir(), Width: 80, Height: 24}) {
		t.Fatal("attach failed")
	}
	if got := consumers(service); got != 1 {
		t.Fatalf("consumers = %d after a successful attach, want 1", got)
	}

	w.closeWith("test")
	if got := consumers(service); got != 0 {
		t.Fatalf("consumers = %d after the close, want 0", got)
	}

	// Closing twice is harmless.
	w.closeWith("again")
	if got := consumers(service); got != 0 {
		t.Fatalf("consumers = %d after a second close, want 0", got)
	}
}

// TestAttachWithARunningSessionIsOneConsumer: a window with a session counts the same.
func TestAttachWithARunningSessionIsOneConsumer(t *testing.T) {
	fixture := newAttachFixture(t)
	service := withService(t, fixture)

	if _, err := fixture.session.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add: %v", err)
	}

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	if !fixture.server.handleAttach(w, wire.Attach{Dir: t.TempDir(), Width: 80, Height: 24}) {
		t.Fatal("attach failed")
	}
	// The consumer started a fetch loop on the fake link; closing the window is what stops it.
	t.Cleanup(func() { w.closeWith("test over") })
	if got := consumers(service); got != 1 {
		t.Fatalf("consumers = %d, want 1", got)
	}
}

// TestExpectedSessionAttachIsOneConsumer: an exact-session window fetches too.
func TestExpectedSessionAttachIsOneConsumer(t *testing.T) {
	fixture := newExpectFixture(t)
	service := newNetsvcFixture(t).service
	fixture.hub.network = service

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	if !fixture.server.handleAttach(w, wire.Attach{Width: 80, Height: 24, ExpectSession: string(fixture.agent)}) {
		t.Fatal("attach failed")
	}
	// The consumer started a fetch loop on the fake link; closing the window is what stops it.
	t.Cleanup(func() { w.closeWith("test over") })
	if got := consumers(service); got != 1 {
		t.Fatalf("consumers = %d, want 1", got)
	}
}

// TestRefusedAttachIsNoConsumer: every refusal path leaves the count at zero.
func TestRefusedAttachIsNoConsumer(t *testing.T) {
	missing := "0192f2b4-9999-9999-9999-999999999999"

	cases := []struct {
		name   string
		attach wire.Attach
		ended  bool
	}{
		{
			name:   "a bad size",
			attach: wire.Attach{Width: 0, Height: 24},
		},
		{
			name:   "an unknown start name",
			attach: wire.Attach{Width: 80, Height: 24, Start: []string{"gemini"}},
		},
		{
			name:   "an expected session that is not running",
			attach: wire.Attach{Width: 80, Height: 24, ExpectSession: missing},
		},
		{
			name:   "an expected session that has ended",
			attach: wire.Attach{Width: 80, Height: 24, ExpectSession: "0192f2b4-1111-2222-3333-444455556666"},
			ended:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fixture *attachFixture
			if tc.ended {
				expected := newExpectFixture(t)
				fixture = expected.attachFixture
				code := 1
				expected.gate.end(expected.agent, &code)
				tc.attach.ExpectSession = string(expected.agent)
			} else {
				fixture = newAttachFixture(t)
			}
			service := withService(t, fixture)

			w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
			if fixture.server.handleAttach(w, tc.attach) {
				t.Fatal("a refused attach reported success")
			}
			if got := consumers(service); got != 0 {
				t.Fatalf("consumers = %d after a refused attach, want 0", got)
			}
		})
	}
}

// TestFailedSendAttachedIsNoConsumer: a window that went away during the attach never acquires.
func TestFailedSendAttachedIsNoConsumer(t *testing.T) {
	fixture := newAttachFixture(t)
	service := withService(t, fixture)

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	// Closing before the attach makes sendAttached fail: the window is already gone.
	w.closeWith("gone before attach")

	if fixture.server.handleAttach(w, wire.Attach{Dir: t.TempDir(), Width: 80, Height: 24}) {
		t.Fatal("attach reported success for a closed window")
	}
	if got := consumers(service); got != 0 {
		t.Fatalf("consumers = %d after a failed sendAttached, want 0", got)
	}
}

// TestAttachThenQueryThenClose: a query no longer changes the count, and the close still releases.
func TestAttachThenQueryThenClose(t *testing.T) {
	fixture := newAttachFixture(t)
	service := withService(t, fixture)
	// A view is needed for the query to answer, which is what makes this the real path.
	fixture.hub.router = &fakeWindowRouter{}

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	if !fixture.server.handleAttach(w, wire.Attach{Dir: t.TempDir(), Width: 80, Height: 24}) {
		t.Fatal("attach failed")
	}
	if got := consumers(service); got != 1 {
		t.Fatalf("consumers = %d after attach, want 1", got)
	}

	envelope, err := encodeRequest(wire.TypeQuery, 1, wire.Query{
		Kind:    wire.QueryNetwork,
		Network: &wire.NetworkRequest{},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if result := w.answer(context.Background(), envelope); result.Error != nil {
		t.Fatalf("the network query failed: %+v", result.Error)
	}
	if got := consumers(service); got != 1 {
		t.Fatalf("consumers = %d after a query, want 1 (a query changes nothing)", got)
	}

	w.closeWith("test")
	if got := consumers(service); got != 0 {
		t.Fatalf("consumers = %d after the close, want 0", got)
	}
}
