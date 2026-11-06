package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// No sockets: sends, acks and reads go through server.Handler() with recorders, and
// recipient streams are bare stream values attached to the hub.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// sendFixture is a gateway with two paired hosts, claude@alpha and codex@beta.
type sendFixture struct {
	server    *Server
	store     *Store
	sender    string
	recipient string
	now       time.Time
}

// newSendFixture builds the fixture with a fixed clock and the given ack wait.
func newSendFixture(t *testing.T, ackWait time.Duration) *sendFixture {
	t.Helper()

	store, _ := openStore(t, false)
	now := gatewayNow
	ctx := context.Background()

	sender := pairHostFrom(t, store, "alpha", now)
	recipient := pairHostFrom(t, store, "beta", now)

	startSessionsForTest(t, store, "alpha", now, "claude")
	startSessionsForTest(t, store, "beta", now, "codex")
	for _, host := range []string{"alpha", "beta"} {
		if err := store.TouchHeartbeat(ctx, host, now); err != nil {
			t.Fatalf("TouchHeartbeat %s: %v", host, err)
		}
	}

	server := NewServer(store, Options{
		Now:           func() time.Time { return now },
		AckWait:       ackWait,
		PingEvery:     time.Hour,
		WriteDeadline: time.Second,
		QueueSize:     8,
	})

	return &sendFixture{server: server, store: store, sender: sender, recipient: recipient, now: now}
}

// sendRequest issues one POST /v1/messages and returns the recorder.
func (f *sendFixture) sendRequest(t *testing.T, body protocol.SendRequest) *httptest.ResponseRecorder {
	t.Helper()
	return call(f.server, http.MethodPost, "/v1/messages", version(), f.sender, mustJSON(t, body))
}

// decodeSend decodes a send answer.
func decodeSend(t *testing.T, recorder *httptest.ResponseRecorder) protocol.SendResponse {
	t.Helper()

	var response protocol.SendResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("send answer is not a SendResponse: %v (%q)", err, recorder.Body.String())
	}
	return response
}

// attachStreamValue puts a bare stream in the hub, as a connected runtime would.
func attachStreamValue(server *Server, host string, queue int) *stream {
	st, _ := testStream(host, queue)
	server.hub.attach(host, st)
	return st
}

// drainFrame takes one frame from a stream's queue.
func drainFrame(t *testing.T, st *stream) (protocol.StreamFrame, bool) {
	t.Helper()

	select {
	case frame := <-st.frames:
		return frame, true
	default:
		return protocol.StreamFrame{}, false
	}
}

// messageRow reads one stored row.
func messageRow(t *testing.T, store *Store, id string) MessageRow {
	t.Helper()

	row, _, err := store.Message(context.Background(), id)
	if err != nil {
		t.Fatalf("Message %s: %v", id, err)
	}
	return row
}

func TestSendValidationStoresNothing(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)

	tests := []struct {
		name string
		body protocol.SendRequest
	}{
		{name: "bad id", body: protocol.SendRequest{ID: "bad id", From: "claude", To: "codex@beta", Body: "x"}},
		{name: "bad from", body: protocol.SendRequest{ID: "m1", From: "Claude", To: "codex@beta", Body: "x"}},
		{name: "bare to", body: protocol.SendRequest{ID: "m1", From: "claude", To: "codex", Body: "x"}},
		{name: "bad reply_to", body: protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta", ReplyTo: "no/pe", Body: "x"}},
		{name: "empty body", body: protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta"}},
		{name: "oversize body", body: protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta", Body: strings.Repeat("x", maxMessageBody+1)}},
		{name: "sender has no session", body: protocol.SendRequest{ID: "m1", From: "codex", To: "codex@beta", Body: "x"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := fixture.sendRequest(t, tc.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}

			var count int
			if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil {
				t.Fatalf("count messages: %v", err)
			}
			if count != 0 {
				t.Fatalf("a rejected send stored %d rows", count)
			}
		})
	}
}

// TestSendStoresBodyBeforeRouting: the whole body is in the ledger even when the
// recipient is unknown, and the answer carries the directory.
func TestSendStoresBodyBeforeRouting(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)

	body := "the whole body, with an escape \x1b and length " + strings.Repeat("z", 100)
	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "nobody@beta", Body: body,
	}))

	if response.Result != protocol.SendUnknownRecipient {
		t.Fatalf("result = %q, want %q", response.Result, protocol.SendUnknownRecipient)
	}
	if len(response.Directory) == 0 {
		t.Fatal("an unknown-recipient answer carries no directory")
	}

	row := messageRow(t, fixture.store, "m1")
	if row.Body != body {
		t.Fatalf("stored body was altered: %d bytes vs %d", len(row.Body), len(body))
	}
	if row.Result != string(protocol.SendUnknownRecipient) {
		t.Fatalf("stored result = %q", row.Result)
	}
	if row.From != "claude@alpha" || row.To != "nobody@beta" {
		t.Fatalf("addresses = %q -> %q", row.From, row.To)
	}
}

