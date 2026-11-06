package daemon

// Untested: under GR-3 nothing here has been run. Attach is driven against fakes: no socket, no
// harness and no pane.

import (
	"context"
	"io"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// attachFixture builds a daemonServer whose manager uses the session fakes.
type attachFixture struct {
	server  *daemonServer
	session *sessionFixture
	hub     *hub
	gate    *gate
	conn    *recordingConn
}

func newAttachFixture(t *testing.T) *attachFixture {
	t.Helper()

	session := newSessionFixture(t)
	session.connect.connected = true

	// A directory the fake link answers with, so the slots are predictable.
	session.link.directory = []protocol.DirectoryEntry{{Address: "claude@mac"}, {Address: "codex@ubuntu-b"}}

	gate := newGate()
	h := newHub(&logger{out: io.Discard}, gate)
	h.router = &fakeWindowRouter{}
	session.manager.setObserver(newPaneObserver(h, &logger{out: io.Discard}))

	server := &daemonServer{
		build:     "test",
		log:       &logger{out: io.Discard},
		sessions:  session.manager,
		harnesses: session.manager,
		hub:       h,
		manager:   session.manager,
	}
	return &attachFixture{server: server, session: session, hub: h, gate: gate, conn: &recordingConn{}}
}

// window builds a window for the fixture and attaches it.
func (f *attachFixture) attach(t *testing.T, request wire.Attach) (*window, bool) {
	t.Helper()
	w := newWindow("w1", f.conn, f.hub, &logger{out: io.Discard})
	return w, f.server.handleAttach(w, request)
}

// TestAttachStartsInOrderAndRecordsErrors: the sessions start in the requested order, an error
// is reported beside the ones that worked, and an already running session is skipped silently.
func TestAttachStartsInOrderAndRecordsErrors(t *testing.T) {
	f := newAttachFixture(t)

	// The second add fails at the gateway, so the window sees a StartError and no slot.
	f.session.link.mu.Lock()
	f.session.link.addErr = nil
	f.session.link.mu.Unlock()

	w, ok := f.attach(t, wire.Attach{Dir: t.TempDir(), Start: []string{"claude", "codex"}, Width: 80, Height: 24})
	if !ok {
		t.Fatal("attach failed")
	}

	calls := f.session.log.all()
	if len(calls) == 0 {
		t.Fatal("no session was started")
	}
	// claude is started before codex.
	claudeAt, codexAt := -1, -1
	for i, call := range calls {
		if call == "gateway:add:claude" {
			claudeAt = i
		}
		if call == "gateway:add:codex" {
			codexAt = i
		}
	}
	if claudeAt < 0 || codexAt < 0 || claudeAt > codexAt {
		t.Fatalf("start order = %v", calls)
	}

	attached := firstAttached(t, w)
	if len(attached.StartErrors) != 0 {
		t.Fatalf("start errors = %+v, want none", attached.StartErrors)
	}
	if len(attached.Slots) != 2 {
		t.Fatalf("slots = %+v, want both sessions", attached.Slots)
	}
	if attached.Slots[0].Name != "claude" || attached.Slots[1].Name != "codex" {
		t.Fatalf("slot order = %s, %s", attached.Slots[0].Name, attached.Slots[1].Name)
	}
	if !attached.Slots[0].Pane.YouHold {
		t.Fatal("an unheld session was not given to the attaching window")
	}
	if attached.WindowID != "w1" {
		t.Fatalf("window id = %q", attached.WindowID)
	}
}

// TestAttachAlreadyRunningIsSkipped: joining a running session is what attach means, so it is not
// a start error.
func TestAttachAlreadyRunningIsSkipped(t *testing.T) {
	f := newAttachFixture(t)
	if _, err := f.session.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add: %v", err)
	}

	w, ok := f.attach(t, wire.Attach{Dir: t.TempDir(), Start: []string{"claude"}, Width: 80, Height: 24})
	if !ok {
		t.Fatal("attach failed")
	}
	attached := firstAttached(t, w)
	if len(attached.StartErrors) != 0 {
		t.Fatalf("start errors = %+v, want none for an already running session", attached.StartErrors)
	}
	if len(attached.Slots) != 1 || attached.Slots[0].Name != "claude" {
		t.Fatalf("slots = %+v", attached.Slots)
	}
}

