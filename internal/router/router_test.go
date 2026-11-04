package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them.

// flush waits until everything already enqueued has run. The actor applies work
// in order, so a submitted no-op returning means the backlog ahead of it is done.
func flush(t *testing.T, r *Router) {
	t.Helper()
	if err := r.call(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func spec(id protocol.AgentID, name string) protocol.LaunchSpec {
	return protocol.LaunchSpec{
		ID:          id,
		DisplayName: name,
		Harness:     "claude",
		Dir:         "/tmp",
	}
}

func TestNewRejectsEmptyRunIDAndNilStore(t *testing.T) {
	if _, err := New(Config{Store: newFakeStore()}); err == nil {
		t.Fatal("New accepted an empty RunID")
	}
	if _, err := New(Config{RunID: "run-1"}); err == nil {
		t.Fatal("New accepted a nil Store")
	}
}

func TestNewFillsDefaults(t *testing.T) {
	fake := newFakeStore()
	r, clock := newTestRouter(fake)

	if r.cfg.Now == nil || r.cfg.NewID == nil {
		t.Fatal("New left Now or NewID nil")
	}
	if got := clock.now(); got.IsZero() {
		t.Fatal("fake clock is zero")
	}
	if r.cfg.BudgetLimit != defaultBudgetLimit {
		t.Fatalf("BudgetLimit = %d, want %d", r.cfg.BudgetLimit, defaultBudgetLimit)
	}
	if r.cfg.MaxBody != defaultMaxBody {
		t.Fatalf("MaxBody = %d, want %d", r.cfg.MaxBody, defaultMaxBody)
	}
	if r.cfg.StartupTimeout != defaultStartupTimeout ||
		r.cfg.BusyTimeout != defaultBusyTimeout ||
		r.cfg.ConfirmTimeout != defaultConfirmTimeout ||
		r.cfg.WriteTimeout != defaultWriteTimeout ||
		r.cfg.TickEvery != defaultTickEvery {
		t.Fatalf("timeouts not defaulted: %+v", r.cfg)
	}
	if cap(r.in) != inboxCapacity {
		t.Fatalf("inbox capacity = %d, want %d", cap(r.in), inboxCapacity)
	}
	if cap(r.events) != defaultEventBuffer {
		t.Fatalf("event buffer = %d, want %d", cap(r.events), defaultEventBuffer)
	}
}

// TestSnapshotBeforeStartIsEmpty proves a caller that reads early gets a usable
// value rather than a nil pointer.
func TestSnapshotBeforeStartIsEmpty(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	snapshot := r.Snapshot()
	if snapshot.Agents == nil {
		t.Fatal("Snapshot before Start returned nil Agents")
	}
	if len(snapshot.Agents) != 0 || len(snapshot.Messages) != 0 {
		t.Fatalf("Snapshot before Start = %+v, want empty", snapshot)
	}
	if r.PendingOnQuit() != 0 {
		t.Fatalf("PendingOnQuit = %d, want 0", r.PendingOnQuit())
	}
}

func TestStartAndCloseAreIdempotent(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	r.Start()
	r.Start()
	r.Close()
	r.Close()

	if _, ok := <-r.Events(); ok {
		t.Fatal("Events channel is still open after Close")
	}
}

func TestSubmitAfterCloseReturnsErrClosed(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	r.Start()
	r.Close()

	if _, err := r.Send(context.Background(), "a", "b", "hi", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send after Close = %v, want ErrClosed", err)
	}
	if err := r.RegisterAgent(context.Background(), spec("a", "alpha")); !errors.Is(err, ErrClosed) {
		t.Fatalf("RegisterAgent after Close = %v, want ErrClosed", err)
	}
}

// TestSnapshotAndPendingDoNotBlockOnAStuckStore is the reason snapshots live
// behind atomics: a write that never returns must not freeze the UI.
func TestSnapshotAndPendingDoNotBlockOnAStuckStore(t *testing.T) {
	fake := newFakeStore()
	r, _ := newTestRouter(fake)
	r.Start()
	defer r.Close()

	if err := r.RegisterAgent(context.Background(), spec("a", "alpha")); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if err := r.RegisterAgent(context.Background(), spec("b", "beta")); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	gate := fake.blockInserts()
	sent := make(chan error, 1)
	go func() {
		_, err := r.Send(context.Background(), "a", "b", "hello", "")
		sent <- err
	}()

	// Give the actor a moment to reach the blocked insert.
	time.Sleep(10 * time.Millisecond)

	read := make(chan struct{})
	go func() {
		defer close(read)
		_ = r.Snapshot()
		_ = r.PendingOnQuit()
	}()

	select {
	case <-read:
	case <-time.After(2 * time.Second):
		t.Fatal("Snapshot or PendingOnQuit blocked while the store was stuck")
	}

	close(gate)
	if err := <-sent; err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestCloseUnblocksAWaitingSend covers shutdown: a Send whose work is queued
// behind a blocked actor must not leave its caller parked forever when the
// router closes.
//
// Whether the queued send runs before the actor notices quit is up to select, so
// the test asserts the property that matters - it returns, with either a result
// or ErrClosed - rather than one particular winner.
func TestCloseUnblocksAWaitingSend(t *testing.T) {
	fake := newFakeStore()
	r, _ := newTestRouter(fake)
	r.Start()

	if err := r.RegisterAgent(context.Background(), spec("a", "alpha")); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if err := r.RegisterAgent(context.Background(), spec("b", "beta")); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// Occupy the actor so the send stays queued behind this function.
	occupied := make(chan struct{})
	release := make(chan struct{})
	go func() {
		r.in <- func() {
			close(occupied)
			<-release
		}
	}()
	<-occupied

	sent := make(chan error, 1)
	go func() {
		_, err := r.Send(context.Background(), "a", "b", "hello", "")
		sent <- err
	}()

	closed := make(chan struct{})
	go func() {
		r.Close()
		close(closed)
	}()

	close(release)

	select {
	case err := <-sent:
		if err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Send after Close = %v, want nil or ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after Close")
	}

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
}

func TestRegisterAgentRejectsDuplicateIDAndName(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	r.Start()
	defer r.Close()

	if err := r.RegisterAgent(context.Background(), spec("a", "alpha")); err != nil {
		t.Fatalf("first RegisterAgent: %v", err)
	}
	if err := r.RegisterAgent(context.Background(), spec("a", "other")); err == nil {
		t.Fatal("duplicate agent ID was accepted")
	}
	if err := r.RegisterAgent(context.Background(), spec("b", "alpha")); err == nil {
		t.Fatal("duplicate display name was accepted")
	}
}

func TestRegisterAgentStartsInStartingState(t *testing.T) {
	fake := newFakeStore()
	r, _ := newTestRouter(fake)
	r.Start()
	defer r.Close()

	if err := r.RegisterAgent(context.Background(), spec("a", "alpha")); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	flush(t, r)

	snapshot := r.Snapshot()
	if len(snapshot.Agents) != 1 {
		t.Fatalf("Snapshot has %d agents, want 1", len(snapshot.Agents))
	}
	if got := snapshot.Agents[0].State; got != protocol.AgentStarting {
		t.Fatalf("agent state = %q, want %q", got, protocol.AgentStarting)
	}
	if fake.countCall("InsertAgent:a") != 1 {
		t.Fatalf("InsertAgent calls = %d, want 1", fake.countCall("InsertAgent:a"))
	}
}

func TestPaneExitedMarksAgentExited(t *testing.T) {
	fake := newFakeStore()
	r, _ := newTestRouter(fake)
	r.Start()
	defer r.Close()

	if err := r.RegisterAgent(context.Background(), spec("a", "alpha")); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	r.PaneExited("a", 3)
	flush(t, r)

	snapshot := r.Snapshot()
	if len(snapshot.Agents) != 1 {
		t.Fatalf("Snapshot has %d agents, want 1", len(snapshot.Agents))
	}
	view := snapshot.Agents[0]
	if view.State != protocol.AgentExited {
		t.Fatalf("agent state = %q, want %q", view.State, protocol.AgentExited)
	}
	if view.ExitCode == nil || *view.ExitCode != 3 {
		t.Fatalf("exit code = %v, want 3", view.ExitCode)
	}
	if fake.countCall("EndAgent:a") != 1 {
		t.Fatalf("EndAgent calls = %d, want 1", fake.countCall("EndAgent:a"))
	}
}

// TestEventOverflowDropsOldestAndMarksTheEventAfterTheGap pins the resync
// contract: a full buffer drops the oldest event, and the event enqueued in its
// place carries Resync, so the UI takes a fresh snapshot right after the gap.
func TestEventOverflowDropsOldestAndMarksTheEventAfterTheGap(t *testing.T) {
	fake := newFakeStore()
	r, err := New(Config{
		RunID:       "run-1",
		Store:       fake,
		Now:         newFakeClock().now,
		NewID:       counterIDs("m"),
		EventBuffer: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Not started: emit runs on one goroutine here, exactly as the actor does.

	r.emit(protocol.Event{Banner: "one"})
	r.emit(protocol.Event{Banner: "two"})
	r.emit(protocol.Event{Banner: "three"}) // full: drops "one", "three" carries Resync

	first := <-r.events
	if first.Banner != "two" {
		t.Fatalf("first surviving event = %q, want two; the oldest must be dropped", first.Banner)
	}
	if first.Resync {
		t.Fatal("an event from before the drop claims Resync")
	}

	second := <-r.events
	if second.Banner != "three" {
		t.Fatalf("second surviving event = %q, want three", second.Banner)
	}
	if !second.Resync {
		t.Fatal("the event enqueued after a drop did not carry Resync")
	}

	// Once the gap has been reported, later events are not marked again.
	r.emit(protocol.Event{Banner: "four"})
	third := <-r.events
	if third.Banner != "four" || third.Resync {
		t.Fatalf("third event = %+v, want four without Resync", third)
	}
}

// TestStartupBannerShowsFromTheFirstSnapshotAndJoinsStorage: app passes the crash
// warning in Config; it must be visible before Start and must still join with a
// storage banner raised later.
func TestStartupBannerShowsFromTheFirstSnapshotAndJoinsStorage(t *testing.T) {
	r, err := New(Config{
		RunID:         "run-1",
		Store:         newFakeStore(),
		Now:           newFakeClock().now,
		NewID:         counterIDs("m"),
		StartupBanner: "previous run crashed (2 messages interrupted)\x1b[31m",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := r.Snapshot().Banner; got != "previous run crashed (2 messages interrupted)[31m" {
		t.Fatalf("first snapshot banner = %q, want the sanitised startup text", got)
	}

	r.setBanner(storageBannerKey, "down")
	r.refresh()
	want := "previous run crashed (2 messages interrupted)[31m | down"
	if got := r.Snapshot().Banner; got != want {
		t.Fatalf("joined banner = %q, want %q", got, want)
	}
}

func TestRegisterAgentPassesTheHarnessVersionToTheStore(t *testing.T) {
	fake := newFakeStore()
	r, _ := newTestRouter(fake)

	s := spec("a", "alpha")
	s.HarnessVersion = "2.1.311 (Claude Code)"
	if err := r.registerAgent(s); err != nil {
		t.Fatalf("registerAgent: %v", err)
	}
	if got := fake.agents["a"].HarnessVersion; got != "2.1.311 (Claude Code)" {
		t.Fatalf("stored harness version = %q", got)
	}
}
