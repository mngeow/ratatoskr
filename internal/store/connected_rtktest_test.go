//go:build rtktest

package store

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// This file needs the rtktest seam, so it carries the build tag: FailNextCommit
// only exists there.

import (
	"context"
	"errors"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// TestReadsSurviveTheLatch is the cross-machine half of the regression bar: a
// failed write latches the store, and the reads through the read handle still
// return what was already recorded.
func TestReadsSurviveTheLatch(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	ctx := context.Background()

	insertTestSend(t, s, "r1", "s1")

	FailNextCommit()
	err := s.InsertSend(ctx, Send{
		ID:        "s2",
		RunID:     "r1",
		From:      protocol.AgentID("r1-c"),
		To:        "claude@ubuntu",
		Body:      "will not commit",
		CreatedAt: connectedNow,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("InsertSend after FailNextCommit = %v, want ErrUnavailable", err)
	}

	sends, err := s.Sends(ctx, "r1", 10)
	if err != nil {
		t.Fatalf("Sends after the latch: %v", err)
	}
	if len(sends) != 1 || sends[0].ID != "s1" {
		t.Fatalf("Sends after the latch = %+v, want the committed row only", sends)
	}

	row, events, err := s.Send(ctx, "s1")
	if err != nil {
		t.Fatalf("Send after the latch: %v", err)
	}
	if row.Result != protocol.SendSending || len(events) != 1 {
		t.Fatalf("Send after the latch = %+v with %d events", row, len(events))
	}
}
