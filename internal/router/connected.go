package router

// Connected-mode plumbing: the types, helpers and view building a runtime with a
// gateway needs.
//
// Nothing here changes local mode. Every helper is either unreachable when
// Config.Link is nil, or returns exactly what the local code returned before.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No send has been recorded, no
// message has arrived from another host and no connection state has been emitted.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

const (
	// sendLogLimit mirrors logLimit for outbound attempts.
	sendLogLimit = 500 // UNTESTED guess; mirrors logLimit

	// recentSendsLimit mirrors historyLimit for the snapshot's send list.
	recentSendsLimit = 200 // UNTESTED guess; mirrors historyLimit
)

// connected reports whether this router is a connected run.
//
// A nil Link is local mode, byte for byte as it always was. Nothing in this package
// may infer connectedness any other way.
//
// UNTESTED.
func (r *Router) connected() bool { return r.cfg.Link != nil }

// senderName is the name a message's frame shows as its sender.
//
// A local message always has an agent, so this returns exactly what m.from.name
// returned before. A message that arrived from another host has no local agent and
// carries the gateway's network name (name@host) instead, which is what the frame
// and the views show.
//
// UNTESTED.
func senderName(m *msg) string {
	if m.from != nil {
		return m.from.name
	}
	return m.origin
}

// sendRec is one outbound attempt, actor-owned. It is the router's record of what
// it asked the gateway to send, independent of the store's row.
type sendRec struct {
	id           string
	from         *agent
	to           string
	replyTo      string
	body         string
	result       protocol.SendResult
	resultReason string
	report       protocol.MessageState
	reportReason string
	at           time.Time
}

// appendSend records one attempt and keeps the newest sendLogLimit of them, the
// same way appendLog keeps the newest messages.
//
// UNTESTED.
func (r *Router) appendSend(rec *sendRec) {
	r.sends = append(r.sends, rec)
	if len(r.sends) > sendLogLimit {
		r.sends = append([]*sendRec(nil), r.sends[len(r.sends)-sendLogLimit:]...)
	}
}

// sendView renders one attempt for the UI.
//
// The sender is the local agent this router knows; the recipient is the network
// name as sent. Names, the report reason and the body are sanitised here, so the
// UI never has to.
//
// UNTESTED.
func (r *Router) sendView(rec *sendRec) protocol.SendView {
	from := ""
	if rec.from != nil {
		from = stripControl(rec.from.name)
	}

	return protocol.SendView{
		ID:           rec.id,
		From:         from,
		To:           stripControl(rec.to),
		Result:       rec.result,
		Report:       rec.report,
		ReportReason: stripControl(rec.reportReason),
		Preview:      preview(rec.body),
		At:           rec.at,
	}
}

func (r *Router) sendViewPtr(rec *sendRec) *protocol.SendView {
	view := r.sendView(rec)
	return &view
}

// recentSends is the newest attempts as views, oldest first. It is never nil, so a
// connected snapshot with no sends carries an empty list rather than a nil one.
//
// UNTESTED.
func (r *Router) recentSends() []protocol.SendView {
	start := len(r.sends) - recentSendsLimit
	if start < 0 {
		start = 0
	}

	views := make([]protocol.SendView, 0, len(r.sends)-start)
	for _, rec := range r.sends[start:] {
		views = append(views, r.sendView(rec))
	}
	return views
}

// connectionCopy is a copy of the current connection view, or nil in local mode.
//
// The peer slice is copied too: a snapshot must not share memory with the router's
// own state, or a reader could change what the router believes.
//
// UNTESTED.
func (r *Router) connectionCopy() *protocol.ConnectionView {
	if r.conn == nil {
		if r.connected() {
			// A connected run's first snapshot shows connecting rather than nothing.
			return &protocol.ConnectionView{State: protocol.ConnConnecting}
		}
		return nil
	}

	view := *r.conn
	if r.conn.Peers != nil {
		view.Peers = append([]string(nil), r.conn.Peers...)
	}
	return &view
}

