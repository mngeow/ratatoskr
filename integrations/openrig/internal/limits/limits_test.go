// UNTESTED (GR-3): this file has never been run - no go test, no -race, nothing executed.

package limits

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is the injected clock: tests move time instead of sleeping.
type fakeClock struct {
	current time.Time
}

// now returns the clock's current instant.
func (c *fakeClock) now() time.Time { return c.current }

// advance moves the clock forward.
func (c *fakeClock) advance(d time.Duration) { c.current = c.current.Add(d) }

// newClock starts at a fixed instant so nothing depends on the wall clock.
func newClock() *fakeClock {
	return &fakeClock{current: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
}

// testConfig is generous except where a test narrows one bound.
func testConfig() Config {
	return Config{InlineBodyBytes: 4096, PerSenderPerMinute: 6, TotalPerMinute: 20}
}

// TestAdmitSizeBoundary: exactly the limit is allowed, one byte more is refused with the contract's text.
func TestAdmitSizeBoundary(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 10, PerSenderPerMinute: 100, TotalPerMinute: 100}, clock.now)

	if reason, ok := limiter.Admit("bot@vm-a", strings.Repeat("a", 10)); !ok {
		t.Fatalf("a body at the limit was refused: %q", reason)
	}

	reason, ok := limiter.Admit("bot@vm-a", strings.Repeat("a", 11))
	if ok {
		t.Fatal("a body over the limit was admitted")
	}
	if want := "message too large for the operator (limit 10 bytes)"; reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
}

// TestAdmitSizeCountsBytes: the bound is bytes, not runes.
func TestAdmitSizeCountsBytes(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 4, PerSenderPerMinute: 100, TotalPerMinute: 100}, clock.now)

	// Two runes, four bytes: at the limit.
	if reason, ok := limiter.Admit("bot@vm-a", "éé"); !ok {
		t.Fatalf("four bytes were refused: %q", reason)
	}
	// Three runes, six bytes: over it.
	if reason, ok := limiter.Admit("bot@vm-a", "ééé"); ok {
		t.Fatalf("six bytes were admitted: %q", reason)
	}
}

// TestAdmitPerSenderRate: the sixth offer is fine, the seventh is refused.
func TestAdmitPerSenderRate(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 6, TotalPerMinute: 100}, clock.now)

	for offer := 1; offer <= 6; offer++ {
		if reason, ok := limiter.Admit("bot@vm-a", "x"); !ok {
			t.Fatalf("offer %d was refused: %q", offer, reason)
		}
	}

	reason, ok := limiter.Admit("bot@vm-a", "x")
	if ok {
		t.Fatal("the seventh offer was admitted")
	}
	if reason != rateReason {
		t.Fatalf("reason = %q, want %q", reason, rateReason)
	}
}

// TestRefusedOffersStillCount: a refusal spends the sender's budget, so a flood stays blocked.
func TestRefusedOffersStillCount(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 1, PerSenderPerMinute: 3, TotalPerMinute: 100}, clock.now)

	// Three offers refused for their size: they still count for this sender.
	for offer := 1; offer <= 3; offer++ {
		if _, ok := limiter.Admit("bot@vm-a", "too large"); ok {
			t.Fatalf("offer %d was admitted despite the size bound", offer)
		}
	}

	// A size-legal offer now hits the sender's rate, not the size bound.
	reason, ok := limiter.Admit("bot@vm-a", "x")
	if ok {
		t.Fatal("a fourth offer from a flooding sender was admitted")
	}
	if reason != rateReason {
		t.Fatalf("reason = %q, want %q (the refusal must be the rate, not the size)", reason, rateReason)
	}
}

// TestPerSenderWindowSlides: after the window the sender may send again, and a timestamp exactly one
// window old is outside it.
func TestPerSenderWindowSlides(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 2, TotalPerMinute: 100}, clock.now)

	for offer := 1; offer <= 2; offer++ {
		if reason, ok := limiter.Admit("bot@vm-a", "x"); !ok {
			t.Fatalf("offer %d was refused: %q", offer, reason)
		}
	}
	if _, ok := limiter.Admit("bot@vm-a", "x"); ok {
		t.Fatal("the third offer was admitted")
	}

	// One second short of the window: still refused.
	clock.advance(window - time.Second)
	if _, ok := limiter.Admit("bot@vm-a", "x"); ok {
		t.Fatal("an offer was admitted inside the window")
	}

	// Exactly one window after the first two: they are outside, so this one is fine.
	clock.advance(time.Second)
	if reason, ok := limiter.Admit("bot@vm-a", "x"); !ok {
		t.Fatalf("an offer exactly one window later was refused: %q", reason)
	}
}

