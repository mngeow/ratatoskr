package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them.

// newSendRouter builds a two-agent router ready to accept sends: alpha sends,
// beta receives. The actor is not started, so send runs on the test goroutine.
func newSendRouter(t *testing.T, budgetLimit, maxBody int) (*Router, *fakeStore) {
	t.Helper()
	fake := newFakeStore()
	r, err := New(Config{
		RunID:       "run-1",
		Store:       fake,
		Now:         newFakeClock().now,
		NewID:       counterIDs("m"),
		BudgetLimit: budgetLimit,
		MaxBody:     maxBody,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range []protocol.LaunchSpec{spec("a", "alpha"), spec("b", "beta")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}
	return r, fake
}

func TestSendRefusalsInContractOrder(t *testing.T) {
	tests := []struct {
		name     string
		from     protocol.AgentID
		to       string
		body     string
		replyTo  string
		exited   bool
		wantReas string
		wantBare bool // a plain error, not a *SendError
	}{
		{
			name:     "unknown sender is a wiring bug",
			from:     "nobody",
			to:       "beta",
			body:     "hello",
			wantBare: true,
		},
		{
			name:     "unknown recipient",
			from:     "a",
			to:       "gamma",
			body:     "hello",
			wantReas: ReasonUnknownRecipient,
		},
		{
			name:     "self send",
			from:     "a",
			to:       "alpha",
			body:     "hello",
			wantReas: ReasonUnknownRecipient,
		},
		{
			name:     "recipient exited",
			from:     "a",
			to:       "beta",
			body:     "hello",
			exited:   true,
			wantReas: ReasonRecipientOffline,
		},
		{
			name:     "empty body",
			from:     "a",
			to:       "beta",
			body:     "",
			wantReas: ReasonEmptyBody,
		},
		{
			name:     "whitespace body",
			from:     "a",
			to:       "beta",
			body:     "   \n\t ",
			wantReas: ReasonEmptyBody,
		},
		{
			name:     "control-only body",
			from:     "a",
			to:       "beta",
			body:     "\x1b\x07\x00",
			wantReas: ReasonEmptyBody,
		},
		{
			name:     "unknown reply-to",
			from:     "a",
			to:       "beta",
			body:     "hello",
			replyTo:  "does-not-exist",
			wantReas: ReasonUnknownReplyTo,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, fake := newSendRouter(t, 20, 64<<10)
			if tc.exited {
				if code := 0; true {
					r.markExited(r.agents["b"], &code)
				}
			}

			_, _, err := r.send(tc.from, tc.to, tc.body, tc.replyTo)
			if err == nil {
				t.Fatal("send accepted a message the contract refuses")
			}

			var sendErr *SendError
			switch {
			case tc.wantBare:
				if errors.As(err, &sendErr) {
					t.Fatalf("error = %v, want a plain error, not a *SendError", err)
				}
			default:
				if !errors.As(err, &sendErr) {
					t.Fatalf("error = %v, want a *SendError", err)
				}
				if sendErr.Reason != tc.wantReas {
					t.Fatalf("reason = %q, want %q", sendErr.Reason, tc.wantReas)
				}
			}

			if n := fake.countCall("InsertMessage:"); n != 0 {
				t.Fatalf("a refused send stored %d rows", n)
			}
		})
	}
}

func TestSendTooLargeUsesRawBytes(t *testing.T) {
	r, fake := newSendRouter(t, 20, 8)

	// Nine raw bytes but two runes: the limit is on stored bytes, not runes.
	body := strings.Repeat("é", 4) + "abcd"
	if _, _, err := r.send("a", "beta", body, ""); err == nil {
		t.Fatal("send accepted a body over MaxBody")
	} else {
		var sendErr *SendError
		if !errors.As(err, &sendErr) || sendErr.Reason != ReasonTooLarge {
			t.Fatalf("error = %v, want reason %q", err, ReasonTooLarge)
		}
	}
	if fake.countCall("InsertMessage:") != 0 {
		t.Fatal("an oversized body was stored")
	}

	// Exactly at the limit is allowed: "over" means greater.
	if _, _, err := r.send("a", "beta", "12345678", ""); err != nil {
		t.Fatalf("send of exactly MaxBody bytes: %v", err)
	}
}

func TestSendBudgetStopsAtTheLimit(t *testing.T) {
	r, _ := newSendRouter(t, 2, 64<<10)

	if _, _, err := r.send("a", "beta", "one", ""); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, _, err := r.send("a", "beta", "two", ""); err != nil {
		t.Fatalf("second send: %v", err)
	}
	_, _, err := r.send("a", "beta", "three", "")
	var sendErr *SendError
	if !errors.As(err, &sendErr) || sendErr.Reason != ReasonBudgetExhausted {
		t.Fatalf("third send error = %v, want reason %q", err, ReasonBudgetExhausted)
	}
}

// TestUserSubmittedResetsBudgetOnly exercises the actor path: the budget is the
// only thing a submit resets. The draft flag is controlled by UserDraft alone, so
// a submit while drafting must leave it set.
func TestUserSubmittedResetsBudgetOnly(t *testing.T) {
	fake := newFakeStore()
	r, err := New(Config{
		RunID:       "run-1",
		Store:       fake,
		Now:         newFakeClock().now,
		NewID:       counterIDs("m"),
		BudgetLimit: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r.Start()
	defer r.Close()

	for _, s := range []protocol.LaunchSpec{spec("a", "alpha"), spec("b", "beta")} {
		if err := r.RegisterAgent(context.Background(), s); err != nil {
			t.Fatalf("RegisterAgent: %v", err)
		}
	}

	if _, err := r.Send(context.Background(), "a", "beta", "one", ""); err != nil {
		t.Fatalf("first send: %v", err)
	}
	var sendErr *SendError
	if _, err := r.Send(context.Background(), "a", "beta", "two", ""); !errors.As(err, &sendErr) {
		t.Fatalf("second send error = %v, want a refusal", err)
	}
	if sendErr.Reason != ReasonBudgetExhausted {
		t.Fatalf("second send reason = %q, want %q", sendErr.Reason, ReasonBudgetExhausted)
	}

	// Mark a draft, then submit: the budget resets and the draft survives.
	r.UserDraft("a", true)
	r.UserSubmitted("a")
	flush(t, r)

	if got := r.Snapshot().Budget.Used; got != 0 {
		t.Fatalf("budget used = %d after UserSubmitted, want 0", got)
	}
	if !r.agents["a"].drafting {
		t.Fatal("UserSubmitted cleared the draft flag; only UserDraft may do that")
	}

	if _, err := r.Send(context.Background(), "a", "beta", "three", ""); err != nil {
		t.Fatalf("send after budget reset: %v", err)
	}
}

func TestSendReturnsAfterInsertMessageCommits(t *testing.T) {
	r, fake := newSendRouter(t, 20, 64<<10)

	gate := fake.blockInserts()
	result := make(chan struct {
		id  string
		err error
	}, 1)
	go func() {
		id, _, err := r.send("a", "beta", "hello", "")
		result <- struct {
			id  string
			err error
		}{id: id, err: err}
	}()

	select {
	case got := <-result:
		t.Fatalf("send returned before InsertMessage committed: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	close(gate)
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("send: %v", got.err)
		}
		if got.id == "" {
			t.Fatal("send returned an empty message ID")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("send did not return after the insert committed")
	}
}

func TestSendStorageFailureLatchesAndReports(t *testing.T) {
	r, fake := newSendRouter(t, 20, 64<<10)
	fake.setUnavailable(true)

	_, _, err := r.send("a", "beta", "hello", "")
	var sendErr *SendError
	if !errors.As(err, &sendErr) || sendErr.Reason != ReasonStorageUnavailable {
		t.Fatalf("error = %v, want reason %q", err, ReasonStorageUnavailable)
	}
	if !r.storageDown {
		t.Fatal("a storage failure did not latch storageDown")
	}
	if banner := r.joinedBanners(); !strings.Contains(banner, "unavailable") {
		t.Fatalf("banner = %q, want it to describe the storage failure", banner)
	}

	// Latched: the next send must not touch the store at all.
	before := fake.countCall("InsertMessage:")
	if _, _, err := r.send("a", "beta", "again", ""); !errors.As(err, &sendErr) || sendErr.Reason != ReasonStorageUnavailable {
		t.Fatalf("second send error = %v, want storage_unavailable", err)
	}
	if after := fake.countCall("InsertMessage:"); after != before {
		t.Fatal("a send reached the store after storage was marked down")
	}
}

func TestSendEmitsQueuedMessageAndBudget(t *testing.T) {
	r, _ := newSendRouter(t, 20, 64<<10)

	id, _, err := r.send("a", "beta", "hello", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case event := <-r.events:
		if event.Message == nil {
			t.Fatalf("first event = %+v, want a message event", event)
		}
		if event.Message.ID != id {
			t.Fatalf("event message id = %q, want %q", event.Message.ID, id)
		}
		if event.Message.State != protocol.MessageQueued {
			t.Fatalf("event state = %q, want %q", event.Message.State, protocol.MessageQueued)
		}
		if event.Message.From != "alpha" || event.Message.To != "beta" {
			t.Fatalf("event names = %q -> %q, want alpha -> beta", event.Message.From, event.Message.To)
		}
		if event.Budget == nil || event.Budget.Used != 1 {
			t.Fatalf("event budget = %+v, want used 1", event.Budget)
		}
	default:
		t.Fatal("send emitted no event")
	}
}

func TestSendTracksPendingUntilFinal(t *testing.T) {
	r, _ := newSendRouter(t, 20, 64<<10)

	if got := r.pendingCount(); got != 0 {
		t.Fatalf("pending before send = %d, want 0", got)
	}
	if _, _, err := r.send("a", "beta", "hello", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := r.pendingCount(); got != 1 {
		t.Fatalf("pending after send = %d, want 1", got)
	}
}
