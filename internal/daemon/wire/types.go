package wire

// Message bodies and error codes. See wire.go for the framing and the package rules.
//
// UNTESTED: see wire.go.

import (
	"time"

	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Role says what a connection is for.
type Role string

const (
	// RoleWindow is `rtk attach`. It needs the daemon's exact build.
	RoleWindow Role = "window"
	// RoleControl is `rtk daemon status` and `rtk daemon stop`. It is allowed across
	// builds, because the refusal for a mismatched window tells the user to run
	// `rtk daemon stop`, and that command must work to be of any use.
	RoleControl Role = "control"
)

// Code is a machine-readable reason in a refusal or an acknowledgement.
type Code string

const (
	CodeVersionMismatch Code = "version_mismatch" // window build differs from the daemon's
	CodeNotAuthorized   Code = "not_authorized"   // peer UID differs from the daemon's
	CodeAgentRefused    Code = "agent_refused"    // peer is a descendant of a harness this daemon started
	CodeBadRequest      Code = "bad_request"      // malformed, unknown type, or out of order
	CodeTooLarge        Code = "too_large"
	CodeNotHolder       Code = "not_holder"      // the window does not hold input for that session
	CodeSessionEnded    Code = "session_ended"   // the session ended; nothing was written
	CodeNoSuchSession   Code = "no_such_session" // no session with that agent ID
	CodeInputFailed     Code = "input_failed"    // the transport failed; Message has the reason
	CodeShuttingDown    Code = "shutting_down"
	CodeBusy            Code = "busy"          // a session operation is already running
	CodeNotRunning      Code = "not_running"   // an attach expected a session that is not running (ended, replaced or never there)
	CodeCardConflict    Code = "card_conflict" // a card operation carried a stale token; the answer holds the current card view
	CodeInternal        Code = "internal"
)

// Error is a code with a message safe to show a user. It is the error half of an Ack or
// a Result.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Hello opens every connection. Build is the exact build string (`rtk --version`).
type Hello struct {
	Build string `json:"build"`
	Role  Role   `json:"role"`
	PID   int    `json:"pid"`
}

// HelloAck answers an accepted Hello. WindowID is empty for a control connection.
type HelloAck struct {
	Build    string `json:"build"`
	PID      int    `json:"pid"`
	WindowID string `json:"window_id,omitempty"`
}

// Refusal is the body of TypeRefused. The message says what to do.
type Refusal struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// Attach turns a connection into a window.
//
// Start lists session names to start if they are not running, in the order wanted; a
// running one is left alone wherever it started. Dir is the window's working
// directory, used only for sessions this attach starts. Width and Height are the
// window's pane size, used as the initial size of those sessions.
//
// ExpectSession is a session's local ID (the router's agent ID). When set, the daemon attaches this
// window to that one session only, and only if it is running now: otherwise it refuses with
// CodeNotRunning before the window is registered or given any input. It cannot be combined with
// Start. An empty value is the unchanged behaviour.
type Attach struct {
	Dir           string   `json:"dir"`
	Start         []string `json:"start,omitempty"`
	Width         int      `json:"width"`
	Height        int      `json:"height"`
	ExpectSession string   `json:"expect_session,omitempty"`
}

// Attached is the whole starting picture of a window. The session set is fixed for
// this window: a session started later needs a new attach.
type Attached struct {
	WindowID string `json:"window_id"`
	// Slots are the sessions, in a stable order (claude, then codex), including any
	// that have already ended.
	Slots []Slot `json:"slots"`
	// StartErrors are sessions this attach tried to start and could not. They have no
	// slot, because a session that failed to register never existed.
	StartErrors []StartError      `json:"start_errors,omitempty"`
	Snapshot    protocol.Snapshot `json:"snapshot"`
	Pending     int               `json:"pending"`
}

// Slot is one session as a window first sees it.
type Slot struct {
	Agent   protocol.AgentID `json:"agent"` // the session's ID; never reused for a replacement
	Name    string           `json:"name"`
	Address string           `json:"address"` // network name, name@host
	Pane    PaneState        `json:"pane"`
}

// StartError is a requested session that did not start.
type StartError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// Mouse tracking mode bits, the DEC private modes a child asked for. They have the
// same meaning as the pane package's: tracking is the highest of 1003, 1002, 1001,
// 1000 and 9 that is set, and 1006 is the SGR encoding.
const (
	MouseX10         uint32 = 1 << iota // 9
	MouseNormal                         // 1000
	MouseHighlight                      // 1001
	MouseButtonEvent                    // 1002
	MouseAnyEvent                       // 1003
	MouseSGR                            // 1006
)

// PaneState is the complete state of one session's pane. Every TypePane message is
// complete, so a window that sees only the latest one is never missing a field.
//
// Merging, for a window: Rev is one counter per session that increases on every holder
// or ended change. Holder and YouHold apply when Rev is at least the highest Rev a
// holder-carrying message has already been applied at. Ended (with ExitCode and
// StartError) is a latch: once a session ID has ended it never reverts, so an Ended
// from any message applies whatever its Rev, and an Ownership that arrives after it is
// ignored for an ended session. A SessionEnded therefore cannot be discarded because a
// newer Ownership arrived first.
type PaneState struct {
	Agent protocol.AgentID `json:"agent"`
	// Rev increases whenever the holder or the ended state changes. A window keeps the
	// highest Rev it has seen for the ownership fields, whichever message carried it.
	Rev uint64 `json:"rev"`

	Frame      pane.Frame `json:"frame"`
	Cols       int        `json:"cols"`
	Rows       int        `json:"rows"`
	Mouse      uint32     `json:"mouse"`
	InputReady bool       `json:"input_ready"` // the transport can take input; says nothing about who may send it
	Err        string     `json:"err,omitempty"`

	Ended      bool   `json:"ended,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	StartError string `json:"start_error,omitempty"` // the harness could not be started

	// Holder is the window that holds input, or empty. YouHold is that window's answer
	// about itself, so a window never has to compare IDs.
	Holder   string `json:"holder,omitempty"`
	YouHold  bool   `json:"you_hold,omitempty"`
	Drafting bool   `json:"drafting,omitempty"` // a draft flag is set, perhaps by a window now gone
}

// Ownership is a reliable change of holder for one session.
type Ownership struct {
	Agent   protocol.AgentID `json:"agent"`
	Rev     uint64           `json:"rev"`
	Holder  string           `json:"holder,omitempty"`
	YouHold bool             `json:"you_hold,omitempty"`
}

// SessionEnded is the reliable notice that a session ended.
type SessionEnded struct {
	Agent    protocol.AgentID `json:"agent"`
	Rev      uint64           `json:"rev"`
	ExitCode *int             `json:"exit_code,omitempty"`
	Reason   string           `json:"reason,omitempty"`
}

// Pending is the router's count of messages that would be lost on quit. It is
// refreshed in the background, so it can be a moment behind.
type Pending struct {
	Count int `json:"count"`
}

// Bye is the last message the daemon sends on a connection it is closing.
type Bye struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// SessionRef names a session by ID. An operation addressed to an ID that is no longer
// current is refused and never reaches a replacement of the same name.
type SessionRef struct {
	Agent protocol.AgentID `json:"agent"`
}

// KeyEvent mirrors uv.Key explicitly: Code, ShiftedCode and BaseCode are runes, Mod is
// uv's modifier bitmask, and Text is the text the key produced.
type KeyEvent struct {
	Code        int32  `json:"code"`
	ShiftedCode int32  `json:"shifted_code,omitempty"`
	BaseCode    int32  `json:"base_code,omitempty"`
	Text        string `json:"text,omitempty"`
	Mod         int    `json:"mod,omitempty"`
	IsRepeat    bool   `json:"is_repeat,omitempty"`
}

// Key is one key press for one session.
type Key struct {
	Agent protocol.AgentID `json:"agent"`
	Key   KeyEvent         `json:"key"`
}

// Paste is bracketed paste text. It is at most MaxPasteBytes.
type Paste struct {
	Agent protocol.AgentID `json:"agent"`
	Text  string           `json:"text"`
}

// Mouse kinds.
const (
	MouseClick   = "click"
	MouseRelease = "release"
	MouseWheel   = "wheel"
	MouseMotion  = "motion"
)

// Mouse is one mouse event in pane-relative cell coordinates. Button and Mod are uv's
// integer values.
type Mouse struct {
	Agent  protocol.AgentID `json:"agent"`
	Kind   string           `json:"kind"`
	X      int              `json:"x"`
	Y      int              `json:"y"`
	Button int              `json:"button,omitempty"`
	Mod    int              `json:"mod,omitempty"`
}

// Resize asks for a pane size. Request is true for tui.Pane.RequestResize, which
// returns before the size is applied, and false for Resize. Only the holder's size
// is applied.
type Resize struct {
	Agent   protocol.AgentID `json:"agent"`
	Width   int              `json:"width"`
	Height  int              `json:"height"`
	Request bool             `json:"request,omitempty"`
}

// Draft is tui.Router.UserDraft.
type Draft struct {
	Agent    protocol.AgentID `json:"agent"`
	Drafting bool             `json:"drafting"`
}

// Ack answers one request. OK means the operation completed: for keys, paste and
// mouse, the bytes reached the transport. Written is the byte count for those.
// Ownership is set only on an answer to take_input, and is authoritative.
type Ack struct {
	OK        bool       `json:"ok"`
	Error     *Error     `json:"error,omitempty"`
	Written   int        `json:"written,omitempty"`
	Ownership *Ownership `json:"ownership,omitempty"`
}

// Query kinds, one per read-only router method.
const (
	QueryHistory       = "history"        // Args: protocol.HistoryQuery
	QueryRuns          = "runs"           // Args: Limit
	QueryMessageDetail = "message_detail" // Args: ID
	QuerySends         = "sends"          // Args: Limit
	QuerySendDetail    = "send_detail"    // Args: ID
	QueryPending       = "pending"
	QuerySnapshot      = "snapshot"
	QueryNetwork       = "network" // Network: NetworkRequest; the answer is Result.Network
	QueryEnter         = "enter"   // Enter: EnterRequest; the answer is Result.Plan. Window connections only.
	QueryCard          = "card"    // no arguments; the answer is Result.Card. Needs no gateway.
	QueryGraph         = "graph"   // Graph: GraphRequest; the answer is Result.Graph. Window connections only.
)

// Query is one read. Only the fields its Kind uses are set.
type Query struct {
	Kind    string                 `json:"kind"`
	History *protocol.HistoryQuery `json:"history,omitempty"`
	Limit   int                    `json:"limit,omitempty"`
	ID      string                 `json:"id,omitempty"`
	Network *NetworkRequest        `json:"network,omitempty"`
	Enter   *EnterRequest          `json:"enter,omitempty"`
	Graph   *GraphRequest          `json:"graph,omitempty"`
}

// Result answers a query or a status request. Exactly one of Error and the payload
// field for the kind is set; the others are empty.
type Result struct {
	Error         *Error                  `json:"error,omitempty"`
	History       []protocol.MessageView  `json:"history,omitempty"`
	Runs          []protocol.RunView      `json:"runs,omitempty"`
	MessageDetail *protocol.MessageDetail `json:"message_detail,omitempty"`
	Sends         []protocol.SendView     `json:"sends,omitempty"`
	SendDetail    *protocol.SendDetail    `json:"send_detail,omitempty"`
	Pending       *int                    `json:"pending,omitempty"`
	Snapshot      *protocol.Snapshot      `json:"snapshot,omitempty"`
	Status        *Status                 `json:"status,omitempty"`
	// Network answers a network query or request. NetworkVersion is the daemon's own change
	// counter for it, and is what a caller sends back as NetworkRequest.Since.
	Network        *protocol.NetworkView `json:"network,omitempty"`
	NetworkVersion uint64                `json:"network_version,omitempty"`
	// Plan answers an enter query.
	Plan *EnterPlan `json:"plan,omitempty"`
	// Card answers a card query or operation. For a card_conflict refusal, Error and Card are both
	// set: Card is the current view the caller should refresh from.
	Card *protocol.CardView `json:"card,omitempty"`
	// Graph answers a graph query. GraphVersion is the daemon's own change counter for it, and is
	// what a caller sends back as GraphRequest.Since.
	Graph        *protocol.GraphView `json:"graph,omitempty"`
	GraphVersion uint64              `json:"graph_version,omitempty"`
}

// Limits for a network request. They sit under the 10 s query deadline so a window's wait always
// ends with an answer rather than a timeout.
const (
	MaxNetworkWaitMS  = 8000
	MaxNetworkFirstMS = 8000
)

// NetworkRequest asks the daemon for the network answer.
//
// Asking registers the caller as a consumer: the daemon fetches from the gateway only while at
// least one consumer exists. A window is a consumer from its first network query until it closes; a
// control request (`rtk daemon network`) is a consumer for the length of the request.
//
// With no Since the answer is immediate. With Since equal to the daemon's current version, the
// daemon waits up to WaitMS for a change and then answers with the same version and the current
// body. FirstWaitMS bounds how long a caller with no snapshot yet waits for the first fetch, so
// that a one-shot caller gets what has arrived by then rather than a null it caused itself.
type NetworkRequest struct {
	Since       uint64 `json:"since,omitempty"`
	WaitMS      int    `json:"wait_ms,omitempty"`
	FirstWaitMS int    `json:"first_wait_ms,omitempty"`
}

// Limits for a graph request, the same as the network's and for the same reason.
const (
	MaxGraphWaitMS  = 8000
	MaxGraphFirstMS = 8000
)

// GraphRequest asks the daemon for the graph answer. Its fields mean what NetworkRequest's do, and
// the window is a graph consumer from a successful attach until it closes, like the network
// consumer: asking does not register anything new.
type GraphRequest struct {
	Since       uint64 `json:"since,omitempty"`
	WaitMS      int    `json:"wait_ms,omitempty"`
	FirstWaitMS int    `json:"first_wait_ms,omitempty"`
}

// Card operations.
const (
	CardOpGet      = "get"      // answer the current view; changes nothing
	CardOpEdit     = "edit"     // apply Edit to the working copy
	CardOpValidate = "validate" // re-validate the working copy; changes nothing but Problems
	CardOpPublish  = "publish"  // validate, then publish the working copy as a new revision
	CardOpHistory  = "history"  // answer the view; with Revision set, also View.Preview = that revision in full (a rollback preview)
	CardOpRollback = "rollback" // copy revision Revision into the working copy and publish it as a new revision
	CardOpRescan   = "rescan"   // scan this host's skill locations again
)

// CardOp is one operation on this host's own card. Only the owner's own daemon serves it, on its
// Unix socket; there is no remote write path.
//
// BaseToken is the CardView.Token the caller last saw. Edit, publish and rollback are refused with
// card_conflict when it is stale. Get, validate, history and rescan ignore it.
//
// Edit.Skills replaces the declared skills. An entry with FoundLabel takes fresh evidence from the scan;
// an entry without one keeps the evidence the working copy already holds for the same ID, so an edit that
// leaves a skill alone cannot lose it. To drop a skill's evidence, remove the skill or give it a new ID.
type CardOp struct {
	Op        string    `json:"op"`
	BaseToken string    `json:"base_token,omitempty"`
	Edit      *CardEdit `json:"edit,omitempty"`
	Revision  int       `json:"revision,omitempty"` // for rollback
}

// CardEdit changes the working copy. A nil field is left alone. Skills, when set, REPLACES the
// declared skills: each entry may name the found skill it is declared from (FoundLabel), which is
// how evidence is attached.
type CardEdit struct {
	Name        *string          `json:"name,omitempty"`
	Description *string          `json:"description,omitempty"`
	Skills      *[]CardSkillEdit `json:"skills,omitempty"`
}

// CardSkillEdit is one declared skill as the owner wrote it.
type CardSkillEdit struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	FoundLabel  string   `json:"found_label,omitempty"`
}

// StopSession names the session `rtk daemon stop --session` ends.
type StopSession struct {
	Name string `json:"name"`
}

// Status is what `rtk daemon status` prints.
type Status struct {
	PID       int       `json:"pid"`
	Build     string    `json:"build"`
	StartedAt time.Time `json:"started_at"`
	LogPath   string    `json:"log_path"`
	Windows   int       `json:"windows"`
	// Link is the gateway link's state, or empty when the daemon has not reported one.
	Link    string `json:"link,omitempty"`
	LinkErr string `json:"link_err,omitempty"`
	// Card is whether this daemon serves its host card, and where. Omitted by a daemon that
	// predates it.
	Card     *protocol.CardListener `json:"card,omitempty"`
	Sessions []SessionStatus        `json:"sessions"`
}

// SessionStatus is one running session in a Status.
type SessionStatus struct {
	Name      string    `json:"name"`
	Agent     string    `json:"agent"`
	Address   string    `json:"address"`
	StartedAt time.Time `json:"started_at"`
	Holder    string    `json:"holder,omitempty"`
	Drafting  bool      `json:"drafting,omitempty"`
}

// EnterRequest asks the daemon what pressing Enter on a Network row should do.
//
// Exactly one of two forms: a row from the gateway snapshot (Host and SessionID, the gateway's own
// session ID), or a local row (LocalID, the daemon's own ID for one of its sessions; the local
// section exists with no gateway, so it has no gateway session ID). Anything else is refused. The
// window never builds a command: it runs the plan's Argv exactly, or shows its text.
type EnterRequest struct {
	Host      string `json:"host,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	LocalID   string `json:"local_id,omitempty"`
}

// Plan kinds.
const (
	EnterLocal   = "local"   // the row is on this host: focus it (InWindow) or say to reattach
	EnterSSH     = "ssh"     // run Argv
	EnterManual  = "manual"  // nothing to run: Manual is the command to run on that host yourself, Reason says why
	EnterRefused = "refused" // nothing is possible: Reason says why. Never an attach without an expected ID.
)

// EnterPlan is the daemon's answer to an enter query.
//
// Every string that came from another machine (names, reasons) is cleaned of control characters
// before it is here. Argv is set only for EnterSSH and is exact: the caller runs it without a shell
// and adds nothing.
type EnterPlan struct {
	Kind    string `json:"kind"`
	Host    string `json:"host,omitempty"`
	LocalID string `json:"local_id,omitempty"`
	// InWindow is meaningful only for EnterLocal: whether this window has the session (a window's
	// sessions are fixed when it attaches).
	InWindow bool     `json:"in_window,omitempty"`
	Argv     []string `json:"argv,omitempty"`
	// Display is Argv as one shell-quoted line for the person to read; never run it.
	Display string `json:"display,omitempty"`
	Note    string `json:"note,omitempty"`
	// Manual is the by-hand command: for EnterManual the command to run on that host, for EnterSSH
	// the fallback if ssh fails.
	Manual string `json:"manual,omitempty"`
	Reason string `json:"reason,omitempty"`
}
