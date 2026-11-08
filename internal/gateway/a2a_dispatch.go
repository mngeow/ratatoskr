package gateway

// Delivering A2A tasks to managed sessions, and waking their owners, through the EXISTING message machinery.
//
// A dispatch is an ordinary ledger message offered once on the normal stream: same readiness gates, same
// pane framing, same log, same receipts (host_stored, pasted, accepted). Its body is a gateway-written
// prompt (internal/a2a/prompt.go) and its sender is the authenticated principal: "name@host" for a
// session, "ext:NAME" for an external participant. A row in a2a_dispatches links the message to its task.
//
// Rules kept here (contract section 2): a dispatch is never retried; an outcome that is not known is
// recorded as delivery=uncertain and the task stays SUBMITTED; a definite failure wraps ErrNotDelivered.
//
// UNTESTED.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/a2a"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// extOrigin is the sender written for an external participant. A colon cannot appear in a host or an agent
// name, so it can never equal or parse as one (see TestExtOriginIsNeverAHostOrSessionAddress).
func extOrigin(name string) string { return "ext:" + name }

// wakeSender is the sender written for a wake notice.
const wakeSender = "gateway"

// sessionByID returns a current (not ended) session by its exact id.
func (s *Store) sessionByID(ctx context.Context, id string) (SessionRow, bool, error) {
	rows, err := (a2aSessions{store: s}).Resolve(ctx, id)
	if err != nil {
		return SessionRow{}, false, err
	}
	for _, row := range rows {
		if row.ID == id {
			return SessionRow{ID: row.ID, Host: row.Host, Name: row.Name}, true, nil
		}
	}
	return SessionRow{}, false, nil
}

// a2aDispatcher implements a2a.Dispatcher for managed-session targets. External-service targets are the
// outbound client's (P4) and are not available until it is wired.
type a2aDispatcher struct{ server *Server }

var _ a2a.Dispatcher = a2aDispatcher{}

// Available implements a2a.Dispatcher: the target host is live and the exact session is current.
func (d a2aDispatcher) Available(ctx context.Context, target a2a.Target) error {
	if target.Kind == a2a.TargetService {
		participant, found, err := d.server.store.A2AStore().GetParticipant(ctx, target.ID)
		if err != nil {
			return err
		}
		if !found || participant.Revoked() || participant.URL == "" {
			return a2a.ErrTargetOffline
		}
		return nil // whether the service answers is known only by asking it: a send is the probe
	}
	if target.Kind != a2a.TargetSession {
		return a2a.ErrTargetOffline
	}
	session, found, err := d.server.store.sessionByID(ctx, target.ID)
	if err != nil {
		return err
	}
	if !found || !d.server.hostLive(ctx, session.Host) {
		return a2a.ErrTargetOffline
	}
	return nil
}

// hostLive is the same liveness decision the send route makes.
func (s *Server) hostLive(ctx context.Context, host string) bool {
	heartbeat, paired, err := s.store.LastHeartbeat(ctx, host)
	if err != nil {
		return false
	}
	return HostLive(heartbeat, paired && s.hub.open(host), s.opts.Now())
}

// Dispatch implements a2a.Dispatcher.
func (d a2aDispatcher) Dispatch(ctx context.Context, request a2a.Dispatch) error {
	s := d.server
	if request.Task.Target.Kind == a2a.TargetService {
		return d.dispatchToService(ctx, request)
	}
	if request.Task.Target.Kind != a2a.TargetSession {
		return fmt.Errorf("%w: not a managed session", a2a.ErrNotDelivered)
	}
	recipient, found, err := s.store.sessionByID(ctx, request.Task.Target.ID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: the session has ended", a2a.ErrNotDelivered)
	}

	from, fromSession, err := d.sender(ctx, request.From)
	if err != nil {
		return err
	}
	nonce, err := randomID()
	if err != nil {
		return fmt.Errorf("%w: no nonce", a2a.ErrNotDelivered)
	}
	body := a2a.BuildPrompt(request.Task.ID, from, request.Text, nonce, request.Continuation)

	messageID, err := randomID()
	if err != nil {
		return fmt.Errorf("%w: no message id", a2a.ErrNotDelivered)
	}
	row, err := s.offerManaged(ctx, messageID, from, fromSession, recipient, body, request.Task.ID, a2a.DispatchKind)
	if err != nil {
		return err
	}

	switch protocol.SendResult(row.Result) {
	case protocol.SendHostStored:
		_ = s.a2aObserve(ctx, request.Task.ID, a2a.ObsDelivery, "stored")
		return nil
	case protocol.SendUnknownRecipient, protocol.SendDestinationUnreachable, protocol.SendSessionGone:
		return fmt.Errorf("%w: %s", a2a.ErrNotDelivered, row.Result)
	}
	// delivery_uncertain, or anything else: the outcome is unknown. Record it here, never retry.
	_ = s.a2aObserve(ctx, request.Task.ID, a2a.ObsDelivery, "uncertain")
	return errors.New("a2a: the dispatch outcome is unknown")
}

