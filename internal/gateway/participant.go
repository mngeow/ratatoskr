package gateway

// The participant API's routes (contract section 4): pairing, discovery, sessions, directory, send, the
// long-poll inbox, ack, status and reading a message. They are served only by the participant listener.
//
// Identity comes from the credential and nothing else: the participant is the one its credential names, and
// a body field never sets an origin. A participant credential never works on a host route and a host
// credential never works here (separate tables, separate middleware, separate path prefix).
//
// UNTESTED. Under GR-3 nothing here has been run: no request served, no poll parked, no offer made.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// participantWriteTimeout is the participant server's WriteTimeout. It must stay well over the longest
	// poll (20 s) plus authentication, database and write time; exactly 20 would cut a full poll.
	participantWriteTimeout = 30 * time.Second // UNTESTED guess
	// participantAnswerWrite bounds the write of an inbox answer after the wait.
	participantAnswerWrite = 10 * time.Second // UNTESTED guess
	// participantBodyReadTimeout bounds reading one request body.
	participantBodyReadTimeout = 15 * time.Second // UNTESTED guess
	// participantJoinWait is how long Shutdown waits for running participant handlers before the shared
	// Store may be closed.
	participantJoinWait = 2 * time.Second // UNTESTED guess
)

// participantRevokeCheck is how often a parked poll re-checks that its participant is still active. A
// revocation is made by another process, so this is how it is noticed. A variable so a test can shorten it.
var participantRevokeCheck = 2 * time.Second // UNTESTED guess

// participantAuthMessage is the one 401 text. It never says whether a credential was missing, unknown or
// revoked.
const participantAuthMessage = "the credential was not accepted"

// ParticipantHandler is the participant API's router. It is mounted on the participant listener only; the
// host Handler never serves it. It is wrapped in rewriteBareErrors and the contract header, and never in
// withVersion: an adapter must not be forced to send the host protocol header. No chi middleware is used,
// so a wrapper cannot hide http.Flusher or the ResponseController.
//
// UNTESTED.
func (s *Server) ParticipantHandler() http.Handler {
	router := chi.NewRouter()
	router.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeWireError(w, http.StatusNotFound, protocol.WireErrNotFound, "no such route")
	})

	router.Get(protocol.ParticipantPathPrefix, s.handleParticipantDiscovery)
	router.Post(protocol.ParticipantPairPath, s.handleParticipantPair)
	router.Post(protocol.ParticipantSessionsPath, s.withParticipantAuth(s.handleParticipantAddSession))
	router.Delete(protocol.ParticipantSessionsPath, s.withParticipantAuth(s.handleParticipantEndSessions))
	router.Delete(protocol.ParticipantSessionsPath+"/{name}", s.withParticipantAuth(s.handleParticipantEndSession))
	router.Get(protocol.ParticipantDirectoryPath, s.withParticipantAuth(s.handleParticipantDirectory))
	router.Post(protocol.ParticipantMessagesPath, s.withParticipantAuth(s.handleParticipantSend))
	router.Get(protocol.ParticipantInboxPath, s.withParticipantAuth(s.handleParticipantInbox))
	router.Post(protocol.ParticipantMessagesPath+"/{id}/ack", s.withParticipantAuth(s.handleParticipantAck))
	router.Post(protocol.ParticipantMessagesPath+"/{id}/status", s.withParticipantAuth(s.handleParticipantStatus))
	router.Get(protocol.ParticipantMessagesPath+"/{id}", s.withParticipantAuth(s.handleParticipantGetMessage))

	return s.trackParticipantWork(withContractHeader(withContractCheck(rewriteBareErrors(router))))
}

// trackParticipantWork counts running participant handlers, so Shutdown can wait for them before the Store
// is closed.
func (s *Server) trackParticipantWork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.participantWork.Add(1)
		defer s.participantWork.Done()
		next.ServeHTTP(w, r)
	})
}