// emitConnection publishes the current connection state.
//
// Nothing calls it yet: slice 5c does. With no connection it emits nothing, so a
// caller may invoke it unconditionally.
//
// UNTESTED.
func (r *Router) emitConnection() {
	view := r.connectionCopy()
	if view == nil {
		return
	}
	r.emit(protocol.Event{Connection: view})
}

// emitSend publishes one outbound attempt's view.
//
// UNTESTED.
func (r *Router) emitSend(rec *sendRec) {
	r.emit(protocol.Event{Send: r.sendViewPtr(rec)})
}

// SendID is the message id a refusal belongs to, once a record exists. It is empty
// for every refusal that happens before anything was recorded.
//
// UNTESTED.
func (e *SendError) SendID() string { return e.ID }

// SendDetail is the gateway's own wording beside a refusal reason, for a caller
// that wants to show it without parsing Reason. Empty in local mode.
//
// UNTESTED.
func (e *SendError) SendDetail() string { return e.Detail }

// Uncertain reports whether a refused send may still arrive. It is true only for
// the two uncertain gateway results; every local refusal reason is false.
//
// UNTESTED.
func (e *SendError) Uncertain() bool { return protocol.SendResult(e.Reason).Uncertain() }

// The server maps a refusal to its tool error through this method, and this
// assertion keeps that seam compiling.
var _ interface{ SendReason() string } = (*SendError)(nil)

// ErrSendNotFound reports a send id this run does not have.
var ErrSendNotFound = errors.New("router: send not found")

// pendingSend is one attempt that has been recorded locally and is on its way to
// the gateway.
type pendingSend struct {
	rec     *sendRec
	request protocol.SendRequest
}

// sendOutcome is what the network goroutine learned.
type sendOutcome struct {
	result    protocol.SendResult
	reason    string
	directory []protocol.DirectoryEntry
}

// sendConnected is the connected send: record locally, hand the request to the
// gateway off the actor, then commit whatever the gateway decided.
//
// The caller's context ends the WAIT, not the record: the network goroutine runs
// under context.WithoutCancel so a caller that hangs up still leaves a committed
// result. That means a caller can see a context error for a message the gateway
// went on to store - which is why the ledger, not the return value, is the truth.
//
// UNTESTED.
func (r *Router) sendConnected(ctx context.Context, from protocol.AgentID, to, body, replyTo string) (string, error) {
	type prepared struct {
		pending *pendingSend
		err     error
	}
	type finished struct {
		id          string
		result      protocol.SendResult
		reason      string
		directory   []protocol.DirectoryEntry
		preparedErr error
	}

	// The id is published from INSIDE the actor closure, before the preparation
	// result is handed over, so a caller that gives up at any instant after the row
	// exists can name it.
	var recordedID atomic.Pointer[string]

	preparedCh := make(chan prepared, 1)
	if err := r.submit(ctx, func() {
		pending, err := r.prepareSend(from, to, body, replyTo)
		if pending != nil {
			id := pending.rec.id
			recordedID.Store(&id)
		}
		preparedCh <- prepared{pending: pending, err: err}
	}); err != nil {
		return "", err
	}

	// This goroutine always finishes what the actor started: a caller that hangs up
	// must not leave a recorded send unfinished and unsent.
	done := make(chan finished, 1)
	go func() {
		var p prepared
		select {
		case p = <-preparedCh:
		case <-r.done:
			// The router is closing. Take a preparation that did happen, so its row
			// is still finished; otherwise there is nothing to finish.
			select {
			case p = <-preparedCh:
			default:
				return
			}
		}

		if p.err != nil {
			done <- finished{preparedErr: p.err}
			return
		}

		// Not ctx: the Link bounds its own call with protocol.SendWait, and the
		// record must be finished even if the caller has gone.
		callCtx := context.WithoutCancel(ctx)
		response, err := r.cfg.Link.Send(callCtx, p.pending.request)
		outcome := sendOutcome{}
		outcome.result, outcome.reason, outcome.directory = linkResult(response, err)

		commit := func() {
			rec := p.pending.rec
			finishErr := r.cfg.Store.FinishSend(r.ctx, rec.id, protocol.SendSending, outcome.result, outcome.reason, r.now())

			// A late correction (a report frame from the gateway) can arrive before
			// this commit and already move the row. The ledger is then right, and the
			// in-memory record must stay as the correction left it.
			superseded := false
			switch {
			case finishErr == nil:
			case errors.Is(finishErr, store.ErrUnavailable):
				r.setStorageDown(finishErr)
			case errors.Is(finishErr, store.ErrStateChanged):
				superseded = true
			}

			if !superseded {
				rec.result = outcome.result
				rec.resultReason = outcome.reason
				r.emitSend(rec)
			}

			done <- finished{
				id:        rec.id,
				result:    outcome.result,
				reason:    outcome.reason,
				directory: outcome.directory,
			}
		}

		if err := r.submit(r.ctx, commit); err != nil {
			// The router is closing. The row stays "sending"; the store's FinishRun
			// turns it into gateway_uncertain with reason run_ended.
			return
		}
	}()

	// The caller's wait may end before the network does. Once a row exists that is
	// not a bare error: the message may still arrive.
	recordedWaitEnded := func(id string) error {
		return &SendError{
			Reason: string(protocol.SendGatewayUncertain),
			Detail: "the wait ended before the gateway answered: the message may still arrive",
			ID:     id,
		}
	}

	select {
	case f := <-done:
		if f.preparedErr != nil {
			return "", f.preparedErr
		}
		return sendOutcome{result: f.result, reason: f.reason, directory: f.directory}.answer(f.id)
	case <-ctx.Done():
		if id := recordedID.Load(); id != nil {
			return "", recordedWaitEnded(*id)
		}
		return "", ctx.Err()
	case <-r.done:
		if id := recordedID.Load(); id != nil {
			return "", recordedWaitEnded(*id)
		}
		return "", ErrClosed
	}
}

