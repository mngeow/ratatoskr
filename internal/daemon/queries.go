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
	if envelope.ID == 0 {
		w.closeWith("it sent a query without an id")
		return
	}

	w.queryMu.Lock()
	if w.queries >= queryLimit {
		w.queryMu.Unlock()
		w.result(envelope.ID, wire.Result{Error: &wire.Error{Code: wire.CodeBusy, Message: "too many queries are already in flight for this window"}})
		return
	}
	w.queries++
	w.queryMu.Unlock()

	// The window's own lifecycle bounds the query: when the connection goes, so does the
	// deadline, without anything having to wait for the read.
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)

	go func() {
		defer func() {
			cancel()
			w.queryMu.Lock()
			w.queries--
			w.queryMu.Unlock()
		}()

		result := w.answer(ctx, envelope)
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

	case wire.QueryNetwork:
		if query.Network == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that query has no network request"}}
		}
		if w.hub == nil || w.hub.network == nil {
			return wire.Result{Error: &wire.Error{Code: wire.CodeInternal, Message: "the network answer is not available"}}
		}
		// The first network query makes this window a consumer, which is what starts fetching.
		w.acquireNetwork()
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
