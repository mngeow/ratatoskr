package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests are written but UNRUN: GR-3 forbids running them.

// refusal is a stand-in for the router's SendError, which this package must not
// import: only the SendReason() method is part of the seam.
type refusal struct{ reason string }

func (r *refusal) Error() string      { return "router: message rejected: " + r.reason }
func (r *refusal) SendReason() string { return r.reason }

func TestRefusalReasonMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "router refusal passes through", err: &refusal{reason: "recipient_offline"}, want: "recipient_offline"},
		{name: "wrapped refusal is found", err: errors.Join(errors.New("context"), &refusal{reason: "too_large"}), want: "too_large"},
		{name: "empty reason is opaque", err: &refusal{reason: ""}, want: internalErrorName},
		{name: "unknown error is opaque", err: errors.New("disk on fire"), want: internalErrorName},
		{name: "nil-ish reason type", err: &plainFail{}, want: internalErrorName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusalReason(tc.err); got != tc.want {
				t.Fatalf("refusalReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

type plainFail struct{}

func (*plainFail) Error() string { return "plain" }

func TestInstructionsText(t *testing.T) {
	got := instructions("alpha", []string{"beta", "gamma"})
	want := "You are alpha on a local ratatoskr network. Your peers: beta, gamma. Use send_message(to, body, reply_to?) to message a peer. Your normal replies are not sent to peers."
	if got != want {
		t.Fatalf("instructions =\n%q\nwant\n%q", got, want)
	}

	alone := instructions("alpha", nil)
	if !strings.Contains(alone, "Your peers: (none).") {
		t.Fatalf("instructions with no peers = %q", alone)
	}
}

// TestSendToolUsesTheTokenAgent is the end-to-end proof of invariant 4: the tool
// has no sender argument, and the agent the fake Inbox sees is the one the token
// belongs to.
func TestSendToolUsesTheTokenAgent(t *testing.T) {
	inbox := &fakeInbox{sendID: "msg-42"}
	s, tokens := newTestServer(t, inbox, "alpha", "beta")

	// The handler runs against an httptest server, so the Host guard must see
	// that server's address rather than the listener New opened. Tests may set
	// it directly because they share the package; production code never does.
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	s.addr = strings.TrimPrefix(ts.URL, "http://")

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{
			token: tokens["alpha"],
			next:  http.DefaultTransport,
		}},
		// The stateless handler answers 405 to the standalone GET stream, so the
		// client must not open one.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if len(names) != 1 || names[0] != toolName {
		t.Fatalf("tools = %v, want exactly %q", names, toolName)
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: toolName,
		Arguments: map[string]any{
			"to":   "beta",
			"body": "hello there",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool reported an error: %+v", result.Content)
	}

	calls := inbox.sendCalls()
	if len(calls) != 1 {
		t.Fatalf("inbox saw %d sends, want 1", len(calls))
	}
	if calls[0].From != "alpha" {
		t.Fatalf("sender = %q, want alpha", calls[0].From)
	}
	if calls[0].To != "beta" || calls[0].Body != "hello there" {
		t.Fatalf("send = %+v", calls[0])
	}
}

// TestSendToolReportsRefusalText: the model sees exactly the reason, and an
// unrelated failure sees only the opaque word.
func TestSendToolReportsRefusalText(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "refusal", err: &refusal{reason: "recipient_offline"}, want: "recipient_offline"},
		{name: "other", err: errors.New("boom"), want: internalErrorName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inbox := &fakeInbox{sendErr: tc.err}
			s, tokens := newTestServer(t, inbox, "alpha", "beta")

			ts := httptest.NewServer(s.routes())
			defer ts.Close()
			s.addr = strings.TrimPrefix(ts.URL, "http://")

			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
			session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
				Endpoint: ts.URL + "/mcp",
				HTTPClient: &http.Client{Transport: bearerTransport{
					token: tokens["alpha"],
					next:  http.DefaultTransport,
				}},
				DisableStandaloneSSE: true,
			}, nil)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			t.Cleanup(func() { _ = session.Close() })

			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      toolName,
				Arguments: map[string]any{"to": "beta", "body": "hi"},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !result.IsError {
				t.Fatal("a refused send was reported as success")
			}
			if !contentContains(result.Content, tc.want) {
				t.Fatalf("tool content = %+v, want it to say %q", result.Content, tc.want)
			}
			if tc.want == internalErrorName && contentContains(result.Content, "boom") {
				t.Fatal("an internal failure leaked its detail to the model")
			}
		})
	}
}

// TestToolHandlerRejectsAForeignContext: if the request context ever disagrees
// with the server's own agent, the tool refuses rather than guessing.
func TestToolHandlerRejectsAForeignContext(t *testing.T) {
	inbox := &fakeInbox{}

	// Building the server proves the registration side is wired, and the tool is
	// the same handler it registers.
	if built := newAgentServer("alpha", "alpha", []string{"beta"}, inbox); built.handler() == nil {
		t.Fatal("newAgentServer built no MCP server")
	}
	tool := sendMessageTool("alpha", inbox)

	_, _, err := tool(withAgent(context.Background(), "beta"), nil, sendArgs{To: "beta", Body: "hi"})
	if err == nil || err.Error() != internalErrorName {
		t.Fatalf("handler error = %v, want %q", err, internalErrorName)
	}
	if len(inbox.sendCalls()) != 0 {
		t.Fatal("a mismatched context still sent a message")
	}

	_, _, err = tool(context.Background(), nil, sendArgs{To: "beta", Body: "hi"})
	if err == nil || err.Error() != internalErrorName {
		t.Fatalf("handler error without a context agent = %v, want %q", err, internalErrorName)
	}

	// And the same tool does send when the context names its agent.
	_, out, err := tool(withAgent(context.Background(), "alpha"), nil, sendArgs{To: "beta", Body: "hi"})
	if err != nil {
		t.Fatalf("handler error with the right agent: %v", err)
	}
	if out.Status != statusQueued {
		t.Fatalf("status = %q, want %q", out.Status, statusQueued)
	}
}

func contentContains(content []mcp.Content, want string) bool {
	for _, item := range content {
		if text, ok := item.(*mcp.TextContent); ok && strings.Contains(text.Text, want) {
			return true
		}
	}
	return false
}

// bearerTransport adds the token to every request the SDK's client makes, which
// is how a harness authenticates.
type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(clone) //nolint:wrapcheck // test transport.
}
