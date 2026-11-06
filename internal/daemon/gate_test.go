package daemon

// Untested: under GR-3 nothing here has been run. The gate is pure state, so these tests need
// no window, no pane and no socket.

import (
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// gateFixture is a gate with two sessions and a recorder for what it announced.
type gateFixture struct {
	gate        *gate
	ownerships  []wire.Ownership
	republished []string
}

func newGateFixture() *gateFixture {
	f := &gateFixture{gate: newGate()}
	f.gate.onHolderChange = func(_ protocol.AgentID, ownership wire.Ownership) {
		f.ownerships = append(f.ownerships, ownership)
	}
	return f
}

// TestGateAdmit: the holder may type, a viewer may not, and an ended session is refused
// whatever the window.
func TestGateAdmit(t *testing.T) {
	g := newGate()
	g.addSession("a1", "claude")
	g.addSession("a2", "codex")

	if err := g.admit("a1", "w1"); err != nil {
		// Nobody holds it yet, so even the first window is refused until it takes input.
		if err.Code != wire.CodeNotHolder {
			t.Fatalf("admit before any holder = %v", err)
		}
	}

	if _, err := g.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	if err := g.admit("a1", "w1"); err != nil {
		t.Fatalf("the holder was refused: %v", err)
	}
	if err := g.admit("a1", "w2"); err == nil || err.Code != wire.CodeNotHolder {
		t.Fatalf("a viewer was admitted: %v", err)
	}

	// An end latches: no holder, and every window refused for that session.
	code := 3
	if _, ok := g.end("a1", &code); !ok {
		t.Fatal("end reported no session")
	}
	if err := g.admit("a1", "w1"); err == nil || err.Code != wire.CodeSessionEnded {
		t.Fatalf("an ended session was admitted: %v", err)
	}
	if _, err := g.takeInput("a1", "w1"); err == nil || err.Code != wire.CodeSessionEnded {
		t.Fatalf("takeInput on an ended session = %v", err)
	}

	// An agent that is not a session at all.
	if err := g.admit("nope", "w1"); err == nil || err.Code != wire.CodeNoSuchSession {
		t.Fatalf("an unknown agent = %v", err)
	}
}

// TestGateTakeInputMovesTheHolder: the previous holder and every viewer are told, and the new
// holder's answer carries the authoritative ownership.
func TestGateTakeInputMovesTheHolder(t *testing.T) {
	g := newGate()
	g.addSession("a1", "claude")

	var announced []wire.Ownership
	g.onHolderChange = func(_ protocol.AgentID, ownership wire.Ownership) {
		announced = append(announced, ownership)
	}

	ownership, err := g.takeInput("a1", "w1")
	if err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	if !ownership.YouHold || ownership.Holder != "w1" || ownership.Rev == 0 {
		t.Fatalf("ownership = %+v", ownership)
	}

	second, err := g.takeInput("a1", "w2")
	if err != nil {
		t.Fatalf("second takeInput: %v", err)
	}
	if second.Rev <= ownership.Rev {
		t.Fatalf("rev = %d, want higher than %d", second.Rev, ownership.Rev)
	}
	if len(announced) != 2 {
		t.Fatalf("announcements = %+v, want two", announced)
	}
	if announced[1].Holder != "w2" || announced[1].YouHold {
		t.Fatalf("the announcement is window-specific: %+v", announced[1])
	}
}

// TestGateReleaseAll: a window leaving releases what it held, and nothing takes it.
func TestGateReleaseAll(t *testing.T) {
	g := newGate()
	g.addSession("a1", "claude")
	g.addSession("a2", "codex")
	if _, err := g.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput a1: %v", err)
	}
	if _, err := g.takeInput("a2", "w2"); err != nil {
		t.Fatalf("takeInput a2: %v", err)
	}

	g.releaseAll("w1")

	if holder := g.holder("a1"); holder != "" {
		t.Fatalf("a1 is still held by %q", holder)
	}
	if holder := g.holder("a2"); holder != "w2" {
		t.Fatalf("a2 was released too: %q", holder)
	}
	// The released session is viewable but not typeable.
	if err := g.admit("a1", "w1"); err == nil {
		t.Fatal("a released session admitted its old holder")
	}
}

// TestGateAssignOnlyUnheld: attach's assignment touches nothing that is already held.
func TestGateAssignOnlyUnheld(t *testing.T) {
	g := newGate()
	g.addSession("a1", "claude")
	g.addSession("a2", "codex")
	if _, err := g.takeInput("a2", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	if !g.assign("a1", "w2") {
		t.Fatal("assign did not give an unheld session")
	}
	if g.assign("a2", "w2") {
		t.Fatal("assign took a session that was already held")
	}
	if holder := g.holder("a2"); holder != "w1" {
		t.Fatalf("a2 = %q, want w1", holder)
	}
}

// TestGateDraftingDoesNotBumpRev: a drafting change rides the pane state, so the revision must
// stay put.
func TestGateDraftingDoesNotBumpRev(t *testing.T) {
	g := newGate()
	g.addSession("a1", "claude")
	if _, err := g.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	_, rev, _, _, _ := g.snapshot("a1")

	g.setDrafting("a1", true)

	_, after, drafting, _, _ := g.snapshot("a1")
	if after != rev {
		t.Fatalf("rev moved from %d to %d on a drafting change", rev, after)
	}
	if !drafting {
		t.Fatal("drafting was not recorded")
	}

	// The draft flag survives the holder leaving, as the contract says.
	g.releaseAll("w1")
	holder, _, stillDrafting, _, _ := g.snapshot("a1")
	if holder != "" || !stillDrafting {
		t.Fatalf("holder = %q, drafting = %t; want no holder and drafting kept", holder, stillDrafting)
	}
}

// TestGateEndLatches: ending bumps the revision once and is idempotent.
func TestGateEndLatches(t *testing.T) {
	g := newGate()
	g.addSession("a1", "claude")
	if _, err := g.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	_, before, _, _, _ := g.snapshot("a1")

	code := 7
	rev, ok := g.end("a1", &code)
	if !ok {
		t.Fatal("end reported no session")
	}
	if rev <= before {
		t.Fatalf("rev = %d, want higher than %d", rev, before)
	}
	holder, _, _, ended, exitCode := g.snapshot("a1")
	if holder != "" || !ended || exitCode == nil || *exitCode != code {
		t.Fatalf("state = %q %t %v", holder, ended, exitCode)
	}

	second, ok := g.end("a1", nil)
	if !ok || second != rev {
		t.Fatalf("second end = %d %t, want %d true", second, ok, rev)
	}
}

// TestGateRemoveSession: after the end is announced the session is forgotten, so a late request
// is no_such_session rather than session_ended.
func TestGateRemoveSession(t *testing.T) {
	g := newGate()
	g.addSession("a1", "claude")
	g.removeSession("a1")

	if g.has("a1") {
		t.Fatal("the session is still there")
	}
	if err := g.admit("a1", "w1"); err == nil || err.Code != wire.CodeNoSuchSession {
		t.Fatalf("admit = %v, want no_such_session", err)
	}
}
