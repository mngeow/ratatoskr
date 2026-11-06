package protocol

import "time"

// The gateway wire protocol, version 1: JSON over HTTP between a runtime and a
// gateway. Routes and rules are in docs/implementation/cross-machine-contract.md;
// this file only fixes the shapes. UNTESTED.

const (
	// WireVersion is sent in WireVersionHeader on every request. A different
	// value is refused with WireErrVersion, which the runtime shows to the user.
	WireVersion       = 1
	WireVersionHeader = "X-Ratatoskr-Protocol"

	// Error codes in WireError.Code.
	WireErrVersion      = "version_mismatch"
	WireErrUnauthorized = "unauthorized"
	WireErrBadRequest   = "bad_request"
	WireErrNotFound     = "not_found"
	WireErrBadCode      = "bad_pairing_code"
	WireErrInternal     = "internal"

	// Stream frame types.
	FrameOffer     = "offer"
	FrameDirectory = "directory"
	FramePing      = "ping"
	// FrameReport tells a sender the recipient host's latest report on one of its
	// messages, or a correction to an uncertain result (Report carries the
	// gateway's current view).
	FrameReport = "report"
)

// WireError is the body of every non-2xx response.
type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PairRequest exchanges a one-time code for a credential. Host is optional: the
// code was minted for one host, and the gateway answers with that host's name. If
// Host is sent, the gateway refuses a mismatch.
type PairRequest struct {
	Code string `json:"code"`
	Host string `json:"host"`
}

// PairResponse carries the credential once. The gateway keeps only its hash.
type PairResponse struct {
	Host       string `json:"host"`
	Credential string `json:"credential"`
}

// SessionsRequest registers this run's agents by local display name. The host is
// taken from the credential, never from the request.
type SessionsRequest struct {
	Agents []string `json:"agents"`
}

// SessionInfo is one registered agent.
type SessionInfo struct {
	Name      string `json:"name"`       // local display name
	Address   string `json:"address"`    // name@host
	SessionID string `json:"session_id"` // new for every registration
}

// DirectoryEntry is one agent the gateway knows, live or not.
type DirectoryEntry struct {
	Address string `json:"address"` // name@host
	Host    string `json:"host"`
	Live    bool   `json:"live"` // host has a stream and the name has a current session
}

// SessionsResponse answers a registration.
type SessionsResponse struct {
	Host      string           `json:"host"`
	Sessions  []SessionInfo    `json:"sessions"`
	Directory []DirectoryEntry `json:"directory"`
}

// Offer is one message the gateway offers a host, once.
type Offer struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`       // name@host, set by the gateway
	To        string    `json:"to"`         // the recipient's local display name on this host
	ToSession string    `json:"to_session"` // the session the gateway meant
	ReplyTo   string    `json:"reply_to,omitempty"`
	Body      string    `json:"body"`
	At        time.Time `json:"at"`
}

// StreamFrame is one line of the long-lived stream. Exactly one of Offer,
// Directory or Report is set for those types; ping carries none.
type StreamFrame struct {
	Type      string           `json:"type"`
	Offer     *Offer           `json:"offer,omitempty"`
	Directory []DirectoryEntry `json:"directory,omitempty"`
	Report    *MessageInfo     `json:"report,omitempty"`
	At        time.Time        `json:"at"`
}

// SendRequest is one send. ID is chosen by the sender, so the same ID applied
// twice stores and offers once. From is the sender's local display name; the
// gateway builds name@host from the credential.
type SendRequest struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"` // name@host
	ReplyTo string `json:"reply_to,omitempty"`
	Body    string `json:"body"`
}

// SendResponse is the result. Directory is set for unknown_recipient.
type SendResponse struct {
	ID        string           `json:"id"`
	Result    SendResult       `json:"result"`
	Detail    string           `json:"detail,omitempty"`
	Directory []DirectoryEntry `json:"directory,omitempty"`
}

// AckRequest is the recipient host's answer to an offer: saved, or refused with
// a reason.
type AckRequest struct {
	Saved  bool   `json:"saved"`
	Reason string `json:"reason,omitempty"`
}

// StatusReport is the recipient host's delivery report. Idempotent; repeated
// after a reconnect.
type StatusReport struct {
	State  MessageState `json:"state"`
	Reason string       `json:"reason,omitempty"`
	At     time.Time    `json:"at"`
}

// MessageInfo is the gateway's current view of one message, used to correct an
// uncertain record.
type MessageInfo struct {
	ID     string     `json:"id"`
	Result SendResult `json:"result"`
	// ResultReason carries why, for results that have a reason: a recipient
	// host's refusal text, for example. It reaches the sender's tool result and
	// is stored in the ledger beside the result.
	ResultReason string       `json:"result_reason,omitempty"`
	Report       MessageState `json:"report,omitempty"`
	ReportReason string       `json:"report_reason,omitempty"`
}

// HeartbeatPath is the liveness route: each runtime posts to it every 5 s while
// its stream is open. The gateway counts a host live only if it has heard from it
// within 15 s, and it stores its own receive time (never the runtime's At).
const HeartbeatPath = "/v1/heartbeat"

// HeartbeatRequest is the body of a heartbeat. It carries nothing the gateway
// trusts: the host comes from the credential, the time from the gateway's clock.
type HeartbeatRequest struct {
	At time.Time `json:"at"`
}
