package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// hookRequest is the body `rtk hook` sends. Payload is the harness's own hook
// JSON, passed through untouched so this server decides nothing about it.
type hookRequest struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

// serveHook reports one harness hook event.
//
// The response is always 204 with no body, on every path: a hook that answered a
// harness question would break invariant 2, and there is nothing safe for a hook
// response to carry. An unreadable body, unparseable JSON, or an event name this
// build does not know all end the same way - nothing is forwarded.
//
// UNTESTED.
func (s *Server) serveHook(w http.ResponseWriter, r *http.Request) {
	agent, ok := agentFromContext(r.Context())
	if !ok {
		// Unreachable behind the auth middleware; answering 204 still keeps the
		// hook silent rather than turning a wiring bug into a harness prompt.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var request hookRequest
	if err := json.Unmarshal(body, &request); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	kind, known := protocolKind(request.Event)
	if !known {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	event := protocol.HookEvent{
		Kind: kind,
		At:   time.Now(),
	}
	event.SessionID, event.Prompt, event.Source = hookFields(request.Payload)

	s.inbox.HookEvent(r.Context(), agent, event)
	w.WriteHeader(http.StatusNoContent)
}

// hookFields pulls the three strings the router uses out of a harness payload.
// A field is taken only when it really is a string: a harness that sends a
// number where a prompt belongs is reporting nothing usable, and pretending
// otherwise would let the router bind or confirm on garbage.
//
// UNTESTED.
func hookFields(payload json.RawMessage) (sessionID, prompt, source string) {
	if len(payload) == 0 {
		return "", "", ""
	}

	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		return "", "", ""
	}

	if value, ok := fields["session_id"].(string); ok {
		sessionID = value
	}
	if value, ok := fields["prompt"].(string); ok {
		prompt = value
	}
	if value, ok := fields["source"].(string); ok {
		source = value
	}
	return sessionID, prompt, source
}