// TestSessionGoneAfterANewerRegistration: the name was seen, but its session is not
// current any more.
func TestSessionGoneAfterANewerRegistration(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	ctx := context.Background()

	// A newer registration for beta ends codex's session.
	startSessionsForTest(t, fixture.store, "beta", fixture.now.Add(time.Minute), "other")
	if err := fixture.store.TouchHeartbeat(ctx, "beta", fixture.now); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	attached := attachStreamValue(fixture.server, "beta", 8)

	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))
	if response.Result != protocol.SendSessionGone {
		t.Fatalf("result = %q, want %q", response.Result, protocol.SendSessionGone)
	}
	if frame, ok := drainFrame(t, attached); ok {
		t.Fatalf("a session_gone message was offered: %+v", frame)
	}
}

// TestDestinationUnreachableWithoutALiveStream covers both halves of HostLive.
func TestDestinationUnreachableWithoutALiveStream(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, f *sendFixture) *stream
	}{
		{
			name:    "no stream",
			arrange: func(t *testing.T, f *sendFixture) *stream { return nil },
		},
		{
			name: "stream but a stale heartbeat",
			arrange: func(t *testing.T, f *sendFixture) *stream {
				st := attachStreamValue(f.server, "beta", 8)
				if err := f.store.TouchHeartbeat(context.Background(), "beta", f.now.Add(-liveWindow-time.Second)); err != nil {
					t.Fatalf("TouchHeartbeat: %v", err)
				}
				return st
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSendFixture(t, 10*time.Millisecond)
			attached := tc.arrange(t, fixture)

			response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
				ID: "m1", From: "claude", To: "codex@beta", Body: "x",
			}))
			if response.Result != protocol.SendDestinationUnreachable {
				t.Fatalf("result = %q, want %q", response.Result, protocol.SendDestinationUnreachable)
			}

			row := messageRow(t, fixture.store, "m1")
			if row.Body != "x" {
				t.Fatalf("body was not stored: %q", row.Body)
			}
			if attached != nil {
				if frame, ok := drainFrame(t, attached); ok {
					t.Fatalf("an unreachable host was offered a frame: %+v", frame)
				}
			}
		})
	}
}

// TestOfferTimeoutIsUncertainWithOneOffer: no ack means uncertain, never a failure.
func TestOfferTimeoutIsUncertainWithOneOffer(t *testing.T) {
	fixture := newSendFixture(t, 20*time.Millisecond)
	attached := attachStreamValue(fixture.server, "beta", 8)

	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))
	if response.Result != protocol.SendDeliveryUncertain {
		t.Fatalf("result = %q, want %q", response.Result, protocol.SendDeliveryUncertain)
	}

	offers := 0
	for {
		frame, ok := drainFrame(t, attached)
		if !ok {
			break
		}
		if frame.Type == protocol.FrameOffer {
			offers++
			if frame.Offer == nil || frame.Offer.To != "codex" {
				t.Fatalf("offer = %+v, want the recipient's local name", frame.Offer)
			}
		}
	}
	if offers != 1 {
		t.Fatalf("%d offers were enqueued, want exactly 1", offers)
	}

	if row := messageRow(t, fixture.store, "m1"); row.Result != string(protocol.SendDeliveryUncertain) {
		t.Fatalf("stored result = %q", row.Result)
	}
}

