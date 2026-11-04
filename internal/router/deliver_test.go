package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them. They drive pump,
// the write result and the timer pass directly on a router whose actor was never
// started, so each step is deterministic and single-goroutine.

// TestPumpHoldsWithTheGatesReason walks every way the gate can refuse, and checks
// both the message state and the reason the user will see.
func TestPumpHoldsWithTheGatesReason(t *testing.T) {
	tests := []struct {
		name     string
		state    protocol.AgentState
		drafting bool
		noPane   bool
		wantHold string
	}{
		{name: "no pane", state: protocol.AgentIdle, noPane: true, wantHold: holdNoPane},
		{name: "busy", state: protocol.AgentBusy, wantHold: holdBusy},
		{name: "needs you", state: protocol.AgentNeedsYou, wantHold: holdNeedsYou},
		{name: "starting", state: protocol.AgentStarting, wantHold: holdStarting},
		{name: "unknown", state: protocol.AgentUnknown, wantHold: holdUnknown},
		{name: "exited", state: protocol.AgentExited, wantHold: holdExited},
		{name: "drafting", state: protocol.AgentIdle, drafting: true, wantHold: holdDrafting},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pane := &fakePane{}
			r, fake, _, beta := newDeliverRouter(t, pane)
			beta.state = tc.state
			beta.drafting = tc.drafting
			if tc.noPane {
				beta.w = nil
			}

			m := queueMessage(t, r, beta, protocol.MessageQueued)
			r.pump(beta, false)

			if beta.inflight != nil {
				t.Fatal("a held message was put in flight")
			}
			if m.state != protocol.MessageHeld {
				t.Fatalf("message state = %q, want %q", m.state, protocol.MessageHeld)
			}
			if m.reason != tc.wantHold {
				t.Fatalf("hold reason = %q, want %q", m.reason, tc.wantHold)
			}
			if len(pane.submitted()) != 0 {
				t.Fatal("a held message was written to the pane")
			}
			if n := fake.countCall("Transition:" + m.id + ":queued>delivering"); n != 0 {
				t.Fatal("a held message was committed as delivering")
			}
		})
	}
}

// TestPumpHeldReasonChangesInMemoryOnly pins the design's rule that only the
// first queued-to-held edge is stored.
func TestPumpHeldReasonChangesInMemoryOnly(t *testing.T) {
	pane := &fakePane{}
	r, fake, _, beta := newDeliverRouter(t, pane)

	beta.state = protocol.AgentBusy
	m := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	if m.state != protocol.MessageHeld || m.reason != holdBusy {
		t.Fatalf("first pump: state %q reason %q, want held/busy", m.state, m.reason)
	}

	transitions := fake.countCall("Transition:" + m.id + ":queued>held")
	if transitions != 1 {
		t.Fatalf("queued->held transitions = %d, want 1", transitions)
	}

	// The agent becomes idle but the user is typing: the reason changes, and no
	// second transition is committed.
	beta.state = protocol.AgentIdle
	beta.drafting = true
	r.pump(beta, false)

	if m.state != protocol.MessageHeld {
		t.Fatalf("message state = %q, want it to stay held", m.state)
	}
	if m.reason != holdDrafting {
		t.Fatalf("hold reason = %q, want %q", m.reason, holdDrafting)
	}
	if again := fake.countCall("Transition:" + m.id + ":queued>held"); again != 1 {
		t.Fatalf("queued->held transitions = %d after the reason changed, want 1", again)
	}
}

