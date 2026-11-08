package protocol

import "time"

// The gateway wire protocol, version 1: JSON over HTTP between a runtime and a
// gateway. Routes and rules are in docs/implementation/cross-machine-contract.md;
// this file only fixes the shapes. UNTESTED.

const (
	// WireVersion is sent in WireVersionHeader on every request. A different
	// value is refused with WireErrVersion, which the runtime shows to the user.
	//
	// Version 2 registers one session at a time: a daemon connects once (ending the
	// previous instance's sessions), adds each session as it starts, and ends one by
	// name. A version 1 client is refused, so every machine upgrades together.
	WireVersion       = 2
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

// SessionInfo is one registered agent.
type SessionInfo struct {
	Name      string `json:"name"`       // local display name
	Address   string `json:"address"`    // name@host
	SessionID string `json:"session_id"` // new for every registration
}

// Session routes of protocol version 2. The /v1/ prefix names the URL family; the
// protocol version is the WireVersionHeader value.
const (
	// ConnectPath: the daemon starting. It ends this host's earlier sessions, once,
	// because they belonged to the previous daemon instance.
	ConnectPath = "/v1/connect"
	// SessionsPath: POST adds one session (AddSessionRequest); DELETE ends all of this
	// host's sessions; DELETE SessionsPath+"/"+name ends one by name.
	SessionsPath = "/v1/sessions"
)

// ConnectResponse answers a connect: the host and the current directory.
type ConnectResponse struct {
	Host      string           `json:"host"`
	Directory []DirectoryEntry `json:"directory"`
}

// AddSessionRequest registers one session by local display name. The host is taken
// from the credential, never from the request.
type AddSessionRequest struct {
	Name string `json:"name"`
}

// AddSessionResponse answers an add: the new session (a new ID every time) and the
// current directory.
type AddSessionResponse struct {
	Host      string           `json:"host"`
	Session   SessionInfo      `json:"session"`
	Directory []DirectoryEntry `json:"directory"`
}

// DirectoryEntry is one agent the gateway knows, live or not.
type DirectoryEntry struct {
	Address string `json:"address"` // name@host
	Host    string `json:"host"`
	Live    bool   `json:"live"` // host has a stream and the name has a current session
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

// TaskReportPathPrefix is the route prefix a managed session uses to report on a task it was dispatched
// (POST /v1/tasks/{id}/report). The acting session is named in the A2A session header; the gateway checks it
// is a current session of the authenticated host AND the task's bound target.
const TaskReportPathPrefix = "/v1/tasks/"

// TaskReportRequest is a target's explicit report on a task: input_required, completed or failed, with text
// and an optional result. It is the only way a managed target moves a task to those states.
type TaskReportRequest struct {
	State  string `json:"state"`
	Text   string `json:"text,omitempty"`
	Result string `json:"result,omitempty"`
}

// TaskView is what a managed session sees of an A2A task through its task tools. State is the A2A name without
// the TASK_STATE_ prefix. Text is the latest status message; Result is the artifact text, if any. Delivery,
// Connection and Reconciled are the gateway's separate observations (never states).
type TaskView struct {
	ID         string `json:"task_id"`
	State      string `json:"state"`
	Text       string `json:"text,omitempty"`
	Result     string `json:"result,omitempty"`
	Delivery   string `json:"delivery,omitempty"`
	Connection string `json:"connection,omitempty"`
	Reconciled string `json:"reconciled,omitempty"`
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