// TestAckSavedGivesHostStored: the inline path, then a duplicate that offers nothing.
func TestAckSavedGivesHostStored(t *testing.T) {
	fixture := newSendFixture(t, 2*time.Second)
	attached := attachStreamValue(fixture.server, "beta", 8)

	answers := make(chan protocol.SendResponse, 1)
	go func() {
		recorder := fixture.sendRequest(t, protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta", Body: "x"})
		answers <- decodeSend(t, recorder)
	}()

	waitFor(t, "the offer frame", func() bool {
		select {
		case frame := <-attached.frames:
			return frame.Type == protocol.FrameOffer
		default:
			return false
		}
	})

	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.recipient,
		mustJSON(t, protocol.AckRequest{Saved: true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("ack = %d, want 204: %s", recorder.Code, recorder.Body.String())
	}

	select {
	case response := <-answers:
		if response.Result != protocol.SendHostStored {
			t.Fatalf("result = %q, want %q", response.Result, protocol.SendHostStored)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the send did not finish after the ack")
	}

	// A duplicate id answers the stored result and offers nothing again.
	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta", Body: "x"}))
	if response.Result != protocol.SendHostStored {
		t.Fatalf("duplicate result = %q, want %q", response.Result, protocol.SendHostStored)
	}
	if frame, ok := drainFrame(t, attached); ok {
		t.Fatalf("a duplicate id was offered again: %+v", frame)
	}
}

// TestAckRefusalCarriesTheReason: a refusal is session_gone with the host's text in
// the answer, the ledger and the event.
func TestAckRefusalCarriesTheReason(t *testing.T) {
	fixture := newSendFixture(t, 2*time.Second)
	attached := attachStreamValue(fixture.server, "beta", 8)

	answers := make(chan protocol.SendResponse, 1)
	go func() {
		recorder := fixture.sendRequest(t, protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta", Body: "x"})
		answers <- decodeSend(t, recorder)
	}()

	waitFor(t, "the offer frame", func() bool {
		select {
		case frame := <-attached.frames:
			return frame.Type == protocol.FrameOffer
		default:
			return false
		}
	})

	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.recipient,
		mustJSON(t, protocol.AckRequest{Saved: false, Reason: "exited"}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("ack = %d, want 204", recorder.Code)
	}

	select {
	case response := <-answers:
		if response.Result != protocol.SendSessionGone {
			t.Fatalf("result = %q, want %q", response.Result, protocol.SendSessionGone)
		}
		if !strings.Contains(response.Detail, "exited") {
			t.Fatalf("detail = %q, want the host's reason", response.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the send did not finish")
	}

	row := messageRow(t, fixture.store, "m1")
	if !strings.Contains(row.ResultReason, "exited") {
		t.Fatalf("stored reason = %q", row.ResultReason)
	}
	_, events, err := fixture.store.Message(context.Background(), "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if last := events[len(events)-1]; !strings.Contains(last.Reason, "exited") {
		t.Fatalf("last event reason = %q", last.Reason)
	}
}

// TestLateAckCorrectsAndReports: a saved ack after delivery_uncertain corrects the
// ledger and pushes a report frame to the sender.
func TestLateAckCorrectsAndReports(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	recipientStream := attachStreamValue(fixture.server, "beta", 8)
	senderStream := attachStreamValue(fixture.server, "alpha", 8)

	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "codex@beta", Body: "x",
	}))
	if response.Result != protocol.SendDeliveryUncertain {
		t.Fatalf("result = %q, want delivery_uncertain", response.Result)
	}
	// Drop the offer so the queue is not what is under test.
	_, _ = drainFrame(t, recipientStream)

	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.recipient,
		mustJSON(t, protocol.AckRequest{Saved: true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("late ack = %d, want 204", recorder.Code)
	}

	if row := messageRow(t, fixture.store, "m1"); row.Result != string(protocol.SendHostStored) {
		t.Fatalf("stored result = %q, want host_stored", row.Result)
	}

	waitFor(t, "a report frame to the sender", func() bool {
		select {
		case frame := <-senderStream.frames:
			return frame.Type == protocol.FrameReport && frame.Report != nil &&
				frame.Report.Result == protocol.SendHostStored
		default:
			return false
		}
	})
}

// TestAckFromTheWrongHostIsNotFound: only the recipient may acknowledge.
func TestAckFromTheWrongHostIsNotFound(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)

	response := decodeSend(t, fixture.sendRequest(t, protocol.SendRequest{
		ID: "m1", From: "claude", To: "nobody@beta", Body: "x",
	}))
	if response.Result != protocol.SendUnknownRecipient {
		t.Fatalf("result = %q", response.Result)
	}

	// The sender's own credential is not the recipient's.
	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/ack", version(), fixture.sender,
		mustJSON(t, protocol.AckRequest{Saved: true}))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("ack from the wrong host = %d, want 404", recorder.Code)
	}

	row := messageRow(t, fixture.store, "m1")
	if row.Result != string(protocol.SendUnknownRecipient) {
		t.Fatalf("a wrong-host ack changed the result to %q", row.Result)
	}
}

// TestGetMessageReportsStoredAsUncertain: a read of an undecided message is honest
// about being undecided, and only the two parties may read it.
func TestGetMessageReportsStoredAsUncertain(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	ctx := context.Background()

	if _, _, err := fixture.store.InsertMessage(ctx, MessageIn{
		ID: "m1", From: "claude@alpha", To: "codex@beta", Body: "x", At: fixture.now,
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	recorder := call(fixture.server, http.MethodGet, "/v1/messages/m1", version(), fixture.sender, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var info protocol.MessageInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &info); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Result != protocol.SendGatewayUncertain {
		t.Fatalf("result = %q, want %q for a stored message", info.Result, protocol.SendGatewayUncertain)
	}

	// A third host learns nothing: the answer is the same as for a missing message.
	third := pairHostFrom(t, fixture.store, "gamma", fixture.now)
	recorder = call(fixture.server, http.MethodGet, "/v1/messages/m1", version(), third, "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET from a third host = %d, want 404", recorder.Code)
	}
}

// TestStatusRankAndReportFrame: a lower rank is ignored, a higher one applies and
// reaches the sender.
func TestStatusRankAndReportFrame(t *testing.T) {
	fixture := newSendFixture(t, 10*time.Millisecond)
	ctx := context.Background()

	if _, _, err := fixture.store.InsertMessage(ctx, MessageIn{
		ID: "m1", From: "claude@alpha", To: "codex@beta", Body: "x", At: fixture.now,
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	senderStream := attachStreamValue(fixture.server, "alpha", 8)

	// accepted (rank 6) arrives first.
	recorder := call(fixture.server, http.MethodPost, "/v1/messages/m1/status", version(), fixture.recipient,
		mustJSON(t, protocol.StatusReport{State: protocol.MessageAccepted, Reason: "ok"}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if row := messageRow(t, fixture.store, "m1"); row.Report != string(protocol.MessageAccepted) {
		t.Fatalf("report = %q, want accepted", row.Report)
	}

	// held (rank 2) is older news and is ignored.
	recorder = call(fixture.server, http.MethodPost, "/v1/messages/m1/status", version(), fixture.recipient,
		mustJSON(t, protocol.StatusReport{State: protocol.MessageHeld, Reason: "busy"}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("older status = %d, want 204", recorder.Code)
	}
	if row := messageRow(t, fixture.store, "m1"); row.Report != string(protocol.MessageAccepted) {
		t.Fatalf("an older report overwrote the newer one: %q", row.Report)
	}

	// An invalid state is a bad request.
	recorder = call(fixture.server, http.MethodPost, "/v1/messages/m1/status", version(), fixture.recipient,
		mustJSON(t, protocol.StatusReport{State: "nonsense"}))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid state = %d, want 400", recorder.Code)
	}

	// The report frame reached the sender, with the reason cleaned.
	waitFor(t, "a report frame", func() bool {
		select {
		case frame := <-senderStream.frames:
			return frame.Type == protocol.FrameReport && frame.Report != nil
		default:
			return false
		}
	})
}

// TestSetResultFrom covers the compare-and-set the routing depends on.
func TestSetResultFrom(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	storeMessage(t, store, "m1", "claude@alpha", "codex@beta", "x")

	// An empty source list is a programming error, refused before the database.
	if _, err := store.SetResultFrom(ctx, "m1", nil, "host_stored", "", "", ledgerWriteNow); err == nil {
		t.Fatal("SetResultFrom accepted an empty source list")
	}

	changed, err := store.SetResultFrom(ctx, "m1", []string{"host_stored"}, "session_gone", "no", "", ledgerWriteNow)
	if err != nil {
		t.Fatalf("SetResultFrom from the wrong result: %v", err)
	}
	if changed {
		t.Fatal("SetResultFrom changed a row that was not in the source list")
	}

	changed, err = store.SetResultFrom(ctx, "m1", []string{resultStored}, "delivery_uncertain", "no ack", "", ledgerWriteNow)
	if err != nil {
		t.Fatalf("SetResultFrom: %v", err)
	}
	if !changed {
		t.Fatal("SetResultFrom did not change a row that was in the source list")
	}

	_, events, err := store.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	// One event from the insert, one from this change.
	if len(events) != 2 {
		t.Fatalf("%d events, want 2", len(events))
	}
	if last := events[1]; last.Value != "delivery_uncertain" || last.Reason != "no ack" {
		t.Fatalf("event = %+v", last)
	}

	if _, err := store.SetResultFrom(ctx, "missing", []string{resultStored}, "host_stored", "", "", ledgerWriteNow); err != ErrNoSuchMessage {
		t.Fatalf("SetResultFrom for an unknown id = %v, want ErrNoSuchMessage", err)
	}
}
