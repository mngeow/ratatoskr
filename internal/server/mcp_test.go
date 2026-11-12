package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mngeow/ratatoskr/internal/protocol"
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
	want := "You are alpha on a local ratatoskr network. Your peers: beta, gamma. To message a peer, use the ratatoskr_send tool from the ratatoskr MCP server: ratatoskr_send(to, body, reply_to?). Other messaging tools, including any built-in send_message, do not reach your peers. Your normal replies are not sent to peers."
	if got != want {
		t.Fatalf("instructions =\n%q\nwant\n%q", got, want)
	}

	alone := instructions("alpha", nil)
	if !strings.Contains(alone, "Your peers: (none).") {
		t.Fatalf("instructions with no peers = %q", alone)
	}
}

// TestInstructionsNameOurToolAndServer pins the fix for the wrong-tool bug: the
// agent must be told the tool name and our server, because Codex 0.166 has its
// own built-in send_message and a generic instruction sent replies there.
func TestInstructionsNameOurToolAndServer(t *testing.T) {
	for _, peers := range [][]string{{"beta"}, nil} {
		text := instructions("alpha", peers)

		if !strings.Contains(text, "ratatoskr_send") {
			t.Fatalf("instructions do not name ratatoskr_send: %q", text)
		}
		if !strings.Contains(text, "ratatoskr MCP server") {
			t.Fatalf("instructions do not name the ratatoskr MCP server: %q", text)
		}
		if !strings.Contains(text, toolName) {
			t.Fatalf("instructions do not mention the registered tool name %q", toolName)
		}
	}

	if strings.Contains(instructions("alpha", nil), "Use send_message(") {
		t.Fatal("instructions still tell the agent to use a bare send_message")
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
	if built := newAgentServer("alpha", "alpha", "", []string{"beta"}, false, inbox, nil); built.handler() == nil {
		t.Fatal("newAgentServer built no MCP server")
	}
	tool := sendMessageTool("alpha", false, inbox)

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

// connectedTestError is a connected refusal the tests can vary field by field. It
// implements exactly the four optional methods the server reads.
type connectedTestError struct {
	reason    string
	detail    string
	id        string
	uncertain bool
}

func (e *connectedTestError) Error() string      { return "router: message rejected: " + e.reason }
func (e *connectedTestError) SendReason() string { return e.reason }
func (e *connectedTestError) SendDetail() string { return e.detail }
func (e *connectedTestError) SendID() string     { return e.id }
func (e *connectedTestError) Uncertain() bool    { return e.uncertain }

// plainTestError implements none of them.
type plainTestError struct{}

func (*plainTestError) Error() string { return "boom" }

func TestConnectedInstructions(t *testing.T) {
	got := connectedInstructions("claude@mac", []string{"codex@ubuntu-b"})
	want := "You are claude@mac on a ratatoskr network that can span machines. Your peers at launch: codex@ubuntu-b. Peers can change; if a name is not found, the tool answers with the agents that exist. To find out who exists, call ratatoskr_discover (no arguments) before you send: it lists the hosts, what each host owner says they offer (declared, not verified) and the exact sessions you can reach. To message a peer, use the ratatoskr_send tool from the ratatoskr MCP server: ratatoskr_send(to, body, reply_to?), where to is name@host, for example codex@ubuntu-b; a bare name means an agent on this machine. Other messaging tools, including any built-in send_message, do not reach your peers. A successful result means the recipient's machine has saved the message, not that it has been read or acted on. Your normal replies are not sent to peers."
	if got != want {
		t.Fatalf("connectedInstructions =\n%q\nwant\n%q", got, want)
	}

	alone := connectedInstructions("claude@mac", nil)
	if !strings.Contains(alone, "Your peers at launch: (none).") {
		t.Fatalf("connectedInstructions with no peers = %q", alone)
	}
}

// TestLocalTextsAreUnchanged is the regression bar for this slice.
func TestLocalTextsAreUnchanged(t *testing.T) {
	// Local instructions are exactly the 5a-6 text.
	local := instructions("alpha", []string{"beta", "gamma"})
	wantLocal := "You are alpha on a local ratatoskr network. Your peers: beta, gamma. To message a peer, use the ratatoskr_send tool from the ratatoskr MCP server: ratatoskr_send(to, body, reply_to?). Other messaging tools, including any built-in send_message, do not reach your peers. Your normal replies are not sent to peers."
	if local != wantLocal {
		t.Fatalf("local instructions =\n%q\nwant\n%q", local, wantLocal)
	}

	// The local tool description is untouched.
	if !strings.HasPrefix(toolDescription, "Send a message to a peer agent on the ratatoskr network. This is the ratatoskr_send tool") {
		t.Fatalf("local description = %q", toolDescription)
	}
	if strings.Contains(toolDescription, "name@host") {
		t.Fatalf("the local description mentions network names: %q", toolDescription)
	}
}

// TestConnectedFailureTextTable walks every wording rule.
func TestConnectedFailureTextTable(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantParts  []string
		refuseText []string
	}{
		{
			name:       "gateway_uncertain",
			err:        &connectedTestError{reason: "gateway_uncertain", detail: "no complete answer", id: "m1", uncertain: true},
			wantParts:  []string{"gateway_uncertain:", "may still arrive", "do not treat it as failed", "Message id: m1.", "no complete answer", "Sending it again would be a NEW message and the recipient could receive both."},
			refuseText: []string{"not sent", "not delivered"},
		},
		{
			name:       "delivery_uncertain",
			err:        &connectedTestError{reason: "delivery_uncertain", detail: "", id: "m2", uncertain: true},
			wantParts:  []string{"delivery_uncertain:", "may still arrive", "Message id: m2.", "NEW message"},
			refuseText: []string{"not sent", "not delivered"},
		},
		{
			name:       "uncertain with no id",
			err:        &connectedTestError{reason: "gateway_uncertain", uncertain: true},
			wantParts:  []string{"may still arrive", "NEW message"},
			refuseText: []string{"Message id:"},
		},
		{
			name:      "gateway_unreachable",
			err:       &connectedTestError{reason: "gateway_unreachable", detail: "connection refused"},
			wantParts: []string{"gateway_unreachable: not sent.", "connection refused", "will not be delivered later"},
		},
		{
			name:      "destination_unreachable",
			err:       &connectedTestError{reason: "destination_unreachable", detail: "the host has no live connection"},
			wantParts: []string{"destination_unreachable: not delivered.", "not connected", "the host has no live connection", "send it again"},
		},
		{
			name:      "unknown_recipient with a listing",
			err:       &connectedTestError{reason: "unknown_recipient", detail: "no agent by that name has been seen (known agents: codex@ubuntu-b)"},
			wantParts: []string{"unknown_recipient: no such agent.", "known agents: codex@ubuntu-b"},
		},
		{
			name:      "unknown_recipient without a detail",
			err:       &connectedTestError{reason: "unknown_recipient"},
			wantParts: []string{"unknown_recipient: no such agent."},
		},
		{
			name:      "session_gone",
			err:       &connectedTestError{reason: "session_gone", detail: "that agent has no current session"},
			wantParts: []string{"session_gone: not delivered.", "no current session"},
		},
		{
			name:      "a local refusal passes through",
			err:       &connectedTestError{reason: "budget_exhausted"},
			wantParts: []string{"budget_exhausted"},
		},
		{
			name:      "an unrecognised error is opaque",
			err:       &plainTestError{},
			wantParts: []string{internalErrorName},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := connectedFailureText(tc.err)
			for _, part := range tc.wantParts {
				if !strings.Contains(got, part) {
					t.Fatalf("text %q does not contain %q", got, part)
				}
			}
			for _, part := range tc.refuseText {
				if strings.Contains(got, part) {
					t.Fatalf("text %q contains %q, which reads as a definite failure", got, part)
				}
			}
			// No double spaces or trailing space anywhere.
			if strings.Contains(got, "  ") || strings.HasSuffix(got, " ") {
				t.Fatalf("text %q has a spacing defect", got)
			}
		})
	}
}