// answer turns an outcome into what Send returns: an id for host_stored, a refusal
// with its reason beside it for everything else.
func (o sendOutcome) answer(id string) (string, error) {
	if o.result == protocol.SendHostStored {
		return id, nil
	}

	detail := o.reason
	if o.result == protocol.SendUnknownRecipient && len(o.directory) > 0 {
		names := make([]string, 0, len(o.directory))
		for _, entry := range o.directory {
			if len(names) == maxDirectoryInDetail {
				break
			}
			names = append(names, stripControl(entry.Address))
		}
		listing := "(known agents: " + strings.Join(names, ", ") + ")"
		if detail == "" {
			detail = listing
		} else {
			detail += " " + listing
		}
	}

	return "", &SendError{Reason: string(o.result), Detail: detail, ID: id}
}

// maxDirectoryInDetail bounds how many addresses a refusal lists.
const maxDirectoryInDetail = 20 // UNTESTED guess

// prepareSend runs the local checks for a connected send, in the same order send()
// uses, with the recipient lookup replaced by the address rules.
//
// The checks are deliberately DUPLICATED from send() rather than shared: a shared
// helper would change a local path, and local mode is the regression bar.
//
// UNTESTED.
func (r *Router) prepareSend(from protocol.AgentID, to, body, replyTo string) (*pendingSend, error) {
	if r.storageDown {
		return nil, &SendError{Reason: ReasonStorageUnavailable}
	}

	sender, ok := r.agents[from]
	if !ok {
		// A wiring bug, not a message rejection, with the same wording as send().
		return nil, fmt.Errorf("router: send: unknown sender %q", from)
	}

	if to == "" {
		return nil, &SendError{Reason: ReasonUnknownRecipient}
	}

	// A bare name means this host: the gateway composes address@host, and a fully
	// qualified name is passed through as typed for the gateway to judge.
	address := to
	if !strings.ContainsRune(address, '@') {
		address = r.cfg.Link.Address(to)
	}

	if address == r.cfg.Link.Address(sender.name) {
		return nil, &SendError{Reason: ReasonUnknownRecipient}
	}

	if isEmptyBody(body) {
		return nil, &SendError{Reason: ReasonEmptyBody}
	}

	if len(body) > r.cfg.MaxBody {
		return nil, &SendError{Reason: ReasonTooLarge}
	}

	if replyTo != "" {
		exists, err := r.cfg.Store.MessageExists(r.ctx, r.cfg.RunID, replyTo)
		switch {
		case err != nil && errors.Is(err, store.ErrUnavailable):
			r.setStorageDown(err)
			return nil, &SendError{Reason: ReasonStorageUnavailable}
		case err != nil:
			return nil, fmt.Errorf("router: send: check reply-to: %w", err)
		case !exists:
			return nil, &SendError{Reason: ReasonUnknownReplyTo}
		}
	}

	if r.used >= r.cfg.BudgetLimit {
		return nil, &SendError{Reason: ReasonBudgetExhausted}
	}

	id, err := r.cfg.NewID()
	if err != nil {
		return nil, fmt.Errorf("router: send: new message id: %w", err)
	}

	at := r.now()
	if err := r.cfg.Store.InsertSend(r.ctx, store.Send{
		ID:        id,
		RunID:     r.cfg.RunID,
		From:      sender.id,
		To:        address,
		ReplyTo:   replyTo,
		Body:      body,
		CreatedAt: at,
	}); err != nil {
		if errors.Is(err, store.ErrUnavailable) {
			r.setStorageDown(err)
			return nil, &SendError{Reason: ReasonStorageUnavailable}
		}
		return nil, fmt.Errorf("router: send: insert send: %w", err)
	}

	rec := &sendRec{
		id:      id,
		from:    sender,
		to:      address,
		replyTo: replyTo,
		body:    body,
		result:  protocol.SendSending,
		at:      at,
	}
	r.used++
	r.appendSend(rec)
	r.emit(protocol.Event{Send: r.sendViewPtr(rec), Budget: r.budgetViewPtr()})

	return &pendingSend{
		rec: rec,
		request: protocol.SendRequest{
			ID:      id,
			From:    sender.name,
			To:      address,
			ReplyTo: replyTo,
			Body:    body,
		},
	}, nil
}

