package router

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// These tests are written but UNRUN: GR-3 forbids running them.

func TestPendingCountIsQueuesPlusInFlight(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	a := &agent{id: "a", name: "alpha"}
	b := &agent{id: "b", name: "beta"}
	a.queue = []*msg{{id: "m1", state: protocol.MessageQueued}, {id: "m2", state: protocol.MessageHeld}}
	b.queue = []*msg{{id: "m3", state: protocol.MessageHeld}}
	b.inflight = &msg{id: "m4", state: protocol.MessagePasted}
	r.order = []*agent{a, b}

	// Final messages (accepted, failed, interrupted, unconfirmed) sit in the log
	// only and are not counted; the log being capped must not change the count.
	for i, state := range []protocol.MessageState{
		protocol.MessageUnconfirmed, protocol.MessageAccepted, protocol.MessageFailed, protocol.MessageInterrupted,
	} {
		r.appendLog(&msg{id: "f" + strconv.Itoa(i), from: a, to: b, state: state})
	}

	if got := r.pendingCount(); got != 4 {
		t.Fatalf("pendingCount = %d, want 4 (three queued/held + one in flight)", got)
	}
}

func TestRecentMessagesArePreviewedAndCapped(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	from := &agent{id: "a", name: "alpha"}
	to := &agent{id: "b", name: "beta"}
	// A body carrying a control sequence and a long tail.
	body := "x\x1b[201~" + strings.Repeat("y", 200)
	r.appendLog(&msg{id: "m1", from: from, to: to, body: body, state: protocol.MessageQueued})

	views := r.recentMessages()
	if len(views) != 1 {
		t.Fatalf("recentMessages returned %d views, want 1", len(views))
	}
	if views[0].Preview == "" {
		t.Fatal("preview of a long body is empty")
	}
	if !strings.HasSuffix(views[0].Preview, "…") {
		t.Fatalf("preview = %q, want a truncated body with an ellipsis", views[0].Preview)
	}
	for _, r := range views[0].Preview {
		if r < 0x20 && r != '\n' && r != '\t' {
			t.Fatalf("preview %q contains a control rune", views[0].Preview)
		}
	}

	// The cap is historyLimit, and the newest survive.
	r.log = nil
	for i := 0; i < historyLimit+10; i++ {
		r.appendLog(&msg{id: "m" + strconv.Itoa(i), from: from, to: to, body: "body", state: protocol.MessageQueued})
	}
	if got := len(r.recentMessages()); got != historyLimit {
		t.Fatalf("recentMessages returned %d views, want %d", got, historyLimit)
	}
}

func TestLogIsCappedAtLogLimit(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	from := &agent{id: "a", name: "alpha"}
	to := &agent{id: "b", name: "beta"}
	for i := 0; i < logLimit+5; i++ {
		r.appendLog(&msg{id: "m" + strconv.Itoa(i), from: from, to: to, state: protocol.MessageQueued})
	}
	if got := len(r.log); got != logLimit {
		t.Fatalf("log holds %d messages, want %d", got, logLimit)
	}
}

func TestJoinedBannersSortedAndTextOnly(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	r.banners["hooks:beta"] = "beta banner"
	r.banners["storage"] = "storage banner"

	// Sorted by key (hooks:beta before storage) and the key is not shown.
	got := r.joinedBanners()
	want := "beta banner | storage banner"
	if got != want {
		t.Fatalf("joinedBanners = %q, want %q", got, want)
	}
}

func TestSetBannerEmitsOnlyOnChange(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	r.setBanner("storage", "down")
	first := <-r.events
	if !first.Resync {
		t.Fatal("a banner change did not force a resync")
	}
	if first.Banner != "down" {
		t.Fatalf("banner event = %q, want %q", first.Banner, "down")
	}

	// Same text again: no event.
	r.setBanner("storage", "down")
	select {
	case unexpected := <-r.events:
		t.Fatalf("an unchanged banner emitted %+v", unexpected)
	default:
	}

	// Clearing emits once, with an empty banner.
	r.setBanner("storage", "")
	cleared := <-r.events
	if cleared.Banner != "" {
		t.Fatalf("cleared banner event = %q, want empty", cleared.Banner)
	}
}

func TestHistoryDefaultsAndPreview(t *testing.T) {
	fake := newFakeStore()
	fake.history = []store.MessageRow{
		{
			ID:        "m1",
			RunID:     "run-1",
			From:      "alpha\x1b[201~",
			To:        "beta",
			Body:      "hello\x1b[31m world",
			State:     protocol.MessageAccepted,
			Reason:    "accepted",
			CreatedAt: newFakeClock().now(),
		},
	}
	r, _ := newTestRouter(fake)

	views, err := r.History(protocol.HistoryQuery{})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if fake.historyRunID != "run-1" {
		t.Fatalf("History queried run %q, want %q", fake.historyRunID, "run-1")
	}
	if fake.historyLimit != historyLimit {
		t.Fatalf("History limit = %d, want %d", fake.historyLimit, historyLimit)
	}
	if len(views) != 1 {
		t.Fatalf("History returned %d views, want 1", len(views))
	}

	view := views[0]
	if view.From == "alpha\x1b[201~" {
		t.Fatal("History passed an unsanitised name through")
	}
	if view.Preview == "" || view.Preview != "hello[31m world" {
		t.Fatalf("preview = %q, want the control characters gone", view.Preview)
	}

	// An explicit query is passed through unchanged.
	if _, err := r.History(protocol.HistoryQuery{RunID: "run-9", Limit: 5}); err != nil {
		t.Fatalf("History with explicit query: %v", err)
	}
	if fake.historyRunID != "run-9" || fake.historyLimit != 5 {
		t.Fatalf("History queried %q/%d, want run-9/5", fake.historyRunID, fake.historyLimit)
	}
}

func TestHistoryErrorIsReturned(t *testing.T) {
	fake := newFakeStore()
	fake.historyErr = errors.New("boom")
	r, _ := newTestRouter(fake)

	if _, err := r.History(protocol.HistoryQuery{}); err == nil {
		t.Fatal("History hid a store error")
	}
}

func TestAgentViewHeldCount(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	a := &agent{id: "a", name: "alpha", state: protocol.AgentIdle}
	a.queue = []*msg{
		{id: "m1", to: a, state: protocol.MessageHeld},
		{id: "m2", to: a, state: protocol.MessageHeld},
		{id: "m3", to: a, state: protocol.MessageQueued},
	}

	view := r.agentView(a)
	if view.Held != 2 {
		t.Fatalf("Held = %d, want 2", view.Held)
	}
	if view.DisplayName != "alpha" || view.State != protocol.AgentIdle {
		t.Fatalf("agent view = %+v", view)
	}
}

func TestMessageViewSanitisesNamesAndPreviewsBody(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	from := &agent{id: "a", name: "alp\x1bha"}
	to := &agent{id: "b", name: "beta\r\n"}
	m := &msg{id: "m1", from: from, to: to, body: "hi\x07 there", state: protocol.MessageQueued, reason: "held"}

	view := r.messageView(m)
	if view.From != "alpha" {
		t.Fatalf("From = %q, want %q", view.From, "alpha")
	}
	if view.To != "beta\n" {
		// stripControl keeps newlines; the UI strips them again before drawing.
		t.Fatalf("To = %q, want the newline kept by stripControl", view.To)
	}
	if view.Preview != "hi there" {
		t.Fatalf("Preview = %q, want %q", view.Preview, "hi there")
	}
	if view.RunID != "run-1" {
		t.Fatalf("RunID = %q, want run-1", view.RunID)
	}
}
