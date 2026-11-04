package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them. Every request is
// built with httptest.NewRequest and req.Host set to the server's own address, so
// nothing here needs the network.

// fakeInbox records what the server asked of the router.
type fakeInbox struct {
	mu sync.Mutex

	sends []sendCall
	hooks []hookCall

	sendID  string
	sendErr error
}

type sendCall struct {
	From    protocol.AgentID
	To      string
	Body    string
	ReplyTo string
}

type hookCall struct {
	From  protocol.AgentID
	Event protocol.HookEvent
}

func (f *fakeInbox) Send(_ context.Context, from protocol.AgentID, to, body, replyTo string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, sendCall{From: from, To: to, Body: body, ReplyTo: replyTo})
	if f.sendErr != nil {
		return "", f.sendErr
	}
	if f.sendID != "" {
		return f.sendID, nil
	}
	return "msg-1", nil
}

func (f *fakeInbox) HookEvent(_ context.Context, from protocol.AgentID, ev protocol.HookEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks = append(f.hooks, hookCall{From: from, Event: ev})
}

func (f *fakeInbox) sendCalls() []sendCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sendCall(nil), f.sends...)
}

func (f *fakeInbox) hookCalls() []hookCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hookCall(nil), f.hooks...)
}

// newTestServer builds a server with one or two registered agents. The listener
// New opened is closed when the test ends; the handler is exercised directly.
func newTestServer(t *testing.T, inbox Inbox, agents ...string) (*Server, map[string]string) {
	t.Helper()

	s, err := New(inbox, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })

	tokens := make(map[string]string, len(agents))
	for _, name := range agents {
		token := s.AddAgent(protocol.AgentID(name), name, peersFor(name, agents))
		if token == "" {
			t.Fatalf("AddAgent(%q) returned no token", name)
		}
		tokens[name] = token
	}
	return s, tokens
}

func peersFor(name string, agents []string) []string {
	peers := make([]string, 0, len(agents))
	for _, other := range agents {
		if other != name {
			peers = append(peers, other)
		}
	}
	return peers
}

// request builds a request the way a harness would, with the server's own Host.
func request(t *testing.T, s *Server, method, path, token, body string) *http.Request {
	t.Helper()

	methodOrDefault := method
	if methodOrDefault == "" {
		methodOrDefault = http.MethodPost
	}
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(methodOrDefault, path, reader)
	req.Host = s.Addr()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestNewRequiresInbox(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("New accepted a nil Inbox")
	}
}

func TestNewBindsLoopbackAndDefaults(t *testing.T) {
	inbox := &fakeInbox{}
	s, _ := newTestServer(t, inbox, "alpha")

	if !strings.HasPrefix(s.Addr(), "127.0.0.1:") {
		t.Fatalf("Addr() = %q, want a 127.0.0.1 address", s.Addr())
	}
	if s.opts.ReadHeaderTimeout != defaultReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want the default", s.opts.ReadHeaderTimeout)
	}
	if s.opts.MaxBodyBytes != defaultMaxBodyBytes {
		t.Fatalf("MaxBodyBytes = %d, want the default", s.opts.MaxBodyBytes)
	}
}

func TestAuthTable(t *testing.T) {
	tests := []struct {
		name   string
		host   bool // false means a Host the server never handed out
		origin bool
		token  bool
		path   string
		method string
		want   int
	}{
		{name: "valid token on /hook", token: true, path: "/hook", want: http.StatusNoContent},
		{name: "no token", path: "/hook", want: http.StatusUnauthorized},
		{name: "wrong token", token: false, path: "/hook", want: http.StatusUnauthorized},
		{name: "foreign host", host: true, token: true, path: "/hook", want: http.StatusForbidden},
		{name: "origin present", origin: true, token: true, path: "/hook", want: http.StatusForbidden},
		{name: "unknown path with a token", token: true, path: "/nope", want: http.StatusNotFound},
		{name: "unknown path without a token", path: "/nope", want: http.StatusUnauthorized},
		{name: "GET on /hook with a token", token: true, path: "/hook", method: http.MethodGet, want: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inbox := &fakeInbox{}
			s, tokens := newTestServer(t, inbox, "alpha")

			token := ""
			switch {
			case tc.token:
				token = tokens["alpha"]
			case tc.name == "wrong token":
				token = "not-a-real-token"
			}

			req := request(t, s, tc.method, tc.path, token, `{"event":"Stop"}`)
			if tc.host {
				req.Host = "evil.example.com:80"
			}
			if tc.origin {
				req.Header.Set("Origin", "http://evil.example.com")
			}

			recorder := httptest.NewRecorder()
			s.routes().ServeHTTP(recorder, req)

			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
					t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
				}
				if recorder.Body.Len() != 0 {
					t.Fatalf("401 carried a body: %q", recorder.Body.String())
				}
			}
			if tc.want == http.StatusForbidden && recorder.Body.Len() != 0 {
				t.Fatalf("403 carried a body: %q", recorder.Body.String())
			}
		})
	}
}