// withContractCheck refuses a client that declares a contract major this gateway does not serve. A client
// that sends no header is served; a value that is not major[.minor] with major "1" is refused.
func withContractCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if declared := r.Header.Get(protocol.ParticipantContractHeader); declared != "" {
			major, _, _ := strings.Cut(declared, ".")
			if major != protocol.ParticipantContractMajor {
				writeWireError(w, http.StatusBadRequest, protocol.WireErrUnsupportedContract,
					"this gateway serves contract major "+protocol.ParticipantContractMajor+" only")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withParticipantAuth requires a bearer credential and hands the handler the participant id it names.
// Authentication reads the database on every request and caches nothing, so a revocation refuses the very
// next request. A failure never says whether the credential was missing, unknown or revoked.
func (s *Server) withParticipantAuth(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credential, ok := bearerCredential(r.Header.Get("Authorization"))
		if !ok {
			writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, participantAuthMessage)
			return
		}
		id, err := s.store.AuthenticateParticipant(r.Context(), credential)
		switch {
		case errors.Is(err, ErrUnauthorized):
			writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, participantAuthMessage)
			return
		case err != nil:
			internalError(w)
			return
		}
		next(w, r, id)
	}
}

// decodeParticipantBody reads one JSON body within its size limit and within participantBodyReadTimeout.
func decodeParticipantBody(w http.ResponseWriter, r *http.Request, limit int64, destination any) bool {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(participantBodyReadTimeout))
	ok := decodeBody(w, r, limit, destination)
	_ = controller.SetReadDeadline(time.Time{})
	return ok
}

// handleParticipantDiscovery answers GET /participant/v1: no credential, no secret.
func (s *Server) handleParticipantDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, protocol.ParticipantDiscovery{
		Contract: protocol.ParticipantContract,
		Versions: []string{protocol.ParticipantContractMajor},
		Now:      s.opts.Now().UTC(),
	})
}

// handleParticipantPair exchanges a one-time code for a credential. Every way of being wrong is the same
// 400 bad_pairing_code.
func (s *Server) handleParticipantPair(w http.ResponseWriter, r *http.Request) {
	var request protocol.ParticipantPairRequest
	if !decodeParticipantBody(w, r, maxPairBodyBytes, &request) {
		return
	}

	credential, id, err := s.store.RedeemParticipantCode(r.Context(), request.Code, request.Participant, s.opts.Now())
	switch {
	case errors.Is(err, ErrBadCode), errors.Is(err, ErrBadHostName):
		writeWireError(w, http.StatusBadRequest, protocol.WireErrBadCode, "that pairing code is not valid")
		return
	case err != nil:
		internalError(w)
		return
	}

	writeJSON(w, http.StatusOK, protocol.ParticipantPairResponse{Participant: id, Credential: credential})
	// A pairing of an existing id ended that participant's sessions.
	s.directoryChanged()
}

// handleParticipantAddSession registers one session, ending only an open session of the same name.
func (s *Server) handleParticipantAddSession(w http.ResponseWriter, r *http.Request, participant string) {
	var request protocol.ParticipantAddSessionRequest
	if !decodeParticipantBody(w, r, maxSessionsBodyBytes, &request) {
		return
	}

	session, err := s.store.AddParticipantSession(r.Context(), participant, request.Name, s.opts.Now(), s.opts.NewID)
	switch {
	case errors.Is(err, ErrBadAgentName):
		badRequest(w, "name")
		return
	case errors.Is(err, ErrTooManySessions):
		writeWireError(w, http.StatusConflict, protocol.WireErrTooManySessions,
			"a participant may hold "+strconv.Itoa(protocol.ParticipantMaxSessions)+" open sessions")
		return
	case errors.Is(err, ErrNoSuchParticipant), errors.Is(err, ErrParticipantRevoked):
		// Revoked between the authentication and here.
		writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, participantAuthMessage)
		return
	case err != nil:
		internalError(w)
		return
	}

	directory, err := s.directoryEntries(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ParticipantAddSessionResponse{
		Participant: participant,
		Session: protocol.SessionInfo{
			Name:      session.Name,
			Address:   session.Name + "@" + participant,
			SessionID: session.ID,
		},
		Directory: directory,
	})
	s.directoryChanged()
}

