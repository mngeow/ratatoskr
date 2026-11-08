package gateway

// The participant hub: each participant's one inbox, its one parked long poll, and the offers waiting for
// the next poll (contract section 6).
//
// It holds no Store and no HTTP. It decides nothing about liveness; it only says whether a poll is parked
// right now, which is the open half of HostLive(lastPoll, open, now).
//
// Nothing here is persistent. A pending offer exists only while the sender's request is waiting for it (at
// most AckWait), and a restart loses them, which is correct because their senders have already been answered.
// Ack wake-ups reuse the host hub's expectAck, so the same recordTimeout logic decides the result.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// maxPendingOffers bounds the offers held for one participant between two polls. It is a memory bound; the
// senders' own 5 s waits are the time bound.
const maxPendingOffers = 64 // UNTESTED guess

// errInboxFull reports a hand-over to an inbox that already holds as many pending offers as it may.
var errInboxFull = errors.New("gateway: the participant's inbox is full")

// offer states. They are guarded by the hub's mutex.
const (
	offerPending   = iota // in memory, unclaimed
	offerClaimed          // written (or about to be written) into a poll answer; never re-offered
	offerWithdrawn        // taken back by its sender before any poll claimed it
)

// pendingOffer is one offer on its way to a participant's poll.
type pendingOffer struct {
	offer protocol.Offer
	state int
}

// parkedPoll is the one open poll of a participant.
type parkedPoll struct {
	// wake carries a nudge: a new pending offer, or being replaced.
	wake     chan struct{}
	replaced bool
}

// inbox is one participant's side of the hub.
type inbox struct {
	poll    *parkedPoll
	pending []*pendingOffer
}

type participantHub struct {
	mu      sync.Mutex
	inboxes map[string]*inbox
}

func newParticipantHub() *participantHub {
	return &participantHub{inboxes: make(map[string]*inbox)}
}

// inboxLocked returns the participant's inbox, creating it. The caller holds mu.
func (h *participantHub) inboxLocked(id string) *inbox {
	ib := h.inboxes[id]
	if ib == nil {
		ib = &inbox{}
		h.inboxes[id] = ib
	}
	return ib
}

// parked reports whether a poll is parked for the participant right now.
func (h *participantHub) parked(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	ib := h.inboxes[id]
	return ib != nil && ib.poll != nil
}

// offer hands one offer to the participant's inbox and nudges a parked poll. It does not block. The caller
// must withdraw the offer if it stops waiting for it.
func (h *participantHub) offer(id string, offer protocol.Offer) (*pendingOffer, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ib := h.inboxLocked(id)
	if len(ib.pending) >= maxPendingOffers {
		return nil, errInboxFull
	}
	pending := &pendingOffer{offer: offer, state: offerPending}
	ib.pending = append(ib.pending, pending)
	if ib.poll != nil {
		nudge(ib.poll.wake)
	}
	return pending, nil
}

// withdraw takes an offer back if no poll has claimed it, and reports whether the offer was never claimed:
// true when it was still pending and is now withdrawn, and true when dropPending had already withdrawn it.
// A claimed offer is never taken back and gives false: it may already have been written to the adapter.
func (h *participantHub) withdraw(id string, pending *pendingOffer) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if pending.state == offerClaimed {
		return false
	}
	pending.state = offerWithdrawn
	if ib := h.inboxes[id]; ib != nil {
		for i, candidate := range ib.pending {
			if candidate == pending {
				ib.pending = append(ib.pending[:i], ib.pending[i+1:]...)
				break
			}
		}
	}
	return true
}

// claimLocked takes up to protocol.ParticipantMaxOffers pending offers for a poll, marking each claimed.
// The caller holds mu.
func (h *participantHub) claimLocked(ib *inbox) []protocol.Offer {
	offers := make([]protocol.Offer, 0, protocol.ParticipantMaxOffers)
	for len(ib.pending) > 0 && len(offers) < protocol.ParticipantMaxOffers {
		pending := ib.pending[0]
		ib.pending = ib.pending[1:]
		pending.state = offerClaimed
		offers = append(offers, pending.offer)
	}
	return offers
}

// pollOutcome says how a poll ended.
type pollOutcome int

const (
	pollOffers   pollOutcome = iota // offers claimed (possibly none, at the wait's end)
	pollReplaced                    // a newer poll displaced this one
	pollCancel                      // the client left or the gateway is closing: nothing was claimed
	pollRevoked                     // the revocation check failed
)

// poll parks the participant's one poll. A second poll replaces the first, which ends at once with
// pollReplaced. The poll leaves on: an offer (claimed and returned), the wait elapsing (a final claim, then
// an empty list is the normal answer), the client leaving or closing being signalled (nothing claimed),
// being replaced, or check reporting that the participant is no longer active.
//
// check is called every checkEvery while parked: a revocation is made by another process, so the poll
// notices it on this schedule and not through a wake-up.
func (h *participantHub) poll(ctx context.Context, id string, wait time.Duration, closing <-chan struct{}, checkEvery time.Duration, check func() bool) ([]protocol.Offer, pollOutcome) {
	mine := &parkedPoll{wake: make(chan struct{}, 1)}

	h.mu.Lock()
	ib := h.inboxLocked(id)
	if previous := ib.poll; previous != nil {
		previous.replaced = true
		nudge(previous.wake)
	}
	ib.poll = mine
	// Offers already waiting go out at once.
	if offers := h.claimLocked(ib); len(offers) > 0 {
		ib.poll = nil
		h.mu.Unlock()
		return offers, pollOffers
	}
	h.mu.Unlock()

	waitTimer := time.NewTimer(wait)
	defer waitTimer.Stop()
	checkTicker := time.NewTicker(checkEvery)
	defer checkTicker.Stop()

	for {
		select {
		case <-mine.wake:
			h.mu.Lock()
			if mine.replaced {
				// Whoever replaced this poll owns ib.poll now.
				h.mu.Unlock()
				return []protocol.Offer{}, pollReplaced
			}
			offers := h.claimLocked(ib)
			if len(offers) > 0 {
				ib.poll = nil
				h.mu.Unlock()
				return offers, pollOffers
			}
			h.mu.Unlock()

		case <-waitTimer.C:
			// The final claim and leaving the inbox happen under one lock, so an offer cannot be left
			// pending with nobody polling for it.
			h.mu.Lock()
			if mine.replaced {
				h.mu.Unlock()
				return []protocol.Offer{}, pollReplaced
			}
			offers := h.claimLocked(ib)
			ib.poll = nil
			h.mu.Unlock()
			return offers, pollOffers

		case <-checkTicker.C:
			if !check() {
				h.leave(id, mine)
				return []protocol.Offer{}, pollRevoked
			}

		case <-ctx.Done():
			h.leave(id, mine)
			return []protocol.Offer{}, pollCancel

		case <-closing:
			h.leave(id, mine)
			return []protocol.Offer{}, pollCancel
		}
	}
}

// leave removes this poll from the inbox if it is still the parked one. Nothing is claimed.
func (h *participantHub) leave(id string, mine *parkedPoll) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ib := h.inboxes[id]; ib != nil && ib.poll == mine {
		ib.poll = nil
	}
}

// dropPending withdraws every unclaimed offer of one participant, for a revocation noticed by this process.
// Their senders find them withdrawn when their own wait ends.
func (h *participantHub) dropPending(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ib := h.inboxes[id]
	if ib == nil {
		return
	}
	for _, pending := range ib.pending {
		pending.state = offerWithdrawn
	}
	ib.pending = nil
}

// nudge sends on a one-slot channel without blocking.
func nudge(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}
