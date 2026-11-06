package gateway

// The send route: store the full body, route it, offer it once, and record every
// outcome.
//
// The database holds the decision. This handler only performs compare-and-sets and
// then reports what the row says, which is why a late acknowledgement racing the
// inline wait cannot corrupt a result.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No message has been routed.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// resultPollInterval and resultPollGrace bound the duplicate-id poll: a repeat
	// of an in-flight id waits for the first request's answer a little longer than
	// the inline wait, because that is what it is.
	resultPollInterval = 100 * time.Millisecond // UNTESTED guess
	resultPollGrace    = time.Second            // UNTESTED guess
)

// handleSend accepts one message from one host and answers with its result.
//
// Every routed outcome is HTTP 200 with a protocol.SendResponse: the result IS the
// answer, including a definite failure. Only a malformed request is a WireError.
//
// UNTESTED.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, host string) {
	ctx := r.Context()

	var request protocol.SendRequest
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &request) {
		return
	}

	toName, toHost, ok := parseAddress(request.To)
	switch {
	case !messageIDPatternMatcher.MatchString(request.ID):
		badRequest(w, "id")
		return
	case !agentNamePattern.MatchString(request.From):
		badRequest(w, "from")
		return
	case !ok:
		badRequest(w, "to")
		return
	case request.ReplyTo != "" && !messageIDPatternMatcher.MatchString(request.ReplyTo):
		badRequest(w, "reply_to")
		return
	case request.Body == "" || len(request.Body) > maxMessageBody:
		badRequest(w, "body")
		return
	}

	// The sender must have a current session on its own host. A name belonging to
	// another host is indistinguishable from an unknown one here, and the answer
	// must not say which host it might have been.
	session, current, err := s.store.CurrentSession(ctx, host, request.From)
	if err != nil {
		internalError(w)
		return
	}
	if !current {
		badRequest(w, "from: no current session for that sender")
		return
	}
	fromAddress := request.From + "@" + host

	now := s.opts.Now()
	inserted, existing, err := s.store.InsertMessage(ctx, MessageIn{
		ID:          request.ID,
		From:        fromAddress,
		To:          request.To,
		ReplyTo:     request.ReplyTo,
		Body:        request.Body,
		FromSession: session.ID,
		At:          now,
	})
	if err != nil {
		internalError(w)
		return
	}

	if !inserted {
		// The id is already taken. An id cannot be taken over by another sender.
		if existing.From != fromAddress {
			badRequest(w, "id")
			return
		}
		if existing.Result != resultStored {
			writeJSON(w, http.StatusOK, sendResponseFor(existing))
			return
		}

		// The first request is still deciding. Wait for its answer rather than
		// guessing, and never write: that row belongs to the other handler.
		row, settled := s.pollResult(ctx, request.ID, s.opts.AckWait+resultPollGrace)
		if !settled {
			writeJSON(w, http.StatusOK, protocol.SendResponse{
				ID:     request.ID,
				Result: protocol.SendDeliveryUncertain,
				Detail: "no result yet; it may still arrive",
			})
			return
		}
		writeJSON(w, http.StatusOK, sendResponseFor(row))
		return
	}

	// Step 1: a reply_to the gateway knows may name a session that has ended.
	if request.ReplyTo != "" {
		from, fromSession, _, _, err := s.store.ReplyTarget(ctx, request.ReplyTo)
		switch {
		case errors.Is(err, ErrNoSuchMessage):
			// The gateway cannot judge a message it never saw.
		case err != nil:
			internalError(w)
			return
		case from == request.To && fromSession != "":
			original, found, err := s.store.CurrentSession(ctx, toHost, toName)
			if err != nil {
				internalError(w)
				return
			}
			if !found || original.ID != fromSession {
				s.answerFailure(ctx, w, request.ID, protocol.SendSessionGone,
					"reply_to names a session that has ended", nil)
				return
			}
		}
	}

	// Step 2: has this host ever had that name?
	seen, err := s.store.EverSeen(ctx, toHost, toName)
	if err != nil {
		internalError(w)
		return
	}
	if !seen {
		entries, err := s.directoryEntries(ctx)
		if err != nil {
			internalError(w)
			return
		}
		s.answerFailure(ctx, w, request.ID, protocol.SendUnknownRecipient,
			"no agent by that name has been seen", entries)
		return
	}

	// Step 3: liveness, decided only by HostLive.
	heartbeat, paired, err := s.store.LastHeartbeat(ctx, toHost)
	if err != nil {
		internalError(w)
		return
	}
	if !HostLive(heartbeat, paired && s.hub.open(toHost), now) {
		s.answerFailure(ctx, w, request.ID, protocol.SendDestinationUnreachable,
			"the host has no live connection", nil)
		return
	}

	// Step 4: is there a current session for that name?
	recipient, found, err := s.store.CurrentSession(ctx, toHost, toName)
	if err != nil {
		internalError(w)
		return
	}
	if !found {
		s.answerFailure(ctx, w, request.ID, protocol.SendSessionGone,
			"that agent has no current session", nil)
		return
	}

	// Step 5: offer exactly once.
	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	if err := s.store.SetToSession(writeCtx, request.ID, recipient.ID); err != nil {
		internalError(w)
		return
	}

	wake, forget := s.hub.expectAck(request.ID)
	defer forget()

	frame := protocol.StreamFrame{
		Type: protocol.FrameOffer,
		Offer: &protocol.Offer{
			ID:        request.ID,
			From:      fromAddress,
			To:        toName,
			ToSession: recipient.ID,
			ReplyTo:   request.ReplyTo,
			Body:      request.Body,
			At:        now,
		},
		At: now,
	}

	if enqueueErr := s.hub.enqueue(toHost, frame); enqueueErr != nil {
		// The offer was never written, so this is a definite failure with a reason
		// that says which of the two it was.
		reason := "the host's stream could not take the offer"
		if errors.Is(enqueueErr, errNoStream) {
			reason = "the host has no live connection"
		}
		s.answerFailure(ctx, w, request.ID, protocol.SendDestinationUnreachable, reason, nil)
		return
	}

	// Wait for the recipient's answer, but let the database decide: whichever of
	// the ack and this timeout commits first wins, and the row is read afterwards.
	timer := time.NewTimer(s.opts.AckWait)
	defer timer.Stop()

	select {
	case <-wake:
	case <-timer.C:
	case <-ctx.Done():
	}

	// The sender may have gone while we waited; the outcome is still recorded.
	if !s.recordTimeout(writeCtx, request.ID) {
		internalError(w)
		return
	}

	row, _, err := s.store.Message(writeCtx, request.ID)
	if err != nil {
		internalError(w)
		return
	}
	writeJSON(w, http.StatusOK, sendResponseFor(row))
}

