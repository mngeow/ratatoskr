package server

// Removing one agent from a running server.
//
// An ended session's token and hook file must stop working: its harness may still hold
// both, and after this it can neither reach its MCP tool nor call a hook.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No agent has been removed.

import "github.com/mngeow/ratatoskr/internal/protocol"

// RemoveAgent forgets one agent: its bearer token and its entry.
//
// It reports whether the agent existed. Requests that arrive with the removed token are
// refused afterwards, because every request is resolved through the token map.
//
// UNTESTED.
func (s *Server) RemoveAgent(id protocol.AgentID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byID[id]
	if !ok {
		return false
	}

	// The token is kept only as its hash, so the entry is found by identity. The map
	// holds one entry per agent, and a ratatoskr run has at most a handful.
	for key, candidate := range s.tokens {
		if candidate == entry {
			delete(s.tokens, key)
			break
		}
	}
	delete(s.byID, id)
	return true
}