// sender is the gateway-written origin of a dispatch and the sending session id, if any.
func (d a2aDispatcher) sender(ctx context.Context, from a2a.Principal) (origin, sessionID string, err error) {
	switch from.Kind {
	case a2a.KindParticipant:
		return extOrigin(from.Name), "", nil
	case a2a.KindSession:
		session, found, err := d.server.store.sessionByID(ctx, from.Session)
		if err != nil {
			return "", "", err
		}
		if !found {
			return "", "", fmt.Errorf("%w: the sending session has ended", a2a.ErrNotDelivered)
		}
		return session.Name + "@" + session.Host, session.ID, nil
	}
	return "", "", fmt.Errorf("%w: unknown sender", a2a.ErrNotDelivered)
}

// offerManaged stores one message, links it to a task and offers it once on the recipient host's stream,
// waiting for the acknowledgement exactly as the send route does. It returns the settled ledger row.
func (s *Server) offerManaged(ctx context.Context, messageID, from, fromSession string, recipient SessionRow, body, taskID, kind string) (MessageRow, error) {
	now := s.opts.Now()
	to := recipient.Name + "@" + recipient.Host
	if _, _, err := s.store.InsertMessage(ctx, MessageIn{
		ID: messageID, From: from, To: to, Body: body, FromSession: fromSession, At: now,
	}); err != nil {
		return MessageRow{}, fmt.Errorf("%w: the ledger refused it", a2a.ErrNotDelivered)
	}
	if err := s.store.A2AStore().LinkMessage(ctx, messageID, taskID, kind, now); err != nil {
		return MessageRow{}, fmt.Errorf("%w: the link could not be stored", a2a.ErrNotDelivered)
	}

	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	if err := s.store.SetToSession(writeCtx, messageID, recipient.ID); err != nil {
		return MessageRow{}, fmt.Errorf("%w: the ledger refused it", a2a.ErrNotDelivered)
	}

	wake, forget := s.hub.expectAck(messageID)
	defer forget()
	frame := protocol.StreamFrame{
		Type: protocol.FrameOffer,
		Offer: &protocol.Offer{
			ID: messageID, From: from, To: recipient.Name, ToSession: recipient.ID, Body: body, At: now,
		},
		At: now,
	}
	if err := s.hub.enqueue(recipient.Host, frame); err != nil {
		// Never written to the stream: a definite non-delivery.
		_ = s.store.SetResult(writeCtx, messageID, string(protocol.SendDestinationUnreachable), "the host's stream could not take the offer", "", s.opts.Now())
		return MessageRow{}, fmt.Errorf("%w: the host's stream could not take it", a2a.ErrNotDelivered)
	}

	timer := time.NewTimer(s.opts.AckWait)
	defer timer.Stop()
	select {
	case <-wake:
	case <-timer.C:
	case <-ctx.Done():
	}
	if !s.recordTimeout(writeCtx, messageID) {
		return MessageRow{}, errors.New("a2a: the ledger could not record the outcome")
	}
	row, _, err := s.store.Message(writeCtx, messageID)
	if err != nil {
		return MessageRow{}, err
	}
	return row, nil
}

// a2aObserve records an observation beside a task.
func (s *Server) a2aObserve(ctx context.Context, taskID, kind, value string) error {
	return s.store.A2AStore().AddObservation(ctx, a2a.Observation{TaskID: taskID, Kind: kind, Value: value, At: s.opts.Now()})
}

// a2aWake implements a2a.OwnerNotifier: a managed owner is told, through the same gates as any message,
// that its task needs input or finished. An owner that is offline gets an observation and no replay.
type a2aWake struct{ server *Server }

var _ a2a.OwnerNotifier = a2aWake{}

// TaskEvent implements a2a.OwnerNotifier.
func (w a2aWake) TaskEvent(ctx context.Context, owner a2a.Principal, task a2a.Task) {
	s := w.server
	if owner.Kind != a2a.KindSession {
		return // an external owner polls GetTask
	}
	recipient, found, err := s.store.sessionByID(ctx, owner.Session)
	if err != nil || !found || !s.hostLive(ctx, recipient.Host) {
		_ = s.a2aObserve(ctx, task.ID, a2a.ObsOwnerEvent, strings.ToLower(task.State)+": owner offline")
		return
	}
	text := ""
	if body, err := a2a.DecodeBody(task); err == nil && body.Status.Message != nil {
		for _, part := range body.Status.Message.Parts {
			if part != nil {
				text += part.Text()
			}
		}
	}
	messageID, err := randomID()
	if err != nil {
		return
	}
	// A wake is best effort and never retried; a lost one is still visible on the next GetTask.
	row, err := s.offerManaged(ctx, messageID, wakeSender, "", recipient, a2a.WakeText(task.ID, task.State, text), task.ID, a2a.WakeKind)
	if err != nil || protocol.SendResult(row.Result) != protocol.SendHostStored {
		_ = s.a2aObserve(ctx, task.ID, a2a.ObsOwnerEvent, strings.ToLower(task.State)+": wake not confirmed")
	}
}

