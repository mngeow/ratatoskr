package router

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// RegisterAgent creates the agents row and puts the agent in the starting state.
//
// It fails on a duplicate agent ID or a duplicate display name. The name is what
// senders address, so two agents cannot share one: Send resolves a name to
// exactly one agent or refuses.
//
// UNTESTED.
func (r *Router) RegisterAgent(ctx context.Context, spec protocol.LaunchSpec) error {
	return r.call(ctx, func() error { return r.registerAgent(spec) })
}

func (r *Router) registerAgent(spec protocol.LaunchSpec) error {
	if spec.ID == "" {
		return errors.New("router: register agent: empty agent ID")
	}
	if _, exists := r.agents[spec.ID]; exists {
		return fmt.Errorf("router: register agent %q: already registered", spec.ID)
	}

	name := stripControl(spec.DisplayName)
	if name == "" {
		name = stripControl(string(spec.ID))
	}
	if _, exists := r.byName[name]; exists {
		return fmt.Errorf("router: register agent %q: duplicate display name %q", spec.ID, name)
	}

	a := &agent{
		id:           spec.ID,
		name:         name,
		harness:      spec.Harness,
		state:        protocol.AgentStarting,
		boundSession: spec.SessionID,
	}

	if err := r.cfg.Store.InsertAgent(r.ctx, store.Agent{
		ID:             a.id,
		RunID:          r.cfg.RunID,
		DisplayName:    a.name,
		Harness:        a.harness,
		HarnessVersion: stripControl(spec.HarnessVersion),
		StartedAt:      r.now(),
	}); err != nil {
		if errors.Is(err, store.ErrUnavailable) {
			r.setStorageDown(err)
		}
		return fmt.Errorf("router: register agent %q: %w", spec.ID, err)
	}

	r.agents[a.id] = a
	r.byName[a.name] = a
	r.order = append(r.order, a)
	r.emit(protocol.Event{Agent: r.agentViewPtr(a)})
	return nil
}

// PaneStarted records the writer and the moment the child process was launched.
// It only enqueues, so it applies after anything the caller submitted before it.
//
// UNTESTED.
func (r *Router) PaneStarted(id protocol.AgentID, w PaneWriter) {
	r.enqueue(func() {
		a, ok := r.agents[id]
		if !ok {
			return
		}
		a.w = w
		a.startedAt = r.now()
		r.emit(protocol.Event{Agent: r.agentViewPtr(a)})
	})
}

// PaneFailed records that the child could not be started and treats the agent as
// gone.
//
// UNTESTED.
func (r *Router) PaneFailed(id protocol.AgentID, err error) {
	r.enqueue(func() {
		a, ok := r.agents[id]
		if !ok {
			return
		}
		if err != nil {
			a.err = stripControl(err.Error())
		} else {
			a.err = "pane failed"
		}
		r.markExited(a, nil)
	})
}

// PaneExited records the child's exit code and treats the agent as gone.
//
// UNTESTED.
func (r *Router) PaneExited(id protocol.AgentID, code int) {
	r.enqueue(func() {
		a, ok := r.agents[id]
		if !ok {
			return
		}
		exitCode := code
		r.markExited(a, &exitCode)
	})
}

// markExited ends an agent: its row is closed, whatever it still held is
// resolved, and the UI is told.
//
// Nothing may be delivered to an agent that is gone, so the queue is cleared.
// A failed transition leaves that message's in-memory state as the store has it
// rather than claiming a state the store never recorded; crash recovery
// interrupts those rows at the next start.
//
// UNTESTED.
func (r *Router) markExited(a *agent, code *int) {
	if code != nil {
		exitCode := *code
		a.exitCode = &exitCode
	}
	if a.state == protocol.AgentExited {
		return
	}
	a.state = protocol.AgentExited

	if err := r.cfg.Store.EndAgent(r.ctx, a.id, a.exitCode, r.now()); err != nil && errors.Is(err, store.ErrUnavailable) {
		r.setStorageDown(err)
	}

	queued := a.queue
	a.queue = nil
	for _, m := range queued {
		_ = r.move(m, protocol.MessageFailed, "recipient_exited")
	}

	// A message handed to a pane that has now gone is not failed: whether the
	// harness received it is unknown, so it is unconfirmed and can still be
	// credited by a late submit.
	if m := a.inflight; m != nil {
		a.inflight = nil
		a.earlyConfirm = false
		a.confirmDeadline = time.Time{}
		if err := r.move(m, protocol.MessageUnconfirmed, "recipient_exited"); err == nil {
			r.rememberUnconfirmed(a, m)
		}
	}

	r.emit(protocol.Event{Agent: r.agentViewPtr(a)})
}
