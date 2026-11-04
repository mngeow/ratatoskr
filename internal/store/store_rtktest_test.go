//go:build rtktest

package store

// Acceptance check 12 at the store level. Written under GR-3, never run.

import (
	"context"
	"errors"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

func TestFailedCommitLeavesNoRecordAndStopsAllWrites(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	FailNextCommit()
	_, err := s.InsertMessage(ctx, Message{ID: "m1", RunID: "r1", From: "r1-c", To: "r1-x", Body: "x", CreatedAt: t0})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("InsertMessage err = %v, want ErrUnavailable", err)
	}
	if ok, _ := s.MessageExists(ctx, "r1", "m1"); ok {
		t.Error("message exists although its commit failed")
	}
	if err := s.Transition(ctx, "m1", protocol.MessageQueued, protocol.MessageDelivering, "", t0); !errors.Is(err, ErrUnavailable) {
		t.Errorf("write after failure err = %v, want ErrUnavailable", err)
	}
}

// TestReadsStillWorkAfterTheWritePathLatches: once a commit has failed, writes are
// refused for good, but a user must still be able to read history and recover a
// message body. Written under GR-3 and never run.
func TestReadsStillWorkAfterTheWritePathLatches(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	insertWithBody(t, s, "r1", "m1", "recover me")
	ctx := context.Background()

	FailNextCommit()
	_, err := s.InsertMessage(ctx, Message{ID: "m2", RunID: "r1", From: "r1-c", To: "r1-x", Body: "x", CreatedAt: t0})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("InsertMessage err = %v, want ErrUnavailable", err)
	}

	row, err := s.Message(ctx, "m1")
	if err != nil || row.Body != "recover me" {
		t.Fatalf("Message after latch = %+v, %v", row, err)
	}
	if events, err := s.MessageEvents(ctx, "m1"); err != nil || len(events) != 1 {
		t.Fatalf("MessageEvents after latch = %d events, %v", len(events), err)
	}
	if runs, err := s.Runs(ctx, 10); err != nil || len(runs) != 1 {
		t.Fatalf("Runs after latch = %+v, %v", runs, err)
	}
	if _, err := s.Message(ctx, "m2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the failed insert left a row: %v", err)
	}
}