// recordTimeout records delivery_uncertain unless the ack already decided.
//
// Whether the compare-and-set changed anything does not matter here: either way the
// row is now the truth, and the caller reads it. What must not happen is a definite
// failure after the offer was enqueued.
func (s *Server) recordTimeout(ctx context.Context, id string) bool {
	_, err := s.store.SetResultFrom(ctx, id, []string{resultStored},
		string(protocol.SendDeliveryUncertain), "no acknowledgement in time", "", s.opts.Now())
	if err != nil && !errors.Is(err, ErrNoSuchMessage) {
		return false
	}
	return true
}

// answerFailure records a definite failure and answers with the stored row.
func (s *Server) answerFailure(ctx context.Context, w http.ResponseWriter, id string, result protocol.SendResult, reason string, directory []protocol.DirectoryEntry) {
	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	if err := s.store.SetResult(writeCtx, id, string(result), reason, "", s.opts.Now()); err != nil {
		internalError(w)
		return
	}

	response := protocol.SendResponse{ID: id, Result: result, Detail: reason}
	if len(directory) > 0 {
		response.Directory = directory
	}
	writeJSON(w, http.StatusOK, response)
}

// sendResponseFor renders the stored row as the wire answer.
func sendResponseFor(row MessageRow) protocol.SendResponse {
	result := protocol.SendResult(row.Result)
	if result == resultStored {
		// The gateway has it and has decided nothing; "uncertain" is the only
		// honest word.
		result = protocol.SendGatewayUncertain
	}
	return protocol.SendResponse{ID: row.ID, Result: result, Detail: row.ResultReason}
}

// pollResult waits for another handler's decision on an id, without writing.
//
// It reports whether the row settled. When it does not, the caller answers
// delivery_uncertain: the other request may still decide, so nothing here may
// claim a failure.
func (s *Server) pollResult(ctx context.Context, id string, timeout time.Duration) (MessageRow, bool) {
	deadline := time.Now().Add(timeout)

	for {
		row, _, err := s.store.Message(ctx, id)
		if err != nil {
			return MessageRow{}, false
		}
		if row.Result != resultStored {
			return row, true
		}
		if !time.Now().Before(deadline) {
			return row, false
		}

		select {
		case <-ctx.Done():
			return row, false
		case <-time.After(resultPollInterval):
		}
	}
}

// badRequest answers a malformed request, naming the field but never the value.
func badRequest(w http.ResponseWriter, field string) {
	writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest, "invalid "+field)
}

// internalError answers without any Go error text, database message or credential.
func internalError(w http.ResponseWriter) {
	writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
}

// outcomeContext returns a context for recording an outcome that survives the
// sender hanging up. Once the full body is stored, every outcome must reach the
// ledger even if the request's own context is already cancelled: a row left at
// "stored" would read as undecided forever, and a definite failure or an uncertain
// result must be written down. It is bounded, so a stuck database cannot hold the
// handler open.
func (s *Server) outcomeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outcomeWriteTimeout)
}

// outcomeWriteTimeout bounds the ledger writes made after a request's context may
// have ended.
const outcomeWriteTimeout = 5 * time.Second // UNTESTED guess