// linkResult maps what the Link returned onto one of the design's results.
//
// The rule that matters: only ErrNotSent is a definite failure. Everything else -
// a 5xx, an unreadable answer, a written-but-unanswered request - is uncertain,
// because the message may have been stored.
//
// UNTESTED.
func linkResult(response protocol.SendResponse, err error) (result protocol.SendResult, reason string, directory []protocol.DirectoryEntry) {
	if err == nil {
		result = response.Result
		if !decidedSendResult(result) {
			return protocol.SendGatewayUncertain, "the gateway's answer was not usable", response.Directory
		}
		return result, cleanDetail(response.Detail), response.Directory
	}

	if errors.Is(err, ErrNotSent) {
		// The sentinel's own prefix is machinery; the user sees why.
		text := strings.TrimPrefix(err.Error(), ErrNotSent.Error()+": ")
		return protocol.SendGatewayUnreachable, cleanDetail(text), nil
	}

	return protocol.SendGatewayUncertain, cleanDetail(err.Error()), nil
}

// decidedSendResult reports whether a result is one a sender can act on: the
// protocol's eight values minus "sending", which is not an outcome.
func decidedSendResult(result protocol.SendResult) bool {
	switch result {
	case protocol.SendGatewayUnreachable,
		protocol.SendGatewayUncertain,
		protocol.SendUnknownRecipient,
		protocol.SendDestinationUnreachable,
		protocol.SendSessionGone,
		protocol.SendDeliveryUncertain,
		protocol.SendHostStored:
		return true
	default:
		return false
	}
}

// cleanDetail makes a gateway's reason safe to store and show: control characters
// become spaces, whitespace collapses, and the result is cut to detailMaxRunes.
func cleanDetail(detail string) string {
	var builder strings.Builder
	builder.Grow(len(detail))

	space := false
	count := 0
	for _, r := range detail {
		if r == 0x7f || r < 0x20 || (r >= 0x80 && r <= 0x9f) {
			r = ' '
		}
		if r == ' ' {
			if builder.Len() == 0 || space {
				continue
			}
			space = true
		} else {
			space = false
		}
		if count == detailMaxRunes {
			break
		}
		builder.WriteRune(r)
		count++
	}
	return strings.TrimSpace(builder.String())
}

