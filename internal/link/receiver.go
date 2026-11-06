package link

// The router's side of a connected run, which slice 5 implements.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import "github.com/mngeow/ratatoskr/internal/protocol"

// Receiver is what the stream tells the router. It is implemented in slice 5.
//
// Every method ONLY enqueues work and returns at once: the stream reader calls them
// inline, on the goroutine that reads frames, and must never block on the router. A
// slow receiver would stall the stream, the watchdog would fire, and a healthy
// connection would be dropped for a reason that is not the gateway's fault.
//
// UNTESTED.
type Receiver interface {
	// Receive reports one offer from the gateway. The router saves it to the inbox
	// and acknowledges it; nothing here does either.
	Receive(offer protocol.Offer)

	// LinkState reports the connection state for the UI. It is called only when
	// the state, the reason or the peer list changed.
	LinkState(view protocol.ConnectionView)

	// LinkDirectory reports the newest peer list: network names (name@host), in the
	// gateway's order, this host's own agents included.
	LinkDirectory(peers []string)

	// LinkUp reports that a connection has just been established (its first frame
	// arrived). The router uses it to re-report inbound messages whose state the
	// gateway has not been told.
	LinkUp()

	// LinkReport reports a recipient host's report for a message this host sent.
	LinkReport(info protocol.MessageInfo)
}
