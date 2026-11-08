package a2a

// The task service: the only writer of task state outside the SDK's own store calls.
//
// Transitions are applied by reports and system events (contract section 7). A turn ending, a closed
// pane or a lost connection never completes a task: a lost connection is an observation. The service
// never retries a dispatch: an uncertain one is recorded as uncertain.
//
// UNTESTED.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Dispatch is one message handed to a target.
type Dispatch struct {
	Task Task
	// From is the authenticated principal; the delivered text carries its Origin line, never a body field.
	From Principal
	Text string
	// Continuation is true for an answer on an existing task (for a service, the remote task id is in Task).
	Continuation bool
}

// Dispatcher delivers a message to a target. The managed-session path (stream frames, readiness gates)
// is P2 and the external-service client is P4; both implement this. Available is asked before a task is
// created so an offline target fails with nothing stored (contract rule 6). Dispatch is never retried.
//
// Dispatch returns an error wrapping ErrNotDelivered ONLY when the message is known not to have arrived.
// Any other error means the outcome is unknown: the task stays SUBMITTED, the dispatcher itself records
// delivery=uncertain beside it, and nothing is replayed (rules 5, 6 and 8: uncertainty is not a TaskState,
// and a target that did get the text must still be able to report).
type Dispatcher interface {
	Available(ctx context.Context, target Target) error
	Dispatch(ctx context.Context, d Dispatch) error
}

// OwnerNotifier is told when a task owned by a managed session needs input or has finished. It reuses
// the message-delivery machinery (P2/P3); an offline owner gets an observation, not a replay.
type OwnerNotifier interface {
	TaskEvent(ctx context.Context, owner Principal, task Task)
}

// Service holds the rules.
type Service struct {
	Store      Store
	Dispatcher Dispatcher
	Notifier   OwnerNotifier
	Now        func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// a2aState and stateName convert between the stored names and the SDK's TaskState strings.
func a2aState(state string) a2a.TaskState { return a2a.TaskState("TASK_STATE_" + state) }

func stateName(state a2a.TaskState) string { return strings.TrimPrefix(string(state), "TASK_STATE_") }

// DecodeBody reads the stored A2A task.
func DecodeBody(t Task) (*a2a.Task, error) {
	var body a2a.Task
	if err := json.Unmarshal(t.Body, &body); err != nil {
		return nil, fmt.Errorf("a2a: stored task %s is unreadable: %w", t.ID, err)
	}
	return &body, nil
}

// IsParty reports whether a principal is the owner or the target of a task.
func IsParty(p Principal, t Task) bool {
	if !p.Valid() {
		return false
	}
	return p.Key() == t.Owner.Key() || isTarget(p, t.Target)
}

func isTarget(p Principal, t Target) bool {
	switch {
	case p.Kind == KindSession && t.Kind == TargetSession:
		return p.Session == t.ID
	case p.Kind == KindParticipant && t.Kind == TargetService:
		return p.Name == t.ID
	}
	return false
}

// reportable are the states a target may report.
func reportable(state string) bool {
	switch state {
	case StateInputRequired, StateCompleted, StateFailed:
		return true
	}
	return false
}

// Report applies an explicit report from the target: INPUT_REQUIRED, COMPLETED or FAILED with text and an
// optional result. Anyone who is not the target gets ErrNotFound, with no hint that the task exists.
func (s *Service) Report(ctx context.Context, reporter Principal, taskID, state, text, artifact string) (Task, error) {
	state = strings.ToUpper(strings.TrimSpace(state))
	if !reportable(state) {
		return Task{}, ErrBadState
	}
	return s.apply(ctx, taskID, func(t *Task, body *a2a.Task) (string, string, error) {
		if !isTarget(reporter, t.Target) {
			return "", "", ErrNotFound
		}
		if Final(t.State) {
			return "", "", ErrFinal
		}
		if artifact != "" {
			body.Artifacts = append(body.Artifacts, &a2a.Artifact{
				ID:    a2a.NewArtifactID(),
				Name:  "result",
				Parts: a2a.ContentParts{a2a.NewTextPart(artifact)},
			})
		}
		s.setStatus(body, state, text)
		return state, "", nil
	}, true)
}

// Accepted records that the harness accepted the input: WORKING, never earlier.
func (s *Service) Accepted(ctx context.Context, taskID string) (Task, error) {
	return s.apply(ctx, taskID, func(t *Task, body *a2a.Task) (string, string, error) {
		if Final(t.State) || t.State == StateInputRequired {
			return "", "", ErrFinal
		}
		s.setStatus(body, StateWorking, "")
		return StateWorking, "", nil
	}, false)
}

// SystemEvent finalises a task because of something that happened to a participant (session ended,
// participant revoked). It is a no-op on a task that is already final.
func (s *Service) SystemEvent(ctx context.Context, taskID, state, reason string) (Task, error) {
	if state != StateFailed && state != StateRejected {
		return Task{}, fmt.Errorf("a2a: a system event finalises as FAILED or REJECTED, not %q", state)
	}
	t, err := s.apply(ctx, taskID, func(t *Task, body *a2a.Task) (string, string, error) {
		if Final(t.State) {
			return "", "", ErrFinal
		}
		s.setStatus(body, state, reason)
		return state, reason, nil
	}, true)
	if errors.Is(err, ErrFinal) {
		return t, nil
	}
	return t, err
}

// Finish is Admin.Revoke's callback: it ends one task as FAILED with a reason.
func (s *Service) Finish(ctx context.Context, t Task, reason string) error {
	_, err := s.SystemEvent(ctx, t.ID, StateFailed, reason)
	return err
}

// SessionEnded fails every open task whose target is the session: "session ended" (rule 2). A reply
// afterwards is refused, never redirected to a replacement session.
func (s *Service) SessionEnded(ctx context.Context, sessionID string) error {
	open, err := s.Store.OpenTasksFor(ctx, Target{Kind: TargetSession, ID: sessionID})
	if err != nil {
		return err
	}
	for _, t := range open {
		if _, err := s.SystemEvent(ctx, t.ID, StateFailed, "session ended"); err != nil {
			return err
		}
	}
	return nil
}

// ConnectionLost records, on every open task for a target, that the participant's connection was lost.
// It is an observation only: the task is not finalised on a disconnect (rule 8).
func (s *Service) ConnectionLost(ctx context.Context, target Target) error {
	open, err := s.Store.OpenTasksFor(ctx, target)
	if err != nil {
		return err
	}
	for _, t := range open {
		if err := s.Store.AddObservation(ctx, Observation{TaskID: t.ID, Kind: ObsConnection, Value: "lost", At: s.now()}); err != nil {
			return err
		}
	}
	return nil
}

// Observe records a fact beside a task.
func (s *Service) Observe(ctx context.Context, taskID, kind, value string) error {
	return s.Store.AddObservation(ctx, Observation{TaskID: taskID, Kind: kind, Value: value, At: s.now()})
}

// setStatus writes a new status and, when text is given, an agent message into history.
func (s *Service) setStatus(body *a2a.Task, state, text string) {
	now := s.now().UTC()
	status := a2a.TaskStatus{State: a2aState(state), Timestamp: &now}
	if text != "" {
		message := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(text))
		message.TaskID, message.ContextID = body.ID, body.ContextID
		status.Message = message
		body.History = append(body.History, message)
	}
	body.Status = status
}

