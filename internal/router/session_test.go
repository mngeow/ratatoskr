package router

// Untested: under GR-3 nothing here has been run. The router's existing fakes are used, so
// no store, pane or socket is involved.

import (
	"context"
	"errors"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// sessionSpec is a launch spec with its own harness, so it is clear which one a test means.
func sessionSpec(id protocol.AgentID, name, harness string) protocol.LaunchSpec {
	return protocol.LaunchSpec{
		ID:          id,
		DisplayName: name,
		Harness:     harness,
		Dir:         "/tmp",
	}
}

// TestAddSessionRefusesALiveDuplicate: a live agent of that name is still refused by
// registerAgent, so two harnesses cannot share a name.
func TestAddSessionRefusesALiveDuplicate(t *testing.T) {
	r, fake, _ := newBareRouter(t)
	r.Start()
	t.Cleanup(r.Close)

	if err := r.AddSession(context.Background(), sessionSpec("a1", "claude", "claude")); err != nil {
		t.Fatalf("first AddSession: %v", err)
	}
	// The agent is starting, not exited, so the second add is a duplicate.
	if err := r.AddSession(context.Background(), sessionSpec("a2", "claude", "claude")); err == nil {
		t.Fatal("AddSession replaced a live agent")
	}

	if got := len(fake.callLog()); got == 0 {
		t.Fatal("nothing reached the store")
	}
}

// TestAddSessionReplacesAnExitedAgent: the new agent takes the name, and the old one is
// still resolvable by ID.
func TestAddSessionReplacesAnExitedAgent(t *testing.T) {
	r, _, _ := newBareRouter(t)
	r.Start()
	t.Cleanup(r.Close)

	if err := r.AddSession(context.Background(), sessionSpec("a1", "claude", "claude")); err != nil {
		t.Fatalf("first AddSession: %v", err)
	}
	if err := r.RegisterAgent(context.Background(), protocol.LaunchSpec{
		ID: "b1", DisplayName: "codex", Harness: "codex", Dir: "/tmp",
	}); err != nil {
		t.Fatalf("RegisterAgent codex: %v", err)
	}

	// The old session ends: its pane exited.
	r.PaneExited("a1", 0)

	if err := r.AddSession(context.Background(), sessionSpec("a2", "claude", "claude")); err != nil {
		t.Fatalf("replacing AddSession: %v", err)
	}

	// The snapshot shows the replacement once, and the old ID is gone from the order.
	snapshot := r.Snapshot()
	var claudes int
	for _, view := range snapshot.Agents {
		if view.DisplayName == "claude" {
			claudes++
			if view.ID != "a2" {
				t.Fatalf("the snapshot shows the replaced agent %q", view.ID)
			}
		}
	}
	if claudes != 1 {
		t.Fatalf("the snapshot shows %d agents named claude, want 1", claudes)
	}

	// The old agent is still addressable by ID, which is what keeps an old message's
	// recipient resolvable.
	found := false
	for _, view := range snapshot.Agents {
		if view.ID == "a1" {
			found = true
		}
	}
	if !found {
		t.Fatal("the replaced agent is no longer in the snapshot at all")
	}
}

// TestAddSessionReplacesOnlySameName: another name is untouched.
func TestAddSessionReplacesOnlySameName(t *testing.T) {
	r, _, _ := newBareRouter(t)
	r.Start()
	t.Cleanup(r.Close)

	if err := r.AddSession(context.Background(), sessionSpec("a1", "claude", "claude")); err != nil {
		t.Fatalf("AddSession claude: %v", err)
	}
	if err := r.AddSession(context.Background(), sessionSpec("b1", "codex", "codex")); err != nil {
		t.Fatalf("AddSession codex: %v", err)
	}

	// Ending codex and adding it again leaves claude exactly as it was.
	r.PaneExited("b1", 0)
	if err := r.AddSession(context.Background(), sessionSpec("b2", "codex", "codex")); err != nil {
		t.Fatalf("replacing AddSession codex: %v", err)
	}

	snapshot := r.Snapshot()
	names := make(map[string]string, len(snapshot.Agents))
	for _, view := range snapshot.Agents {
		if view.DisplayName == "claude" || view.DisplayName == "codex" {
			names[view.DisplayName] = string(view.ID)
		}
	}
	if names["claude"] != "a1" {
		t.Fatalf("claude = %q, want a1", names["claude"])
	}
	if names["codex"] != "b2" {
		t.Fatalf("codex = %q, want b2", names["codex"])
	}
}

// TestAddSessionOnAClosedRouter: closing wins, as it does for RegisterAgent.
func TestAddSessionOnAClosedRouter(t *testing.T) {
	r, _, _ := newBareRouter(t)
	r.Start()
	r.Close()

	if err := r.AddSession(context.Background(), sessionSpec("a1", "claude", "claude")); !errors.Is(err, ErrClosed) {
		t.Fatalf("AddSession on a closed router = %v, want ErrClosed", err)
	}
}
