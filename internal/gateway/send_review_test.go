package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// These cover cases the first draft of the routing tests left out: a late refusal,
// reply_to against an ended session, the current session being the offered one,
// a full queue at offer time, an ack for something never offered, an outcome that
// must still be recorded after the sender hangs up, and a repeated in-flight id.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// TestLateRefusalCorrectsToSessionGone: offered, no ack in time, then the host
// refuses. The sender's record must stop saying "may still arrive".
func TestLateRefusalCorrectsToSessionGone(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	recipientStream := attachStreamValue(fixture.server, "beta", 8)
	senderStream := attachStreamValue(fixture.server, "alpha", 8)

	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))
	if response.Result != protocol.SendDeliveryUncertain {
		t.Fatalf("result = %q, want delivery_uncertain", response.Result)
	}
	_, _ = drainFrame(t, recipientStream)

	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.recipient,
		mustJSON(t, protocol.AckRequest{Saved: false, Reason: "exited"}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("late refusal = %d, want 204", recorder.Code)
	}

	row := messageRow(t, fixture.store, "m1")
	if row.Result != string(protocol.SendSessionGone) || !strings.Contains(row.ResultReason, "exited") {
		t.Fatalf("stored = %q / %q, want session_gone carrying the reason", row.Result, row.ResultReason)
	}

	waitFor(t, "a report frame carrying the refusal", func() bool {
		select {
		case frame := <-senderStream.frames:
			return frame.Type == protocol.FrameReport && frame.Report != nil &&
				frame.Report.Result == protocol.SendSessionGone &&
				strings.Contains(frame.Report.ResultReason, "exited")
		default:
			return false
		}
	})
}

// TestFinalResultsAreNeverOverwrittenByLaterAcks: host_stored stays after a
// refusal, and a refusal stays after a saved ack.
func TestFinalResultsAreNeverOverwrittenByLaterAcks(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	recipientStream := attachStreamValue(fixture.server, "beta", 8)

	_ = decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))
	_, _ = drainFrame(t, recipientStream)

	ack := func(saved bool, reason string) {
		t.Helper()
		recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.recipient,
			mustJSON(t, protocol.AckRequest{Saved: saved, Reason: reason}))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("ack = %d, want 204", recorder.Code)
		}
	}

	ack(true, "")
	ack(false, "too late")
	if row := messageRow(t, fixture.store, "m1"); row.Result != string(protocol.SendHostStored) {
		t.Fatalf("a refusal overwrote host_stored: %q", row.Result)
	}

	_ = decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m2", From: "claude", To: "codex@beta", Body: "y",
	}))
	_, _ = drainFrame(t, recipientStream)
	ack2 := func(saved bool, reason string) {
		t.Helper()
		recorder := call(fixture.server, http.MethodPost, "/v1/messages/m2/ack", version(), fixture.recipient,
			mustJSON(t, protocol.AckRequest{Saved: saved, Reason: reason}))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("ack = %d, want 204", recorder.Code)
		}
	}
	ack2(false, "exited")
	ack2(true, "")
	if row := messageRow(t, fixture.store, "m2"); row.Result != string(protocol.SendSessionGone) {
		t.Fatalf("a saved ack overwrote a refusal: %q", row.Result)
	}
}

// TestReplyToAnEndedSessionIsSessionGone: a reply to a message whose sender
// session has since ended is not given to the sender's newer session.
func TestReplyToAnEndedSessionIsSessionGone(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	ctx := context.Background()

	old, found, err := fixture.store.CurrentSession(ctx, "beta", "codex")
	if err != nil || !found {
		t.Fatalf("CurrentSession: found=%v err=%v", found, err)
	}
	// codex@beta sent m0 to claude@alpha from its first session.
	if _, _, err := fixture.store.InsertMessage(ctx, MessageIn{
		ID: "m0", From: "codex@beta", To: "claude@alpha", Body: "question",
		FromSession: old.ID, At: fixture.now,
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// beta restarts: a new session for the same name.
	if _, err := fixture.store.StartSessions(ctx, "beta", []string{"codex"}, fixture.now, idGenerator("c")); err != nil {
		t.Fatalf("StartSessions: %v", err)
	}
	if err := fixture.store.TouchHeartbeat(ctx, "beta", fixture.now); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	recipientStream := attachStreamValue(fixture.server, "beta", 8)

	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", ReplyTo: "m0", Body: "answer",
	}))
	if response.Result != protocol.SendSessionGone {
		t.Fatalf("result = %q, want session_gone", response.Result)
	}
	if row := messageRow(t, fixture.store, "m1"); row.Body != "answer" {
		t.Fatalf("the failure was not stored with its full body: %+v", row)
	}
	if _, offered := drainFrame(t, recipientStream); offered {
		t.Fatal("a reply to an ended session was offered to the new session")
	}

	// A reply_to the gateway has never seen is not judged.
	response = decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m2", From: "claude", To: "codex@beta", ReplyTo: "unknown-id", Body: "answer",
	}))
	if response.Result == protocol.SendSessionGone || response.Result == protocol.SendUnknownRecipient {
		t.Fatalf("an unknown reply_to was judged: %q", response.Result)
	}
}