// detailMaxRunes bounds a stored or shown detail.
const detailMaxRunes = 200 // UNTESTED guess

// Sends lists this run's outbound attempts for the UI.
//
// It reads the store like History does, not the actor, and needs no Link: in local
// mode the table simply has no rows.
//
// UNTESTED.
func (r *Router) Sends(limit int) ([]protocol.SendView, error) {
	if limit <= 0 {
		limit = historyLimit
	}

	rows, err := r.cfg.Store.Sends(r.ctx, r.cfg.RunID, limit)
	if err != nil {
		return nil, fmt.Errorf("router: sends: %w", err)
	}

	views := make([]protocol.SendView, 0, len(rows))
	for _, row := range rows {
		views = append(views, protocol.SendView{
			ID:           row.ID,
			From:         stripControl(row.From),
			To:           stripControl(row.To),
			Result:       row.Result,
			Report:       row.Report,
			ReportReason: stripControl(row.ReportReason),
			Preview:      preview(row.Body),
			At:           row.CreatedAt,
		})
	}
	return views, nil
}

// SendDetail returns one attempt in full, with its recorded events.
//
// UNTESTED.
func (r *Router) SendDetail(id string) (protocol.SendDetail, error) {
	row, events, err := r.cfg.Store.Send(r.ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return protocol.SendDetail{}, fmt.Errorf("%w: %s", ErrSendNotFound, id)
	case err != nil:
		return protocol.SendDetail{}, fmt.Errorf("router: send %s: %w", id, err)
	}

	detail := protocol.SendDetail{
		SendView: protocol.SendView{
			ID:           row.ID,
			From:         stripControl(row.From),
			To:           stripControl(row.To),
			Result:       row.Result,
			Report:       row.Report,
			ReportReason: stripControl(row.ReportReason),
			Preview:      preview(row.Body),
			At:           row.CreatedAt,
		},
		ReplyTo: stripControl(row.ReplyTo),
		Body:    stripControl(row.Body),
		Events:  make([]protocol.SendEventView, 0, len(events)),
	}

	for _, event := range events {
		switch event.Kind {
		case "result":
			detail.Events = append(detail.Events, protocol.SendEventView{
				At:     event.At,
				Result: protocol.SendResult(event.Value),
				Reason: stripControl(event.Reason),
			})
		case "report":
			detail.Events = append(detail.Events, protocol.SendEventView{
				At:       event.At,
				Report:   protocol.MessageState(event.Value),
				Reported: true,
				Reason:   stripControl(event.Reason),
			})
		default:
			// An event kind this build does not know: skip it rather than guess.
		}
	}
	return detail, nil
}

// The Receiver the link package defines. It cannot be asserted here, because
// internal/link imports this package; app adds the compile-time assertion.
//
// Receive, LinkState, LinkDirectory, LinkUp and LinkReport all only enqueue work and
// return at once, so the stream reader never waits on the router. Every one is a
// no-op in local mode.

const (
	// ackTimeout bounds one acknowledgement. The Link already retries a few times.
	ackTimeout = 15 * time.Second // UNTESTED guess
	// reportQueue is how many delivery reports may wait to be sent.
	reportQueue = 256 // UNTESTED guess
	// reportTimeout bounds one delivery report.
	reportTimeout = 15 * time.Second // UNTESTED guess
	// relinkLookupLimit is how many uncertain sends a relink asks about.
	relinkLookupLimit = 50 // UNTESTED guess
	// lookupTimeout bounds one deferred lookup.
	lookupTimeout = 10 * time.Second // UNTESTED guess
	// linkBannerKey is the banner slot for a refused gateway.
	linkBannerKey = "link"
)

// reportJob is one delivery report waiting to be sent.
type reportJob struct {
	id     string
	state  protocol.MessageState
	reason string
	at     time.Time
}

