package a2a

// The outbound seam. The gateway is an A2A CLIENT to externally managed services (P4); this package does
// not dial. The gateway supplies a ClientFactory that returns an *http.Client bound to ONE validated
// target and built on the card dial rules (cardtarget.go, carddial.go: IP literal, dial-time deny list and
// class check, no proxy, no redirects, no cookies, HTTP/1.1, per-target TLS pin). Keeping the rules there
// leaves them unchanged and avoids an import cycle.
//
// Rules the caller of the factory keeps (DevOps consult, contract section 8): SendMessage is never
// retried, not even by middleware; a lost or oversized SendMessage reply is UNCERTAIN and never replayed;
// the A2A calls use their own fixed limits (connect 3 s, total 5 s, headers 16 KiB: untested guesses) and
// read bodies as limit+1; the registration card keeps the card caps. Mirroring a remote task is P4.
//
// UNTESTED.

import (
	"context"
	"net/http"
)

// ClientFactory returns an HTTP client bound to one registered destination. The client refuses to dial
// anything else. It is called per use and holds no state between uses.
type ClientFactory func(ctx context.Context, destination Destination) (*http.Client, error)