// TestPlainNameReachesTheCurrentSession: after a restart the offer carries the NEW
// session and the recipient's local name.
func TestPlainNameReachesTheCurrentSession(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	ctx := context.Background()

	if _, err := fixture.store.StartSessions(ctx, "beta", []string{"codex"}, fixture.now, idGenerator("c")); err != nil {
		t.Fatalf("StartSessions: %v", err)
	}
	current, _, err := fixture.store.CurrentSession(ctx, "beta", "codex")
	if err != nil {
		t.Fatalf("CurrentSession: %v", err)
	}
	recipientStream := attachStreamValue(fixture.server, "beta", 8)

	_ = decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))
	frame, ok := drainFrame(t, recipientStream)
	if !ok || frame.Type != protocol.FrameOffer || frame.Offer == nil {
		t.Fatalf("no offer was queued: %+v ok=%v", frame, ok)
	}
	if frame.Offer.ToSession != current.ID || frame.Offer.To != "codex" || frame.Offer.From != "claude@alpha" {
		t.Fatalf("offer = %+v, want the current session, local name and sender address", frame.Offer)
	}
	if row := messageRow(t, fixture.store, "m1"); row.ToSession != current.ID {
		t.Fatalf("stored to_session = %q, want %q", row.ToSession, current.ID)
	}
}

// TestFullQueueAtOfferTimeIsDestinationUnreachable: the offer was never written, so
// this is a definite failure, and no second frame was added.
func TestFullQueueAtOfferTimeIsDestinationUnreachable(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	recipientStream := attachStreamValue(fixture.server, "beta", 1)
	recipientStream.frames <- protocol.StreamFrame{Type: protocol.FramePing}

	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))
	if response.Result != protocol.SendDestinationUnreachable {
		t.Fatalf("result = %q, want destination_unreachable", response.Result)
	}
	if got := len(recipientStream.frames); got != 1 {
		t.Fatalf("queue holds %d frames, want only the filler", got)
	}
	if row := messageRow(t, fixture.store, "m1"); row.Body != "x" || row.Result != string(protocol.SendDestinationUnreachable) {
		t.Fatalf("stored = %+v, want the full body and the failure", row)
	}
}

// TestAckForAMessageNeverOfferedIsIgnored: a host cannot decide a result by acking
// something the gateway has not offered it.
func TestAckForAMessageNeverOfferedIsIgnored(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	ctx := context.Background()

	if _, _, err := fixture.store.InsertMessage(ctx, MessageIn{
		ID: "m1", From: "claude@alpha", To: "codex@beta", Body: "x", At: fixture.now,
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.recipient,
		mustJSON(t, protocol.AckRequest{Saved: true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("ack = %d, want 204", recorder.Code)
	}
	if row := messageRow(t, fixture.store, "m1"); row.Result != resultStored {
		t.Fatalf("an ack for an unoffered message changed the result to %q", row.Result)
	}
}

// TestOutcomeIsRecordedAfterTheSenderHangsUp: once the offer is out, the sender's
// connection ending must not leave the row undecided.
func TestOutcomeIsRecordedAfterTheSenderHangsUp(t *testing.T) {
	fixture := newSendFixture(t, time.Minute) // only the cancelled context can end the wait
	recipientStream := attachStreamValue(fixture.server, "beta", 8)

	ctx, hangUp := context.WithCancel(context.Background())
	defer hangUp()

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(mustJSON(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))).WithContext(ctx)
	request.Header.Set(protocol.WireVersionHeader, version())
	request.Header.Set("Authorization", "Bearer "+fixture.sender)
	recorder := httptest.NewRecorder()

	finished := make(chan struct{})
	go func() {
		fixture.server.Handler().ServeHTTP(recorder, request)
		close(finished)
	}()

	waitFor(t, "the offer", func() bool {
		select {
		case frame := <-recipientStream.frames:
			return frame.Type == protocol.FrameOffer
		default:
			return false
		}
	})
	hangUp()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler did not return after the sender hung up")
	}

	if row := messageRow(t, fixture.store, "m1"); row.Result != string(protocol.SendDeliveryUncertain) {
		t.Fatalf("stored result = %q, want delivery_uncertain: an outcome must be recorded after a hang-up", row.Result)
	}
}

// TestRepeatOfAnInFlightIdWaitsForTheFirstResult: the second request writes
// nothing, waits, and gets the first request's final answer; one offer total.
func TestRepeatOfAnInFlightIdWaitsForTheFirstResult(t *testing.T) {
	fixture := newSendFixture(t, 2*time.Second)
	recipientStream := attachStreamValue(fixture.server, "beta", 8)

	type answer struct{ response protocol.SendResponse }
	first := make(chan answer, 1)
	go func() {
		first <- answer{decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
			ID: "m1", From: "claude", To: "codex@beta", Body: "x",
		}))}
	}()

	waitFor(t, "the first request to store the message", func() bool {
		row, _, err := fixture.store.Message(context.Background(), "m1")
		return err == nil && row.ToSession != ""
	})

	second := make(chan answer, 1)
	go func() {
		second <- answer{decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
			ID: "m1", From: "claude", To: "codex@beta", Body: "x",
		}))}
	}()

	// The recipient host acknowledges once.
	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.recipient,
		mustJSON(t, protocol.AckRequest{Saved: true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("ack = %d, want 204", recorder.Code)
	}

	for name, channel := range map[string]chan answer{"first": first, "second": second} {
		select {
		case got := <-channel:
			if got.response.Result != protocol.SendHostStored {
				t.Fatalf("%s answer = %q, want host_stored", name, got.response.Result)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the %s request never answered", name)
		}
	}

	offers := 0
	for {
		if _, ok := drainFrame(t, recipientStream); !ok {
			break
		}
		offers++
	}
	if offers != 1 {
		t.Fatalf("%d offers were queued, want exactly 1", offers)
	}
}