// Receive reports one offer from the gateway.
//
// UNTESTED.
func (r *Router) Receive(offer protocol.Offer) {
	if !r.connected() {
		return
	}
	r.enqueue(func() { r.receive(offer) })
}

// receive saves an offered message to the inbox and queues it for the existing gate,
// frame, paste and confirmation code.
//
// The order matters: insert, then acknowledge, then report "queued", then pump. An
// acknowledgement before the insert committed could leave the gateway believing a
// message was saved that this host lost.
//
// UNTESTED.
func (r *Router) receive(offer protocol.Offer) {
	recipient, ok := r.byName[offer.To]
	if !ok {
		r.ackAsync(offer.ID, false, "no_such_agent")
		return
	}
	if recipient.state == protocol.AgentExited {
		r.ackAsync(offer.ID, false, "exited")
		return
	}
	// A new session is never given an older message. The gateway names the session
	// it meant; a restart between its lookup and this offer would otherwise hand an
	// old message to a new session that shares only the name. An offer that names no
	// session, or a recipient whose session is not known yet, is refused too: nothing
	// may be accepted that cannot be tied to the session that is actually running.
	if offer.ToSession == "" || recipient.gatewaySession == "" || offer.ToSession != recipient.gatewaySession {
		r.ackAsync(offer.ID, false, "session_ended")
		return
	}
	if r.storageDown {
		r.ackAsync(offer.ID, false, "storage_unavailable")
		return
	}
	if isEmptyBody(offer.Body) {
		r.ackAsync(offer.ID, false, "empty_body")
		return
	}
	if len(offer.Body) > r.cfg.MaxBody {
		r.ackAsync(offer.ID, false, "too_large")
		return
	}

	now := r.now()
	inserted, _, err := r.cfg.Store.InsertInbound(r.ctx, store.Message{
		ID:        offer.ID,
		RunID:     r.cfg.RunID,
		From:      protocol.AgentID(offer.From),
		To:        recipient.id,
		ReplyTo:   offer.ReplyTo,
		Body:      offer.Body,
		CreatedAt: now,
	}, offer.From)
	switch {
	case err != nil && errors.Is(err, store.ErrUnavailable):
		r.setStorageDown(err)
		r.ackAsync(offer.ID, false, "storage_unavailable")
		return
	case err != nil:
		r.ackAsync(offer.ID, false, "storage_error")
		return
	case !inserted:
		// Already saved: acknowledge again and do nothing else. This is the
		// duplicate check that prevents a second paste.
		r.ackAsync(offer.ID, true, "")
		return
	}

	m := &msg{
		id:      offer.ID,
		from:    nil,
		to:      recipient,
		replyTo: offer.ReplyTo,
		body:    offer.Body,
		state:   protocol.MessageQueued,
		at:      now,
		origin:  offer.From,
	}
	r.appendLog(m)
	recipient.queue = append(recipient.queue, m)
	r.emit(protocol.Event{Message: r.messageViewPtr(m)})

	// The insert committed, so the acknowledgement is safe; then report the state.
	// An inbound message does not touch the budget: that is the sender's.
	r.ackAsync(offer.ID, true, "")
	r.queueReport(m)
	r.pump(recipient, false)
}

// ackAsync acknowledges one offer off the actor. The error is ignored: a lost
// acknowledgement ends as delivery_uncertain at the sender, which is honest.
//
// UNTESTED.
func (r *Router) ackAsync(id string, saved bool, reason string) {
	if !r.connected() {
		return
	}
	link := r.cfg.Link

	go func() {
		ctx, cancel := context.WithTimeout(r.ctx, ackTimeout)
		defer cancel()
		_ = link.Ack(ctx, id, saved, reason)
	}()
}

// queueReport reports one message's state change. It is called only from the actor.
//
// UNTESTED.
func (r *Router) queueReport(m *msg) {
	r.queueReportJob(reportJob{id: m.id, state: m.state, reason: m.reason, at: r.now()})
}