// a2aSessionsChanged finishes the open tasks whose target session is no longer current (ended, replaced or
// gone with its host): FAILED "session ended" (contract rule 2). Nothing is redirected to a replacement
// session, which is a different participant. It is called after every handler that can end a session.
func (s *Server) a2aSessionsChanged(ctx context.Context) {
	if s.a2aService == nil {
		return
	}
	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	rows, err := s.store.db.QueryContext(writeCtx,
		`SELECT t.id FROM a2a_tasks t
		 WHERE t.target_kind='session' AND t.state NOT IN ('COMPLETED','FAILED','REJECTED')
		   AND NOT EXISTS (SELECT 1 FROM sessions x WHERE x.id=t.target_id AND x.ended_at IS NULL)`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		_, _ = s.a2aService.SystemEvent(writeCtx, id, a2a.StateFailed, "session ended")
	}
}

// a2aReceipt turns a recipient's delivery receipt on a dispatch message into an observation and, for
// "accepted", the WORKING state: never earlier (contract section 7). Receipts are state names only.
func (s *Server) a2aReceipt(ctx context.Context, messageID string, state protocol.MessageState) {
	if s.a2aService == nil {
		return
	}
	taskID, kind, found, err := s.store.A2AStore().TaskForMessage(ctx, messageID)
	if err != nil || !found || kind != a2a.DispatchKind {
		return
	}
	switch state {
	case protocol.MessagePasted:
		_ = s.a2aObserve(ctx, taskID, a2a.ObsDelivery, "pasted")
	case protocol.MessageAccepted:
		_ = s.a2aObserve(ctx, taskID, a2a.ObsDelivery, "accepted")
		_, _ = s.a2aService.Accepted(ctx, taskID)
	case protocol.MessageFailed, protocol.MessageInterrupted, protocol.MessageUnconfirmed:
		_ = s.a2aObserve(ctx, taskID, a2a.ObsDelivery, string(state))
	}
}

const (
	maxTaskReportText   = 16 << 10
	maxTaskReportResult = 64 << 10
	taskReportBodyLimit = 128 << 10
)

// handleTaskReport applies a managed target's explicit report. Only the task's CURRENT target party may
// report: the host credential names the host, the header names the session, and the session must be a
// current session of that host AND the one the task is bound to. Anyone else gets the same not-found as an
// unknown task. A report on a final task is refused, not applied.
//
// UNTESTED.
func (s *Server) handleTaskReport(w http.ResponseWriter, r *http.Request, host string) {
	ctx := r.Context()
	if s.a2aService == nil {
		noSuchTask(w)
		return
	}
	sessionID := r.Header.Get(a2a.SessionHeader)
	current, err := (a2aSessions{store: s.store}).Current(ctx, host, sessionID)
	if err != nil {
		internalError(w)
		return
	}
	if !current {
		noSuchTask(w)
		return
	}

	var request protocol.TaskReportRequest
	if !decodeBody(w, r, taskReportBodyLimit, &request) {
		return
	}
	if len(request.Text) > maxTaskReportText {
		badRequest(w, "text")
		return
	}
	if len(request.Result) > maxTaskReportResult {
		badRequest(w, "result")
		return
	}

	reporter := a2a.Principal{Kind: a2a.KindSession, Host: host, Session: sessionID}
	writeCtx, endWrite := s.outcomeContext(ctx)
	defer endWrite()
	_, err = s.a2aService.Report(writeCtx, reporter, pathParam(r, "id"),
		request.State, a2a.CleanText(request.Text), a2a.CleanText(request.Result))
	switch {
	case errors.Is(err, a2a.ErrNotFound):
		noSuchTask(w)
	case errors.Is(err, a2a.ErrFinal):
		writeWireError(w, http.StatusConflict, protocol.WireErrBadRequest, "the task is already final")
	case errors.Is(err, a2a.ErrBadState):
		badRequest(w, "state")
	case err != nil:
		internalError(w)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// noSuchTask is the one answer for "missing" and "not yours".
func noSuchTask(w http.ResponseWriter) {
	writeWireError(w, http.StatusNotFound, protocol.WireErrNotFound, "no such task")
}