// TestPumpWritesOnlyAfterTheDeliveringCommit is the commit-order rule: the write
// may not start until delivering is recorded, and a recorded delivery is what
// starts it.
func TestPumpWritesOnlyAfterTheDeliveringCommit(t *testing.T) {
	pane := &fakePane{}
	r, fake, _, beta := newDeliverRouter(t, pane)

	m := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)

	if beta.inflight != m {
		t.Fatal("the message was not put in flight")
	}
	if m.state != protocol.MessageDelivering {
		t.Fatalf("message state = %q, want %q", m.state, protocol.MessageDelivering)
	}
	if n := fake.countCall("Transition:" + m.id + ":queued>delivering"); n != 1 {
		t.Fatalf("queued->delivering transitions = %d, want 1", n)
	}
	if len(beta.queue) != 0 {
		t.Fatal("the delivered message is still queued")
	}

	// The write goroutine reports back by enqueueing its result; running it here
	// is what the actor would do next.
	fn := <-r.in
	fn()

	frames := pane.submitted()
	if len(frames) != 1 {
		t.Fatalf("pane received %d frames, want 1", len(frames))
	}
	if !strings.Contains(frames[0], m.id) {
		t.Fatalf("frame does not carry the message id: %q", frames[0])
	}
	if !strings.Contains(frames[0], "alpha") {
		t.Fatalf("frame does not carry the sender name: %q", frames[0])
	}
	if m.state != protocol.MessagePasted {
		t.Fatalf("message state = %q, want %q", m.state, protocol.MessagePasted)
	}
	if beta.confirmDeadline.IsZero() {
		t.Fatal("a pasted message has no confirmation deadline")
	}
}

// TestPumpDeliveringCommitFailureMeansNoWrite is the other half of the rule: if
// the commit fails, no goroutine is started at all.
func TestPumpDeliveringCommitFailureMeansNoWrite(t *testing.T) {
	pane := &fakePane{}
	r, fake, _, beta := newDeliverRouter(t, pane)

	m := queueMessage(t, r, beta, protocol.MessageQueued)
	fake.setUnavailable(true)

	r.pump(beta, false)

	if beta.inflight != nil {
		t.Fatal("a message went in flight although its commit failed")
	}
	if m.state != protocol.MessageQueued {
		t.Fatalf("message state = %q, want it left as %q", m.state, protocol.MessageQueued)
	}
	if len(pane.submitted()) != 0 {
		t.Fatal("the pane was written although the delivering commit failed")
	}
	select {
	case <-r.in:
		t.Fatal("a write result was queued despite no write")
	default:
	}
}

// TestPumpF4OnlyOverridesUnknown checks the override boundary, including the
// invariant-2 refusal for a permission prompt.
func TestPumpF4OnlyOverridesUnknown(t *testing.T) {
	tests := []struct {
		name   string
		state  protocol.AgentState
		noPane bool
		inFlt  bool
		wantF4 bool
	}{
		{name: "unknown with a pane", state: protocol.AgentUnknown, wantF4: true},
		{name: "needs you is refused", state: protocol.AgentNeedsYou},
		{name: "busy is refused", state: protocol.AgentBusy},
		{name: "no pane is refused", state: protocol.AgentUnknown, noPane: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pane := &fakePane{}
			r, _, _, beta := newDeliverRouter(t, pane)
			beta.state = tc.state
			if tc.noPane {
				beta.w = nil
			}

			m := queueMessage(t, r, beta, protocol.MessageQueued)
			r.pump(beta, true)

			if tc.wantF4 {
				if beta.inflight != m {
					t.Fatal("F4 did not deliver to an unknown agent")
				}
				return
			}
			if beta.inflight != nil {
				t.Fatal("F4 delivered where the gate must refuse")
			}
			if m.state != protocol.MessageHeld {
				t.Fatalf("message state = %q, want %q", m.state, protocol.MessageHeld)
			}
		})
	}
}

// TestPumpStopsOnStorageDown is the first step of the pump: no store, no work.
func TestPumpF4OverridesIdleOverADraftOnlyAndDeliverHeldClearsTheDraft(t *testing.T) {
	pane := &fakePane{}
	r, _, _, beta := newDeliverRouter(t, pane)
	beta.state = protocol.AgentIdle
	beta.drafting = true

	m := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	if beta.inflight != nil || m.state != protocol.MessageHeld || m.reason != holdDrafting {
		t.Fatalf("a draft did not hold: state %q reason %q", m.state, m.reason)
	}

	r.DeliverHeld(beta.id)
	fn := <-r.in
	fn()
	if beta.inflight != m {
		t.Fatal("F4 did not deliver over a draft on an idle agent")
	}
	if beta.drafting {
		t.Fatal("DeliverHeld left the draft flag set after overriding it")
	}
}

func TestDeliverHeldLeavesTheDraftWhenTheOverrideDoesNotApply(t *testing.T) {
	pane := &fakePane{}
	r, _, _, beta := newDeliverRouter(t, pane)
	beta.state = protocol.AgentBusy
	beta.drafting = true

	queueMessage(t, r, beta, protocol.MessageQueued)
	r.DeliverHeld(beta.id)
	fn := <-r.in
	fn()
	if beta.inflight != nil || !beta.drafting {
		t.Fatal("F4 touched a busy agent or its draft flag")
	}
}