// queueReportJob hands one report to the reporter goroutine, starting it on first
// use. A full queue drops the job: LinkUp recovers it from InboundUnreported.
//
// UNTESTED.
func (r *Router) queueReportJob(job reportJob) {
	if !r.connected() {
		return
	}

	r.reportOnce.Do(func() {
		r.reports = make(chan reportJob, reportQueue)
		go r.reportLoop(r.reports)
	})

	select {
	case r.reports <- job:
	default:
		// Dropped, and recovered by the next relink.
	}
}

// reportLoop sends reports in order until the router closes. A report is purely
// outbound: it never touches a gate, a queue or an agent.
//
// UNTESTED.
func (r *Router) reportLoop(jobs <-chan reportJob) {
	for {
		select {
		case <-r.ctx.Done():
			return
		case job := <-jobs:
			ctx, cancel := context.WithTimeout(r.ctx, reportTimeout)
			err := r.cfg.Link.Report(ctx, job.id, protocol.StatusReport{
				State:  job.state,
				Reason: job.reason,
				At:     job.at,
			})
			cancel()

			if err != nil {
				// Recovered by the next relink, which re-reads what the gateway has
				// not been told.
				continue
			}
			// The store's single writer makes this safe off the actor.
			_ = r.cfg.Store.MarkReported(r.ctx, job.id, job.state)
		}
	}
}

// LinkUp reports a fresh connection: re-report what the gateway is missing, then ask
// about the sends whose outcome is still uncertain.
//
// UNTESTED.
func (r *Router) LinkUp() {
	if !r.connected() {
		return
	}
	r.enqueue(func() { r.relink() })
}

// relink re-reports unreported inbound messages and looks up uncertain sends.
//
// UNTESTED.
func (r *Router) relink() {
	rows, err := r.cfg.Store.InboundUnreported(r.ctx, r.cfg.RunID)
	if err == nil {
		for _, row := range rows {
			r.queueReportJob(reportJob{id: row.ID, state: row.State, reason: row.Reason, at: row.At})
		}
	}

	// Newest first, bounded: an old uncertain send is unlikely to be the one that
	// needs correcting.
	uncertain := make([]string, 0, relinkLookupLimit)
	for i := len(r.sends) - 1; i >= 0 && len(uncertain) < relinkLookupLimit; i-- {
		result := r.sends[i].result
		if result == protocol.SendGatewayUncertain || result == protocol.SendDeliveryUncertain {
			uncertain = append(uncertain, r.sends[i].id)
		}
	}
	if len(uncertain) == 0 {
		return
	}

	link := r.cfg.Link
	go func() {
		for _, id := range uncertain {
			if r.ctx.Err() != nil {
				return
			}

			ctx, cancel := context.WithTimeout(r.ctx, lookupTimeout)
			info, err := link.Lookup(ctx, id)
			cancel()

			switch {
			case err == nil:
				answer := info
				_ = r.submit(r.ctx, func() { r.applyInfo(answer) })
			case errors.Is(err, ErrUnknownMessage):
				// "The gateway has no record" is NOT proof that the message cannot
				// arrive: a request the gateway had received may still have been in
				// flight when the sender gave up. That certainty would rest on timing,
				// and the contract says an uncertain case is never reported as a
				// definite failure. The send stays gateway_uncertain.
			default:
				// Still uncertain. Never guessed into a failure.
			}
		}
	}()
}

// LinkReport applies the gateway's view of one send: a corrected result, the
// recipient's report, or both.
//
// UNTESTED.
func (r *Router) LinkReport(info protocol.MessageInfo) {
	if !r.connected() {
		return
	}
	r.enqueue(func() { r.applyInfo(info) })
}

