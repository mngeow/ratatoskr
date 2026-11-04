package router

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// seedReadPath fills a fake store with one run, one message and its events, and
// returns the fake. The router is never started: the read path must not touch
// actor state.
func seedReadPath(t *testing.T) (*Router, *fakeStore) {
	t.Helper()

	red := "\x1b[31mred"
	body := "line one\r\nline two\ttabbed " + red + "\x07bell"
	ended := time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC)

	fake := newFakeStore()
	fake.runs = []store.RunRow{
		{
			ID:          "run-2",
			StartedAt:   time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
			EndedAt:     &ended,
			EndReason:   "stopped\x1b[0m",
			Interrupted: 2,
		},
		{ID: "run-1", StartedAt: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)},
	}
	fake.rows["m1"] = store.MessageRow{
		Seq:       7,
		ID:        "m1",
		RunID:     "run-2",
		FromID:    "alpha",
		ToID:      "beta",
		From:      "alpha\x1b[0m",
		To:        "beta",
		ReplyTo:   "m0\x1b[2J",
		Body:      body,
		State:     protocol.MessageUnconfirmed,
		Reason:    "no_confirmation\x1b[0m",
		CreatedAt: time.Date(2026, 11, 4, 10, 5, 0, 0, time.UTC),
	}
	fake.events = map[string][]store.EventRow{
		"m1": {
			{At: time.Date(2026, 11, 4, 10, 5, 0, 0, time.UTC), From: "", To: protocol.MessageQueued},
			{At: time.Date(2026, 11, 4, 10, 6, 0, 0, time.UTC), From: protocol.MessageQueued, To: protocol.MessageDelivering},
			{At: time.Date(2026, 11, 4, 10, 7, 0, 0, time.UTC), From: protocol.MessageDelivering, To: protocol.MessageUnconfirmed, Reason: "no_confirmation\x1b[0m"},
		},
	}

	r, _ := newTestRouter(fake)
	return r, fake
}

func TestRunsMapsAndSanitises(t *testing.T) {
	r, fake := seedReadPath(t)

	views, err := r.Runs(0)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}

	if fake.runsLimit != runsLimit {
		t.Fatalf("store was asked for %d runs, want the default %d", fake.runsLimit, runsLimit)
	}
	if len(views) != 2 {
		t.Fatalf("got %d runs, want 2", len(views))
	}

	// Newest first, as the store returned them.
	if views[0].ID != "run-2" || views[1].ID != "run-1" {
		t.Fatalf("order = %q, %q; want run-2 then run-1", views[0].ID, views[1].ID)
	}
	if views[0].EndReason != "stopped[0m" {
		t.Fatalf("end reason = %q, want the escape stripped", views[0].EndReason)
	}
	if views[0].Interrupted != 2 {
		t.Fatalf("interrupted = %d, want 2", views[0].Interrupted)
	}
	if views[0].EndedAt == nil {
		t.Fatal("a run with an end time has no EndedAt")
	}
	if !views[0].EndedAt.Equal(*fake.runs[0].EndedAt) {
		t.Fatalf("EndedAt = %v, want %v", views[0].EndedAt, *fake.runs[0].EndedAt)
	}
	if views[1].EndedAt != nil {
		t.Fatal("a run that never ended has an EndedAt")
	}

	// A copy, never the store's pointer.
	if views[0].EndedAt == fake.runs[0].EndedAt {
		t.Fatal("EndedAt shares the store's pointer")
	}
	*views[0].EndedAt = views[0].EndedAt.Add(time.Hour)
	if fake.runs[0].EndedAt.Equal(*views[0].EndedAt) {
		t.Fatal("mutating the view's EndedAt changed the stored row")
	}
}

func TestRunsPassesAnExplicitLimit(t *testing.T) {
	r, fake := seedReadPath(t)

	if _, err := r.Runs(3); err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if fake.runsLimit != 3 {
		t.Fatalf("store was asked for %d runs, want 3", fake.runsLimit)
	}

	// The call log records the read, which is how the tests know it went to the
	// store rather than to the actor.
	if len(fake.callLog()) == 0 || fake.callLog()[len(fake.callLog())-1] != "Runs" {
		t.Fatalf("call log = %v, want a Runs entry", fake.callLog())
	}
}