// TestTokenIdentifiesTheAgent is invariant 4 at the HTTP layer: the sender is the
// token's agent, whatever else the request says.
func TestTokenIdentifiesTheAgent(t *testing.T) {
	inbox := &fakeInbox{}
	s, tokens := newTestServer(t, inbox, "alpha", "beta")

	req := request(t, s, http.MethodPost, "/hook", tokens["beta"], `{"event":"Stop","payload":{"session_id":"s-beta"}}`)
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, req)

	calls := inbox.hookCalls()
	if len(calls) != 1 {
		t.Fatalf("inbox saw %d hook events, want 1", len(calls))
	}
	if calls[0].From != "beta" {
		t.Fatalf("sender = %q, want beta (the token's agent)", calls[0].From)
	}
}

// TestOversizeBodyIsNotForwarded: the cap makes an unreadable body, and the hook
// answer is still 204 with nothing forwarded.
func TestOversizeBodyIsNotForwarded(t *testing.T) {
	inbox := &fakeInbox{}
	s, tokens := newTestServer(t, inbox, "alpha")

	big := strings.Repeat("x", int(defaultMaxBodyBytes)+1024)
	body := `{"event":"UserPromptSubmit","payload":{"prompt":"` + big + `"}}`

	req := request(t, s, http.MethodPost, "/hook", tokens["alpha"], body)
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if calls := inbox.hookCalls(); len(calls) != 0 {
		t.Fatalf("an over-size body forwarded %d events", len(calls))
	}
}

func TestBearerTokenParsing(t *testing.T) {
	tests := []struct {
		header string
		want   string
		ok     bool
	}{
		{header: "Bearer abc", want: "abc", ok: true},
		{header: "bearer abc", want: "abc", ok: true},
		{header: "BEARER abc", want: "abc", ok: true},
		{header: "Bearer ", ok: false},
		{header: "Bearer", ok: false},
		{header: "Basic abc", ok: false},
		{header: "", ok: false},
	}

	for _, tc := range tests {
		got, ok := bearerToken(tc.header)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("bearerToken(%q) = (%q, %v), want (%q, %v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAddAgentRejectsDuplicatesAndEmpties(t *testing.T) {
	inbox := &fakeInbox{}
	s, err := New(inbox, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })

	if token := s.AddAgent("", "alpha", nil); token != "" {
		t.Fatal("AddAgent accepted an empty ID")
	}
	if token := s.AddAgent("alpha", "", nil); token != "" {
		t.Fatal("AddAgent accepted an empty name")
	}

	first := s.AddAgent("alpha", "alpha", nil)
	if first == "" {
		t.Fatal("AddAgent returned no token for a fresh agent")
	}
	if second := s.AddAgent("alpha", "alpha-again", nil); second != "" {
		t.Fatal("AddAgent rotated the token of an existing agent")
	}
	if len(first) != tokenBytes*2 {
		t.Fatalf("token length = %d, want %d hex characters", len(first), tokenBytes*2)
	}
}

// TestShutdownIsIdempotentAndServeReturnsNil runs the real listener, which is the
// only way to see Serve's contract.
func TestShutdownIsIdempotentAndServeReturnsNil(t *testing.T) {
	inbox := &fakeInbox{}
	s, err := New(inbox, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	served := make(chan error, 1)
	go func() { served <- s.Serve() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil after Shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}
