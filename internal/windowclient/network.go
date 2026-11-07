package windowclient

// The network query: the daemon's own answer about this host and, when it has one, the gateway's
// snapshot.
//
// It is a query like any other, so it shares the mux, the query semaphore and the timeout, and it
// never talks to the gateway itself: the daemon does that, and only while somebody is looking.
//
// UNTESTED: see doc.go.

import (
	"context"
	"errors"
	"fmt"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Network asks the daemon for the network answer.
//
// The request carries at most one bound: a first-wait for a caller with no snapshot yet, or a wait
// for a caller whose revision is still current. Asking for both would let one request consume two
// daemon budgets, so it is refused rather than clamped, and each bound is checked against the wire
// maximum instead of being quietly shortened.
//
// The answer's version is the daemon's own change counter, which the caller echoes back in Since;
// it is not the gateway's revision.
//
// Success requires a body: an empty answer is an error, even when the version is unchanged, because
// the caller has nothing to draw.
//
// UNTESTED.
func (c *Client) Network(ctx context.Context, request wire.NetworkRequest) (protocol.NetworkView, uint64, error) {
	switch {
	case request.WaitMS < 0 || request.WaitMS > wire.MaxNetworkWaitMS:
		return protocol.NetworkView{}, 0, fmt.Errorf("windowclient: wait_ms must be between 0 and %d", wire.MaxNetworkWaitMS)
	case request.FirstWaitMS < 0 || request.FirstWaitMS > wire.MaxNetworkFirstMS:
		return protocol.NetworkView{}, 0, fmt.Errorf("windowclient: first_wait_ms must be between 0 and %d", wire.MaxNetworkFirstMS)
	case request.WaitMS > 0 && request.FirstWaitMS > 0:
		return protocol.NetworkView{}, 0, errors.New("windowclient: a network request carries a first wait or a wait, not both")
	}

	result, err := c.queryContext(ctx, wire.Query{Kind: wire.QueryNetwork, Network: &request})
	if err != nil {
		return protocol.NetworkView{}, 0, err
	}
	if result.Network == nil {
		return protocol.NetworkView{}, 0, errors.New("windowclient: the daemon answered the network request without a body")
	}
	return *result.Network, result.NetworkVersion, nil
}