// TestMessageDetailKeepsBodyShapeAndSanitises is the sanitisation contract: the
// body keeps its newlines and tabs, loses ESC and BEL, and the metadata is
// cleaned before it can reach a screen.
func TestMessageDetailKeepsBodyShapeAndSanitises(t *testing.T) {
	r, fake := seedReadPath(t)

	detail, err := r.MessageDetail("m1")
	if err != nil {
		t.Fatalf("MessageDetail: %v", err)
	}

	if detail.ID != "m1" || detail.RunID != "run-2" {
		t.Fatalf("identity = %q / %q", detail.ID, detail.RunID)
	}
	if detail.From != "alpha" || detail.To != "beta" {
		t.Fatalf("names = %q -> %q", detail.From, detail.To)
	}
	if detail.ReplyTo != "m0[2J" {
		t.Fatalf("reply-to = %q, want the escape stripped", detail.ReplyTo)
	}
	if detail.Reason != "no_confirmation[0m" {
		t.Fatalf("reason = %q, want the escape stripped", detail.Reason)
	}

	// Newlines and tabs survive; CRLF became one newline; ESC and BEL are gone.
	want := "line one\nline two\ttabbed [31mredbell"
	if detail.Body != want {
		t.Fatalf("body =\n%q\nwant\n%q", detail.Body, want)
	}
	if strings.ContainsAny(detail.Body, "\x1b\x07") {
		t.Fatalf("body still carries a control character: %q", detail.Body)
	}

	// The short form stays short.
	if !strings.Contains(detail.Preview, "line one line two") {
		t.Fatalf("preview = %q, want the collapsed single-line form", detail.Preview)
	}

	calls := fake.callLog()
	if !containsCall(calls, "Message:m1") || !containsCall(calls, "MessageEvents:m1") {
		t.Fatalf("call log = %v, want both reads", calls)
	}
}

func TestMessageDetailSanitisesEveryEvent(t *testing.T) {
	r, _ := seedReadPath(t)

	detail, err := r.MessageDetail("m1")
	if err != nil {
		t.Fatalf("MessageDetail: %v", err)
	}

	if len(detail.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(detail.Events))
	}
	if detail.Events[0].From != "" || detail.Events[0].To != protocol.MessageQueued {
		t.Fatalf("first event = %q -> %q", detail.Events[0].From, detail.Events[0].To)
	}
	if last := detail.Events[2]; last.To != protocol.MessageUnconfirmed || last.Reason != "no_confirmation[0m" {
		t.Fatalf("last event = %+v, want a sanitised reason", last)
	}
	for _, event := range detail.Events {
		if strings.ContainsRune(event.Reason, '\x1b') {
			t.Fatalf("event reason still carries ESC: %q", event.Reason)
		}
	}
}

func TestMessageDetailUnknownIDIsSentinel(t *testing.T) {
	r, _ := seedReadPath(t)

	_, err := r.MessageDetail("nope")
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("error = %v, want ErrMessageNotFound", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %q, want it to name the id", err)
	}
}

// TestReadsWorkWhileWritesAreLatched: the read path must not consult the
// unavailable latch, because recovering a message body is what a user needs when
// writing has failed.
func TestReadsWorkWhileWritesAreLatched(t *testing.T) {
	r, fake := seedReadPath(t)
	fake.setUnavailable(true)

	if _, err := r.Runs(0); err != nil {
		t.Fatalf("Runs with writes latched: %v", err)
	}
	detail, err := r.MessageDetail("m1")
	if err != nil {
		t.Fatalf("MessageDetail with writes latched: %v", err)
	}
	if detail.Body == "" {
		t.Fatal("the message body came back empty")
	}
}

// TestReadsTouchNoActorState runs the read path on a router that was never
// Started, which is only possible if nothing here reaches the actor.
func TestReadsTouchNoActorState(t *testing.T) {
	r, _ := seedReadPath(t)

	if r.started.Load() {
		t.Fatal("the test router was started")
	}
	if _, err := r.Runs(0); err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if _, err := r.MessageDetail("m1"); err != nil {
		t.Fatalf("MessageDetail: %v", err)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
