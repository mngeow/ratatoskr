package daemon

// Untested: under GR-3 nothing here has been run. The status format is a pure function, so
// no daemon and no socket are involved.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

// TestPrintStatus: one item per line, both build forms, and the session table.
func TestPrintStatus(t *testing.T) {
	started := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	now := started.Add(2*time.Hour + 3*time.Minute)

	tests := []struct {
		name     string
		status   wire.Status
		cliBuild string
		want     []string
		absent   []string
	}{
		{
			name: "a foreground daemon with no link and no sessions",
			status: wire.Status{
				PID:       4321,
				Build:     "1.2.3",
				StartedAt: started,
				LogPath:   "",
			},
			cliBuild: "1.2.3",
			want: []string{
				"pid: 4321",
				"build: 1.2.3",
				"uptime: 2h3m",
				"log: stderr (foreground)",
				"windows: 0",
				"sessions: none",
			},
			absent: []string{"this CLI", "link:"},
		},
		{
			name: "a detached daemon with a link and sessions",
			status: wire.Status{
				PID:       99,
				Build:     "1.2.3",
				StartedAt: started,
				LogPath:   "/state/daemon/daemon.log",
				Windows:   2,
				Link:      "connected",
				Sessions: []wire.SessionStatus{
					{Name: "claude", Address: "claude@mac", Holder: "w1"},
					{Name: "codex", Address: "codex@mac", Drafting: true},
				},
			},
			cliBuild: "1.2.3",
			want: []string{
				"pid: 99",
				"log: /state/daemon/daemon.log",
				"link: connected",
				"windows: 2",
				"sessions: 2",
				"claude  claude@mac  holder w1",
				"codex  codex@mac  no holder  drafting",
			},
		},
		{
			name: "a differing CLI build is shown beside the daemon's",
			status: wire.Status{
				PID:       7,
				Build:     "1.2.3",
				StartedAt: started,
			},
			cliBuild: "1.2.4",
			want:     []string{"build: 1.2.3 (this CLI: 1.2.4)"},
		},
		{
			name: "a link error is its own line",
			status: wire.Status{
				PID:       7,
				Build:     "1.2.3",
				StartedAt: started,
				Link:      "reconnecting",
				LinkErr:   "connection reset",
			},
			cliBuild: "1.2.3",
			want: []string{
				"link: reconnecting",
				"link error: connection reset",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			PrintStatus(&out, tc.status, tc.cliBuild, now)

			got := out.String()
			for _, part := range tc.want {
				if !strings.Contains(got, part) {
					t.Fatalf("output %q does not contain %q", got, part)
				}
			}
			for _, part := range tc.absent {
				if strings.Contains(got, part) {
					t.Fatalf("output %q contains %q, which should not be shown", got, part)
				}
			}
			// Every line ends with exactly one newline and none is empty.
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			for _, line := range lines {
				if line == "" {
					t.Fatalf("output %q has an empty line", got)
				}
			}
		})
	}
}

// TestShortDuration: the uptime forms.
func TestShortDuration(t *testing.T) {
	tests := []struct {
		of   time.Duration
		want string
	}{
		{of: 0, want: "0s"},
		{of: 5 * time.Second, want: "5s"},
		{of: 59 * time.Second, want: "59s"},
		{of: time.Minute, want: "1m"},
		{of: 90 * time.Minute, want: "1h30m"},
		{of: 26 * time.Hour, want: "1d2h"},
		{of: -time.Minute, want: "0s"},
	}

	for _, tc := range tests {
		if got := shortDuration(tc.of); got != tc.want {
			t.Fatalf("shortDuration(%v) = %q, want %q", tc.of, got, tc.want)
		}
	}
}

// TestErrNotRunningText: the one text for a missing daemon, and its advice.
func TestNotRunningText(t *testing.T) {
	text := ErrNotRunning.Error()
	for _, part := range []string{"no daemon is running", "rtk daemon"} {
		if !strings.Contains(text, part) {
			t.Fatalf("text %q does not contain %q", text, part)
		}
	}
}
