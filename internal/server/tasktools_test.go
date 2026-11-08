package server

// Written, not run (GR-3). Pure tests: the closures' building blocks, no transport.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

type taskRefusal struct {
	code    int
	message string
}

func (r taskRefusal) Error() string       { return "refused" }
func (r taskRefusal) TaskCode() int       { return r.code }
func (r taskRefusal) TaskMessage() string { return r.message }

func TestTaskFailureTextRefusalAndUncertainty(t *testing.T) {
	if got := taskFailureText(taskRefusal{404, "no such task"}, false); got != "refused (code 404): no such task" {
		t.Fatalf("taskRefusal = %q", got)
	}
	// A wrapped taskRefusal reads the same.
	if got := taskFailureText(errors.Join(errors.New("x"), taskRefusal{-32001, "task not found"}), true); !strings.HasPrefix(got, "refused (code -32001)") {
		t.Fatalf("wrapped taskRefusal = %q", got)
	}
	// An unknown send failure never says "failed": the task may exist, and a second send would duplicate.
	got := taskFailureText(errors.New("connection reset"), true)
	if !strings.Contains(got, "may or may not") || !strings.Contains(got, "Do not send it again") || strings.Contains(got, "reset") {
		t.Fatalf("uncertain send = %q", got)
	}
	if got := taskFailureText(errors.New("connection reset"), false); strings.Contains(got, "reset") {
		t.Fatalf("a read failure leaked the transport error: %q", got)
	}
}

func TestTaskBackendChecksIdentityAndAvailability(t *testing.T) {
	var none Tasks
	id := protocol.AgentID("agent-1")
	available := func() Tasks { return none }

	// No identity in the context, or another agent's: the one thing a task tool must never do is act as
	// somebody else.
	if _, err := taskBackend(context.Background(), id, available); err == nil || err.Error() != internalErrorName {
		t.Fatalf("no identity: %v", err)
	}
	if _, err := taskBackend(withAgent(context.Background(), "agent-2"), id, available); err == nil || err.Error() != internalErrorName {
		t.Fatalf("another agent: %v", err)
	}
	// The right agent but no task backend (a daemon that is not connected).
	if _, err := taskBackend(withAgent(context.Background(), id), id, available); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("no backend: %v", err)
	}
}

func TestSetTasksIsSeenByTheTools(t *testing.T) {
	s := &Server{}
	if s.currentTasks() != nil {
		t.Fatal("tasks set before SetTasks")
	}
	s.SetTasks(fakeTasks{})
	if s.currentTasks() == nil {
		t.Fatal("SetTasks did not stick")
	}
}

type fakeTasks struct{}

func (fakeTasks) TaskSend(context.Context, string, string, string, string) (protocol.TaskView, error) {
	return protocol.TaskView{}, nil
}
func (fakeTasks) TaskGet(context.Context, string, string) (protocol.TaskView, error) {
	return protocol.TaskView{}, nil
}
func (fakeTasks) TaskReport(context.Context, string, string, protocol.TaskReportRequest) error {
	return nil
}
