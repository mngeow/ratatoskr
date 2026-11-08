package a2a

// The adapter that lets the SDK handler use our Store.
//
// The SDK calls Create and Update while an execution is open; our service writes state afterwards, by
// reports and system events. The two are kept apart by (a) returning TaskVersionMissing so the SDK does
// not rely on its optimistic versioning, (b) refusing an SDK update that would undo a final state our
// service set, which makes the SDK re-read and retry, and (c) scoping every read by the caller carried in
// the request context: a task you are not a party to is not found, no matter why.
//
// UNTESTED.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

// MetadataKey is the Task.metadata key under which the gateway publishes its observations.
const MetadataKey = "ratatoskr.observations"

type sdkStore struct {
	store Store
}

// NewSDKStore adapts a Store to the SDK's taskstore.Store.
func NewSDKStore(store Store) taskstore.Store { return &sdkStore{store: store} }

var _ taskstore.Store = (*sdkStore)(nil)

// Create implements taskstore.Store. The owner and target come from the request context.
func (s *sdkStore) Create(ctx context.Context, task *a2a.Task) (taskstore.TaskVersion, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return taskstore.TaskVersionMissing, a2a.ErrUnauthenticated
	}
	target, ok := TargetFrom(ctx)
	if !ok {
		return taskstore.TaskVersionMissing, a2a.ErrInvalidRequest
	}
	body, err := json.Marshal(task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: encode task: %w", err)
	}
	record := Task{
		ID: string(task.ID), ContextID: task.ContextID, Owner: caller, Target: target,
		State: stateName(task.Status.State), Body: body,
	}
	record.CreatedAt = time.Now()
	record.UpdatedAt = record.CreatedAt
	switch err := s.store.CreateTask(ctx, record); err {
	case nil:
		return taskstore.TaskVersionMissing, nil
	case ErrExists:
		return taskstore.TaskVersionMissing, taskstore.ErrTaskAlreadyExists
	default:
		return taskstore.TaskVersionMissing, err
	}
}

// Update implements taskstore.Store.
func (s *sdkStore) Update(ctx context.Context, req *taskstore.UpdateRequest) (taskstore.TaskVersion, error) {
	stored, found, err := s.store.GetTask(ctx, string(req.Task.ID))
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	if !found {
		return taskstore.TaskVersionMissing, a2a.ErrTaskNotFound
	}
	state := stateName(req.Task.Status.State)
	if Final(stored.State) && state != stored.State {
		// Our service finalised this task out of band; the SDK's snapshot is older. Make it re-read.
		return taskstore.TaskVersionMissing, taskstore.ErrConcurrentModification
	}
	body, err := json.Marshal(req.Task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a: encode task: %w", err)
	}
	stored.State, stored.Body = state, body
	stored.UpdatedAt = time.Now()
	if _, err := s.store.SaveTask(ctx, stored); err != nil {
		if err == ErrConflict {
			return taskstore.TaskVersionMissing, taskstore.ErrConcurrentModification
		}
		return taskstore.TaskVersionMissing, err
	}
	return taskstore.TaskVersionMissing, nil
}

// Get implements taskstore.Store: a task the caller is not a party to is not found.
func (s *sdkStore) Get(ctx context.Context, id a2a.TaskID) (*taskstore.StoredTask, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, a2a.ErrUnauthenticated
	}
	t, found, err := s.store.GetTask(ctx, string(id))
	if err != nil {
		return nil, err
	}
	if !found || !IsParty(caller, t) {
		return nil, a2a.ErrTaskNotFound
	}
	body, err := DecodeBody(t)
	if err != nil {
		return nil, err
	}
	if err := s.addObservations(ctx, body); err != nil {
		return nil, err
	}
	return &taskstore.StoredTask{Task: body, Version: taskstore.TaskVersionMissing, User: t.Owner.Key()}, nil
}

// List implements taskstore.Store: caller-scoped and cursor-paginated.
func (s *sdkStore) List(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return nil, a2a.ErrUnauthenticated
	}
	filter := ListFilter{Caller: caller, ContextID: req.ContextID, PageSize: req.PageSize, Cursor: req.PageToken}
	if req.Status != a2a.TaskStateUnspecified {
		filter.State = stateName(req.Status)
	}
	tasks, next, err := s.store.ListTasks(ctx, filter)
	if err != nil {
		if err == ErrNotFound {
			return nil, a2a.ErrInvalidParams // an unreadable page token
		}
		return nil, err
	}
	out := &a2a.ListTasksResponse{Tasks: make([]*a2a.Task, 0, len(tasks)), PageSize: filter.PageSize, NextPageToken: next}
	for _, t := range tasks {
		body, err := DecodeBody(t)
		if err != nil {
			return nil, err
		}
		if !req.IncludeArtifacts {
			body.Artifacts = nil
		}
		if req.HistoryLength != nil {
			trimHistory(body, *req.HistoryLength)
		}
		out.Tasks = append(out.Tasks, body)
	}
	// totalSize is the number of matching tasks the caller can see BEFORE pagination (spec 3.1.4).
	total, err := s.store.CountTasks(ctx, filter)
	if err != nil {
		return nil, err
	}
	out.TotalSize = total
	return out, nil
}

func trimHistory(task *a2a.Task, length int) {
	if length <= 0 {
		task.History = []*a2a.Message{}
	} else if length < len(task.History) {
		task.History = task.History[len(task.History)-length:]
	}
}

// addObservations publishes the delivery, connection and reconciled observations in Task.metadata under
// a Ratatoskr-namespaced key. They are separate from the state (contract section 7).
func (s *sdkStore) addObservations(ctx context.Context, body *a2a.Task) error {
	observed, err := s.store.Observations(ctx, string(body.ID))
	if err != nil {
		return err
	}
	if len(observed) == 0 {
		return nil
	}
	latest := map[string]string{}
	for _, o := range observed {
		value := o.Value
		if o.Kind == ObsConnection && o.Value == "lost" {
			value = "lost at " + o.At.UTC().Format("2006-01-02T15:04:05Z")
		}
		latest[o.Kind] = value
	}
	metadata := make(map[string]any, len(body.Metadata)+1)
	for key, value := range body.Metadata {
		metadata[key] = value
	}
	metadata[MetadataKey] = latest
	body.Metadata = metadata
	return nil
}
