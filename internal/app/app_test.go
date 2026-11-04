package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Written but not run: GR-3 (docs/design/ground-rules.md) forbids running tests
// until the user says otherwise.

func TestExitReason(t *testing.T) {
	failure := errors.New("failure")
	for _, tc := range []struct {
		name      string
		signalled bool
		err       error
		reason    string
		wantErr   error
	}{
		{"clean", false, nil, "clean", nil},
		{"error", false, failure, "error", failure},
		{"signal hides the kill error", true, tea.ErrProgramKilled, "signal", nil},
		{"interrupted is a signal exit", false, tea.ErrInterrupted, "signal", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := exitReason(tc.signalled, tc.err)
			if reason != tc.reason || !errors.Is(err, tc.wantErr) {
				t.Fatalf("got (%q, %v), want (%q, %v)", reason, err, tc.reason, tc.wantErr)
			}
		})
	}
}

func TestBoundedAbandonsAStepThatOverruns(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	err := bounded(time.Millisecond, func() error {
		<-release
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
}

func TestBoundedReturnsTheStepsError(t *testing.T) {
	failure := errors.New("failure")
	if err := bounded(time.Minute, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("got %v, want %v", err, failure)
	}
}

func TestNoteOverrunOnlyHidesTimeouts(t *testing.T) {
	if err := noteOverrun("step", context.DeadlineExceeded); err != nil {
		t.Fatalf("timeout: got %v, want nil", err)
	}
	failure := errors.New("failure")
	if err := noteOverrun("step", failure); !errors.Is(err, failure) {
		t.Fatalf("got %v, want %v", err, failure)
	}
}

type fakeCloser func(ctx context.Context) error

func (f fakeCloser) CloseContext(ctx context.Context) error { return f(ctx) }

// A pane that uses the whole limit must not delay the other's close: both must
// have started before either finishes.
func TestCloseAllClosesConcurrently(t *testing.T) {
	var started atomic.Int32
	bothStarted := make(chan struct{})
	closer := fakeCloser(func(ctx context.Context) error {
		if started.Add(1) == 2 {
			close(bothStarted)
		}
		select {
		case <-bothStarted:
			return nil
		case <-ctx.Done():
			return errors.New("the other pane never started")
		}
	})
	if err := closeAll(time.Minute, []fakeCloser{closer, closer}); err != nil {
		t.Fatal(err)
	}
}

func TestCloseAllKeepsRealFailuresAndNotesTimeouts(t *testing.T) {
	failure := errors.New("failure")
	err := closeAll(time.Millisecond, []fakeCloser{
		func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		func(context.Context) error { return failure },
	})
	if !errors.Is(err, failure) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want only the real failure", err)
	}
}