// applyInfo corrects a send's result and stores a recipient report. Neither touches
// a gate, a queue or an agent state.
//
// UNTESTED.
func (r *Router) applyInfo(info protocol.MessageInfo) {
	rec := r.findSend(info.ID)
	if rec == nil {
		return
	}

	changed := false

	if r.correctableResult(rec.result) && decidedSendResult(info.Result) &&
		info.Result != protocol.SendGatewayUncertain && info.Result != rec.result {
		reason := cleanDetail(info.ResultReason)
		err := r.cfg.Store.FinishSend(r.ctx, rec.id, rec.result, info.Result, reason, r.now())
		switch {
		case err == nil:
			rec.result = info.Result
			rec.resultReason = reason
			changed = true
		case errors.Is(err, store.ErrUnavailable):
			r.setStorageDown(err)
		default:
			// ErrIllegalTransition, ErrStateChanged or anything else: the record is
			// left exactly as it is rather than guessed.
		}
	}

	if info.Report != "" {
		reason := cleanDetail(info.ReportReason)
		if info.Report != rec.report || reason != rec.reportReason {
			err := r.cfg.Store.ReportSend(r.ctx, rec.id, info.Report, reason, r.now())
			switch {
			case err == nil:
				rec.report = info.Report
				rec.reportReason = reason
				changed = true
			case errors.Is(err, store.ErrUnavailable):
				r.setStorageDown(err)
			}
		}
	}

	if changed {
		r.emitSend(rec)
	}
}

// correctableResult reports whether a result may still be corrected by the gateway.
func (r *Router) correctableResult(result protocol.SendResult) bool {
	switch result {
	case protocol.SendSending, protocol.SendGatewayUncertain, protocol.SendDeliveryUncertain:
		return true
	default:
		return false
	}
}

// findSend returns the record for one id, newest first.
func (r *Router) findSend(id string) *sendRec {
	for i := len(r.sends) - 1; i >= 0; i-- {
		if r.sends[i].id == id {
			return r.sends[i]
		}
	}
	return nil
}

// LinkState reports the connection state for the UI.
//
// UNTESTED.
func (r *Router) LinkState(view protocol.ConnectionView) {
	if !r.connected() {
		return
	}
	r.enqueue(func() { r.linkState(view) })
}

// linkState stores a sanitised connection view and, for a refusal, raises the one
// banner that explains it. Connecting and reconnecting set no banner: the connection
// view itself carries that.
//
// UNTESTED.
func (r *Router) linkState(view protocol.ConnectionView) {
	view.Err = cleanDetail(view.Err)

	peers := make([]string, 0, len(view.Peers))
	for _, peer := range view.Peers {
		peers = append(peers, stripControl(peer))
	}
	view.Peers = peers

	copied := view
	r.conn = &copied
	r.peers = append([]string(nil), peers...)
	r.emitConnection()

	if view.State == protocol.ConnRefused {
		// The link's reason already reads "the gateway refused this host: ...", so it
		// is not prefixed again.
		text := view.Err
		if text == "" {
			text = "the gateway refused this host"
		}
		r.setBanner(linkBannerKey, text+". Sends fail until it is fixed.")
		return
	}
	r.setBanner(linkBannerKey, "")
}

// LinkDirectory reports the newest peer list.
//
// UNTESTED.
func (r *Router) LinkDirectory(peers []string) {
	if !r.connected() {
		return
	}
	copied := append([]string(nil), peers...)
	r.enqueue(func() { r.linkDirectory(copied) })
}

// linkDirectory stores the peers and re-emits the connection so the UI sees them.
//
// UNTESTED.
func (r *Router) linkDirectory(peers []string) {
	sanitised := make([]string, 0, len(peers))
	for _, peer := range peers {
		sanitised = append(sanitised, stripControl(peer))
	}

	r.peers = sanitised
	if r.conn == nil {
		// No connection view yet: the next LinkState carries its own peers.
		return
	}
	r.conn.Peers = append([]string(nil), sanitised...)
	r.emitConnection()
}

// SetGatewaySession records the session id the gateway gave one of this host's
// agents when the run registered. receive() compares every offer's ToSession with
// it, so an offer meant for an earlier session of the same name is refused as
// session_ended (the sender sees session_gone). app calls it for every agent right
// after registering and before the link starts. It only enqueues; in local mode it
// does nothing. The agent name is the LOCAL display name.
//
// UNTESTED.
func (r *Router) SetGatewaySession(name, sessionID string) {
	if !r.connected() {
		return
	}
	r.enqueue(func() {
		if a, ok := r.byName[name]; ok {
			a.gatewaySession = sessionID
		}
	})
}
