// Package protocol holds the value types shared across the seams described in
// docs/implementation/interfaces.md. It holds no behaviour and imports nothing
// from this module.
//
// Untested: under GR-3 (docs/design/ground-rules.md) nothing built here has been run.
package protocol

import "time"

// AgentID is the opaque address of one agent in one run. Display names resolve
// to it; only the server and the UI deal in display names.
type AgentID string

// AgentState is the router's view of an agent, driven by hook events.
type AgentState string

const (
	AgentStarting AgentState = "starting"
	AgentIdle     AgentState = "idle"
	AgentBusy     AgentState = "busy"
	AgentNeedsYou AgentState = "needs_you"
	AgentUnknown  AgentState = "unknown"
	AgentExited   AgentState = "exited"
)

// MessageState is a message's delivery status (design section 8).
type MessageState string

const (
	MessageQueued      MessageState = "queued"
	MessageHeld        MessageState = "held"
	MessageDelivering  MessageState = "delivering"
	MessagePasted      MessageState = "pasted"
	MessageAccepted    MessageState = "accepted"
	MessageUnconfirmed MessageState = "unconfirmed"
	MessageFailed      MessageState = "failed"
	MessageInterrupted MessageState = "interrupted"
)

// HookKind is a harness hook event that drives agent state (design section 7).
type HookKind string

const (
	HookSessionStart      HookKind = "SessionStart"
	HookUserPromptSubmit  HookKind = "UserPromptSubmit"
	HookStop              HookKind = "Stop"
	HookPermissionRequest HookKind = "PermissionRequest"
	HookPostToolUse       HookKind = "PostToolUse"
)

// HookEvent is one hook report. Prompt is the submitted text when the harness
// provides it; whether each harness does is unverified. SessionID is the
// harness's own session ID from the hook payload, and Source is the
// SessionStart source. Either may be empty.
type HookEvent struct {
	Kind      HookKind
	Prompt    string
	SessionID string
	Source    string
	At        time.Time
}

// LaunchSpec is how to start one harness. Env is the complete child
// environment, never inherited implicitly. SessionID is the harness session ID
// chosen at launch; empty means the router binds it from the harness's first
// SessionStart.
type LaunchSpec struct {
	ID          AgentID
	DisplayName string
	Harness     string
	// HarnessVersion is what the harness reported, or empty if unknown.
	HarnessVersion string
	SessionID      string
	Argv           []string
	Env            []string
	Dir            string
}

// AgentView is an agent as the UI shows it.
type AgentView struct {
	ID          AgentID
	DisplayName string
	// Address is the network name (name@host) in a connected run, and empty in
	// local mode. DisplayName stays the local name.
	Address  string
	State    AgentState
	Held     int
	ExitCode *int
	Err      string
}

// MessageView is a message as the UI shows it. Preview is already sanitized.
type MessageView struct {
	ID      string
	RunID   string
	From    string
	To      string
	State   MessageState
	Reason  string
	Preview string
	At      time.Time
}

// BudgetView is the relay budget (invariant 10).
type BudgetView struct {
	Used  int
	Limit int
}

// Snapshot is the full state for first paint and for resync.
//
// Connection and Sends exist only in a connected run (rtk runtime): in plain
// local mode Connection is nil and Sends is empty, so the UI shows what it
// always showed.
type Snapshot struct {
	Agents   []AgentView
	Messages []MessageView
	Budget   BudgetView
	Banner   string

	// Runtime marks a connected run (`rtk runtime`). It is false in plain local
	// mode, and then Connection is nil and Sends is empty.
	Runtime    bool
	Connection *ConnectionView
	Sends      []SendView
}

// Event is one router update for the UI. Resync means events were dropped and
// the UI must take a new Snapshot.
type Event struct {
	Agent   *AgentView
	Message *MessageView
	Budget  *BudgetView
	Banner  string
	Resync  bool

	// Connected runs only; always nil in local mode.
	Connection *ConnectionView
	Send       *SendView
}

// RunView is one run as the F3 log lists it.
type RunView struct {
	ID          string
	StartedAt   time.Time
	EndedAt     *time.Time
	EndReason   string
	Interrupted int
}

// MessageEventView is one recorded state change of a message.
type MessageEventView struct {
	At     time.Time
	From   MessageState
	To     MessageState
	Reason string
}

// Messages here (MessageView, MessageDetail) are the ones this host observed and
// decided: local sends in local mode, and in a connected run the messages
// received from the gateway, whose states this host's own gate and hooks set.
// What another host reports about a message this host sent is a SendView, never a
// MessageView.

// MessageDetail is one stored message in full: its whole body, sanitized for
// display, and its state history. There is no resend, so this is how a user
// recovers an interrupted message (design section 9).
type MessageDetail struct {
	MessageView
	ReplyTo string
	Body    string
	Events  []MessageEventView
}

// HistoryQuery selects messages for the F3 log. An empty RunID means the
// current run.
type HistoryQuery struct {
	RunID string
	Limit int
}
