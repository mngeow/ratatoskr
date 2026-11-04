package store

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// insertWithBody adds one message with a caller-chosen body. It exists here so
// the shared helper in store_test.go stays untouched.
func insertWithBody(t *testing.T, s *Store, run, id, body string) {
	t.Helper()

	_, err := s.InsertMessage(context.Background(), Message{
		ID:        id,
		RunID:     run,
		From:      protocol.AgentID(run + "-c"),
		To:        protocol.AgentID(run + "-x"),
		Body:      body,
		CreatedAt: t0,
	})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
}

// TestMessageEventsInOrderWithReasons reads back what the writer recorded:
// InsertMessage writes the initial queued event, then each Transition appends
// one, oldest first.
func TestMessageEventsInOrderWithReasons(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertWithBody(t, s, "r1", "m1", "hello")

	steps := []struct {
		from, to protocol.MessageState
		reason   string
	}{
		{protocol.MessageQueued, protocol.MessageHeld, "busy"},
		{protocol.MessageHeld, protocol.MessageDelivering, ""},
		{protocol.MessageDelivering, protocol.MessagePasted, ""},
		{protocol.MessagePasted, protocol.MessageUnconfirmed, "no_confirmation"},
	}
	for i, step := range steps {
		at := t0.Add(time.Duration(i+1) * time.Minute)
		if err := s.Transition(ctx, "m1", step.from, step.to, step.reason, at); err != nil {
			t.Fatalf("Transition %s -> %s: %v", step.from, step.to, err)
		}
	}

	events, err := s.MessageEvents(ctx, "m1")
	if err != nil {
		t.Fatalf("MessageEvents: %v", err)
	}

	if want := len(steps) + 1; len(events) != want {
		t.Fatalf("got %d events, want %d (the initial queued event plus each transition)", len(events), want)
	}

	// The first event is the message being recorded.
	if events[0].From != "" || events[0].To != protocol.MessageQueued {
		t.Fatalf("first event = %q -> %q, want an empty from and queued", events[0].From, events[0].To)
	}
	if !events[0].At.Equal(t0) {
		t.Fatalf("first event time = %v, want %v", events[0].At, t0)
	}

	for i, step := range steps {
		event := events[i+1]
		if event.From != step.from || event.To != step.to {
			t.Fatalf("event %d = %q -> %q, want %q -> %q", i+1, event.From, event.To, step.from, step.to)
		}
		if event.Reason != step.reason {
			t.Fatalf("event %d reason = %q, want %q", i+1, event.Reason, step.reason)
		}
		want := t0.Add(time.Duration(i+1) * time.Minute)
		if !event.At.Equal(want) {
			t.Fatalf("event %d time = %v, want %v", i+1, event.At, want)
		}
	}
}

// TestMessageEventsUnknownIDIsEmpty: an id with no events is not an error.
func TestMessageEventsUnknownIDIsEmpty(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")

	events, err := s.MessageEvents(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("MessageEvents: %v", err)
	}
	if events == nil {
		t.Fatal("MessageEvents returned a nil slice, want an empty one")
	}
	if len(events) != 0 {
		t.Fatalf("got %d events for an unknown id", len(events))
	}
}

// TestMessageReturnsNamesLatestReasonAndFullBody covers the whole row: display
// names from the agents table, the newest event's reason, and a body far larger
// than a preview.
func TestMessageReturnsNamesLatestReasonAndFullBody(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	body := strings.Repeat("line of text\n", 200) // over 1 KiB, with newlines
	insertWithBody(t, s, "r1", "m1", body)

	if err := s.Transition(ctx, "m1", protocol.MessageQueued, protocol.MessageHeld, "busy", t0.Add(time.Minute)); err != nil {
		t.Fatalf("first Transition: %v", err)
	}
	if err := s.Transition(ctx, "m1", protocol.MessageHeld, protocol.MessageDelivering, "sent", t0.Add(2*time.Minute)); err != nil {
		t.Fatalf("second Transition: %v", err)
	}

	row, err := s.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}

	if row.ID != "m1" || row.RunID != "r1" {
		t.Fatalf("identity = %q / %q", row.ID, row.RunID)
	}
	if row.Seq == 0 {
		t.Fatal("Seq was not populated")
	}
	if row.From != "claude" || row.To != "codex" {
		t.Fatalf("display names = %q -> %q, want claude -> codex", row.From, row.To)
	}
	if row.FromID != "r1-c" || row.ToID != "r1-x" {
		t.Fatalf("agent ids = %q -> %q", row.FromID, row.ToID)
	}
	// The latest reason, not the first.
	if row.Reason != "sent" {
		t.Fatalf("reason = %q, want the latest event's reason %q", row.Reason, "sent")
	}
	if row.State != protocol.MessageDelivering {
		t.Fatalf("state = %q, want delivering", row.State)
	}
	if row.Body != body {
		t.Fatalf("body was altered: got %d bytes, want %d", len(row.Body), len(body))
	}
	if !row.CreatedAt.Equal(t0) {
		t.Fatalf("created = %v, want %v", row.CreatedAt, t0)
	}
}

func TestMessageUnknownIDIsNotFound(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")

	if _, err := s.Message(context.Background(), "does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Message error = %v, want ErrNotFound", err)
	}
}

// TestReadsSurviveTheWritePath seeds through the public API and reads back, which
// is what a user does after a run: the two reads only ever use the read handle.
//
// The latched variant (reads still working after a failed commit) needs
// FailNextCommit, which is rtktest-only, so it is not asserted here.
func TestReadsSurviveTheWritePath(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertWithBody(t, s, "r1", "m1", "after the fact")

	row, err := s.Message(ctx, "m1")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if row.Body != "after the fact" {
		t.Fatalf("body = %q", row.Body)
	}

	events, err := s.MessageEvents(ctx, "m1")
	if err != nil {
		t.Fatalf("MessageEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events for a message that never moved", len(events))
	}

	runs, err := s.Runs(ctx, 10)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "r1" {
		t.Fatalf("Runs = %+v, want the seeded run", runs)
	}
}
