package daemon

// Attaching a window: validate the request, register the window, start any session it asked
// for, and answer with the whole starting picture.
//
// The order matters. The window is registered first, so a session that starts during this call
// already has somewhere for its frames to go; it is paused, so nothing is written before the
// attached answer. Then the requested sessions start sequentially under one shared deadline, and
// every session nobody holds is given to this window before the slots are built, so the window
// sees you_hold for what it now holds.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No window has attached.

import (
	"context"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// attachTimeout is the shared budget for starting the sessions one attach asks for. It
	// covers every name together, so a slow gateway cannot hold an attach open indefinitely.
	attachTimeout = 30 * time.Second // UNTESTED guess
)

// handleAttach validates and serves one attach request.
//
// A validation failure is a refusal and the connection closes; everything after the window is
// registered is reported inside the attached answer, so one failed session does not cost the
// window the others.
//
// UNTESTED.
func (s *daemonServer) handleAttach(w *window, request wire.Attach) bool {
	if message, ok := s.validateAttach(request); !ok {
		s.refuseOpen(w.conn, wire.CodeBadRequest, message)
		return false
	}

	// Registered first, and still paused: the hub may queue messages now, and they go after
	// the attached answer.
	s.hub.register(w)

	startErrors := s.startAttachSessions(w, request)

	// Every session nobody holds belongs to the window that just arrived, so one window needs
	// no keypress per session.
	slots := make([]wire.Slot, 0, 2)
	for _, view := range s.manager.Running() {
		w.addSession(view)
		s.hub.gate.assign(view.Agent, w.id)

		holder, rev, drafting, ended, exitCode := s.hub.gate.snapshot(view.Agent)
		state := wire.PaneState{
			Agent:    view.Agent,
			Rev:      rev,
			Holder:   holder,
			YouHold:  holder == w.id,
			Drafting: drafting,
			Ended:    ended,
			ExitCode: exitCode,
		}
		if view.Pane != nil {
			state.Frame = view.Pane.Snapshot()
			state.Cols, state.Rows = view.Pane.Size()
			state.Mouse = view.Pane.MouseModeBits()
			state.InputReady = view.Pane.InputReady()
			if err := view.Pane.Err(); err != nil {
				state.Err = sanitizeMessage(err.Error())
			}
		}

		slots = append(slots, wire.Slot{
			Agent:   view.Agent,
			Name:    view.Name,
			Address: view.Address,
			Pane:    state,
		})
	}

	snapshot := s.routerSnapshot()
	pending := s.pendingCount()

	if !w.sendAttached(wire.Attached{
		WindowID:    w.id,
		Slots:       slots,
		StartErrors: startErrors,
		Snapshot:    snapshot,
		Pending:     pending,
	}) {
		// The window went away during a long attach. Its close already ran releaseAll, before
		// the assignment above gave it sessions, so they are released again: no session may be
		// held by a window that is gone.
		s.hub.gate.releaseAll(w.id)
		return false
	}

	w.startWriter()
	s.log.logf("window %s attached: %d slots, %d start errors", w.id, len(slots), len(startErrors))
	return true
}

// validateAttach checks what can be checked without touching anything.
func (s *daemonServer) validateAttach(request wire.Attach) (string, bool) {
	if request.Width < 1 || request.Height < 1 {
		return "the window's size must be at least 1x1", false
	}
	for _, name := range request.Start {
		if !s.manager.startable(name) {
			return "this build can start claude and codex only", false
		}
	}
	return "", true
}

// startAttachSessions starts the sessions the attach asked for, in order.
//
// A session that is already running is not an error: the window simply joins it. Every other
// failure is reported to the window beside the sessions that did start.
func (s *daemonServer) startAttachSessions(w *window, request wire.Attach) []wire.StartError {
	if len(request.Start) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), attachTimeout)
	defer cancel()

	var startErrors []wire.StartError
	for _, name := range request.Start {
		_, err := s.manager.Add(ctx, name, request.Dir, request.Width, request.Height)
		switch {
		case err == nil:
			continue
		case isAlreadyRunning(err):
			// Joining a running session is what attach means.
			continue
		}
		startErrors = append(startErrors, wire.StartError{Name: name, Message: err.Error()})
	}
	return startErrors
}

// routerSnapshot is the router's state, or an empty one when a test has no router.
func (s *daemonServer) routerSnapshot() protocol.Snapshot {
	if s.router == nil {
		return protocol.Snapshot{}
	}
	return s.router.Snapshot()
}

// pendingCount is the count of messages that would be lost on quit.
func (s *daemonServer) pendingCount() int {
	if s.router == nil {
		return 0
	}
	return s.router.PendingOnQuit()
}
