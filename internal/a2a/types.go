// Package a2a is the gateway's A2A v1.0 participant layer: principals, enrolment, tasks and their
// storage, the JSON-RPC handler built on the A2A Go SDK, and the outbound client interface.
//
// The package imports nothing from the rest of this repository. The gateway wires it in (routes, the
// SQLite handle, the dial-ruled HTTP client for external services), so the card dial rules stay where
// they are and there is no import cycle.
//
// Task state is evidence-grounded (contract section 2, rule 5): COMPLETED, FAILED, REJECTED and
// INPUT_REQUIRED come only from an explicit report, the external service's own state, or a system
// event. A turn ending is never completion, and a lost connection is an observation, not a state.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run: only gofmt, go vet and
// go build have been applied. No request has been served and no migration has been run.
package a2a

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Principal kinds.
const (
	KindSession     = "session"     // a managed session, authenticated by its paired host's credential
	KindParticipant = "participant" // an enrolled external service, authenticated by its own credential
)

// Principal is the authenticated party behind a request. It is derived by the gateway from the
// credential, never from a body (contract section 2, rule 4).
type Principal struct {
	Kind string
	// Host and Session are set for a session principal: the paired host and the exact session id.
	Host    string
	Session string
	// Name is set for a participant principal: its enrolment id.
	Name string
}

// Key is the stable text form used in storage and scoping: "session:<host>/<id>" or
// "participant:<name>". It is not secret.
func (p Principal) Key() string {
	if p.Kind == KindSession {
		return KindSession + ":" + p.Host + "/" + p.Session
	}
	return KindParticipant + ":" + p.Name
}

// Valid reports whether the principal names someone.
func (p Principal) Valid() bool {
	switch p.Kind {
	case KindSession:
		return p.Host != "" && p.Session != ""
	case KindParticipant:
		return p.Name != ""
	}
	return false
}

// Origin is the line the gateway writes in front of delivered text. It names the authenticated principal
// only; nothing in a request body can change it.
func (p Principal) Origin() string {
	if p.Kind == KindSession {
		return p.Session + "@" + p.Host
	}
	return p.Name
}

// Target kinds.
const (
	TargetSession = "session" // a managed session, addressed by its exact session id
	TargetService = "service" // an externally managed A2A service the owner registered
)

// Target is who a task is for.
type Target struct {
	Kind string
	// ID is the session id for a session target, or the participant id for a service.
	ID string
	// Host is the session's host; empty for a service.
	Host string
}

// Key is the stable text form: "session:<id>" or "service:<id>".
func (t Target) Key() string { return t.Kind + ":" + t.ID }

// Participant is one enrolled external principal and, when registered, the service it runs.
type Participant struct {
	// ID is the stable name the owner enrolled, lowercase.
	ID string
	// URL is the registered A2A endpoint, or "" for a pure ingress client with no URL.
	URL string
	// Fingerprint is the pinned certificate (sha256:...) for an https URL, or "".
	Fingerprint string
	// InsecureHTTP records the owner's explicit opt-in for plain HTTP to a LAN address.
	InsecureHTTP bool
	// Auth is how the gateway authenticates to the service: AuthBearer (a per-registration secret, kept only
	// in the database and read only by the outbound-client factory) or AuthNone (the explicit demo mode).
	// The secret itself is deliberately NOT a field here: this struct is passed to list output and logs.
	Auth string
	// CardJSON is the strictly validated card fetched at registration, or "".
	CardJSON  string
	CreatedAt time.Time
	// RevokedAt is zero while the participant is active.
	RevokedAt time.Time
}

// Revoked reports whether the participant has been revoked.
func (p Participant) Revoked() bool { return !p.RevokedAt.IsZero() }

// Outbound authentication modes of a registration.
const (
	AuthBearer = "bearer"
	AuthNone   = "none"
)

// CardAuth is what a service's card declares about authentication, as far as this gateway can satisfy it.
type CardAuth int

const (
	// CardAuthNone: the card declares no security.
	CardAuthNone CardAuth = iota
	// CardAuthBearer: the card requires HTTP Bearer, which the gateway can send.
	CardAuthBearer
	// CardAuthUnsupported: the card requires something the gateway cannot satisfy (OAuth2, OpenID Connect,
	// mutual TLS, an API key, or a requirement it cannot meet).
	CardAuthUnsupported
)

// Task states stored by the gateway. They are the A2A TaskState names without the TASK_STATE_ prefix,
// kept as plain strings so the store does not depend on the SDK.
const (
	StateSubmitted     = "SUBMITTED"
	StateWorking       = "WORKING"
	StateInputRequired = "INPUT_REQUIRED"
	StateCompleted     = "COMPLETED"
	StateFailed        = "FAILED"
	StateRejected      = "REJECTED"
)

