package gateway

// The recipient's side of the wire: acknowledgements, delivery reports, and the
// read a sender falls back to when its stream has missed a report.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No ack, report or read has
// been served.

import (
	"context"
	"errors"
	"net/http"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// ackBodyLimit bounds the small bodies these routes take.
const ackBodyLimit = 4 << 10 // UNTESTED guess

// handleAck records the recipient host's answer to an offer.
//
// Both the inline case (the sender is still waiting) and the late case (the sender
// already recorded delivery_uncertain) are the same compare-and-set, which is what
// makes the race harmless: whichever commits first decides, and the other one
// either corrects an uncertain result or changes nothing.
//
// UNTESTED.
func (s *Server) handleAck(w http.ResponseWriter, r *http.Request, host string) {
	ctx := r.Context()
	id := pathParam(r, "id")

	row, _, err := s.store.Message(ctx, id)
	switch {
	case errors.Is(err, ErrNoSuchMessage):
		noSuchMessage(w)
		return
	case err != nil:
		internalError(w)
		return
	}
	if !s.recipientIsHost(row, host) {
		noSuchMessage(w)
		return
	}

	var ack protocol.AckRequest
	if !decodeBody(w, r, ackBodyLimit, &ack) {
		return
	}
	if row.ToSession == "" {
		// Nothing was ever offered for this message, so there is nothing to
		// acknowledge: a recipient host cannot decide a result by acking a message
		// the gateway has not offered it.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The decision is written even if the recipient hangs up right after sending.
	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()

	result := protocol.SendHostStored
	reason := ""
	if !ack.Saved {
		// A refusal is certain not to have been delivered, so it is session_gone
		// with the host's own reason travelling beside it.
		result = protocol.SendSessionGone
		reason = cleanReason("the recipient host refused it: " + ack.Reason)
	}

	now := s.opts.Now()
	changed, err := s.store.SetResultFrom(writeCtx, id, []string{resultStored}, string(result), reason, "", now)
	if err != nil {
		internalError(w)
		return
	}
	if changed {
		// The sender is waiting inline: wake it, and let it read the row.
		s.hub.wake(id)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	late, err := s.store.SetResultFrom(writeCtx, id, []string{string(protocol.SendDeliveryUncertain)}, string(result), reason, "", now)
	if err != nil {
		internalError(w)
		return
	}
	if late {
		// A late correction: the sender has stopped waiting, so it learns the
		// truth through a report frame or a later read.
		s.sendReportFrame(writeCtx, id)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleStatus stores the recipient host's delivery report.
//
// Reports are resent after a reconnect and can arrive out of order, so a lower rank
// never overwrites a higher one. A report never touches the message's result.
//
// UNTESTED.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request, host string) {
	ctx := r.Context()
	id := pathParam(r, "id")

	row, _, err := s.store.Message(ctx, id)
	switch {
	case errors.Is(err, ErrNoSuchMessage):
		noSuchMessage(w)
		return
	case err != nil:
		internalError(w)
		return
	}
	if !s.recipientIsHost(row, host) {
		noSuchMessage(w)
		return
	}

	var report protocol.StatusReport
	if !decodeBody(w, r, ackBodyLimit, &report) {
		return
	}

	rank := reportRank(report.State)
	if rank == 0 {
		badRequest(w, "state")
		return
	}
	if row.ToSession == "" {
		// Never offered, so there is nothing for the recipient to report on.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if storedRank := reportRank(protocol.MessageState(row.Report)); storedRank != 0 && rank < storedRank {
		// Older news: keep what is stored, and say nothing about it.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The gateway's clock, never the runtime's At: a report is a claim about now.
	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	if err := s.store.SetReport(writeCtx, id, string(report.State), cleanReason(report.Reason), s.opts.Now()); err != nil {
		internalError(w)
		return
	}
	s.a2aReceipt(writeCtx, id, report.State)
	s.sendReportFrame(writeCtx, id)
	w.WriteHeader(http.StatusNoContent)
}

// handleGetMessage answers the fallback read: one message's wire view.
//
// Only the two parties may read it. A third host is answered exactly as if the
// message did not exist, so nothing confirms that somebody else's message is there.
//
// UNTESTED.
func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request, host string) {
	ctx := r.Context()

	row, _, err := s.store.Message(ctx, pathParam(r, "id"))
	switch {
	case errors.Is(err, ErrNoSuchMessage):
		noSuchMessage(w)
		return
	case err != nil:
		internalError(w)
		return
	}

	_, fromHost, fromOK := parseAddress(row.From)
	_, toHost, toOK := parseAddress(row.To)
	if !fromOK || !toOK || (host != fromHost && host != toHost) {
		noSuchMessage(w)
		return
	}

	writeJSON(w, http.StatusOK, MessageInfoFor(row))
}

// sendReportFrame tells the sender's host what the recipient's host reported.
//
// The row is re-read so the frame always carries what the ledger now says. A
// sender with no stream loses nothing: it recovers through GET /v1/messages/{id}.
//
// The signature takes the id rather than a row because the freshness is the point;
// the brief's "freshRowFromDatabase" is exactly this read.
//
// UNTESTED.
func (s *Server) sendReportFrame(ctx context.Context, id string) {
	row, _, err := s.store.Message(ctx, id)
	if err != nil {
		// No stale frame: silence is better than an old result.
		return
	}

	_, senderHost, ok := parseAddress(row.From)
	if !ok {
		return
	}

	info := MessageInfoFor(row)
	frame := protocol.StreamFrame{
		Type:   protocol.FrameReport,
		Report: &info,
		At:     s.opts.Now(),
	}

	if err := s.hub.enqueue(senderHost, frame); errors.Is(err, errQueueFull) {
		// The sender is not reading; ending its stream is the honest answer.
		s.hub.cancel(senderHost)
	}
}

// recipientIsHost reports whether the caller's host is the one the message was
// addressed to.
func (s *Server) recipientIsHost(row MessageRow, host string) bool {
	_, toHost, ok := parseAddress(row.To)
	return ok && toHost == host
}

// noSuchMessage answers a read of something the caller may not see. It is the same
// answer for "missing" and "not yours", on purpose.
func noSuchMessage(w http.ResponseWriter) {
	writeWireError(w, http.StatusNotFound, protocol.WireErrNotFound, "no such message")
}
