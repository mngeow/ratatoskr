package store

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func seedRun(t *testing.T, s *Store, run string) {
	t.Helper()
	ctx := context.Background()
	if err := s.StartRun(ctx, run, "test", t0); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Agent{{ID: protocol.AgentID(run + "-c"), DisplayName: "claude"}, {ID: protocol.AgentID(run + "-x"), DisplayName: "codex"}} {
		a.RunID, a.Harness, a.StartedAt = run, "h", t0
		if err := s.InsertAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
}

func insert(t *testing.T, s *Store, run, id string) {
	t.Helper()
	_, err := s.InsertMessage(context.Background(), Message{
		ID: id, RunID: run, From: protocol.AgentID(run + "-c"), To: protocol.AgentID(run + "-x"), Body: "hi", CreatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func state(t *testing.T, s *Store, run, id string) (protocol.MessageState, string) {
	t.Helper()
	rows, err := s.History(context.Background(), run, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.State, r.Reason
		}
	}
	t.Fatalf("message %s not found", id)
	return "", ""
}

func advance(t *testing.T, s *Store, id string, path ...protocol.MessageState) {
	t.Helper()
	from := protocol.MessageQueued
	for _, to := range path {
		if err := s.Transition(context.Background(), id, from, to, "", t0); err != nil {
			t.Fatalf("%s -> %s: %v", from, to, err)
		}
		from = to
	}
}

func TestSecondOpenRefusedWhileFirstHoldsLock(t *testing.T) {
	s, dir := openTemp(t)
	if _, err := Open(context.Background(), dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Open err = %v, want ErrAlreadyRunning", err)
	}
	s.Close()
	s2, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	s2.Close()
}

func TestPermissions(t *testing.T) {
	_, dir := openTemp(t)
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, dbFile): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestTransitionEnforcesLifecycle(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	insert(t, s, "r1", "m1")
	ctx := context.Background()

	if err := s.Transition(ctx, "m1", protocol.MessageQueued, protocol.MessageAccepted, "", t0); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("queued->accepted err = %v, want ErrIllegalTransition", err)
	}
	if err := s.Transition(ctx, "m1", protocol.MessageHeld, protocol.MessageDelivering, "", t0); !errors.Is(err, ErrStateChanged) {
		t.Errorf("wrong from-state err = %v, want ErrStateChanged", err)
	}
	if got, _ := state(t, s, "r1", "m1"); got != protocol.MessageQueued {
		t.Errorf("state after refused transitions = %s, want queued", got)
	}

	advance(t, s, "m1", protocol.MessageDelivering, protocol.MessagePasted, protocol.MessageUnconfirmed, protocol.MessageAccepted)
	if err := s.Transition(ctx, "m1", protocol.MessageAccepted, protocol.MessageFailed, "", t0); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("accepted is final, err = %v", err)
	}
}

func TestFinishRunInterruptsWithMatchingReasons(t *testing.T) {
	s, _ := openTemp(t)
	seedRun(t, s, "r1")
	want := map[string]struct {
		path   []protocol.MessageState
		state  protocol.MessageState
		reason string
	}{
		"queued":     {nil, protocol.MessageInterrupted, ReasonNeverWritten},
		"held":       {[]protocol.MessageState{protocol.MessageHeld}, protocol.MessageInterrupted, ReasonNeverWritten},
		"delivering": {[]protocol.MessageState{protocol.MessageDelivering}, protocol.MessageInterrupted, ReasonWriteUnknown},
		"pasted":     {[]protocol.MessageState{protocol.MessageDelivering, protocol.MessagePasted}, protocol.MessageInterrupted, ReasonPastedUnconfirmed},
		"accepted":   {[]protocol.MessageState{protocol.MessageDelivering, protocol.MessagePasted, protocol.MessageAccepted}, protocol.MessageAccepted, ""},
		"failed":     {[]protocol.MessageState{protocol.MessageFailed}, protocol.MessageFailed, ""},
	}
	for id, w := range want {
		insert(t, s, "r1", id)
		advance(t, s, id, w.path...)
	}
	n, err := s.FinishRun(context.Background(), "r1", "clean", t0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("interrupted = %d, want 4", n)
	}
	for id, w := range want {
		got, reason := state(t, s, "r1", id)
		if got != w.state {
			t.Errorf("%s: state = %s, want %s", id, got, w.state)
		}
		if w.state == protocol.MessageInterrupted && reason != w.reason {
			t.Errorf("%s: reason = %q, want %q", id, reason, w.reason)
		}
	}
}

func TestRecoverClosesOnlyUnclosedRunsAndNeverResends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	ctx := context.Background()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	seedRun(t, s, "old")
	insert(t, s, "old", "m1")
	advance(t, s, "m1", protocol.MessageDelivering)
	s.Close() // stands in for a crash: the run was never finished

	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Recover(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RunID != "old" || got[0].Interrupted != 1 {
		t.Fatalf("Recover = %+v", got)
	}
	if st, reason := state(t, s, "old", "m1"); st != protocol.MessageInterrupted || reason != ReasonWriteUnknown {
		t.Errorf("m1 = %s/%s", st, reason)
	}
	runs, _ := s.Runs(ctx, 10)
	if len(runs) != 1 || runs[0].EndReason != "crashed" || runs[0].Interrupted != 1 {
		t.Errorf("Runs = %+v", runs)
	}
	if again, _ := s.Recover(ctx, t0); len(again) != 0 {
		t.Errorf("second Recover = %+v, want none", again)
	}
}

func TestRetainDeletesOnlyOldEndedRunsWithTheirMessages(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	seedRun(t, s, "old")
	insert(t, s, "old", "m-old")
	s.FinishRun(ctx, "old", "clean", t0.Add(-40*24*time.Hour))
	seedRun(t, s, "recent")
	s.FinishRun(ctx, "recent", "clean", t0.Add(-1*time.Hour))
	seedRun(t, s, "live") // never ended: must survive

	n, err := s.Retain(ctx, t0.Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("Retain = %d, %v; want 1", n, err)
	}
	runs, _ := s.Runs(ctx, 10)
	if len(runs) != 2 {
		t.Errorf("runs left = %d, want 2", len(runs))
	}
	if ok, _ := s.MessageExists(ctx, "old", "m-old"); ok {
		t.Error("message of deleted run still present")
	}
}

func TestNewerSchemaRefusedAndLeftUntouched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	ctx := context.Background()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.ExecContext(ctx, "PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, dir); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("Open err = %v, want ErrNewerSchema", err)
	}
	// The failed Open must have released the lock.
	if l, err := acquireLock(filepath.Join(dir, lockFile)); err != nil {
		t.Errorf("lock still held after failed Open: %v", err)
	} else {
		l.Close()
	}
}

func TestWritesAfterCloseReturnErrClosed(t *testing.T) {
	s, _ := openTemp(t)
	s.Close()
	if err := s.StartRun(context.Background(), "r", "v", t0); !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}
