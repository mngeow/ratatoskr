package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

var ledgerWriteNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// storeMessage stores one message and fails the test if it cannot.
func storeMessage(t *testing.T, store *Store, id, from, to, body string) {
	t.Helper()

	inserted, _, err := store.InsertMessage(context.Background(), MessageIn{
		ID:          id,
		From:        from,
		To:          to,
		Body:        body,
		FromSession: "session-a",
		At:          ledgerWriteNow,
	})
	if err != nil {
		t.Fatalf("InsertMessage %s: %v", id, err)
	}
	if !inserted {
		t.Fatalf("InsertMessage %s reported an existing row", id)
	}
}

func TestInsertMessageStoresAndDedupes(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	storeMessage(t, store, "m1", "codex@alpha", "claude@beta", "hello")

	row, events, err := store.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if row.Result != resultStored {
		t.Fatalf("result = %q, want %q", row.Result, resultStored)
	}
	if row.FromSession != "session-a" {
		t.Fatalf("from session = %q", row.FromSession)
	}
	if len(events) != 1 || events[0].Kind != "result" || events[0].Value != resultStored {
		t.Fatalf("events = %+v, want one stored result", events)
	}

	// The same id again: inserted=false, the stored row back, nothing written.
	inserted, existing, err := store.InsertMessage(ctx, MessageIn{
		ID: "m1", From: "someone@else", To: "x@y", Body: "different", At: ledgerWriteNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("duplicate InsertMessage: %v", err)
	}
	if inserted {
		t.Fatal("a duplicate id was reported as inserted")
	}
	if existing.ID != "m1" || existing.Body != "hello" || existing.From != "codex@alpha" {
		t.Fatalf("existing row = %+v, want the stored one", existing)
	}

	var messages, eventCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM message_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if messages != 1 || eventCount != 1 {
		t.Fatalf("rows after the duplicate = %d messages, %d events; want 1 and 1", messages, eventCount)
	}
}

func TestSetResult(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	storeMessage(t, store, "m1", "codex@alpha", "claude@beta", "hello")

	if err := store.SetResult(ctx, "m1", "host_stored", "saved", "session-b", ledgerWriteNow.Add(time.Minute)); err != nil {
		t.Fatalf("SetResult: %v", err)
	}

	row, events, err := store.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if row.Result != "host_stored" || row.ResultReason != "saved" || row.ToSession != "session-b" {
		t.Fatalf("row = %+v", row)
	}
	last := events[len(events)-1]
	if last.Kind != "result" || last.Value != "host_stored" || last.Reason != "saved" {
		t.Fatalf("last event = %+v", last)
	}

	// An empty to_session keeps the stored one, but an empty reason overwrites.
	if err := store.SetResult(ctx, "m1", "host_stored", "", "", ledgerWriteNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("SetResult without a session: %v", err)
	}
	row, _, err = store.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if row.ToSession != "session-b" {
		t.Fatalf("to session = %q, want the earlier one kept", row.ToSession)
	}
	if row.ResultReason != "" {
		t.Fatalf("reason = %q, want it cleared by the empty value", row.ResultReason)
	}

	if err := store.SetResult(ctx, "missing", "host_stored", "", "", ledgerWriteNow); !errors.Is(err, ErrNoSuchMessage) {
		t.Fatalf("SetResult for an unknown id = %v, want ErrNoSuchMessage", err)
	}
}

func TestSetReport(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	storeMessage(t, store, "m1", "codex@alpha", "claude@beta", "hello")

	if err := store.SetReport(ctx, "m1", string(protocol.MessageHeld), "busy", ledgerWriteNow.Add(time.Minute)); err != nil {
		t.Fatalf("SetReport: %v", err)
	}

	row, events, err := store.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if row.Report != string(protocol.MessageHeld) || row.ReportReason != "busy" {
		t.Fatalf("report = %q / %q", row.Report, row.ReportReason)
	}
	// A report never changes the result.
	if row.Result != resultStored {
		t.Fatalf("result = %q, want it untouched", row.Result)
	}
	last := events[len(events)-1]
	if last.Kind != "report" || last.Value != string(protocol.MessageHeld) {
		t.Fatalf("last event = %+v", last)
	}

	before := len(events)
	if err := store.SetReport(ctx, "m1", string(protocol.MessageHeld), "busy", ledgerWriteNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("repeated SetReport: %v", err)
	}
	if _, events, err = store.Message(ctx, "m1"); err != nil {
		t.Fatalf("Message: %v", err)
	}
	if len(events) != before {
		t.Fatalf("a repeated report added %d events", len(events)-before)
	}

	if err := store.SetReport(ctx, "m1", "", "", ledgerWriteNow); err == nil {
		t.Fatal("SetReport accepted an empty state")
	}
	if err := store.SetReport(ctx, "missing", string(protocol.MessageHeld), "", ledgerWriteNow); !errors.Is(err, ErrNoSuchMessage) {
		t.Fatalf("SetReport for an unknown id = %v, want ErrNoSuchMessage", err)
	}
}

func TestReplyTarget(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	storeMessage(t, store, "m1", "codex@alpha", "claude@beta", "hello")
	if err := store.SetResult(ctx, "m1", "host_stored", "", "session-b", ledgerWriteNow); err != nil {
		t.Fatalf("SetResult: %v", err)
	}

	from, fromSession, to, toSession, err := store.ReplyTarget(ctx, "m1")
	if err != nil {
		t.Fatalf("ReplyTarget: %v", err)
	}
	if from != "codex@alpha" || fromSession != "session-a" || to != "claude@beta" || toSession != "session-b" {
		t.Fatalf("targets = %q/%q -> %q/%q", from, fromSession, to, toSession)
	}

	if _, _, _, _, err := store.ReplyTarget(ctx, "missing"); !errors.Is(err, ErrNoSuchMessage) {
		t.Fatalf("ReplyTarget for an unknown id = %v, want ErrNoSuchMessage", err)
	}
}

// TestToSessionEndedInEveryRead: the marker has to appear wherever a row is read,
// not just in the admin view.
func TestToSessionEndedInEveryRead(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	storeMessage(t, store, "m1", "codex@alpha", "claude@beta", "hello")
	storeMessage(t, store, "m2", "codex@alpha", "claude@beta", "still open")

	startSessionsForTest(t, store, "beta", ledgerWriteNow, "claude")
	sessions, err := store.Directory(ctx)
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	_ = sessions

	// Point the first message at a session that is then ended.
	if err := store.SetResult(ctx, "m1", "host_stored", "", "ended-session", ledgerWriteNow); err != nil {
		t.Fatalf("SetResult: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES('ended-session','beta','claude',?,?)`,
		ledgerWriteNow.UnixMilli(), ledgerWriteNow.UnixMilli()); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Point the second at an open one.
	if err := store.SetResult(ctx, "m2", "host_stored", "", "open-session", ledgerWriteNow); err != nil {
		t.Fatalf("SetResult: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES('open-session','beta','claude',?,NULL)`,
		ledgerWriteNow.UnixMilli()); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	row, _, err := store.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if !row.ToSessionEnded {
		t.Fatal("Message did not report an ended recipient session")
	}

	tail, err := store.Tail(ctx, 10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	for _, candidate := range tail {
		switch candidate.ID {
		case "m1":
			if !candidate.ToSessionEnded {
				t.Fatal("Tail did not report an ended recipient session")
			}
		case "m2":
			if candidate.ToSessionEnded {
				t.Fatal("Tail reported an open session as ended")
			}
		}
	}

	rows, err := store.Log(ctx, 0, 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	for _, candidate := range rows {
		if candidate.ID == "m1" && !candidate.ToSessionEnded {
			t.Fatal("Log did not report an ended recipient session")
		}
	}
}
