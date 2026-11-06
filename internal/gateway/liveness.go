package gateway

// Liveness: the one place that decides whether a host is live.
//
// Nothing else in this package may re-implement this test. The design settles
// that liveness comes from what the gateway receives - a heartbeat - not from
// whether a write to the host's stream appeared to succeed.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import "time"

const (
	// liveWindow is how long a heartbeat keeps a host live. It is the contract's
	// 15 s value, not a measured one.
	liveWindow = 15 * time.Second // UNTESTED guess; contract value

	// clockSkewTolerance is how far a heartbeat may appear to come from the
	// future and still be believed. A larger jump is a broken clock, and a broken
	// clock must not read as permanently live.
	clockSkewTolerance = time.Second // UNTESTED guess
)

// HostLive reports whether a host counts as live: its stream is open and it has
// sent a heartbeat recently enough.
//
// The honest window: for up to liveWindow after a silent drop the gateway still
// counts the host live, so a send in that window ends delivery_uncertain rather
// than destination_unreachable.
//
// UNTESTED.
func HostLive(lastHeartbeat time.Time, streamOpen bool, now time.Time) bool {
	return streamOpen && HeartbeatFresh(lastHeartbeat, now)
}

// HeartbeatFresh applies the freshness rule without needing a stream, for the
// admin `hosts` command, which cannot see streams.
//
// A zero heartbeat is never fresh. A heartbeat more than clockSkewTolerance in
// the future is not fresh either: it means the stored time cannot be trusted, and
// treating it as fresh would keep a host live forever.
//
// UNTESTED.
func HeartbeatFresh(lastHeartbeat, now time.Time) bool {
	if lastHeartbeat.IsZero() {
		return false
	}

	age := now.Sub(lastHeartbeat)
	if age > liveWindow {
		return false
	}
	if age < -clockSkewTolerance {
		return false
	}
	return true
}