// TestPastedMessageKeepsTheSlotUntilConfirmed pins one message per recipient in
// delivering or pasted: the next message waits, even with an idle agent.
func TestPastedMessageKeepsTheSlotUntilConfirmed(t *testing.T) {
	pane := &fakePane{}
	r, _, _, beta := newDeliverRouter(t, pane)
	beta.boundSession = "s1"

	m1 := queueMessage(t, r, beta, protocol.MessageQueued)
	m2 := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	<-r.in
	r.onWriteResult(beta, m1, nil)

	beta.state = protocol.AgentIdle // even an idle agent must not take a second message
	r.pump(beta, false)
	if beta.inflight != m1 || m2.state == protocol.MessageDelivering {
		t.Fatalf("a second message was delivered while the first was pasted (m2 %q)", m2.state)
	}

	// A confirmation frees the slot; the agent is busy then, so m2 is held.
	r.hook(beta, protocol.HookEvent{Kind: protocol.HookUserPromptSubmit, SessionID: "s1"})
	if m1.state != protocol.MessageAccepted || beta.inflight != nil {
		t.Fatalf("m1 %q inflight %v, want accepted and freed", m1.state, beta.inflight)
	}
	if m2.state != protocol.MessageHeld {
		t.Fatalf("m2 = %q, want held while the agent is busy", m2.state)
	}
}

func TestPumpStopsOnStorageDown(t *testing.T) {
	pane := &fakePane{}
	r, _, _, beta := newDeliverRouter(t, pane)

	m := queueMessage(t, r, beta, protocol.MessageQueued)
	r.storageDown = true
	r.pump(beta, false)

	if m.state != protocol.MessageQueued || beta.inflight != nil {
		t.Fatalf("pump acted while storage was down: state %q", m.state)
	}
}

func TestWriteResultFailureDetails(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nothing written", err: &fakeWriteError{written: false, text: "boom"}, want: "write_failed:no_bytes_written"},
		{name: "partially written", err: &fakeWriteError{written: true, text: "boom"}, want: "write_failed:partial_write"},
		{name: "unknown", err: &plainError{text: "boom"}, want: "write_failed:unknown_if_written"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pane := &fakePane{}
			r, _, _, beta := newDeliverRouter(t, pane)

			m := queueMessage(t, r, beta, protocol.MessageQueued)
			r.pump(beta, false)
			// Drop the queued success result; this test drives the failure itself.
			<-r.in

			r.onWriteResult(beta, m, tc.err)

			if m.state != protocol.MessageFailed {
				t.Fatalf("message state = %q, want %q", m.state, protocol.MessageFailed)
			}
			if m.reason != tc.want {
				t.Fatalf("reason = %q, want %q", m.reason, tc.want)
			}
			if beta.inflight != nil {
				t.Fatal("a failed message stayed in flight")
			}
		})
	}
}

// TestWriteResultSuccessMarksPastedAndArmsTheDeadline covers the success path.
func TestWriteResultSuccessMarksPastedAndArmsTheDeadline(t *testing.T) {
	pane := &fakePane{}
	r, _, clock, beta := newDeliverRouter(t, pane)

	m := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	<-r.in

	before := clock.now()
	r.onWriteResult(beta, m, nil)

	if m.state != protocol.MessagePasted {
		t.Fatalf("message state = %q, want %q", m.state, protocol.MessagePasted)
	}
	if beta.inflight != m {
		t.Fatal("a pasted message left flight before it was confirmed; the slot must stay taken")
	}
	want := before.Add(r.cfg.ConfirmTimeout)
	if !beta.confirmDeadline.Equal(want) {
		t.Fatalf("confirm deadline = %v, want %v", beta.confirmDeadline, want)
	}
}

