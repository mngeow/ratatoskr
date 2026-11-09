package sidecar

// One offer, in order (contract section 8.3). Every budget runs from t0, the instant the poll answer was read,
// because the gateway's 5 s started before the answer travelled here. Nothing is typed unless the
// acknowledgement was answered 200 and the instant is still before t0 + ackBudget.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/gw"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/journal"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/render"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/rigcli"
)

func (s *Sidecar) count(f func(*stats)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.stats)
}

// refuse acknowledges saved:false with a reason, inside the window. The reason is one of the contract's fixed
// words and never includes message text.
func (s *Sidecar) refuse(ctx context.Context, o gw.Offer, t0 time.Time, reason string) {
	s.count(func(st *stats) {
		st.Refused++
		st.LastInboundAt, st.LastInboundID, st.LastInboundState = s.now(), o.ID, "refused: "+reason
	})
	s.logf("refused %s: %s", o.ID, reason)
	ackCtx, cancel := context.WithDeadline(ctx, t0.Add(ackBudget))
	defer cancel()
	if e := s.GW.Ack(ackCtx, o.ID, false, reason); e != nil {
		s.logf("the refusal of %s was not acknowledged: %s", o.ID, e.Msg)
	}
}

func (s *Sidecar) handleOffer(ctx context.Context, o gw.Offer, t0 time.Time) {
	s.count(func(st *stats) { st.Received++ })

	// Step 1: validate. An invalid id gets no acknowledgement (the sender sees delivery_uncertain).
	if !render.ValidID(o.ID) {
		s.logf("dropped an offer with an invalid id")
		s.count(func(st *stats) { st.Refused++ })
		return
	}
	var body string
	if json.Unmarshal(o.Body, &body) != nil {
		s.refuse(ctx, o, t0, "malformed offer")
		return
	}
	text, err := render.Render(render.Offer{ID: o.ID, From: o.From, ReplyTo: o.ReplyTo, Body: body})
	if err != nil {
		s.refuse(ctx, o, t0, "malformed offer")
		return
	}

	// Step 2: never type an id twice. Repeat an acknowledgement that was already given.
	if last, seen := s.journal.Last(o.ID); seen {
		switch last.Event {
		case journal.Acked, journal.Typing, journal.Pasted, journal.Failed, journal.Unconfirmed, journal.Expired:
			ackCtx, cancel := context.WithDeadline(ctx, t0.Add(ackBudget))
			_ = s.GW.Ack(ackCtx, o.ID, true, "")
			cancel()
		}
		s.logf("duplicate offer %s ignored", o.ID)
		return
	}

	// Steps 3 and 4: size and rate.
	if reason, ok := s.limiter.Admit(o.From, body); !ok {
		s.refuse(ctx, o, t0, reason)
		return
	}

	// Step 5: the typing slot.
	slotTimer := time.NewTimer(t0.Add(slotDeadline).Sub(s.now()))
	defer slotTimer.Stop()
	select {
	case s.slot <- struct{}{}:
	case <-slotTimer.C:
		s.refuse(ctx, o, t0, "bridge busy")
		return
	case <-ctx.Done():
		return
	}
	var once sync.Once
	release := func() { once.Do(func() { <-s.slot }) }
	defer release()

	// Step 6: the seat must be ready. Busy is ready.
	readyCtx, cancel := context.WithDeadline(ctx, t0.Add(readyDeadline))
	ready, word := s.Seat.Ready(readyCtx)
	cancel()
	if !ready {
		s.refuse(ctx, o, t0, "operator seat not ready: "+word)
		return
	}

	// Step 7: journal, then check the deadline.
	if err := s.journal.Append(journal.Record{ID: o.ID, Event: journal.Accepted, From: o.From}); err != nil {
		s.logf("journal: %v", err)
		s.refuse(ctx, o, t0, "bridge journal unavailable")
		return
	}
	if s.now().After(t0.Add(journalDeadline)) {
		_ = s.journal.Append(journal.Record{ID: o.ID, Event: journal.Refused, Reason: "journal too slow"})
		s.refuse(ctx, o, t0, "bridge journal unavailable")
		return
	}

	// Step 8: acknowledge saved:true, complete by t0 + 4 s.
	ackCtx, ackCancel := context.WithDeadline(ctx, t0.Add(ackBudget))
	e := s.GW.Ack(ackCtx, o.ID, true, "")
	ackCancel()
	if e != nil {
		s.logf("the acknowledgement of %s failed (%s): not typing", o.ID, e.Msg)
		_ = s.journal.Append(journal.Record{ID: o.ID, Event: journal.AckFailed, Reason: "acknowledgement not confirmed"})
		s.count(func(st *stats) {
			st.Failed++
			st.LastInboundAt, st.LastInboundID, st.LastInboundState = s.now(), o.ID, "ack failed, not typed"
		})
		return
	}
	s.limiter.Accepted()
	if err := s.journal.Append(journal.Record{ID: o.ID, Event: journal.Acked, From: o.From}); err != nil {
		s.logf("journal: %v", err)
	}

	// Step 9: the typing precondition.
	if !s.now().Before(t0.Add(ackBudget)) {
		s.expire(ctx, o.ID, release)
		return
	}

	// Step 10: record typing (fsync), look at the clock once more, then run rig send.
	if err := s.journal.Append(journal.Record{ID: o.ID, Event: journal.Typing}); err != nil {
		// Without a durable typing record a crash could cause a second paste: do not type.
		s.logf("journal: %v: not typing %s", err, o.ID)
		s.expire(ctx, o.ID, release)
		return
	}
	if !s.now().Before(t0.Add(ackBudget)) {
		s.expire(ctx, o.ID, release)
		return
	}
	result := s.Seat.Send(context.WithoutCancel(ctx), text)

	// Step 11: record, release the slot, then report.
	var event journal.Event
	var state, reason string
	switch result.Outcome {
	case rigcli.Pasted:
		event, state = journal.Pasted, "pasted"
	case rigcli.Failed:
		event, state, reason = journal.Failed, "failed", result.Note
	default:
		event, state, reason = journal.Unconfirmed, "unconfirmed", result.Note
	}
	if err := s.journal.Append(journal.Record{ID: o.ID, Event: event, Reason: reason}); err != nil {
		s.logf("journal: %v", err)
	}
	s.logf("rig send for %s: %s (exit %d) %s", o.ID, state, result.ExitCode, result.Note)
	s.count(func(st *stats) {
		switch state {
		case "pasted":
			st.Pasted++
		case "failed":
			st.Failed++
		default:
			st.Unconfirmed++
		}
		st.LastInboundAt, st.LastInboundID, st.LastInboundState = s.now(), o.ID, state
	})
	release()
	reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*statusRetryGap+10*time.Second)
	defer reportCancel()
	s.reportStatus(reportCtx, o.ID, state, reason)
}

// expire handles an acknowledged offer whose typing window has passed: it is not typed.
func (s *Sidecar) expire(ctx context.Context, id string, release func()) {
	const reason = "window elapsed before typing"
	_ = s.journal.Append(journal.Record{ID: id, Event: journal.Expired, Reason: reason})
	s.logf("%s: %s", id, reason)
	s.count(func(st *stats) {
		st.Failed++
		st.LastInboundAt, st.LastInboundID, st.LastInboundState = s.now(), id, "failed: "+reason
	})
	release()
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*statusRetryGap+10*time.Second)
	defer cancel()
	s.reportStatus(reportCtx, id, "failed", reason)
}
