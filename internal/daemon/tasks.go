package daemon

// The daemon's side of the A2A task tools: it turns an agent's tool call into a gateway call as that agent's
// gateway session. It holds no state of its own and writes nothing locally; a task lives at the gateway.
//
// UNTESTED. Under GR-3 nothing here has been run: only gofmt, go vet and go build.

import (
	"context"
	"errors"
	"time"

	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// taskCallTimeout bounds one task call to the gateway. An untested guess.
const taskCallTimeout = 20 * time.Second

// taskBridge implements server.Tasks.
type taskBridge struct {
	client   *link.Client
	sessions interface {
		gatewaySessionFor(name string) (string, bool)
	}
}

var errNoGatewaySession = errors.New("daemon: this agent has no live gateway session")

func (b *taskBridge) session(agent string) (string, error) {
	id, ok := b.sessions.gatewaySessionFor(agent)
	if !ok {
		return "", errNoGatewaySession
	}
	return id, nil
}

// TaskSend implements server.Tasks.
func (b *taskBridge) TaskSend(ctx context.Context, agent, to, body, taskID string) (protocol.TaskView, error) {
	session, err := b.session(agent)
	if err != nil {
		return protocol.TaskView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, taskCallTimeout)
	defer cancel()
	return b.client.TaskSend(ctx, session, to, body, taskID)
}

// TaskGet implements server.Tasks. A task is read through the agent's own endpoint: the gateway scopes the
// answer to the tasks this session is a party to.
func (b *taskBridge) TaskGet(ctx context.Context, agent, taskID string) (protocol.TaskView, error) {
	session, err := b.session(agent)
	if err != nil {
		return protocol.TaskView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, taskCallTimeout)
	defer cancel()
	return b.client.TaskGet(ctx, session, session, taskID)
}

// TaskReport implements server.Tasks.
func (b *taskBridge) TaskReport(ctx context.Context, agent, taskID string, report protocol.TaskReportRequest) error {
	session, err := b.session(agent)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, taskCallTimeout)
	defer cancel()
	return b.client.TaskReport(ctx, session, taskID, report)
}