// handleParticipantEndSession ends one session by name. Ending one that is not open is not an error.
func (s *Server) handleParticipantEndSession(w http.ResponseWriter, r *http.Request, participant string) {
	name := pathParam(r, "name")
	if !agentNamePattern.MatchString(name) {
		badRequest(w, "name")
		return
	}
	if err := s.store.EndSession(r.Context(), participant, name, s.opts.Now()); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	s.directoryChanged()
}

// handleParticipantEndSessions ends all of the participant's open sessions.
func (s *Server) handleParticipantEndSessions(w http.ResponseWriter, r *http.Request, participant string) {
	if err := s.store.EndSessions(r.Context(), participant, s.opts.Now()); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	s.directoryChanged()
}

// handleParticipantDirectory answers every registered session, live or not.
func (s *Server) handleParticipantDirectory(w http.ResponseWriter, r *http.Request, _ string) {
	directory, err := s.directoryEntries(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ParticipantDirectoryResponse{Directory: directory})
}

// handleParticipantSend is the shared send routine with a participant as the sender. A bare `to` fails the
// address parse there and is a 400 on `to`.
func (s *Server) handleParticipantSend(w http.ResponseWriter, r *http.Request, participant string) {
	var request protocol.SendRequest
	if !decodeParticipantBody(w, r, s.opts.MaxBodyBytes, &request) {
		return
	}
	s.routeSend(w, r, participant, request)
}

// handleParticipantInbox is the long poll. It answers {offers, replaced}; an empty list is the normal
// timeout. A parked poll leaves at once when the gateway is closing or the client goes, and within
// participantRevokeCheck of a revocation, which it answers with 401.
func (s *Server) handleParticipantInbox(w http.ResponseWriter, r *http.Request, participant string) {
	wait, ok := parseInboxWait(r)
	if !ok {
		badRequest(w, "wait")
		return
	}
	ctx := r.Context()
	now := s.opts.Now()

	lastPoll, active, err := s.store.ParticipantState(ctx, participant)
	switch {
	case err != nil:
		internalError(w)
		return
	case !active:
		writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, participantAuthMessage)
		return
	}
	wasLive := HostLive(lastPoll, s.phub.parked(participant), now)

	// The poll's start is recorded, so a send in the instant between two polls still finds it online.
	if err := s.store.TouchPoll(ctx, participant, now); err != nil {
		if errors.Is(err, ErrNoSuchParticipant) {
			writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, participantAuthMessage)
			return
		}
		internalError(w)
		return
	}
	if !wasLive {
		// It just became live: tell the hosts, as a first heartbeat does.
		s.directoryChanged()
	}

	stillActive := func() bool {
		_, active, err := s.store.ParticipantState(ctx, participant)
		return err != nil || active // a failed read says nothing about the participant
	}
	offers, outcome := s.phub.poll(ctx, participant, wait, s.closing, participantRevokeCheck, stillActive)

	// The end of a poll is recorded even if the client has gone.
	endCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	_ = s.store.TouchPoll(endCtx, participant, s.opts.Now())

	switch outcome {
	case pollRevoked:
		s.phub.dropPending(participant)
		s.directoryChanged()
		writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, participantAuthMessage)
		return
	case pollCancel:
		if ctx.Err() != nil {
			return // the client left: nobody to answer
		}
		// Closing: answer at once with an empty list.
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(participantAnswerWrite))
	writeJSON(w, http.StatusOK, protocol.InboxResponse{Offers: offers, Replaced: outcome == pollReplaced})
}

