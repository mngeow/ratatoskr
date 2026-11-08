package link

// The A2A task calls a managed session makes through its daemon: send a task to a participant, read one, and
// report on one it was given. They use the host credential and name the acting session in a header; the
// gateway checks the session is a current one of this host before it believes the header.
//
// UNTESTED. Under GR-3 nothing here has been run: only gofmt, go vet and go build.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	a2aAgentsPath  = "/a2a/agents/"
	sessionHeader  = "X-Ratatoskr-Session"
	maxTaskText    = 4 << 10
	maxTaskResults = 16 << 10
)

// TaskError is a task call the gateway or the participant refused. Message is bounded and written by the
// peer, so a caller shows it as data.
type TaskError struct {
	Code    int
	Message string
}

// TaskCode and TaskMessage let internal/server read a refusal without importing this package.
func (e *TaskError) TaskCode() int { return e.Code }

// TaskMessage is the bounded text of the refusal.
func (e *TaskError) TaskMessage() string { return e.Message }

func (e *TaskError) Error() string {
	return fmt.Sprintf("the task call was refused (code %d): %s", e.Code, e.Message)
}

// rpcRequest builds a JSON-RPC 2.0 request body.
func rpcRequest(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
}

// TaskSend sends a message to a participant: a new task, or an answer on task taskID. to is a session id, a
// name or name@host, or a registered participant name.
func (c *Client) TaskSend(ctx context.Context, session, to, body, taskID string) (protocol.TaskView, error) {
	message := map[string]any{
		"messageId": newTaskMessageID(),
		"role":      "ROLE_USER",
		"parts":     []map[string]any{{"text": body}},
	}
	if taskID != "" {
		message["taskId"] = taskID
	}
	return c.taskRPC(ctx, session, to, "SendMessage", map[string]any{
		"message":       message,
		"configuration": map[string]any{"returnImmediately": true},
	})
}

// TaskGet reads one task the session is a party to.
func (c *Client) TaskGet(ctx context.Context, session, to, taskID string) (protocol.TaskView, error) {
	return c.taskRPC(ctx, session, to, "GetTask", map[string]any{"id": taskID})
}

func (c *Client) taskRPC(ctx context.Context, session, to, method string, params any) (protocol.TaskView, error) {
	if session == "" {
		return protocol.TaskView{}, errors.New("link: the session is not registered with the gateway")
	}
	var raw json.RawMessage
	status, _, payload, err := c.doHeaders(ctx, http.MethodPost, a2aAgentsPath+url.PathEscape(to),
		map[string]string{sessionHeader: session, "A2A-Version": "1.0"}, rpcRequest(method, params), &raw)
	if err != nil {
		return protocol.TaskView{}, errors.New("link: the gateway did not answer the task call")
	}
	if status < 200 || status >= 300 {
		return protocol.TaskView{}, &TaskError{Code: status, Message: httpReason(status)}
	}
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return protocol.TaskView{}, errors.New("link: the answer was not JSON-RPC")
	}
	if envelope.Error != nil {
		return protocol.TaskView{}, &TaskError{Code: envelope.Error.Code, Message: boundText(envelope.Error.Message, 200)}
	}
	return decodeTask(envelope.Result)
}

// decodeTask reads the fields the tools show from a SendMessage or GetTask result.
func decodeTask(raw json.RawMessage) (protocol.TaskView, error) {
	var wrapper struct {
		Task *json.RawMessage `json:"task"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && wrapper.Task != nil {
		raw = *wrapper.Task
	}
	var task struct {
		ID     string `json:"id"`
		Status struct {
			State   string `json:"state"`
			Message *struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"status"`
		Artifacts []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"artifacts"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &task); err != nil || task.ID == "" {
		return protocol.TaskView{}, errors.New("link: the answer carried no task")
	}
	view := protocol.TaskView{ID: task.ID, State: strings.TrimPrefix(task.Status.State, "TASK_STATE_")}
	if task.Status.Message != nil {
		var texts []string
		for _, part := range task.Status.Message.Parts {
			texts = append(texts, part.Text)
		}
		view.Text = boundText(strings.Join(texts, "\n"), maxTaskText)
	}
	var results []string
	for _, artifact := range task.Artifacts {
		for _, part := range artifact.Parts {
			results = append(results, part.Text)
		}
	}
	view.Result = boundText(strings.Join(results, "\n"), maxTaskResults)
	if observed, ok := task.Metadata["ratatoskr.observations"]; ok {
		var values map[string]string
		if json.Unmarshal(observed, &values) == nil {
			view.Delivery = boundText(values["delivery"], 80)
			view.Connection = boundText(values["participant_connection"], 80)
			view.Reconciled = boundText(values["reconciled"], 80)
		}
	}
	return view, nil
}

// TaskReport sends the session's explicit report on a task it was given.
func (c *Client) TaskReport(ctx context.Context, session, taskID string, report protocol.TaskReportRequest) error {
	if session == "" {
		return errors.New("link: the session is not registered with the gateway")
	}
	status, _, _, err := c.doHeaders(ctx, http.MethodPost, protocol.TaskReportPathPrefix+url.PathEscape(taskID)+"/report",
		map[string]string{sessionHeader: session}, report, nil)
	if err != nil {
		return errors.New("link: the gateway did not answer the report")
	}
	if status < 200 || status >= 300 {
		return &TaskError{Code: status, Message: httpReason(status)}
	}
	return nil
}

func httpReason(status int) string {
	switch status {
	case http.StatusNotFound:
		return "no such task, or you are not the participant it was given to"
	case http.StatusConflict:
		return "that task is already final, or the name is ambiguous"
	case http.StatusForbidden, http.StatusUnauthorized:
		return "the gateway did not accept this session"
	case http.StatusBadRequest:
		return "the request was not valid"
	}
	return "the gateway could not complete it"
}

func boundText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// newTaskMessageID is the A2A messageId of one send: random, so two sends are never one message.
func newTaskMessageID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "m-fallback"
	}
	return "m-" + hex.EncodeToString(raw[:])
}
