package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"testing"
	"time"
)

func TestHostLive(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		lastHeartbeat time.Time
		streamOpen    bool
		want          bool
	}{
		{name: "fresh and open", lastHeartbeat: now.Add(-time.Second), streamOpen: true, want: true},
		{name: "exactly the window", lastHeartbeat: now.Add(-liveWindow), streamOpen: true, want: true},
		{name: "one nanosecond over", lastHeartbeat: now.Add(-liveWindow - time.Nanosecond), streamOpen: true, want: false},
		{name: "open but never heard", streamOpen: true, want: false},
		{name: "fresh but closed", lastHeartbeat: now.Add(-time.Second), want: false},
		{name: "closed and stale", lastHeartbeat: now.Add(-time.Hour), want: false},
		{name: "one second in the future is tolerated", lastHeartbeat: now.Add(time.Second), streamOpen: true, want: true},
		{name: "far future is not trusted", lastHeartbeat: now.Add(time.Minute), streamOpen: true, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostLive(tc.lastHeartbeat, tc.streamOpen, now); got != tc.want {
				t.Fatalf("HostLive(%v, %v) = %v, want %v", tc.lastHeartbeat, tc.streamOpen, got, tc.want)
			}
		})
	}
}

func TestHeartbeatFresh(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		lastHeartbeat time.Time
		want          bool
	}{
		{name: "zero", want: false},
		{name: "just now", lastHeartbeat: now, want: true},
		{name: "exactly the window", lastHeartbeat: now.Add(-liveWindow), want: true},
		{name: "one nanosecond over", lastHeartbeat: now.Add(-liveWindow - time.Nanosecond), want: false},
		{name: "skew inside the tolerance", lastHeartbeat: now.Add(time.Second), want: true},
		{name: "skew beyond the tolerance", lastHeartbeat: now.Add(time.Second + time.Nanosecond), want: false},
		{name: "an hour old", lastHeartbeat: now.Add(-time.Hour), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HeartbeatFresh(tc.lastHeartbeat, now); got != tc.want {
				t.Fatalf("HeartbeatFresh(%v) = %v, want %v", tc.lastHeartbeat, got, tc.want)
			}
		})
	}
}