// parseInboxWait reads the wait query: whole seconds, 0 to 20, default 20.
func parseInboxWait(r *http.Request) (time.Duration, bool) {
	raw := r.URL.Query().Get("wait")
	if raw == "" {
		return protocol.ParticipantMaxWait * time.Second, true
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 || seconds > protocol.ParticipantMaxWait {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// handleParticipantAck records the adapter's answer to an offer, like a host's ack: the inline case and the
// late case are the same compare-and-set, and a second ack is idempotent.
func (s *Server) handleParticipantAck(w http.ResponseWriter, r *http.Request, participant string) {
	ctx := r.Context()
	id := pathParam(r, "id")

	row, ok := s.messageForParticipant(ctx, w, id, participant)
	if !ok {
		return
	}
	var ack protocol.AckRequest
	if !decodeParticipantBody(w, r, ackBodyLimit, &ack) {
		return
	}
	if row.ToSession == "" {
		// Never offered: there is nothing to acknowledge.
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}

	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()

	result := protocol.SendHostStored
	reason := "the participant's adapter saved it"
	if !ack.Saved {
		result = protocol.SendSessionGone
		reason = cleanReason("the participant's adapter refused it: " + ack.Reason)
	}

	now := s.opts.Now()
	changed, err := s.store.SetResultFrom(writeCtx, id, []string{resultStored}, string(result), reason, "", now)
	if err != nil {
		internalError(w)
		return
	}
	if changed {
		s.hub.wake(id)
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	late, err := s.store.SetResultFrom(writeCtx, id, []string{string(protocol.SendDeliveryUncertain)}, string(result), reason, "", now)
	if err != nil {
		internalError(w)
		return
	}
	if late {
		// A late correction: a host sender learns it through a report frame, a participant sender by reading
		// the message.
		s.sendReportFrame(writeCtx, id)
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// handleParticipantStatus stores the adapter's delivery report: idempotent, ranked so that an older report
// never overwrites a newer one, shown as a report and never a change of result. The time is the gateway's.
func (s *Server) handleParticipantStatus(w http.ResponseWriter, r *http.Request, participant string) {
	ctx := r.Context()
	id := pathParam(r, "id")

	row, ok := s.messageForParticipant(ctx, w, id, participant)
	if !ok {
		return
	}
	var report protocol.StatusReport
	if !decodeParticipantBody(w, r, ackBodyLimit, &report) {
		return
	}

	rank := reportRank(report.State)
	if rank == 0 {
		badRequest(w, "state")
		return
	}
	if row.ToSession == "" {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	if storedRank := reportRank(protocol.MessageState(row.Report)); storedRank != 0 && rank < storedRank {
		writeJSON(w, http.StatusOK, struct{}{}) // older news: keep what is stored
		return
	}

	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	if err := s.store.SetReport(writeCtx, id, string(report.State), cleanReason(report.Reason), s.opts.Now()); err != nil {
		internalError(w)
		return
	}
	s.sendReportFrame(writeCtx, id)
	writeJSON(w, http.StatusOK, struct{}{})
}

// messageForParticipant loads a message the participant received (ack and status). Anything else, including
// a message that does not exist, is the same 404.
func (s *Server) messageForParticipant(ctx context.Context, w http.ResponseWriter, id, participant string) (MessageRow, bool) {
	row, _, err := s.store.Message(ctx, id)
	switch {
	case errors.Is(err, ErrNoSuchMessage):
		noSuchMessage(w)
		return MessageRow{}, false
	case err != nil:
		internalError(w)
		return MessageRow{}, false
	}
	if !s.recipientIsHost(row, participant) {
		noSuchMessage(w)
		return MessageRow{}, false
	}
	return row, true
}

// handleParticipantGetMessage answers one message's wire view to the participant that sent or received it.
// A third party is answered exactly as if the message did not exist.
func (s *Server) handleParticipantGetMessage(w http.ResponseWriter, r *http.Request, participant string) {
	row, _, err := s.store.Message(r.Context(), pathParam(r, "id"))
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
	if !fromOK || !toOK || (participant != fromHost && participant != toHost) {
		noSuchMessage(w)
		return
	}
	writeJSON(w, http.StatusOK, MessageInfoFor(row))
}
