package daemon

// Untested: under GR-3 nothing here has been run. Attach-with-expectation is driven through the real
// handler with fakes, so nothing is registered in a hub that has no socket.

import (
	"context"
	"io"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// expectFixture is the attach fixture with one running session whose id is a UUID.
type expectFixture struct {
	*attachFixture
	agent protocol.AgentID
}

// newExpectFixture starts one session and returns its agent id.
func newExpectFixture(t *testing.T) *expectFixture {
	t.Helper()

	fixture := newAttachFixture(t)
	entry, err := fixture.session.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return &expectFixture{attachFixture: fixture, agent: entry.id}
}

// TestAttachExpectedSessionOnly: the window gets one slot, and only that session.
func TestAttachExpectedSessionOnly(t *testing.T) {
	fixture := newExpectFixture(t)

	// A second session must not appear.
	if _, err := fixture.session.manager.Add(context.Background(), "codex", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add codex: %v", err)
	}

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	ok := fixture.server.handleAttach(w, wire.Attach{
		Width: 80, Height: 24, ExpectSession: string(fixture.agent),
	})
	if !ok {
		t.Fatal("attach with a running expectation was refused")
	}

	attached := firstAttached(t, w)
	if len(attached.Slots) != 1 {
		t.Fatalf("slots = %+v, want exactly the expected one", attached.Slots)
	}
	if attached.Slots[0].Agent != fixture.agent {
		t.Fatalf("slot = %q, want %q", attached.Slots[0].Agent, fixture.agent)
	}
	if !attached.Slots[0].Pane.YouHold {
		t.Fatal("the expected session was not given to the window")
	}
}

// TestAttachExpectedSessionRefusals: a missing session, a malformed id and a combined start all
// refuse without registering anything.
func TestAttachExpectedSessionRefusals(t *testing.T) {
	missing := "0192f2b4-9999-9999-9999-999999999999"

	cases := []struct {
		name    string
		attach  wire.Attach
		wantReg int
	}{
		{
			name:   "a session that is not running",
			attach: wire.Attach{Width: 80, Height: 24, ExpectSession: missing},
		},
		{
			name:   "an id that is not a session id",
			attach: wire.Attach{Width: 80, Height: 24, ExpectSession: "not-a-uuid"},
		},
		{
			name:   "an expectation combined with starting",
			attach: wire.Attach{Width: 80, Height: 24, ExpectSession: missing, Start: []string{"claude"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExpectFixture(t)
			w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})

			if fixture.server.handleAttach(w, tc.attach) {
				t.Fatal("a refused attach reported success")
			}
			if got := fixture.hub.count(); got != 0 {
				t.Fatalf("the window was registered anyway: %d", got)
			}
			if attached := len(fixture.session.log.all()); attached == 0 {
				t.Fatal("the fixture logged nothing, so this proves nothing")
			}
			// No hold may be taken for a refused window.
			for _, view := range fixture.session.manager.Running() {
				if holder := fixture.gate.holder(view.Agent); holder != "" {
					t.Fatalf("%s is held by %q after a refused attach", view.Agent, holder)
				}
			}
		})
	}
}

// TestAttachExpectedSessionEnded: a session the gate says ended is refused.
func TestAttachExpectedSessionEnded(t *testing.T) {
	fixture := newExpectFixture(t)

	// Latch the end in the gate without ending the manager's session, which is the window between an
	// exit and the cleanup.
	code := 1
	fixture.gate.end(fixture.agent, &code)

	w := newWindow("w1", fixture.conn, fixture.hub, &logger{out: io.Discard})
	if fixture.server.handleAttach(w, wire.Attach{Width: 80, Height: 24, ExpectSession: string(fixture.agent)}) {
		t.Fatal("an ended session was attached")
	}
	if fixture.hub.count() != 0 {
		t.Fatalf("the window was registered: %d", fixture.hub.count())
	}
}
