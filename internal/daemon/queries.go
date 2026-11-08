package daemon

// Reads: history, runs, one message, the outbound list, one outbound message, the pending
// count and the snapshot.
//
// Each query runs on its own goroutine with a deadline, answers with the request's id, and is
// bounded per window, so one client cannot flood the store and no daemon lock is held across a
// read. A window that goes away simply stops reading its answers.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No query has been served.

import (
	"context"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

const (
	// queryTimeout bounds one read. UNTESTED guess.
	queryTimeout = 10 * time.Second // UNTESTED guess

	// queryLimit is how many reads one window may have in flight. Beyond it the answer is
	// busy rather than a queue.
	queryLimit = 8
)

// startQuery answers one query on its own goroutine.
//
// UNTESTED.
func (w *window) startQuery(envelope wire.Envelope) {
	w.runBounded(envelope, func(ctx context.Context) wire.Result {
		return w.answer(ctx, envelope)
	})
}

// startCardOp performs one card operation on its own goroutine, under the same per-window bound and
// deadline as a query. A card operation is host-level, not per-session, so it does not go through a
// session's worker; the card service serializes writes itself.
//
// UNTESTED.
func (w *window) startCardOp(envelope wire.Envelope) {
	w.runBounded(envelope, func(ctx context.Context) wire.Result {
		var op wire.CardOp
		if err := wire.Decode(envelope, &op); err != nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that card operation could not be read"}}
		}
		return w.cardResult(ctx, op)
	})
}

// cardResult runs one card operation as this window's caller.
func (w *window) cardResult(_ context.Context, op wire.CardOp) wire.Result {
	if w.hub == nil || w.hub.card == nil {
		return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "the host card is not available"}}
	}
	// The window itself is the draft owner key: its drafts end with it (window.closeWith).
	return w.hub.card.Do(op, w)
}

// runBounded runs one request on its own goroutine with the window's concurrency bound and the query
// deadline, and answers with the request's id.
func (w *window) runBounded(envelope wire.Envelope, run func(context.Context) wire.Result) {
	if envelope.ID == 0 {
		w.closeWith("it sent a request without an id")
		return
	}

	w.queryMu.Lock()
	if w.queries >= queryLimit {
		w.queryMu.Unlock()
		w.result(envelope.ID, wire.Result{Error: &wire.Error{Code: wire.CodeBusy, Message: "too many requests are already in flight for this window"}})
		return
	}
	w.queries++
	w.queryMu.Unlock()

	// The window's own lifecycle bounds the request: when the connection goes, so does the
	// deadline, without anything having to wait for the read.
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)

	go func() {
		defer func() {
			cancel()
			w.queryMu.Lock()
			w.queries--
			w.queryMu.Unlock()
		}()

		result := run(ctx)
		if w.isClosed() {
			return
		}
		w.result(envelope.ID, result)
	}()
}

// answer performs one read.
func (w *window) answer(ctx context.Context, envelope wire.Envelope) wire.Result {
	var query wire.Query
	if err := wire.Decode(envelope, &query); err != nil {
		return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that query could not be read"}}
	}

	if ctx.Err() != nil {
		return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "the query timed out"}}
	}

	router := w.hub.router
	if router == nil {
		return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "no router is available"}}
	}

	switch query.Kind {
	case wire.QueryEnter:
		if query.Enter == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that query has no enter request"}}
		}
		if w.hub == nil || w.hub.network == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "the entry planner is not available"}}
		}
		plan := w.hub.network.enterPlan(*query.Enter, w)
		return wire.Result{Plan: &plan}

	case wire.QueryCard:
		// The card needs no gateway, no graph and no network: it is this host's own file.
		return w.cardResult(ctx, wire.CardOp{Op: wire.CardOpGet})

	case wire.QueryGraph:
		if query.Graph == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that query has no graph request"}}
		}
		if w.hub == nil || w.hub.graph == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "the graph answer is not available"}}
		}
		// Attaching already made this window a graph consumer; a query only reads the answer.
		view, version := w.hub.graph.answer(ctx, *query.Graph)
		return wire.Result{Graph: &view, GraphVersion: version}

	case wire.QueryNetwork:
		if query.Network == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that query has no network request"}}
		}
		if w.hub == nil || w.hub.network == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "the network answer is not available"}}
		}
		// Attaching already made this window a consumer; a query only reads the answer.
		view, version := w.hub.network.answer(ctx, *query.Network)
		return wire.Result{Network: &view, NetworkVersion: version}

	case wire.QuerySnapshot:
		snapshot := router.Snapshot()
		return wire.Result{Snapshot: &snapshot}

	case wire.QueryPending:
		count := router.PendingOnQuit()
		return wire.Result{Pending: &count}

	case wire.QueryHistory:
		if query.History == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that query has no history request"}}
		}
		views, err := router.History(*query.History)
		if err != nil {
			return queryError(err)
		}
		return wire.Result{History: views}

	case wire.QueryRuns:
		runs, err := router.Runs(query.Limit)
		if err != nil {
			return queryError(err)
		}
		return wire.Result{Runs: runs}

	case wire.QueryMessageDetail:
		detail, err := router.MessageDetail(query.ID)
		if err != nil {
			return queryError(err)
		}
		return wire.Result{MessageDetail: &detail}

	case wire.QuerySends:
		sends, err := router.Sends(query.Limit)
		if err != nil {
			return queryError(err)
		}
		return wire.Result{Sends: sends}

	case wire.QuerySendDetail:
		detail, err := router.SendDetail(query.ID)
		if err != nil {
			return queryError(err)
		}
		return wire.Result{SendDetail: &detail}
	}

	return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that query kind is not one this build knows"}}
}

// queryError turns a read error into an answer.
//
// "No such message" stays internal with the words in the text: the wire has no code for it, and
// the window shows the message anyway.
func queryError(err error) wire.Result {
	message := sanitizeMessage(err.Error())
	if containsNotFound(err) {
		return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "message not found: " + message}}
	}
	return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: message}}
}

// containsNotFound reports whether an error text says the read found nothing, since the router
// does not export one sentinel for every read.
func containsNotFound(err error) bool {
	text := err.Error()
	for _, marker := range []string{"no such message", "not found", "no rows"} {
		if len(text) >= len(marker) && contains(text, marker) {
			return true
		}
	}
	return false
}

// contains is strings.Contains, kept local so this file needs no import for one call.
func contains(text, marker string) bool {
	for i := 0; i+len(marker) <= len(text); i++ {
		if text[i:i+len(marker)] == marker {
			return true
		}
	}
	return false
}
