package a2a

// The SDK executor. It is deliberately "submit and return": it checks the message, announces the task,
// hands the text to the target through the Dispatcher, and ends. Every later transition is applied by our
// service from an explicit report or a system event, never by the executor and never because a turn ended.
//
// UNTESTED.

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// Executor implements a2asrv.AgentExecutor.
type Executor struct {
	Svc *Service
}

var _ a2asrv.AgentExecutor = (*Executor)(nil)

// Execute implements a2asrv.AgentExecutor.
func (e *Executor) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		caller, ok := CallerFrom(ctx)
		target, hasTarget := TargetFrom(ctx)
		if !ok || !hasTarget {
			yield(nil, a2a.ErrUnauthenticated)
			return
		}
		text, err := messageText(ec.Message)
		if err != nil {
			yield(nil, err)
			return
		}

		if ec.StoredTask != nil {
			e.continueTask(ctx, ec, caller, target, text, yield)
			return
		}

		// A new task. An offline target fails here with nothing stored (contract rule 6).
		if err := e.Svc.Dispatcher.Available(ctx, target); err != nil {
			yield(nil, unavailable())
			return
		}
		submitted := a2a.NewSubmittedTask(ec, ec.Message)
		if !yield(submitted, nil) {
			return
		}
		record := Task{ID: string(submitted.ID), ContextID: submitted.ContextID, Owner: caller, Target: target, State: StateSubmitted}
		if err := e.Svc.Dispatcher.Dispatch(ctx, Dispatch{Task: record, From: caller, Text: text}); err != nil {
			e.undelivered(ec, err, yield)
		}
	}
}

// continueTask delivers an answer on an existing task. Only a party can reach here (the store hides other
// tasks), and a final task takes no more input.
func (e *Executor) continueTask(ctx context.Context, ec *a2asrv.ExecutorContext, caller Principal, target Target, text string, yield func(a2a.Event, error) bool) {
	if ec.StoredTask.Status.State.Terminal() {
		yield(nil, fmt.Errorf("%w: the task is already final", a2a.ErrInvalidRequest))
		return
	}
	record, found, err := e.Svc.Store.GetTask(ctx, string(ec.TaskID))
	if err != nil || !found {
		yield(nil, a2a.ErrTaskNotFound)
		return
	}
	// The task's target is fixed when it is created: an answer goes to the same participant, never to
	// whatever the route now names (a replacement session is a different participant).
	if record.Target.Key() != target.Key() {
		yield(nil, a2a.ErrTaskNotFound)
		return
	}
	if err := e.Svc.Dispatcher.Available(ctx, record.Target); err != nil {
		yield(nil, unavailable())
		return
	}
	if !yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateSubmitted, nil), nil) {
		return
	}
	if err := e.Svc.Dispatcher.Dispatch(ctx, Dispatch{Task: record, From: caller, Text: text, Continuation: true}); err != nil {
		e.undelivered(ec, err, yield)
	}
}

// undelivered handles a failed dispatch. Only a dispatch KNOWN not to have arrived ends the task (FAILED).
// An unknown outcome leaves the task SUBMITTED: uncertainty is not a state, and a target that did receive
// the text must still be able to report. The dispatcher records delivery=uncertain itself and nothing is
// retried.
func (e *Executor) undelivered(ec *a2asrv.ExecutorContext, err error, yield func(a2a.Event, error) bool) {
	if !errors.Is(err, ErrNotDelivered) {
		return
	}
	text := "The message could not be delivered to the target."
	var reason *NotDeliveredError
	if errors.As(err, &reason) && reason.Reason != "" {
		// The reason is a fixed sentence written by the gateway (never a secret, header or peer text).
		text = reason.Reason
	}
	message := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))
	yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, message), nil)
}

// Cancel implements a2asrv.AgentExecutor: a working task is not cancelable (contract rule 7). No CANCELED
// state is produced and nothing is interrupted.
func (e *Executor) Cancel(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) { yield(nil, a2a.ErrTaskNotCancelable) }
}

// unavailable is the JSON-RPC error for an offline or unknown target. The spec has no code for it, so it
// is the implementation-defined server error with a fixed sentence.
func unavailable() error {
	return fmt.Errorf("%w: the target is not available", a2a.ErrServerError)
}

// messageText joins the text parts of a message. A non-text part is a content-type refusal.
func messageText(m *a2a.Message) (string, error) {
	if m == nil {
		return "", a2a.ErrInvalidParams
	}
	texts := make([]string, 0, len(m.Parts))
	for _, part := range m.Parts {
		if part == nil {
			continue
		}
		if _, ok := part.Content.(a2a.Text); !ok {
			return "", a2a.ErrUnsupportedContentType
		}
		texts = append(texts, part.Text())
	}
	return strings.Join(texts, "\n"), nil
}