// TestConnectedFailureTextNeverLeaksSecrets: no token, URL or gateway address.
func TestConnectedFailureTextNeverLeaksSecrets(t *testing.T) {
	got := connectedFailureText(&connectedTestError{
		reason: "gateway_uncertain", detail: "the gateway refused this host: unauthorized", id: "m1", uncertain: true,
	})
	for _, secret := range []string{"Bearer", "http", "127.0.0.1", "token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("text %q contains %q", got, secret)
		}
	}
}

// TestConnectedAgentRegistration: the token, the duplicate refusal and the local
// hook-file name all behave as they do locally.
func TestConnectedAgentRegistration(t *testing.T) {
	inbox := &fakeInbox{}
	s, err := New(inbox, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })

	if token := s.AddConnectedAgent("alpha", "alpha", "", nil); token != "" {
		t.Fatal("AddConnectedAgent accepted an empty address")
	}
	if token := s.AddConnectedAgent("", "alpha", "alpha@mac", nil); token != "" {
		t.Fatal("AddConnectedAgent accepted an empty id")
	}

	token := s.AddConnectedAgent("alpha", "alpha", "alpha@mac", []string{"beta@ubuntu-b"})
	if token == "" {
		t.Fatal("AddConnectedAgent returned no token")
	}
	if again := s.AddConnectedAgent("alpha", "alpha", "alpha@mac", nil); again != "" {
		t.Fatal("AddConnectedAgent rotated the token of an existing agent")
	}

	// The token still authenticates, and the local name is still what the server
	// uses for the agent.
	entry, found := s.authenticate(token)
	if !found || entry.id != "alpha" {
		t.Fatalf("the connected token did not authenticate: %+v %v", entry, found)
	}

	// And the entry is keyed by the local name, which is what the hook file uses.
	if entry.name != "alpha" {
		t.Fatalf("entry = %+v, want the local name alpha", entry)
	}
	if entry.srv == nil || entry.srv.handler() == nil {
		t.Fatal("the connected agent has no MCP server")
	}

	// A local agent still registers as before.
	if token := s.AddAgent("beta", "beta", nil); token == "" {
		t.Fatal("AddAgent stopped working")
	}
}

