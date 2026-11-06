package protocol

import "time"

// Types for a connected run (`rtk runtime`): the results a send can have, the
// state of the gateway connection, and how the UI sees both. Design:
// docs/implementation/cross-machine-design.md; contract:
// docs/implementation/cross-machine-contract.md.
//
// None of this exists in plain local mode, and none of it has been run.

// SendResult is how an outbound send ended, or "sending" while it is open. It is
// what the sending agent is told and what the sender's own record keeps. A
// result says what is known: the uncertain ones say that a message may have
// arrived, and are never reported as failures.
type SendResult string

const (
	// SendSending is recorded before any network call and replaced by a result.
	SendSending SendResult = "sending"

	// SendGatewayUnreachable: no gateway connection, or the connection failed
	// before anything could be written. Could not arrive. Local record only.
	SendGatewayUnreachable SendResult = "gateway_unreachable"
	// SendGatewayUncertain: the request was sent and no complete response came
	// in time. May have been stored and delivered.
	SendGatewayUncertain SendResult = "gateway_uncertain"
	// SendUnknownRecipient: the gateway has never seen that name.
	SendUnknownRecipient SendResult = "unknown_recipient"
	// SendDestinationUnreachable: the host has no live stream; never offered.
	SendDestinationUnreachable SendResult = "destination_unreachable"
	// SendSessionGone: the host is live but the name has no current session, or
	// reply_to names an ended session. Never offered.
	SendSessionGone SendResult = "session_gone"
	// SendDeliveryUncertain: offered once, no ack in time. May have been saved.
	SendDeliveryUncertain SendResult = "delivery_uncertain"
	// SendHostStored: the recipient host saved it. Tool success. It means the
	// host has it, not that it was delivered.
	SendHostStored SendResult = "host_stored"
)

// Uncertain reports whether the message may still arrive, so that it must not be
// shown or described as a failure.
func (r SendResult) Uncertain() bool {
	return r == SendGatewayUncertain || r == SendDeliveryUncertain
}

// Failed reports whether the message certainly did not and will not arrive.
func (r SendResult) Failed() bool {
	switch r {
	case SendGatewayUnreachable, SendUnknownRecipient, SendDestinationUnreachable, SendSessionGone:
		return true
	}
	return false
}

// ConnState is the state of the runtime's connection to its gateway. The set is
// closed: the UI may switch on it exhaustively.
type ConnState string

const (
	// ConnConnecting: the first stream of this run is being opened.
	ConnConnecting ConnState = "connecting"
	// ConnConnected: the stream is open and pings are arriving.
	ConnConnected ConnState = "connected"
	// ConnReconnecting: the stream was lost, or no ping arrived within the limit,
	// and the runtime is redialling with backoff. Sends fail at once as
	// gateway_unreachable while in this state.
	ConnReconnecting ConnState = "reconnecting"
	// ConnRefused: the gateway answered and refused this host (credential removed,
	// or a protocol version mismatch). The runtime keeps trying; Err says why.
	ConnRefused ConnState = "refused"
)

// ConnectionView is the gateway link as the UI shows it. It is nil in local mode.
type ConnectionView struct {
	State   ConnState
	Host    string // this machine's name at the gateway
	Gateway string // the gateway address as configured
	Since   time.Time
	// Err is why the link is down or connecting, already sanitized, or empty.
	Err string
	// Peers are the network names (name@host) the gateway last listed, this
	// machine's own agents included.
	Peers []string
	// LANPlainHTTP is true only for a run paired to a LAN gateway with --insecure-http.
	// It is false for HTTPS, Tailscale and loopback, which look the same as before.
	LANPlainHTTP bool
}

// SendView is one outbound send as the UI shows it. Preview is sanitized. Report
// is the recipient host's own latest delivery report, shown as a report (it never
// moves a gate here); it is empty until one arrives.
type SendView struct {
	ID           string
	From         string // local display name
	To           string // network name, name@host
	Result       SendResult
	Report       MessageState
	ReportReason string
	Preview      string
	At           time.Time
}

// SendEventView is one entry in an outbound send's history. Reported is true when
// Report is the recipient host's own report; false means this host observed or
// decided it (Result). The UI must keep the two apart: a report is what another
// machine says, and it never means the model acted.
type SendEventView struct {
	At       time.Time
	Result   SendResult   // set when this entry is a local result
	Report   MessageState // set when Reported
	Reported bool
	Reason   string
}

// SendDetail is one outbound send in full, as the F3 detail shows it.
type SendDetail struct {
	SendView
	ReplyTo string
	Body    string // sanitized for display
	Events  []SendEventView
}

// The two waits whose ORDER matters, owned here so no other package invents its
// own copy. A runtime's POST /v1/messages is deliberately answered only when the
// gateway's offer wait ends, so the runtime must wait for that answer LONGER than
// the gateway waits for the recipient host. If it did not, a slow ack would be
// reported by the sender as "gateway_uncertain" (no answer) instead of the gateway's
// own "delivery_uncertain", and an ack arriving just before the gateway's limit
// could lose a real host_stored.
const (
	// AckWait is how long the gateway waits for the recipient host's ack to an
	// offer before answering delivery_uncertain. UNTESTED guess; contract value.
	AckWait = 5 * time.Second

	// SendWait is how long a runtime waits for the whole POST /v1/messages answer.
	// It covers the gateway's AckWait plus connect and database time. UNTESTED
	// guess; contract value.
	SendWait = 8 * time.Second
)

// The ordering is checked when the package is compiled: SendWait must exceed
// AckWait by at least two seconds, or this constant is negative and the conversion
// to uint64 does not compile.
const _ = uint64(SendWait - AckWait - 2*time.Second)