// apply loads a task, lets change edit its body, and saves it with the store's own version check. A
// conflict is retried a few times from a fresh read; the change function decides again each time.
// notify says whether an owner notification is sent for an INPUT_REQUIRED or final result.
func (s *Service) apply(ctx context.Context, taskID string, change func(*Task, *a2a.Task) (state, reason string, err error), notify bool) (Task, error) {
	for attempt := 0; attempt < 3; attempt++ {
		t, found, err := s.Store.GetTask(ctx, taskID)
		if err != nil {
			return Task{}, err
		}
		if !found {
			return Task{}, ErrNotFound
		}
		body, err := DecodeBody(t)
		if err != nil {
			return Task{}, err
		}
		state, reason, err := change(&t, body)
		if err != nil {
			return t, err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return t, fmt.Errorf("a2a: encode task: %w", err)
		}
		t.State, t.Reason, t.Body, t.UpdatedAt = state, reason, encoded, s.now()
		saved, err := s.Store.SaveTask(ctx, t)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return t, err
		}
		if notify && s.Notifier != nil && (state == StateInputRequired || Final(state)) {
			s.Notifier.TaskEvent(ctx, saved.Owner, saved)
		}
		return saved, nil
	}
	return Task{}, ErrConflict
}

// ApplyRemote mirrors an external service's own view of a task: its state, its status text and its
// artifacts. The service is the authority for its own task (contract section 7), but the mapping stays
// inside our states: CANCELED becomes FAILED with a reason (no CANCELED is produced), and states this
// build does not know change nothing and are recorded as an observation. A final task is never changed.
// It reports whether anything changed.
func (s *Service) ApplyRemote(ctx context.Context, taskID string, remote RemoteTask) (changed bool, err error) {
	state, reason := remote.State, ""
	switch state {
	case StateSubmitted, StateWorking, StateInputRequired, StateCompleted, StateFailed, StateRejected:
	case "CANCELED":
		state, reason = StateFailed, "the service canceled the task"
	default:
		_ = s.Observe(ctx, taskID, ObsRemoteState, "unmapped state "+boundRunesBytes(CleanText(state), 40))
		state = ""
	}

	_, err = s.apply(ctx, taskID, func(t *Task, body *a2a.Task) (string, string, error) {
		if Final(t.State) {
			return "", "", ErrFinal
		}
		next := t.State
		if state != "" {
			next = state
		}
		results := make([]*a2a.Artifact, 0, len(remote.Results))
		for i, text := range remote.Results {
			results = append(results, &a2a.Artifact{
				ID: a2a.ArtifactID(fmt.Sprintf("result-%d", i+1)), Name: "result", Parts: a2a.ContentParts{a2a.NewTextPart(text)},
			})
		}
		sameText := body.Status.Message != nil && len(body.Status.Message.Parts) == 1 && body.Status.Message.Parts[0].Text() == remote.Text
		if next == t.State && (remote.Text == "" || sameText) && sameResults(body.Artifacts, results) && t.RemoteTaskID == remote.ID {
			return "", "", errNoChange
		}
		t.RemoteTaskID = remote.ID
		body.Artifacts = results
		s.setStatus(body, next, remote.Text)
		return next, reason, nil
	}, true)
	switch {
	case errors.Is(err, errNoChange), errors.Is(err, ErrFinal):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

var errNoChange = errors.New("a2a: nothing changed")

func sameResults(have, want []*a2a.Artifact) bool {
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if len(have[i].Parts) != 1 || len(want[i].Parts) != 1 || have[i].Parts[0].Text() != want[i].Parts[0].Text() {
			return false
		}
	}
	return true
}
