package router

import (
	"context"
	"errors"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// UserDraft records whether the user is mid-composition in that agent's pane.
//
// The router cannot see keystrokes, so it trusts the UI, which calls this before
// forwarding input. `true` holds deliveries; only `false` can release them, so a
// UI that forgets to send `false` leaves that agent's messages held as
// "drafting" - there is no router-side draft timeout.
//
// UNTESTED.
func (r *Router) UserDraft(id protocol.AgentID, drafting bool) {
	r.enqueue(func() {
		a, ok := r.agents[id]
		if !ok {
			return
		}
		a.drafting = drafting
		if !drafting {
			r.pump(a, false)
		}
	})
}

// UserSubmitted resets the relay budget and gives the queue a chance to move.
//
// It deliberately does not touch the draft flag: the UI may still have later
// keystrokes queued, and only the UI knows when the pane is really quiet. It
// does not touch a message's state either - the hook event does that.
//
// UNTESTED.
func (r *Router) UserSubmitted(id protocol.AgentID) {
	r.enqueue(func() {
		a, ok := r.agents[id]
		if !ok {
			return
		}
		r.used = 0
		r.emit(protocol.Event{Budget: r.budgetViewPtr()})
		r.pump(a, false)
	})
}

// DeliverHeld is the user's explicit deliver-anyway for one agent. The gate still
// decides: this only asks, and does nothing unless the override applies (unknown,
// or idle over a draft; design v0.10). When it does apply, the draft flag is
// cleared, because the user has just chosen to send over it.
//
// UNTESTED.
func (r *Router) DeliverHeld(id protocol.AgentID) {
	r.enqueue(func() {
		a, ok := r.agents[id]
		if !ok {
			return
		}
		if len(a.queue) > 0 && overrideOK(r.gateInputFor(a)) {
			a.drafting = false
		}
		r.pump(a, true)
	})
}

// gateInputFor is the gate's view of one agent.
func (r *Router) gateInputFor(a *agent) gateInput {
	return gateInput{
		state:    a.state,
		drafting: a.drafting,
		inFlight: a.inflight != nil,
		hasPane:  a.w != nil,
	}
}

// pump is the delivery decision for one agent. It runs only in the actor, after
// a send, a hook event, a draft clearing, a write result, a tick or F4.
//
// It looks only at the head of the queue: messages are delivered in order, and a
// held head holds everything behind it. The delivery commit happens before any
// byte is written - if it fails, nothing is written - and the write itself is
// handed to a goroutine, so the actor never touches a pane.
//
// UNTESTED.
func (r *Router) pump(a *agent, override bool) {
	if a == nil || r.storageDown || a.inflight != nil || len(a.queue) == 0 {
		return
	}

	head := a.queue[0]
	in := r.gateInputFor(a)

	ok, hold := gate(in)
	if override && overrideOK(in) {
		ok = true
	}
	if !ok {
		r.holdMessage(head, hold)
		return
	}

	// The commit must land before the write goroutine exists: a message that is
	// not recorded as delivering is a message nobody may write.
	if err := r.move(head, protocol.MessageDelivering, ""); err != nil {
		return
	}

	a.queue = a.queue[1:]
	a.inflight = head
	a.earlyConfirm = false
	killPoint("afterDelivering") // rtktest only: acceptance check 10
	r.startWrite(a, head)
}

// holdMessage records why a message is being held.
//
// The reason is committed only on the first queued-to-held edge; while the
// message stays held, a different reason is updated in memory and shown to the
// UI, because re-transitioning on every keystroke would be a write per event.
//
// UNTESTED.
func (r *Router) holdMessage(m *msg, reason string) {
	switch m.state {
	case protocol.MessageQueued:
		if err := r.move(m, protocol.MessageHeld, reason); err != nil {
			// The store would not take the hold. The message stays queued and the
			// next pump tries again.
			return
		}
	case protocol.MessageHeld:
		if m.reason == reason {
			return
		}
		m.reason = reason
		r.emit(protocol.Event{Message: r.messageViewPtr(m)})
	}
}

// startWrite hands one message to a pane.
//
// The goroutine owns the PTY call and the timeout; it returns to the actor with
// submit, so the result is applied in order with everything else. If the router
// closed meanwhile, the result is dropped: there is no state left to update.
//
// UNTESTED.
func (r *Router) startWrite(a *agent, m *msg) {
	w := a.w
	if w == nil {
		// pump only reaches here when the gate saw a pane; this is defensive.
		return
	}

	frame := buildFrame(m.from.name, m.id, m.replyTo, m.body)
	go func() {
		ctx, cancel := context.WithTimeout(r.ctx, r.cfg.WriteTimeout)
		err := w.PasteSubmit(ctx, frame)
		cancel()
		if err == nil {
			killPoint("afterPasteWrite") // rtktest only: acceptance check 10
		}
		_ = r.submit(r.ctx, func() { r.onWriteResult(a, m, err) })
	}()
}

// onWriteResult applies the outcome of one paste.
//
// A result for a message that is no longer in flight is dropped: the agent may
// have exited, and a late write must not resurrect a finished message.
//
// The message stays in flight after a successful paste: it is pasted, not yet
// confirmed, and only an accepted or unconfirmed outcome frees the slot. That is
// what keeps one message per recipient in delivering or pasted, so a confirmation
// can never be credited to the wrong message.
//
// UNTESTED.
func (r *Router) onWriteResult(a *agent, m *msg, err error) {
	if a.inflight != m {
		return
	}

	if err != nil {
		a.inflight = nil
		_ = r.move(m, protocol.MessageFailed, "write_failed:"+writeFailureDetail(err))
		r.pump(a, false)
		return
	}

	// If this commit fails the store is down: the message stays in flight as
	// "delivering" (what the store has), and recovery interrupts it.
	if moveErr := r.move(m, protocol.MessagePasted, ""); moveErr != nil {
		return
	}
	killPoint("afterPasted") // rtktest only: acceptance check 10
	a.confirmDeadline = r.now().Add(r.cfg.ConfirmTimeout)

	// A prompt submitted before the write finished: the paste is on the wire, so
	// the submit that followed it belongs to this message. There is no
	// delivering-to-accepted edge; it is applied here, after pasted committed.
	if a.earlyConfirm {
		a.earlyConfirm = false
		if r.move(m, protocol.MessageAccepted, "prompt_submitted") == nil {
			a.inflight = nil
			r.pump(a, false)
		}
	}
}

// writeFailureDetail says whether any bytes reached the pane. The pane's error
// exposes this structurally, so neither package has to import the other.
//
// UNTESTED.
func writeFailureDetail(err error) string {
	var written interface{ BytesWritten() bool }
	if errors.As(err, &written) {
		if written.BytesWritten() {
			return "partial_write"
		}
		return "no_bytes_written"
	}
	return "unknown_if_written"
}

// tick is the timer pass: startup and busy timeouts, then the confirmation
// deadline for a pasted message.
//
// Time comes from the router's own clock, never from a harness payload.
//
// UNTESTED.
func (r *Router) tick(now time.Time) {
	for _, a := range r.order {
		switch a.state {
		case protocol.AgentStarting:
			// startedAt is zero until a pane exists, and a zero start never
			// times out: nothing has been launched yet.
			if !a.startedAt.IsZero() && now.Sub(a.startedAt) > r.cfg.StartupTimeout {
				a.state = protocol.AgentUnknown
				r.setBanner(r.hooksBannerKey(a), noHooksBanner(a.name))
				r.emit(protocol.Event{Agent: r.agentViewPtr(a)})
			}
		case protocol.AgentBusy:
			if now.Sub(a.lastEvent) > r.cfg.BusyTimeout {
				a.state = protocol.AgentUnknown
				r.emit(protocol.Event{Agent: r.agentViewPtr(a)})
			}
		}

		m := a.inflight
		if m == nil || m.state != protocol.MessagePasted || a.confirmDeadline.IsZero() || !now.After(a.confirmDeadline) {
			continue
		}
		// The paste landed, but no submit was seen in time.
		a.inflight = nil
		if err := r.move(m, protocol.MessageUnconfirmed, "no_confirmation"); err == nil {
			r.rememberUnconfirmed(a, m)
		}
		r.pump(a, false)
	}
}