// TestSendersAreIndependent: one sender's refusals do not spend another's budget.
func TestSendersAreIndependent(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 2, TotalPerMinute: 100}, clock.now)

	for offer := 1; offer <= 2; offer++ {
		if _, ok := limiter.Admit("a@vm-a", "x"); !ok {
			t.Fatalf("a@vm-a offer %d was refused", offer)
		}
	}
	if _, ok := limiter.Admit("a@vm-a", "x"); ok {
		t.Fatal("a@vm-a exceeded its own bound")
	}

	if reason, ok := limiter.Admit("b@vm-b", "x"); !ok {
		t.Fatalf("b@vm-b was refused because of a@vm-a: %q", reason)
	}
}

// TestTotalCountsOnlyAccepted: the total bound is spent by Accepted, not by offers seen or refused.
func TestTotalCountsOnlyAccepted(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 100, TotalPerMinute: 2}, clock.now)

	// A refused offer must not spend the total budget.
	if _, ok := limiter.Admit("a@vm-a", "x"); !ok {
		t.Fatal("the first offer was refused")
	}
	// Nothing accepted yet: two more offers still pass.
	for _, sender := range []string{"b@vm-b", "c@vm-c"} {
		if reason, ok := limiter.Admit(sender, "x"); !ok {
			t.Fatalf("%s was refused before anything was accepted: %q", sender, reason)
		}
	}

	limiter.Accepted()
	limiter.Accepted()

	reason, ok := limiter.Admit("d@vm-d", "x")
	if ok {
		t.Fatal("an offer was admitted after the total bound was reached")
	}
	if reason != rateReason {
		t.Fatalf("reason = %q, want %q", reason, rateReason)
	}
}

// TestTotalWindowSlides: the accepted window is rolling, not permanent.
func TestTotalWindowSlides(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 100, TotalPerMinute: 1}, clock.now)

	if reason, ok := limiter.Admit("a@vm-a", "x"); !ok {
		t.Fatalf("the first offer was refused: %q", reason)
	}
	limiter.Accepted()

	if _, ok := limiter.Admit("b@vm-b", "x"); ok {
		t.Fatal("an offer was admitted past the total bound")
	}

	clock.advance(window)
	if reason, ok := limiter.Admit("c@vm-c", "x"); !ok {
		t.Fatalf("an offer was refused after the window: %q", reason)
	}
}

// TestSenderCap: the map stops growing at maxSenders, and a tracked sender keeps working there.
func TestSenderCap(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 100, TotalPerMinute: 1 << 30}, clock.now)

	for index := 0; index < maxSenders; index++ {
		sender := "s" + strconv.Itoa(index) + "@vm-a"
		if reason, ok := limiter.Admit(sender, "x"); !ok {
			t.Fatalf("sender %d was refused before the cap: %q", index, reason)
		}
	}

	reason, ok := limiter.Admit("late@vm-a", "x")
	if ok {
		t.Fatal("a new sender was admitted past the cap")
	}
	if reason != rateReason {
		t.Fatalf("reason = %q, want %q", reason, rateReason)
	}

	// A sender already tracked is unaffected by the cap.
	if reason, ok := limiter.Admit("s0@vm-a", "x"); !ok {
		t.Fatalf("a tracked sender was refused at the cap: %q", reason)
	}
}

// TestSenderCapForgetsQuietSenders: the cap is not exhausted by senders whose offers expired.
func TestSenderCapForgetsQuietSenders(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 100, TotalPerMinute: 1 << 30}, clock.now)

	for index := 0; index < maxSenders; index++ {
		if _, ok := limiter.Admit("s"+strconv.Itoa(index)+"@vm-a", "x"); !ok {
			t.Fatalf("sender %d was refused before the cap", index)
		}
	}

	// Everyone goes quiet for a window: the sweep at the cap must forget them and admit a new sender.
	clock.advance(window)
	if reason, ok := limiter.Admit("fresh@vm-a", "x"); !ok {
		t.Fatalf("the cap was exhausted by senders with nothing in the window: %q", reason)
	}
}

// TestConcurrentAdmit is written, never run under -race: it only checks that concurrent use neither
// panics nor refuses, with one sender per goroutine so each has its own budget.
func TestConcurrentAdmit(t *testing.T) {
	clock := newClock()
	limiter := New(Config{InlineBodyBytes: 100, PerSenderPerMinute: 100, TotalPerMinute: 1 << 30}, clock.now)

	var group sync.WaitGroup
	results := make([]bool, 32)
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func(slot int) {
			defer group.Done()
			_, ok := limiter.Admit("g"+strconv.Itoa(slot)+"@vm-a", "x")
			results[slot] = ok
		}(index)
	}
	group.Wait()

	for slot, ok := range results {
		if !ok {
			t.Fatalf("goroutine %d was refused", slot)
		}
	}
}
