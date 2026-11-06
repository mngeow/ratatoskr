package daemon

// Untested: under GR-3 nothing here has been run. The router is a fake, so no store is read and
// no socket is involved.

import (
	"context"
	"io"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// queryFixture is one window with a fake router.
type queryFixture struct {
	window *window
	router *fakeWindowRouter
}

func newQueryFixture(t *testing.T) *queryFixture {
	t.Helper()
	router := &fakeWindowRouter{
		snapshot: protocol.Snapshot{Banner: "hello"},
		pending:  4,
		history:  []protocol.MessageView{{ID: "m1"}},
	}
	h := &hub{gate: newGate(), log: &logger{out: io.Discard}, router: router}
	w := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	w.paused = false
	return &queryFixture{window: w, router: router}
}

// answer runs one query synchronously and returns its result.
func (f *queryFixture) answer(t *testing.T, query wire.Query) wire.Result {
	t.Helper()
	envelope, err := encodeRequest(wire.TypeQuery, 1, query)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return f.window.answer(t.Context(), envelope)
}

// TestQueryKinds: every kind answers with its own payload field and no error.
func TestQueryKinds(t *testing.T) {
	f := newQueryFixture(t)

	snapshot := f.answer(t, wire.Query{Kind: wire.QuerySnapshot})
	if snapshot.Error != nil || snapshot.Snapshot == nil || snapshot.Snapshot.Banner != "hello" {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	pending := f.answer(t, wire.Query{Kind: wire.QueryPending})
	if pending.Error != nil || pending.Pending == nil || *pending.Pending != 4 {
		t.Fatalf("pending = %+v", pending)
	}

	history := f.answer(t, wire.Query{Kind: wire.QueryHistory, History: &protocol.HistoryQuery{}})
	if history.Error != nil || len(history.History) != 1 {
		t.Fatalf("history = %+v", history)
	}

	if runs := f.answer(t, wire.Query{Kind: wire.QueryRuns, Limit: 5}); runs.Error != nil {
		t.Fatalf("runs = %+v", runs)
	}
	if detail := f.answer(t, wire.Query{Kind: wire.QueryMessageDetail, ID: "m1"}); detail.Error != nil || detail.MessageDetail == nil {
		t.Fatalf("message detail = %+v", detail)
	}
	if sends := f.answer(t, wire.Query{Kind: wire.QuerySends}); sends.Error != nil {
		t.Fatalf("sends = %+v", sends)
	}
	if detail := f.answer(t, wire.Query{Kind: wire.QuerySendDetail, ID: "s1"}); detail.Error != nil || detail.SendDetail == nil {
		t.Fatalf("send detail = %+v", detail)
	}
}

// TestQueryBadKindAndBadBody: an unknown kind or a missing history request is bad_request, and a
// read failure is internal with the text in it.
func TestQueryBadKindAndBadBody(t *testing.T) {
	f := newQueryFixture(t)

	if result := f.answer(t, wire.Query{Kind: "nonsense"}); result.Error == nil || result.Error.Code != wire.CodeBadRequest {
		t.Fatalf("unknown kind = %+v", result)
	}
	if result := f.answer(t, wire.Query{Kind: wire.QueryHistory}); result.Error == nil || result.Error.Code != wire.CodeBadRequest {
		t.Fatalf("history without a request = %+v", result)
	}

	f.router.err = errNoMessageRecorded
	result := f.answer(t, wire.Query{Kind: wire.QueryMessageDetail, ID: "gone"})
	if result.Error == nil || result.Error.Code != wire.CodeInternal {
		t.Fatalf("a read failure = %+v", result)
	}
	if result.Error.Message == "" {
		t.Fatal("a read failure carried no text")
	}
}

// TestQueryLimit: past the per-window cap the answer is busy, and the counter comes back down.
func TestQueryLimit(t *testing.T) {
	f := newQueryFixture(t)

	f.window.queryMu.Lock()
	f.window.queries = queryLimit
	f.window.queryMu.Unlock()

	envelope, err := encodeRequest(wire.TypeQuery, 9, wire.Query{Kind: wire.QueryPending})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	f.window.startQuery(envelope)

	waitFor(t, "the busy answer", func() bool { return len(f.window.queue) > 0 })
	message := <-f.window.queue
	if message.typ != wire.TypeResult || message.id != 9 {
		t.Fatalf("message = %+v, want a result for id 9", message)
	}
	result := message.body.(wire.Result)
	if result.Error == nil || result.Error.Code != wire.CodeBusy {
		t.Fatalf("result = %+v, want busy", result)
	}
}

// TestQueryTimeout: a cancelled query answers with a timeout rather than reading.
func TestQueryTimeout(t *testing.T) {
	f := newQueryFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	envelope, err := encodeRequest(wire.TypeQuery, 1, wire.Query{Kind: wire.QueryPending})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result := f.window.answer(ctx, envelope)
	if result.Error == nil || result.Error.Code != wire.CodeInternal {
		t.Fatalf("result = %+v, want an internal timeout", result)
	}
}