// TestEarlyConfirmIsAppliedOnlyAfterPasted pins that there is no
// delivering-to-accepted edge: the armed confirm waits for pasted to commit.
func TestEarlyConfirmIsAppliedOnlyAfterPasted(t *testing.T) {
	pane := &fakePane{}
	r, _, _, beta := newDeliverRouter(t, pane)

	beta.boundSession = "s1"

	m := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	<-r.in

	if m.state != protocol.MessageDelivering {
		t.Fatalf("message state = %q, want %q", m.state, protocol.MessageDelivering)
	}

	// A submit arrives before the write result.
	r.hook(beta, protocol.HookEvent{Kind: protocol.HookUserPromptSubmit, SessionID: "s1"})
	if !beta.earlyConfirm {
		t.Fatal("an early submit did not arm the confirm")
	}
	if m.state == protocol.MessageAccepted {
		t.Fatal("the message was accepted before its paste was recorded")
	}

	r.onWriteResult(beta, m, nil)

	if m.state != protocol.MessageAccepted {
		t.Fatalf("message state = %q, want %q", m.state, protocol.MessageAccepted)
	}
	if m.reason != "prompt_submitted" {
		t.Fatalf("reason = %q, want %q", m.reason, "prompt_submitted")
	}
	if beta.earlyConfirm {
		t.Fatal("the early confirm was not consumed")
	}
}

// TestStaleWriteResultIsIgnored: a result for something that is not in flight may
// not change anything.
func TestStaleWriteResultIsIgnored(t *testing.T) {
	pane := &fakePane{}
	r, _, _, beta := newDeliverRouter(t, pane)

	m1 := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	<-r.in

	m2 := &msg{id: "m-stale", from: r.byName["alpha"], to: beta, state: protocol.MessageQueued}
	before := len(r.events)
	r.onWriteResult(beta, m2, nil)

	if m2.state != protocol.MessageQueued {
		t.Fatalf("a stale result changed the message to %q", m2.state)
	}
	if beta.inflight != m1 {
		t.Fatal("a stale result cleared the live in-flight message")
	}
	if after := len(r.events); after != before {
		t.Fatalf("a stale result emitted %d events", after-before)
	}
}

// TestTickStartupTimeout shows the no-hooks banner and the unknown state.
func TestTickStartupTimeout(t *testing.T) {
	pane := &fakePane{}
	r, _, clock, beta := newDeliverRouter(t, pane)

	beta.state = protocol.AgentStarting
	beta.startedAt = clock.now()

	clock.advance(r.cfg.StartupTimeout - 1)
	r.tick(clock.now())
	if beta.state != protocol.AgentStarting {
		t.Fatalf("agent moved to %q before the startup timeout", beta.state)
	}

	clock.advance(2)
	r.tick(clock.now())
	if beta.state != protocol.AgentUnknown {
		t.Fatalf("agent state = %q after the startup timeout, want %q", beta.state, protocol.AgentUnknown)
	}
	banner, ok := r.banners[r.hooksBannerKey(beta)]
	if !ok {
		t.Fatal("no hooks banner was raised")
	}
	if !strings.Contains(banner, "no hook events from beta") {
		t.Fatalf("banner = %q", banner)
	}
}

// TestTickStartupTimeoutNeedsAStartedPane: with no launch time there is nothing
// to be late for.
func TestTickStartupTimeoutNeedsAStartedPane(t *testing.T) {
	pane := &fakePane{}
	r, _, clock, beta := newDeliverRouter(t, pane)

	beta.state = protocol.AgentStarting
	beta.startedAt = time.Time{} // never launched

	clock.advance(10 * r.cfg.StartupTimeout)
	r.tick(clock.now())

	if beta.state != protocol.AgentStarting {
		t.Fatalf("an agent with no start time timed out to %q", beta.state)
	}
}

// TestTickBusyTimeoutAndNeedsYou covers the other two timer states.
func TestTickBusyTimeoutAndNeedsYou(t *testing.T) {
	pane := &fakePane{}
	r, _, clock, beta := newDeliverRouter(t, pane)

	beta.state = protocol.AgentBusy
	beta.lastEvent = clock.now()
	clock.advance(r.cfg.BusyTimeout - 1)
	r.tick(clock.now())
	if beta.state != protocol.AgentBusy {
		t.Fatalf("busy agent moved to %q before its timeout", beta.state)
	}

	clock.advance(2)
	r.tick(clock.now())
	if beta.state != protocol.AgentUnknown {
		t.Fatalf("agent state = %q after the busy timeout, want %q", beta.state, protocol.AgentUnknown)
	}

	// needs_you never times out.
	beta.state = protocol.AgentNeedsYou
	beta.lastEvent = clock.now()
	clock.advance(10 * r.cfg.BusyTimeout)
	r.tick(clock.now())
	if beta.state != protocol.AgentNeedsYou {
		t.Fatalf("needs_you agent moved to %q", beta.state)
	}
}

