package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them.

func TestHookExtractsStringFields(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantKind    protocol.HookKind
		wantPrompt  string
		wantSession string
		wantSource  string
		forwarded   bool
	}{
		{
			name:        "session start carries a source",
			body:        `{"event":"SessionStart","payload":{"session_id":"s1","source":"startup"}}`,
			wantKind:    protocol.HookSessionStart,
			wantSession: "s1",
			wantSource:  "startup",
			forwarded:   true,
		},
		{
			name:        "user prompt submit carries a prompt",
			body:        `{"event":"UserPromptSubmit","payload":{"session_id":"s1","prompt":"hello <ratatoskr-message id=\"m1\">"}}`,
			wantKind:    protocol.HookUserPromptSubmit,
			wantSession: "s1",
			wantPrompt:  `hello <ratatoskr-message id="m1">`,
			forwarded:   true,
		},
		{
			name:      "stop with no payload",
			body:      `{"event":"Stop"}`,
			wantKind:  protocol.HookStop,
			forwarded: true,
		},
		{
			name:      "non-string fields are ignored",
			body:      `{"event":"UserPromptSubmit","payload":{"session_id":7,"prompt":["x"],"source":true}}`,
			wantKind:  protocol.HookUserPromptSubmit,
			forwarded: true,
		},
		{
			name:      "unknown event is not forwarded",
			body:      `{"event":"SomethingNew","payload":{"prompt":"x"}}`,
			forwarded: false,
		},
		{
			name:      "malformed json is not forwarded",
			body:      `{"event":`,
			forwarded: false,
		},
		{
			name:      "empty body is not forwarded",
			body:      ``,
			forwarded: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inbox := &fakeInbox{}
			s, tokens := newTestServer(t, inbox, "alpha")

			recorder := httptest.NewRecorder()
			s.routes().ServeHTTP(recorder, request(t, s, http.MethodPost, "/hook", tokens["alpha"], tc.body))

			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204 on every hook path", recorder.Code)
			}
			if recorder.Body.Len() != 0 {
				t.Fatalf("hook answered with a body: %q (a hook must never look like a decision)", recorder.Body.String())
			}

			calls := inbox.hookCalls()
			if !tc.forwarded {
				if len(calls) != 0 {
					t.Fatalf("forwarded %d events, want none", len(calls))
				}
				return
			}
			if len(calls) != 1 {
				t.Fatalf("forwarded %d events, want 1", len(calls))
			}

			call := calls[0]
			if call.From != "alpha" {
				t.Fatalf("sender = %q, want alpha", call.From)
			}
			if call.Event.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", call.Event.Kind, tc.wantKind)
			}
			if call.Event.Prompt != tc.wantPrompt {
				t.Fatalf("prompt = %q, want %q", call.Event.Prompt, tc.wantPrompt)
			}
			if call.Event.SessionID != tc.wantSession {
				t.Fatalf("session id = %q, want %q", call.Event.SessionID, tc.wantSession)
			}
			if call.Event.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q", call.Event.Source, tc.wantSource)
			}
			if call.Event.At.IsZero() {
				t.Fatal("hook event has no timestamp")
			}
		})
	}
}

// TestHookFieldsToleratesOddPayloads keeps a harness surprise from panicking.
func TestHookFieldsToleratesOddPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "absent", payload: ``},
		{name: "not an object", payload: `"a string"`},
		{name: "array", payload: `[1,2,3]`},
		{name: "null", payload: `null`},
		{name: "nested wrong types", payload: `{"session_id":{"a":1},"prompt":null}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessionID, prompt, source := hookFields([]byte(tc.payload))
			if sessionID != "" || prompt != "" || source != "" {
				t.Fatalf("hookFields(%s) = (%q, %q, %q), want all empty", tc.payload, sessionID, prompt, source)
			}
		})
	}
}

// TestHookAnswerCannotBeADecision drives every shape of request through the hook
// route and insists the response is empty: a PermissionRequest hook that printed
// anything could answer the harness's question.
func TestHookAnswerCannotBeADecision(t *testing.T) {
	bodies := []string{
		`{"event":"PermissionRequest","payload":{"session_id":"s1"}}`,
		`{"event":"Stop"}`,
		`{}`,
		`not json at all`,
		`{"event":"Stop","payload":{"decision":"allow"}}`,
	}

	inbox := &fakeInbox{}
	s, tokens := newTestServer(t, inbox, "alpha")

	for _, body := range bodies {
		recorder := httptest.NewRecorder()
		s.routes().ServeHTTP(recorder, request(t, s, http.MethodPost, "/hook", tokens["alpha"], body))

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("body %q: status = %d, want 204", body, recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("body %q: response has %q, want nothing", body, recorder.Body.String())
		}
	}
}
