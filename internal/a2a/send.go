package a2a

// SendMessage without the SDK's execution manager.
//
// The SDK runs an executor under an in-memory execution that ends only when the executor yields a message,
// a terminal state or INPUT_REQUIRED. Ours is "submit and return": every later transition is written by the
// Service straight to the store, so the SDK execution never ended and its per-task lock refused the next
// message on the task with -32004. This handler sits inside the SDK's InterceptedHandler (so the version
// check and the card enforcer still run) and runs the executor itself, synchronously, applying each event
// to the store. Nothing is registered anywhere, so there is nothing to leak or to release.
//
// UNTESTED.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aevent"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

// sendTimeout bounds one SendMessage run. The run is detached from the client's connection so that a client
// that hangs up mid-dispatch does not turn a delivery into an unknown outcome.
const sendTimeout = 30 * time.Second

// writeAttempts is how many times a status write is retried when the task changed underneath it.
const writeAttempts = 3

// sendHandler overrides SendMessage and leaves every other method to the embedded SDK handler.
type sendHandler struct {
	a2asrv.RequestHandler
	agent a2asrv.AgentExecutor
	tasks taskstore.Store
}

// SendMessage implements a2asrv.RequestHandler.
func (h *sendHandler) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	if err := checkSend(req); err != nil {
		return nil, err
	}
	ec, err := h.executorContext(ctx, req)
	if err != nil {
		return nil, err
	}

	run, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	applied := false
	for event, err := range h.agent.Execute(run, ec) {
		if err != nil {
			return nil, err
		}
		if err := h.apply(run, ec, event); err != nil {
			return nil, err
		}
		applied = true
	}
	if !applied {
		return nil, fmt.Errorf("execution finished without producing any events: %w", a2a.ErrInvalidAgentResponse)
	}

	// The task is returned at once, in its current state, whether or not the client set returnImmediately.
	// A blocking wait is not offered (contract section 14.2).
	stored, err := h.tasks.Get(ctx, ec.TaskID)
	if err != nil {
		return nil, err
	}
	return stored.Task, nil
}

// checkSend is the SDK's request validation.
func checkSend(req *a2a.SendMessageRequest) error {
	switch {
	case req == nil:
		return fmt.Errorf("SendMessageRequest is required: %w", a2a.ErrInvalidParams)
	case req.Message == nil:
		return fmt.Errorf("message is required: %w", a2a.ErrInvalidParams)
	case req.Message.ID == "":
		return fmt.Errorf("message ID is required: %w", a2a.ErrInvalidParams)
	case len(req.Message.Parts) == 0:
		return fmt.Errorf("message parts is required: %w", a2a.ErrInvalidParams)
	case req.Message.Role == "":
		return fmt.Errorf("message role is required: %w", a2a.ErrInvalidParams)
	}
	return nil
}

// executorContext builds what the executor sees. A message with no task id starts a new task; one with a
// task id continues it, read through the caller-scoped store so a non-party gets not-found.
func (h *sendHandler) executorContext(ctx context.Context, req *a2a.SendMessageRequest) (*a2asrv.ExecutorContext, error) {
	message := req.Message
	ec := &a2asrv.ExecutorContext{Message: message, Metadata: req.Metadata, Tenant: req.Tenant}
	if callCtx, ok := a2asrv.CallContextFrom(ctx); ok {
		ec.User = callCtx.User
		ec.ServiceParams = callCtx.ServiceParams()
		ec.Tenant = callCtx.Tenant()
	}

	if message.TaskID == "" {
		ec.TaskID = a2a.NewTaskID()
		ec.ContextID = message.ContextID
		if ec.ContextID == "" {
			ec.ContextID = a2a.NewContextID()
		}
		return ec, nil
	}

	stored, err := h.tasks.Get(ctx, message.TaskID)
	if err != nil {
		return nil, err
	}
	task := stored.Task
	if message.ContextID != "" && message.ContextID != task.ContextID {
		return nil, fmt.Errorf("message contextID different from task contextID: %w", a2a.ErrInvalidParams)
	}
	if task.Status.State.Terminal() {
		return nil, fmt.Errorf("task in a terminal state %q: %w", task.Status.State, a2a.ErrUnsupportedOperation)
	}
	withMessage, err := withHistory(task, message)
	if err != nil {
		return nil, err
	}
	ec.StoredTask, ec.TaskID, ec.ContextID = withMessage, task.ID, task.ContextID
	return ec, nil
}

// withHistory returns the task with the message appended to its history, unless it is already there.
func withHistory(task *a2a.Task, message *a2a.Message) (*a2a.Task, error) {
	if slices.ContainsFunc(task.History, func(m *a2a.Message) bool { return m.ID == message.ID }) {
		return task, nil
	}
	next, err := a2aevent.ApplyUpdate(task, message)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", a2a.ErrInvalidParams, err)
	}
	return next, nil
}

// apply writes one executor event. The executor yields a new task, or a status update on an existing one.
func (h *sendHandler) apply(ctx context.Context, ec *a2asrv.ExecutorContext, event a2a.Event) error {
	switch ev := event.(type) {
	case *a2a.Task:
		_, err := h.tasks.Create(ctx, ev)
		return err
	case *a2a.TaskStatusUpdateEvent:
		return h.applyStatus(ctx, ec, ev)
	default:
		return fmt.Errorf("unexpected %T from the executor: %w", event, a2a.ErrInvalidAgentResponse)
	}
}

// applyStatus reads the task fresh (the Service may have written since), adds the request message to its
// history if it is not there, applies the status and writes it back.
func (h *sendHandler) applyStatus(ctx context.Context, ec *a2asrv.ExecutorContext, event *a2a.TaskStatusUpdateEvent) error {
	var last error
	for range writeAttempts {
		stored, err := h.tasks.Get(ctx, ec.TaskID)
		if err != nil {
			return err
		}
		if stored.Task.Status.State.Terminal() && !event.Status.State.Terminal() {
			return fmt.Errorf("task in a terminal state %q: %w", stored.Task.Status.State, a2a.ErrUnsupportedOperation)
		}
		base, err := withHistory(stored.Task, ec.Message)
		if err != nil {
			return err
		}
		next, err := a2aevent.ApplyUpdate(base, event)
		if err != nil {
			return fmt.Errorf("%w: %v", a2a.ErrInvalidAgentResponse, err)
		}
		// Observations are added on every read; they are not part of the stored body.
		delete(next.Metadata, MetadataKey)
		if len(next.Metadata) == 0 {
			next.Metadata = nil
		}
		_, err = h.tasks.Update(ctx, &taskstore.UpdateRequest{Task: next, Event: event, PrevTask: stored.Task, PrevVersion: stored.Version})
		if err == nil {
			return nil
		}
		if !errors.Is(err, taskstore.ErrConcurrentModification) {
			return err
		}
		last = err
	}
	return last
}
