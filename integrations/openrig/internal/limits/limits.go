// Package limits decides whether one offer may be typed into the operator's seat.
//
// Authority: docs/implementation/openrig-bridge-contract.md section 9, "Limits", evaluated at steps 3
// and 4 of the pipeline in section 8.3. Three bounds: the body's size, how often one sender may reach
// the operator, and how much the whole bridge may accept. A refusal is not an error: it is the reason
// the bridge sends back as `saved:false`, which the participant contract already defines.
//
// EVERY NUMBER HERE IS AN UNTESTED GUESS (GR-3): no run has chosen them. They are defaults in the
// contract's configuration table, and the caller is expected to configure them.
package limits

import (
	"fmt"
	"sync"
	"time"
)

const (
	// window is the rolling period both rate limits measure: the last 60 seconds.
	window = 60 * time.Second

	// maxSenders bounds memory when a crowd of senders exists. Beyond it a sender the bridge has not
	// seen before is refused rather than recorded, so a flood cannot grow the map without limit.
	maxSenders = 1024
)

// rateReason is the one answer for both rate limits: the contract requires the same refusal whichever
// bound was hit.
const rateReason = "rate limit"

// Config is the configured bounds. Zero values are the caller's problem: nothing here invents a
// default, so a zero InlineBodyBytes refuses every non-empty body.
type Config struct {
	// InlineBodyBytes is the largest body, in bytes of UTF-8, the bridge will type.
	InlineBodyBytes int
	// PerSenderPerMinute is how many offers one sender may make in the window, refused ones included.
	PerSenderPerMinute int
	// TotalPerMinute is how many offers the bridge may accept in the window.
	TotalPerMinute int
}

// Limiter holds the sliding windows. It is safe for concurrent use.
//
// Both windows are lists of timestamps pruned on use, so nothing grows without bound while the traffic
// is finite; the accepted list is bounded by TotalPerMinute per window, and each sender's list by
// PerSenderPerMinute plus the offers still inside the window. Counters are in memory only: a restart
// forgets them, which the contract states.
type Limiter struct {
	mu     sync.Mutex
	config Config
	// now is the clock. A test replaces it; a nil clock falls back to time.Now so a missing one cannot
	// panic.
	now func() time.Time

	// senders holds one window per sender, keyed by the offer's `from` address.
	senders map[string][]time.Time
	// accepted holds the window of offers the bridge accepted, which is what the total bound counts.
	accepted []time.Time
}

// New returns a limiter for one configuration and one clock.
//
// A nil clock means time.Now. The configuration is used as given.
func New(config Config, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		config:  config,
		now:     now,
		senders: make(map[string][]time.Time),
	}
}

// Admit decides one offer, in the contract's order: record the offer, then the size bound, then the
// sender's rate, then the total rate.
//
// Every offer is recorded for its sender before anything is refused, so a flood stays blocked while it
// lasts. The returned reason is what the bridge reports as the refusal, and ok is true only when the
// offer may be typed.
func (l *Limiter) Admit(sender, body string) (reason string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	kind := l.now()

	// A sender the bridge has not seen, when the map is full: sweep what has expired and, if the map is
	// still full, refuse without recording so memory cannot grow past the cap.
	previous, tracked := l.senders[sender]
	if !tracked && len(l.senders) >= maxSenders {
		l.sweep(kind)
		if len(l.senders) >= maxSenders {
			return rateReason, false
		}
	}

	// Step 1 of the order: record this offer for the sender, refused or not.
	previous = append(prune(previous, kind), kind)
	l.senders[sender] = previous

	// Step 3: the body's size.
	if len(body) > l.config.InlineBodyBytes {
		return fmt.Sprintf("message too large for the operator (limit %d bytes)", l.config.InlineBodyBytes), false
	}

	// Step 4a: this sender's own rate, counting the offer just recorded.
	if len(previous) > l.config.PerSenderPerMinute {
		return rateReason, false
	}

	// Step 4b: the total the bridge has accepted, which is not the same as the offers it has seen.
	l.accepted = prune(l.accepted, kind)
	if len(l.accepted) >= l.config.TotalPerMinute {
		return rateReason, false
	}

	return "", true
}

// Accepted records that the bridge accepted one offer, for the total window.
//
// The caller records it when it acknowledges `saved:true`: an offer that was refused must never spend
// the total budget, which is what keeps one flooding sender from starving everyone else.
func (l *Limiter) Accepted() {
	l.mu.Lock()
	defer l.mu.Unlock()

	kind := l.now()
	l.accepted = append(prune(l.accepted, kind), kind)
}

// prune drops the timestamps that are no longer inside the window.
//
// A timestamp exactly one window old is outside: the window is the last 60 seconds, not the last 61.
func prune(timestamps []time.Time, now time.Time) []time.Time {
	kept := timestamps[:0]
	for _, stamp := range timestamps {
		if now.Sub(stamp) < window {
			kept = append(kept, stamp)
		}
	}
	return kept
}

// sweep prunes every sender's window and forgets the senders that have nothing left, so the cap can
// not be exhausted by senders that went quiet. It is lazy: it runs when the cap is reached, not on
// every offer.
func (l *Limiter) sweep(now time.Time) {
	for sender, timestamps := range l.senders {
		kept := prune(timestamps, now)
		if len(kept) == 0 {
			delete(l.senders, sender)
			continue
		}
		l.senders[sender] = kept
	}
}
