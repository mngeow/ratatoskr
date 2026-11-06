package store

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// connectedNow is a fixed time one minute after t0, so an updated_at can be told
// apart from a created_at.
var connectedNow = t0.Add(time.Minute)

// insertTestSend records one attempt and fails the test if it cannot.
func insertTestSend(t *testing.T, s *Store, run, id string) {
	t.Helper()

	if err := s.InsertSend(context.Background(), Send{
		ID:        id,
		RunID:     run,
		From:      protocol.AgentID(run + "-c"),
		To:        "claude@ubuntu",
		Body:      "hello there",
		CreatedAt: t0,
	}); err != nil {
		t.Fatalf("InsertSend %s: %v", id, err)
	}
}

// driveSend moves a fresh attempt to the result a test wants to start from.
func driveSend(t *testing.T, s *Store, id string, target protocol.SendResult) {
	t.Helper()

	if target == protocol.SendSending {
		return
	}
	if err := s.FinishSend(context.Background(), id, protocol.SendSending, target, "", connectedNow); err != nil {
		t.Fatalf("drive %s to %s: %v", id, target, err)
	}
}

func TestInsertSendRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertTestSend(t, s, "r1", "s1")

	row, events, err := s.Send(ctx, "s1")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if row.Result != protocol.SendSending {
		t.Fatalf("result = %q, want %q", row.Result, protocol.SendSending)
	}
	// The display name comes from the agents table seeded by seedRun.
	if row.From != "claude" {
		t.Fatalf("From = %q, want the agents display name claude", row.From)
	}
	if row.FromID != "r1-c" {
		t.Fatalf("FromID = %q, want r1-c", row.FromID)
	}
	if row.To != "claude@ubuntu" || row.Body != "hello there" {
		t.Fatalf("row = %+v", row)
	}
	if len(events) != 1 || events[0].Kind != sendEventResult || events[0].Value != string(protocol.SendSending) {
		t.Fatalf("events = %+v, want one result event for sending", events)
	}

	// A second attempt, so the limit and order can be checked.
	insertTestSend(t, s, "r1", "s2")

	sends, err := s.Sends(ctx, "r1", 1)
	if err != nil {
		t.Fatalf("Sends: %v", err)
	}
	if len(sends) != 1 || sends[0].ID != "s2" {
		t.Fatalf("Sends with limit 1 = %+v, want only the newest", sends)
	}

	all, err := s.Sends(ctx, "r1", 0)
	if err != nil {
		t.Fatalf("Sends with no limit: %v", err)
	}
	if len(all) != 2 || all[0].ID != "s1" || all[1].ID != "s2" {
		t.Fatalf("Sends order = %+v, want oldest first s1, s2", all)
	}

	if _, _, err := s.Send(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Send for an unknown id = %v, want ErrNotFound", err)
	}
}

// TestFinishSendWholeGraph walks every (from, to) pair of the eight results. The
// package owns the graph, so the test asserts exactly the edges it declares.
func TestFinishSendWholeGraph(t *testing.T) {
	results := []protocol.SendResult{
		protocol.SendSending,
		protocol.SendGatewayUnreachable,
		protocol.SendGatewayUncertain,
		protocol.SendUnknownRecipient,
		protocol.SendDestinationUnreachable,
		protocol.SendSessionGone,
		protocol.SendDeliveryUncertain,
		protocol.SendHostStored,
	}

	for _, from := range results {
		for _, to := range results {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				s, _ := openTemp(t)
				seedRun(t, s, "r1")
				ctx := context.Background()

				insertTestSend(t, s, "r1", "s1")
				// Every result except sending is reachable from sending, so the
				// row can be put in the state this pair starts from.
				driveSend(t, s, "s1", from)

				err := s.FinishSend(ctx, "s1", from, to, "reason", connectedNow)
				// The expectation is restated here, not read back from sendLegal:
				// a test that asks the code what is legal proves nothing.
				if !expectedSendEdge(from, to) {
					if !errors.Is(err, ErrIllegalTransition) {
						t.Fatalf("%s -> %s error = %v, want ErrIllegalTransition", from, to, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("%s -> %s error = %v, want nil", from, to, err)
				}

				row, events, err := s.Send(ctx, "s1")
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				if row.Result != to {
					t.Fatalf("result = %q, want %q", row.Result, to)
				}
				// One event per success: the initial one, plus each drive step,
				// plus this one. The last event records the transition.
				last := events[len(events)-1]
				if last.Value != string(to) || last.Reason != "reason" || last.Kind != sendEventResult {
					t.Fatalf("last event = %+v, want the transition just made", last)
				}
			})
		}
	}
}