// TestSessionGoneFromARefusalDoesNotClaimThereIsNoSession: when the recipient's host
// refused the offer (for example its storage is down), the host's own reason is the
// whole story; the generic "no current session" sentence would mislead.
func TestSessionGoneFromARefusalDoesNotClaimThereIsNoSession(t *testing.T) {
	refusal := connectedFailureText(&connectedTestError{
		reason: "session_gone",
		detail: "the recipient host refused it: storage_unavailable",
	})
	if !strings.Contains(refusal, "storage_unavailable") {
		t.Fatalf("text = %q, want the host's reason", refusal)
	}
	if strings.Contains(refusal, "no current session") {
		t.Fatalf("text = %q, a refusal must not say the agent has no current session", refusal)
	}

	plain := connectedFailureText(&connectedTestError{reason: "session_gone", detail: "that agent has no current session"})
	if !strings.Contains(plain, "no current session") {
		t.Fatalf("text = %q, want the ordinary session_gone explanation", plain)
	}

	// A wrapped error is read the same way.
	wrapped := connectedFailureText(fmt.Errorf("outer: %w", &connectedTestError{reason: "destination_unreachable"}))
	if !strings.Contains(wrapped, "not delivered") {
		t.Fatalf("a wrapped error was not read: %q", wrapped)
	}
}

// TestDiscoverToolIsConnectedOnlyAndRefusesAnotherAgent: the discovery tool exists for a connected agent only,
// answers with the TOON text of the daemon's views, and (like the send tool) refuses a request whose token is
// not this server's agent.
func TestDiscoverToolIsConnectedOnlyAndRefusesAnotherAgent(t *testing.T) {
	source := func() DiscoverySource {
		return func(context.Context) (protocol.NetworkView, protocol.GraphView, error) {
			return protocol.NetworkView{Gateway: protocol.NetworkGatewayView{State: "connected"}}, protocol.GraphView{}, nil
		}
	}
	handler := discoverTool("alpha", source)

	if _, _, err := handler(context.Background(), nil, discoverArgs{}); err == nil || err.Error() != internalErrorName {
		t.Fatalf("a request with no agent in its context: %v", err)
	}
	ctx := withAgent(context.Background(), "alpha")
	result, _, err := handler(ctx, nil, discoverArgs{})
	if err != nil || result == nil || len(result.Content) != 1 {
		t.Fatalf("%v %+v", err, result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "gateway: connected") || !strings.Contains(text.Text, "hosts: []") {
		t.Fatalf("reply = %+v", result.Content[0])
	}
	if _, _, err := discoverTool("alpha", nil)(ctx, nil, discoverArgs{}); err == nil || !strings.HasPrefix(err.Error(), "discovery_unavailable") {
		t.Fatalf("no source: %v", err)
	}
}