// TestAttachRecordsAStartError: a session that cannot start appears as a start error and not as
// a slot.
func TestAttachRecordsAStartError(t *testing.T) {
	f := newAttachFixture(t)
	f.session.link.mu.Lock()
	f.session.link.addErr = errAddRefused
	f.session.link.mu.Unlock()

	w, ok := f.attach(t, wire.Attach{Dir: t.TempDir(), Start: []string{"claude"}, Width: 80, Height: 24})
	if !ok {
		t.Fatal("attach failed")
	}
	attached := firstAttached(t, w)
	if len(attached.StartErrors) != 1 || attached.StartErrors[0].Name != "claude" {
		t.Fatalf("start errors = %+v", attached.StartErrors)
	}
	if len(attached.Slots) != 0 {
		t.Fatalf("slots = %+v, want none", attached.Slots)
	}
}

// TestAttachZeroSlotsIsFine: an attach with nothing to start and nothing running is a normal
// attach with no slots.
func TestAttachZeroSlotsIsFine(t *testing.T) {
	f := newAttachFixture(t)

	w, ok := f.attach(t, wire.Attach{Dir: t.TempDir(), Width: 80, Height: 24})
	if !ok {
		t.Fatal("attach with no sessions failed")
	}
	attached := firstAttached(t, w)
	if len(attached.Slots) != 0 || len(attached.StartErrors) != 0 {
		t.Fatalf("attached = %+v", attached)
	}
}

// TestAttachRefusals: a bad size or an unknown name is refused before the window is registered.
func TestAttachRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request wire.Attach
	}{
		{name: "no width", request: wire.Attach{Dir: "/tmp", Width: 0, Height: 24}},
		{name: "no height", request: wire.Attach{Dir: "/tmp", Width: 80, Height: 0}},
		{name: "unknown agent", request: wire.Attach{Dir: "/tmp", Start: []string{"gemini"}, Width: 80, Height: 24}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAttachFixture(t)
			w := newWindow("w1", f.conn, f.hub, &logger{out: io.Discard})
			if f.server.handleAttach(w, tc.request) {
				t.Fatal("a refused attach reported success")
			}
			if f.hub.count() != 0 {
				t.Fatalf("the window was registered anyway: %d", f.hub.count())
			}
			if len(f.session.log.all()) != 0 {
				t.Fatalf("a refused attach started something: %v", f.session.log.all())
			}
		})
	}
}

// TestAttachGivesOnlyUnheldSessions: a session another window holds is not taken.
func TestAttachGivesOnlyUnheldSessions(t *testing.T) {
	f := newAttachFixture(t)
	if _, err := f.session.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add: %v", err)
	}
	view := f.session.manager.Running()[0]
	if _, err := f.gate.takeInput(view.Agent, "w0"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	w, ok := f.attach(t, wire.Attach{Dir: t.TempDir(), Width: 80, Height: 24})
	if !ok {
		t.Fatal("attach failed")
	}
	attached := firstAttached(t, w)
	if len(attached.Slots) != 1 {
		t.Fatalf("slots = %+v", attached.Slots)
	}
	if attached.Slots[0].Pane.YouHold {
		t.Fatal("the attaching window was given a session another window holds")
	}
	if attached.Slots[0].Pane.Holder != "w0" {
		t.Fatalf("holder = %q, want w0", attached.Slots[0].Pane.Holder)
	}
}

// firstAttached takes the attached answer off a window's queue.
func firstAttached(t *testing.T, w *window) wire.Attached {
	t.Helper()
	select {
	case message := <-w.queue:
		if message.typ != wire.TypeAttached {
			t.Fatalf("first message = %q, want attached", message.typ)
		}
		attached, ok := message.body.(wire.Attached)
		if !ok {
			t.Fatalf("attached body = %T", message.body)
		}
		return attached
	default:
		t.Fatal("no attached answer was queued")
		return wire.Attached{}
	}
}

// errAddRefused is the gateway failure an attach test injects.
var errAddRefused = errorString("the gateway refused")

// errorString is a tiny error type, so a test needs no import for one error.
type errorString string

func (e errorString) Error() string { return string(e) }