func TestFinishSendStateChanged(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertTestSend(t, s, "r1", "s1")

	// The row is "sending", so claiming it is host_stored is a mismatch.
	if err := s.FinishSend(ctx, "s1", protocol.SendHostStored, protocol.SendDeliveryUncertain, "", connectedNow); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("wrong-from error = %v, want ErrStateChanged", err)
	}

	row, events, err := s.Send(ctx, "s1")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if row.Result != protocol.SendSending {
		t.Fatalf("a mismatched finish changed the result to %q", row.Result)
	}
	if len(events) != 1 {
		t.Fatalf("a mismatched finish appended %d events", len(events)-1)
	}
}

func TestReportSend(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertTestSend(t, s, "r1", "s1")
	if err := s.FinishSend(ctx, "s1", protocol.SendSending, protocol.SendDeliveryUncertain, "", connectedNow); err != nil {
		t.Fatalf("FinishSend: %v", err)
	}

	if err := s.ReportSend(ctx, "s1", protocol.MessageAccepted, "prompt_submitted", connectedNow); err != nil {
		t.Fatalf("ReportSend: %v", err)
	}

	row, events, err := s.Send(ctx, "s1")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if row.Report != protocol.MessageAccepted || row.ReportReason != "prompt_submitted" {
		t.Fatalf("report = %q / %q", row.Report, row.ReportReason)
	}
	// The report never touches the result.
	if row.Result != protocol.SendDeliveryUncertain {
		t.Fatalf("result = %q, want delivery_uncertain untouched", row.Result)
	}
	last := events[len(events)-1]
	if last.Kind != sendEventReport || last.Value != string(protocol.MessageAccepted) {
		t.Fatalf("last event = %+v, want a report event", last)
	}

	// Idempotent: the same report writes nothing and adds no event.
	before := len(events)
	if err := s.ReportSend(ctx, "s1", protocol.MessageAccepted, "prompt_submitted", connectedNow.Add(time.Minute)); err != nil {
		t.Fatalf("repeated ReportSend: %v", err)
	}
	_, events, err = s.Send(ctx, "s1")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(events) != before {
		t.Fatalf("a repeated report added %d events", len(events)-before)
	}

	// A different reason is a new report.
	if err := s.ReportSend(ctx, "s1", protocol.MessageAccepted, "later", connectedNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("ReportSend with a new reason: %v", err)
	}
	if _, events, err = s.Send(ctx, "s1"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(events) != before+1 {
		t.Fatalf("a changed reason added %d events, want 1", len(events)-before)
	}

	if err := s.ReportSend(ctx, "nope", protocol.MessageAccepted, "", connectedNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReportSend for an unknown id = %v, want ErrNotFound", err)
	}

	// A report can only be set, never cleared: an empty state is a bug.
	err = s.ReportSend(ctx, "s1", "", "", connectedNow)
	if err == nil {
		t.Fatal("ReportSend accepted an empty state")
	}
	if err.Error() != "store: report state must not be empty" {
		t.Fatalf("empty-state error = %v, want the programming-error message", err)
	}
}

// TestNotFoundDoesNotLatchTheStore: a connected-mode write that names a row which
// does not exist is a domain error, like ErrStateChanged. It must roll back and
// leave the store usable; exec once treated every other error as a failed disk
// and latched ErrUnavailable for good, which would have stopped all delivery.
func TestNotFoundDoesNotLatchTheStore(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	if err := s.ReportSend(ctx, "nope", protocol.MessageAccepted, "", connectedNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReportSend for an unknown id = %v, want ErrNotFound", err)
	}
	if err := s.MarkReported(ctx, "nope", protocol.MessageQueued); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkReported for an unknown id = %v, want ErrNotFound", err)
	}
	if cause := s.UnavailableCause(); cause != nil {
		t.Fatalf("a not-found write latched the store: %v", cause)
	}

	// And the store still takes an ordinary write afterwards.
	if err := s.InsertSend(ctx, Send{
		ID: "s1", RunID: "r1", From: "r1-c", To: "codex@ubuntu", Body: "hi", CreatedAt: connectedNow,
	}); err != nil {
		t.Fatalf("InsertSend after not-found writes: %v", err)
	}
}

