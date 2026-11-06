package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// insertMessage writes a ledger row directly. The routing slice owns this write
// path; these tests only need rows to read.
func insertMessage(t *testing.T, store *Store, id, from, to, body, result string, at time.Time) {
	t.Helper()

	if _, err := store.db.ExecContext(context.Background(),
		`INSERT INTO messages(id, from_address, to_address, reply_to, body, result, report, report_reason, created_at, updated_at)
		 VALUES(?,?,?,'',?,?,'','',?,?)`,
		id, from, to, body, result, at.UnixMilli(), at.UnixMilli()); err != nil {
		t.Fatalf("insert message %s: %v", id, err)
	}
}

func insertEvent(t *testing.T, store *Store, id, kind, value, reason string, at time.Time) {
	t.Helper()

	if _, err := store.db.ExecContext(context.Background(),
		`INSERT INTO message_events(message_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
		id, at.UnixMilli(), kind, value, reason); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

var ledgerNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func TestTailAndLog(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	for i, id := range []string{"m1", "m2", "m3"} {
		insertMessage(t, store, id, "codex@mac", "claude@ubuntu", "body "+id, "host_stored",
			ledgerNow.Add(time.Duration(i)*time.Minute))
	}

	tail, err := store.Tail(ctx, 2)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(tail) != 2 {
		t.Fatalf("Tail returned %d rows, want 2", len(tail))
	}
	// Oldest first, even though the SQL read newest first.
	if tail[0].ID != "m2" || tail[1].ID != "m3" {
		t.Fatalf("tail order = %q, %q; want m2, m3", tail[0].ID, tail[1].ID)
	}
	if !tail[1].CreatedAt.Equal(ledgerNow.Add(2 * time.Minute)) {
		t.Fatalf("created at = %v", tail[1].CreatedAt)
	}

	after, err := store.Log(ctx, tail[0].Seq, 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(after) != 1 || after[0].ID != "m3" {
		t.Fatalf("Log after %d = %+v, want just m3", tail[0].Seq, after)
	}

	// A non-positive limit falls back to the default, not to zero rows.
	all, err := store.Log(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Log with no limit: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("Log with no limit returned %d rows, want 3", len(all))
	}
}

func TestMessageWithEvents(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	insertMessage(t, store, "m1", "codex@mac", "claude@ubuntu", "line one\nline two\tkept", "delivery_uncertain", ledgerNow)
	insertEvent(t, store, "m1", "result", "delivery_uncertain", "no ack", ledgerNow)
	insertEvent(t, store, "m1", "report", "accepted", "", ledgerNow.Add(time.Minute))

	message, events, err := store.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if message.Body != "line one\nline two\tkept" {
		t.Fatalf("body = %q, want the stored text untouched", message.Body)
	}
	if message.From != "codex@mac" || message.To != "claude@ubuntu" {
		t.Fatalf("addresses = %q -> %q", message.From, message.To)
	}
	if len(events) != 2 {
		t.Fatalf("Message returned %d events, want 2", len(events))
	}
	if events[0].Kind != "result" || events[0].Reason != "no ack" {
		t.Fatalf("first event = %+v", events[0])
	}
	if events[1].Kind != "report" || events[1].Value != "accepted" {
		t.Fatalf("second event = %+v", events[1])
	}

	if _, _, err := store.Message(ctx, "missing"); !errors.Is(err, ErrNoSuchMessage) {
		t.Fatalf("Message for an unknown id = %v, want ErrNoSuchMessage", err)
	}
}

// TestLedgerIsAppendOnly: nothing in this package deletes or updates a message
// row, which is why the ledger is only ever read here.
func TestLedgerIsAppendOnly(t *testing.T) {
	store, _ := openStore(t, false)

	// The schema has no trigger or cascade that would remove a ledger row, which
	// is the only way a package this small could delete one by accident.
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger'`).Scan(&count); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if count != 0 {
		t.Fatalf("the schema grew %d triggers; nothing should delete ledger rows", count)
	}
}
