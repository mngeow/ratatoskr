package a2a

// The storage seam. Everything in this package that needs durable state talks to a Store; the SQLite
// implementation lives in sqlitestore.go and takes a *sql.DB from the gateway. Tests and the admin CLI
// use the same interface.
//
// Contract rules the implementation must keep (section 2):
//   - retention is until the owner deletes: no expiry, no purge, nothing here removes a task except
//     DeleteTasks;
//   - nothing references the gateway's hosts or sessions tables, so unpairing a host never removes a
//     task;
//   - a credential is stored as a hash only.
//
// UNTESTED.

import (
	"context"
	"time"
)

// Link kinds for LinkMessage.
const (
	DispatchKind = "dispatch" // the message that delivers a task (or an answer) to a managed target
	WakeKind     = "wake"     // the message that tells a managed owner a task needs input or finished
)

// ListFilter selects tasks for ListTasks. Caller scoping is not optional: the store returns only tasks
// where Caller is the owner or the target, so a handler cannot forget it.
type ListFilter struct {
	Caller    Principal
	ContextID string
	State     string
	// PageSize is the page size (the handler clamps it); Cursor is the opaque value a previous page
	// returned, "" for the first page.
	PageSize int
	Cursor   string
}

// DeleteFilter selects tasks for the owner's delete command. Exactly one field is set.
type DeleteFilter struct {
	TaskID      string
	AllFinal    bool
	Participant string
}

// Store is the durable state of A2A participants and tasks.
type Store interface {
	// CreateParticipant enrols a participant with the hash of its credential. ErrExists if the name is
	// taken (revoked names stay taken).
	CreateParticipant(ctx context.Context, p Participant, credentialHash string) error
	// GetParticipant reads one participant by id.
	GetParticipant(ctx context.Context, id string) (Participant, bool, error)
	// ParticipantByCredential finds the participant whose credential hash this is. A revoked participant
	// is returned (with RevokedAt set) so the caller can refuse it without saying which part failed.
	ParticipantByCredential(ctx context.Context, credentialHash string) (Participant, bool, error)
	ListParticipants(ctx context.Context) ([]Participant, error)
	// SetRegistration stores the destination URL, pin, opt-in, validated card and outbound authentication
	// of a participant. For AuthBearer the secret REPLACES any earlier one (rotation); for AuthNone it is
	// cleared. The secret is never returned by any other method.
	SetRegistration(ctx context.Context, id, url, fingerprint string, insecureHTTP bool, cardJSON, auth, secret string) error
	// OutboundSecret returns the outbound secret of a registration. It exists for the outbound-client
	// factory ONLY; nothing that lists, prints, logs or serves may call it.
	OutboundSecret(ctx context.Context, id string) (string, error)
	// Revoke marks a participant revoked; it reports whether it changed anything.
	Revoke(ctx context.Context, id string, at time.Time) (bool, error)

	// CreateTask stores a new task. ErrExists if the id is taken.
	CreateTask(ctx context.Context, t Task) error
	GetTask(ctx context.Context, id string) (Task, bool, error)
	// SaveTask replaces a task if its stored Version still equals t.Version, then advances it; otherwise
	// ErrConflict. The caller re-reads and decides.
	SaveTask(ctx context.Context, t Task) (Task, error)
	// ListTasks is caller-scoped and cursor-paginated, newest status first; next is "" on the last page.
	ListTasks(ctx context.Context, f ListFilter) (tasks []Task, next string, err error)
	// CountTasks is the number of tasks the caller can see that match the filter, BEFORE pagination (A2A
	// spec 3.1.4 totalSize). The filter's PageSize and Cursor are ignored.
	CountTasks(ctx context.Context, f ListFilter) (int, error)
	// OpenTasksFor lists the non-final tasks whose target is this one (system events finalise them).
	OpenTasksFor(ctx context.Context, target Target) ([]Task, error)
	// OpenServiceTasks lists every non-final task whose target is an external service (the mirror polls these).
	OpenServiceTasks(ctx context.Context) ([]Task, error)
	// OpenTasksOwnedBy lists the non-final tasks this principal created.
	OpenTasksOwnedBy(ctx context.Context, owner Principal) ([]Task, error)
	// DeleteTasks removes tasks and their observations; it returns how many tasks went.
	DeleteTasks(ctx context.Context, f DeleteFilter) (int, error)

	// LinkMessage records that a gateway ledger message carries a task: kind is DispatchKind or WakeKind.
	LinkMessage(ctx context.Context, messageID, taskID, kind string, at time.Time) error
	// TaskForMessage finds the task a ledger message belongs to.
	TaskForMessage(ctx context.Context, messageID string) (taskID, kind string, found bool, err error)
	// MessagesFor lists the ledger message ids linked to the tasks a DeleteFilter would remove. Call it
	// BEFORE DeleteTasks, which drops the links.
	MessagesFor(ctx context.Context, f DeleteFilter) ([]string, error)

	// AddObservation records a fact beside a task; Observations lists them oldest first.
	AddObservation(ctx context.Context, o Observation) error
	Observations(ctx context.Context, taskID string) ([]Observation, error)
}
