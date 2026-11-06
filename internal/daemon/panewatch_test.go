package daemon

// Untested: under GR-3 nothing here has been run. A fake pane stands in for the PTY, so nothing
// is executed and no frame is rendered.

import (
	"errors"
	"io"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// TestPaneWatcherPublishesOnlyChanges: the state is built from the pane, and the hub coalesces
// repeats.
func TestPaneWatcherPublishesOnlyChanges(t *testing.T) {
	fake := &fakePane{ready: true, cols: 80, rows: 24, mouse: 1}
	gate := newGate()
	gate.addSession("a1", "claude")
	h := &hub{gate: gate, log: &logger{out: io.Discard}, panes: make(map[protocol.AgentID]wire.PaneState, 1)}

	watcher := newPaneWatcher(sessionView{Name: "claude", Agent: "a1", Pane: fake}, h)
	state := watcher.state(mustSnapshot(t, watcher))

	if state.Agent != "a1" || state.Cols != 80 || state.Rows != 24 || state.Mouse != 1 || !state.InputReady {
		t.Fatalf("state = %+v", state)
	}
	if state.YouHold {
		t.Fatal("the watcher decided you_hold; the hub must")
	}

	// A transport error is part of the frame.
	fake.err = errors.New("write failed")
	state = watcher.state(mustSnapshot(t, watcher))
	if state.Err == "" {
		t.Fatal("the pane's error did not reach the frame")
	}
}

// TestEndedPaneStateIsComplete: the ended state carries the frame and the exit code, and the hub
// adds the holder facts.
func TestEndedPaneStateIsComplete(t *testing.T) {
	fake := &fakePane{ready: true, cols: 100, rows: 40, mouse: 3}
	fake.frame = pane.Frame{Content: "last frame"}
	gate := newGate()
	gate.addSession("a1", "claude")
	h := &hub{gate: gate, log: &logger{out: io.Discard}, panes: make(map[protocol.AgentID]wire.PaneState, 1)}

	code := 2
	state := endedPaneState(h, sessionView{Name: "claude", Agent: "a1", Pane: fake}, &code)
	if !state.Ended || state.ExitCode == nil || *state.ExitCode != 2 {
		t.Fatalf("state = %+v", state)
	}
	if state.Frame.Content != "last frame" || state.Cols != 100 {
		t.Fatalf("the last frame was lost: %+v", state)
	}

	h.publishPane(state)
	_, rev, _, ended, _ := gate.snapshot("a1")
	if rev == 0 {
		t.Fatal("the published state has no revision")
	}
	if !ended {
		t.Fatal("the gate does not know the session ended")
	}
}

// TestObserverStartsAndStopsWatchers: start creates one, an exit latches the gate and sends the
// notice, and an end forgets the session.
func TestObserverStartsAndStopsWatchers(t *testing.T) {
	gate := newGate()
	h := newHub(&logger{out: io.Discard}, gate)
	h.panes = make(map[protocol.AgentID]wire.PaneState, 1)
	observer := newPaneObserver(h, &logger{out: io.Discard})

	holder := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	holder.paused = false
	h.register(holder)

	fake := &fakePane{ready: true, cols: 80, rows: 24}
	view := sessionView{Name: "claude", Agent: "a1", Pane: fake}

	observer.sessionStarted(view)
	if !gate.has("a1") {
		t.Fatal("the gate was not opened for the new session")
	}

	code := 5
	observer.sessionExited(view, &code)

	waitFor(t, "the ended state", func() bool {
		_, _, _, ended, _ := gate.snapshot("a1")
		return ended
	})
	if _, _, _, ended, exitCode := gate.snapshot("a1"); !ended || exitCode == nil || *exitCode != code {
		t.Fatalf("the exit was not latched")
	}

	// The reliable end notice reached the window.
	waitFor(t, "the session_ended message", func() bool { return len(holder.queue) > 0 })
	sawEnded := false
	for len(holder.queue) > 0 {
		if message := <-holder.queue; message.typ == wire.TypeSessionEnded {
			sawEnded = true
		}
	}
	if !sawEnded {
		t.Fatal("no session_ended message was sent")
	}

	observer.sessionEnded(view)
	if gate.has("a1") {
		t.Fatal("the session is still in the gate after it ended")
	}
	// The observer tolerates both calls for one session, which is what an exit does.
	observer.sessionEnded(view)
}

// mustSnapshot reads a watcher's snapshot state.
func mustSnapshot(t *testing.T, watcher *paneWatcher) paneSnapshot {
	t.Helper()
	state, ok := watcher.snapshot()
	if !ok {
		t.Fatal("the watcher could not read its pane")
	}
	return state
}