// TestTickConfirmationTimeoutUnconfirms covers the pasted deadline and the late
// rule it enables.
func TestTickConfirmationTimeoutUnconfirms(t *testing.T) {
	pane := &fakePane{}
	r, _, clock, beta := newDeliverRouter(t, pane)

	m := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	<-r.in
	r.onWriteResult(beta, m, nil)
	if m.state != protocol.MessagePasted {
		t.Fatalf("message state = %q, want pasted", m.state)
	}

	clock.advance(r.cfg.ConfirmTimeout + 1)
	r.tick(clock.now())

	if m.state != protocol.MessageUnconfirmed {
		t.Fatalf("message state = %q, want %q", m.state, protocol.MessageUnconfirmed)
	}
	if m.reason != "no_confirmation" {
		t.Fatalf("reason = %q, want %q", m.reason, "no_confirmation")
	}
	if beta.inflight != nil {
		t.Fatal("an unconfirmed message stayed in flight")
	}
	if len(beta.unconfirmed) != 1 || beta.unconfirmed[0] != m {
		t.Fatal("the unconfirmed message was not remembered for the late rule")
	}
}

// plainError is an error with no BytesWritten method.
type plainError struct{ text string }

func (e *plainError) Error() string { return e.text }

// TestMarkExitedFailsQueuedAndUnconfirmsInFlight: nothing more is delivered to an
// agent that is gone; waiting messages fail, and the one handed to the pane is
// unconfirmed (whether the harness received it is unknown) and stays creditable.
func TestMarkExitedFailsQueuedAndUnconfirmsInFlight(t *testing.T) {
	pane := &fakePane{}
	r, _, _, beta := newDeliverRouter(t, pane)

	m1 := queueMessage(t, r, beta, protocol.MessageQueued)
	m2 := queueMessage(t, r, beta, protocol.MessageQueued)
	r.pump(beta, false)
	<-r.in
	r.onWriteResult(beta, m1, nil)

	code := 7
	r.markExited(beta, &code)

	if beta.state != protocol.AgentExited || beta.inflight != nil || len(beta.queue) != 0 {
		t.Fatalf("exited agent keeps work: state %q inflight %v queue %d", beta.state, beta.inflight, len(beta.queue))
	}
	if m1.state != protocol.MessageUnconfirmed || m1.reason != "recipient_exited" {
		t.Fatalf("in-flight message = %q/%q, want unconfirmed/recipient_exited", m1.state, m1.reason)
	}
	if len(beta.unconfirmed) != 1 || beta.unconfirmed[0] != m1 {
		t.Fatal("the unconfirmed message was not remembered for the late rule")
	}
	if m2.state != protocol.MessageFailed || m2.reason != "recipient_exited" {
		t.Fatalf("queued message = %q/%q, want failed/recipient_exited", m2.state, m2.reason)
	}
}

// TestNotificationsAfterCloseAreNoOpsAndDoNotBlock covers the quit order: app
// closes the router, runs FinishRun, then closes the panes, whose exits arrive
// as PaneExited after the router stopped. They must neither block nor change
// anything, so the unfinished messages stay interrupted, not unconfirmed.
func TestNotificationsAfterCloseAreNoOpsAndDoNotBlock(t *testing.T) {
	fake := newFakeStore()
	r, _ := newTestRouter(fake)
	r.Start()
	if err := r.RegisterAgent(context.Background(), spec("a", "alpha")); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	r.Close()
	calls := len(fake.callLog())

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.PaneExited("a", 1)
		r.PaneFailed("a", nil)
		r.PaneStarted("a", &fakePane{})
		r.HookEvent(context.Background(), "a", protocol.HookEvent{Kind: protocol.HookStop})
		r.UserDraft("a", true)
		r.UserSubmitted("a")
		r.DeliverHeld("a")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a notification after Close blocked")
	}
	if got := len(fake.callLog()); got != calls {
		t.Fatalf("store was called %d times after Close", got-calls)
	}
}