// Final reports whether a stored state is final. CANCELED is not produced in the first delivery.
func Final(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateRejected:
		return true
	}
	return false
}

// Task is one stored task. Body is the complete A2A task as JSON, the form returned to clients; the other
// fields are what scoping and queries need.
type Task struct {
	ID        string
	ContextID string
	// Owner is the creating principal; Target is the participant it is for. Both are "party" for scoping.
	Owner  Principal
	Target Target
	State  string
	// Reason is a short sentence for FAILED/REJECTED system events ("session ended"); never a body.
	Reason string
	// RemoteTaskID is the task id at an external service, when Target is a service.
	RemoteTaskID string
	// Body is the A2A task JSON (status, history, artifacts, metadata).
	Body []byte
	// Version is the store's own counter, advanced on every save; it is not the SDK's TaskVersion.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Observation kinds. They sit beside a task, never inside its state (contract section 7).
const (
	ObsDelivery    = "delivery"               // stored | pasted | accepted | uncertain
	ObsConnection  = "participant_connection" // connected | lost
	ObsReconciled  = "reconciled"             // by an explicit report or an owner action
	ObsOwnerEvent  = "owner_event"            // a task event recorded for an owner that was offline
	ObsRemoteState = "remote_state"           // the last state mirrored from an external service
	ObsAuth        = "auth"                   // refused: the service answered 401/403 to the gateway's credential
)

// Observation is one recorded fact about a task.
type Observation struct {
	TaskID string
	Kind   string
	Value  string
	At     time.Time
}

// Errors the layer returns. The handler maps them to A2A errors; none carries a body or a credential.
var (
	ErrNotFound        = errors.New("a2a: not found")
	ErrExists          = errors.New("a2a: already exists")
	ErrConflict        = errors.New("a2a: the record changed meanwhile")
	ErrRevoked         = errors.New("a2a: the participant is revoked")
	ErrBadName         = errors.New("a2a: invalid participant name")
	ErrBadURL          = errors.New("a2a: invalid participant URL")
	ErrNeedsOptIn      = errors.New("a2a: a LAN http URL needs --insecure-http")
	ErrNoAuthDeclared  = errors.New("a2a: the service's card declares no authentication; register with --no-auth for a demonstration")
	ErrUnsupportedAuth = errors.New("a2a: the service's card requires an authentication scheme the gateway cannot use (it can send HTTP Bearer only)")
	ErrAuthMismatch    = errors.New("a2a: --no-auth cannot be used: the service's card declares Bearer authentication")
	// ErrServiceAuth: the service answered 401 or 403 to the gateway's credential. It is a definite refusal.
	ErrServiceAuth   = errors.New("a2a: the service refused the gateway's credential")
	ErrCardInvalid   = errors.New("a2a: the service's card is not valid")
	ErrNotAParty     = errors.New("a2a: not a party to this task")
	ErrFinal         = errors.New("a2a: the task is already final")
	ErrBadState      = errors.New("a2a: a report may say input_required, completed or failed")
	ErrTargetOffline = errors.New("a2a: the target is not available")
	// ErrNotDelivered marks a dispatch that is KNOWN not to have reached the target (refused before any
	// write). Any other dispatch error means the outcome is unknown.
	ErrNotDelivered = errors.New("a2a: the message did not reach the target")
	// ErrUncertain marks a send whose outcome is unknown: the request may have been written but no valid,
	// complete reply was read. It is never retried and never makes a task FAILED.
	ErrUncertain = errors.New("a2a: the outcome of the send is unknown")
)

var participantNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

var hex32Pattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidParticipantName reports whether a name may be enrolled. A 32-character hex string is refused so a
// participant id can never be mistaken for a session id on the shared /a2a/agents/{id} route.
func ValidParticipantName(name string) bool {
	return participantNamePattern.MatchString(name) && !hex32Pattern.MatchString(name) && !strings.HasPrefix(name, "session")
}

// NotDeliveredError is ErrNotDelivered with a reason that is safe to show to the task's owner (a fixed
// sentence, never a secret, a header or a peer's text).
type NotDeliveredError struct{ Reason string }

func (e *NotDeliveredError) Error() string { return ErrNotDelivered.Error() + ": " + e.Reason }

// Is makes errors.Is(err, ErrNotDelivered) true.
func (e *NotDeliveredError) Is(target error) bool { return target == ErrNotDelivered }

// NotDelivered builds a NotDeliveredError.
func NotDelivered(reason string) error { return &NotDeliveredError{Reason: reason} }
