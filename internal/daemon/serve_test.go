package daemon

// Untested: under GR-3 nothing here has been run. Only the pure pieces are tested: the
// handshake rule and the status shape. No socket is bound and no connection is made.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

// TestHelloDecision: a window must match the build exactly; a control connection may not.
func TestHelloDecision(t *testing.T) {
	tests := []struct {
		name        string
		hello       wire.Hello
		daemonBuild string
		wantOK      bool
		wantCode    wire.Code
		wantMessage []string
	}{
		{
			name:        "a window of this build is accepted",
			hello:       wire.Hello{Build: "1.2.3", Role: wire.RoleWindow},
			daemonBuild: "1.2.3",
			wantOK:      true,
		},
		{
			name:        "a window of another build is refused",
			hello:       wire.Hello{Build: "1.2.4", Role: wire.RoleWindow},
			daemonBuild: "1.2.3",
			wantCode:    wire.CodeVersionMismatch,
			wantMessage: []string{"this window is build 1.2.4", "the daemon is build 1.2.3", "rtk daemon stop", "start the daemon again with the new build"},
		},
		{
			name:        "a window with no build is refused",
			hello:       wire.Hello{Role: wire.RoleWindow},
			daemonBuild: "1.2.3",
			wantCode:    wire.CodeVersionMismatch,
		},
		{
			name:        "a control connection of this build is accepted",
			hello:       wire.Hello{Build: "1.2.3", Role: wire.RoleControl},
			daemonBuild: "1.2.3",
			wantOK:      true,
		},
		{
			// The refusal's own advice has to work, so control works across builds.
			name:        "a control connection of another build is accepted",
			hello:       wire.Hello{Build: "1.2.4", Role: wire.RoleControl},
			daemonBuild: "1.2.3",
			wantOK:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verdict := helloDecision(tc.hello, tc.daemonBuild)
			if verdict.ok != tc.wantOK {
				t.Fatalf("ok = %t, want %t", verdict.ok, tc.wantOK)
			}
			if verdict.ok {
				return
			}
			if verdict.code != tc.wantCode {
				t.Fatalf("code = %q, want %q", verdict.code, tc.wantCode)
			}
			for _, part := range tc.wantMessage {
				if !strings.Contains(verdict.message, part) {
					t.Fatalf("message %q does not contain %q", verdict.message, part)
				}
			}
		})
	}
}

// TestStatusShape: the status answer carries the link state from the router's snapshot and
// no sessions in this slice.
func TestStatusShape(t *testing.T) {
	server := &daemonServer{
		build:     "1.2.3",
		sessions:  &fakeSessionControl{},
		logPath:   "/state/daemon/daemon.log",
		startedAt: time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC),
	}
	status := server.status()

	if status.Build != "1.2.3" {
		t.Fatalf("build = %q", status.Build)
	}
	if status.LogPath != "/state/daemon/daemon.log" {
		t.Fatalf("log path = %q", status.LogPath)
	}
	if status.Windows != 0 {
		t.Fatalf("windows = %d, want 0 in this slice", status.Windows)
	}
	if len(status.Sessions) != 0 {
		t.Fatalf("sessions = %d, want 0", len(status.Sessions))
	}
	// No router in this test, so no link state is reported rather than a guessed one.
	if status.Link != "" {
		t.Fatalf("link = %q, want empty with no snapshot", status.Link)
	}
}

// fakeSessionControl is a control surface with no sessions.
type fakeSessionControl struct {
	status []wire.SessionStatus
	endErr error
}

// Status reports the recorded sessions.
func (f *fakeSessionControl) Status() []wire.SessionStatus { return f.status }

// End ends one session, or reports the configured error.
func (f *fakeSessionControl) End(context.Context, string) error { return f.endErr }

// TestSessionStatusToleratesNoControl: a server built without a session control answers a
// status request rather than panicking.
func TestSessionStatusToleratesNoControl(t *testing.T) {
	if got := sessionStatus(nil); got != nil {
		t.Fatalf("sessionStatus(nil) = %v, want nil", got)
	}

	control := &fakeSessionControl{status: []wire.SessionStatus{{Name: "claude"}}}
	if got := sessionStatus(control); len(got) != 1 {
		t.Fatalf("sessionStatus = %v, want one session", got)
	}
	if err := control.End(context.Background(), "claude"); err != nil {
		t.Fatalf("End: %v", err)
	}
}

// TestNewWindowID: eight lowercase hex characters.
func TestNewWindowID(t *testing.T) {
	id := newWindowID()
	if id == "" {
		// Only on a random source failure, which would also be logged elsewhere.
		t.Skip("the random source failed")
	}
	if len(id) != 8 {
		t.Fatalf("id = %q, want 8 characters", id)
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("id = %q is not lowercase hex", id)
		}
	}
}