func TestInsertInboundDedupes(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	message := Message{
		ID:        "m-in",
		RunID:     "r1",
		From:      protocol.AgentID("codex@mac"),
		To:        "r1-x",
		Body:      "from another host",
		CreatedAt: t0,
	}

	inserted, seq, err := s.InsertInbound(ctx, message, "codex@mac")
	if err != nil {
		t.Fatalf("InsertInbound: %v", err)
	}
	if !inserted || seq == 0 {
		t.Fatalf("first InsertInbound = (%v, %d), want inserted with a seq", inserted, seq)
	}

	// The sender name shows through both reads, because there is no agents row
	// for a remote name.
	history, err := s.History(ctx, "r1", 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 || history[0].From != "codex@mac" {
		t.Fatalf("History = %+v, want From codex@mac", history)
	}
	row, err := s.Message(ctx, "m-in")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if row.From != "codex@mac" {
		t.Fatalf("Message From = %q, want codex@mac", row.From)
	}
	if row.State != protocol.MessageQueued {
		t.Fatalf("state = %q, want queued", row.State)
	}

	// The second call is a no-op everywhere.
	inserted, seq, err = s.InsertInbound(ctx, message, "codex@mac")
	if err != nil {
		t.Fatalf("second InsertInbound: %v", err)
	}
	if inserted || seq != 0 {
		t.Fatalf("second InsertInbound = (%v, %d), want not inserted", inserted, seq)
	}

	var messages, events, inbound int
	if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM messages WHERE id='m-in'`).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM message_events WHERE message_id='m-in'`).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM inbound WHERE message_id='m-in'`).Scan(&inbound); err != nil {
		t.Fatalf("count inbound: %v", err)
	}
	if messages != 1 || events != 1 || inbound != 1 {
		t.Fatalf("rows after the duplicate = messages %d, events %d, inbound %d; want 1/1/1", messages, events, inbound)
	}
}

func TestInboundUnreported(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertTestSend(t, s, "r1", "s1")
	message := Message{ID: "m-in", RunID: "r1", From: "codex@mac", To: "r1-x", Body: "hi", CreatedAt: t0}
	if _, _, err := s.InsertInbound(ctx, message, "codex@mac"); err != nil {
		t.Fatalf("InsertInbound: %v", err)
	}
	// A plain local message must never appear in the report queue.
	insert(t, s, "r1", "m-local")

	unreported, err := s.InboundUnreported(ctx, "r1")
	if err != nil {
		t.Fatalf("InboundUnreported: %v", err)
	}
	if len(unreported) != 1 || unreported[0].ID != "m-in" {
		t.Fatalf("unreported = %+v, want only m-in", unreported)
	}
	if unreported[0].State != protocol.MessageQueued {
		t.Fatalf("state = %q, want queued", unreported[0].State)
	}

	// Reporting the queued state clears it from the queue.
	if err := s.MarkReported(ctx, "m-in", protocol.MessageQueued); err != nil {
		t.Fatalf("MarkReported: %v", err)
	}
	unreported, err = s.InboundUnreported(ctx, "r1")
	if err != nil {
		t.Fatalf("InboundUnreported: %v", err)
	}
	if len(unreported) != 0 {
		t.Fatalf("a reported message is still unreported: %+v", unreported)
	}

	// A later transition makes it unreported again, with the new reason.
	if err := s.Transition(ctx, "m-in", protocol.MessageQueued, protocol.MessageHeld, "busy", connectedNow); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	unreported, err = s.InboundUnreported(ctx, "r1")
	if err != nil {
		t.Fatalf("InboundUnreported: %v", err)
	}
	if len(unreported) != 1 || unreported[0].State != protocol.MessageHeld {
		t.Fatalf("unreported after the transition = %+v", unreported)
	}
	if unreported[0].Reason != "busy" {
		t.Fatalf("reason = %q, want busy", unreported[0].Reason)
	}

	if err := s.MarkReported(ctx, "nope", protocol.MessageQueued); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkReported for an unknown id = %v, want ErrNotFound", err)
	}
}

func TestMessageExistsSeesSends(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	seedRun(t, s, "r2")
	ctx := context.Background()

	insertTestSend(t, s, "r1", "s1")
	insert(t, s, "r1", "m1")

	tests := []struct {
		name string
		run  string
		id   string
		want bool
	}{
		{name: "send id", run: "r1", id: "s1", want: true},
		{name: "message id", run: "r1", id: "m1", want: true},
		{name: "wrong run", run: "r2", id: "s1", want: false},
		{name: "unknown id", run: "r1", id: "nope", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.MessageExists(ctx, tc.run, tc.id)
			if err != nil {
				t.Fatalf("MessageExists: %v", err)
			}
			if got != tc.want {
				t.Fatalf("MessageExists(%q, %q) = %v, want %v", tc.run, tc.id, got, tc.want)
			}
		})
	}
}

// TestFinishRunMarksSendsUncertain covers both end reasons, and the promise that
// the interrupted-message count does not start counting sends.
func TestFinishRunMarksSendsUncertain(t *testing.T) {
	tests := []struct {
		name      string
		endReason string
		want      string
	}{
		{name: "clean run", endReason: "clean", want: "run_ended"},
		{name: "crashed run", endReason: "crashed", want: "crashed"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTemp(t)
			seedRun(t, s, "r1")
			ctx := context.Background()

			insertTestSend(t, s, "r1", "s-sending")
			insertTestSend(t, s, "r1", "s-done")
			if err := s.FinishSend(ctx, "s-done", protocol.SendSending, protocol.SendHostStored, "", connectedNow); err != nil {
				t.Fatalf("FinishSend: %v", err)
			}

			var interrupted int
			var err error
			if tc.endReason == "crashed" {
				// Recover is the crash path: the run is still open.
				recovered, recoverErr := s.Recover(ctx, connectedNow)
				if recoverErr != nil {
					t.Fatalf("Recover: %v", recoverErr)
				}
				if len(recovered) != 1 {
					t.Fatalf("Recover found %d runs, want 1", len(recovered))
				}
				interrupted = recovered[0].Interrupted
			} else {
				interrupted, err = s.FinishRun(ctx, "r1", tc.endReason, connectedNow)
				if err != nil {
					t.Fatalf("FinishRun: %v", err)
				}
			}

			if interrupted != 0 {
				t.Fatalf("interrupted count = %d, want 0: sends are not messages", interrupted)
			}

			row, events, err := s.Send(ctx, "s-sending")
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if row.Result != protocol.SendGatewayUncertain {
				t.Fatalf("result = %q, want %q", row.Result, protocol.SendGatewayUncertain)
			}
			last := events[len(events)-1]
			if last.Value != string(protocol.SendGatewayUncertain) || last.Reason != tc.want {
				t.Fatalf("last event = %+v, want gateway_uncertain with reason %q", last, tc.want)
			}

			// A finished send is left alone.
			done, doneEvents, err := s.Send(ctx, "s-done")
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if done.Result != protocol.SendHostStored {
				t.Fatalf("a finished send moved to %q", done.Result)
			}
			if len(doneEvents) != 2 {
				t.Fatalf("a finished send gained events: %d", len(doneEvents))
			}
		})
	}
}

// TestRetainCascadesSendsAndInbound: the new tables are removed with their run.
//
// It relies on foreign_keys(1), which the writer DSN sets. If that pragma were
// not honoured the rows would instead be left behind, and this test would fail on
// the counts rather than on Retain itself.
func TestRetainCascadesSendsAndInbound(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertTestSend(t, s, "r1", "s1")
	message := Message{ID: "m-in", RunID: "r1", From: "codex@mac", To: "r1-x", Body: "hi", CreatedAt: t0}
	if _, _, err := s.InsertInbound(ctx, message, "codex@mac"); err != nil {
		t.Fatalf("InsertInbound: %v", err)
	}
	if _, err := s.FinishRun(ctx, "r1", "clean", connectedNow); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	if _, err := s.Retain(ctx, connectedNow.Add(time.Hour)); err != nil {
		t.Fatalf("Retain: %v", err)
	}

	for _, table := range []string{"sends", "send_events", "inbound"} {
		var count int
		if err := s.rdb.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s still holds %d rows after Retain", table, count)
		}
	}
}

// TestLocalModeHistoryUnchanged is the regression guard: with no connected rows,
// History reports those display names and the same column shape it always did.
func TestLocalModeHistoryUnchanged(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insert(t, s, "r1", "m1")

	history, err := s.History(ctx, "r1", 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("History returned %d rows, want 1", len(history))
	}
	if history[0].From != "claude" || history[0].To != "codex" {
		t.Fatalf("names = %q -> %q, want claude -> codex from the agents table", history[0].From, history[0].To)
	}

	row, err := s.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if row.From != "claude" {
		t.Fatalf("Message From = %q, want claude", row.From)
	}
}

// expectedSendEdge is the result graph written out by hand, independently of
// sendLegal: sending reaches the other seven results; gateway_uncertain reaches
// the other six (a later lookup reports the gateway's truth); delivery_uncertain
// reaches host_stored (late ack) or session_gone (late refusal); every other
// result is final.
func expectedSendEdge(from, to protocol.SendResult) bool {
	switch from {
	case protocol.SendSending:
		return to != protocol.SendSending
	case protocol.SendGatewayUncertain:
		return to != protocol.SendSending && to != protocol.SendGatewayUncertain
	case protocol.SendDeliveryUncertain:
		return to == protocol.SendHostStored || to == protocol.SendSessionGone
	}
	return false
}
