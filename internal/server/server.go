// Package server is the localhost HTTP surface: the MCP tool an agent uses to
// message a peer, the hook endpoint a harness reports to, and the `rtk hook`
// client that reports those events.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No request has been served.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Inbox is what the server may do to the router. Defined here because this
// package is the consumer; internal/router implements it. The server never
// imports the router: a refusal crosses the seam as an error with a
// SendReason() string method.
type Inbox interface {
	Send(ctx context.Context, from protocol.AgentID, to, body, replyTo string) (id string, err error)
	HookEvent(ctx context.Context, from protocol.AgentID, ev protocol.HookEvent)
}

// Options tunes the HTTP server. Zero fields take the defaults below.
type Options struct {
	ReadHeaderTimeout time.Duration
	MaxBodyBytes      int64
}

const (
	defaultReadHeaderTimeout = 5 * time.Second // UNTESTED guess
	// defaultMaxBodyBytes is generous because a hook payload carries the user's
	// prompt, and a long pasted prompt must not be mistaken for a missing
	// session id.
	defaultMaxBodyBytes = 8 << 20 // UNTESTED guess

	loopbackAddr = "127.0.0.1:0"

	// tokenBytes is the entropy behind one agent's bearer token.
	tokenBytes = 32
)

// agentEntry is one registered agent: who it is, and the MCP server built for it.
type agentEntry struct {
	id   protocol.AgentID
	name string
	srv  *mcpServer
}

// Server is the HTTP surface. It listens on loopback the moment it is created,
// so its address is known before anything is served.
//
// UNTESTED.
type Server struct {
	inbox Inbox
	opts  Options

	ln   net.Listener
	http *http.Server
	mcp  http.Handler

	mu     sync.RWMutex
	tokens map[[sha256.Size]byte]*agentEntry
	byID   map[protocol.AgentID]*agentEntry

	addr string

	// discovery is the daemon's directory source (SetDiscovery), read on every discover call because the
	// daemon builds its network and graph services after this server exists.
	discovery DiscoverySource

	shutdownOnce sync.Once
	shutdownErr  error
}

// New binds a loopback listener immediately and builds the handler. A failure
// here means no port could be taken or the listener was not loopback, and
// nothing was started.
//
// UNTESTED.
func New(in Inbox, opts Options) (*Server, error) {
	if in == nil {
		return nil, errors.New("server: New: nil Inbox")
	}
	if opts.ReadHeaderTimeout == 0 {
		opts.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}

	listener, err := net.Listen("tcp", loopbackAddr)
	if err != nil {
		return nil, fmt.Errorf("server: listen on loopback: %w", err)
	}

	addr := listener.Addr().String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !isLoopbackHost(host) {
		_ = listener.Close()
		return nil, fmt.Errorf("server: listener %q is not loopback", addr)
	}

	s := &Server{
		inbox:  in,
		opts:   opts,
		ln:     listener,
		addr:   addr,
		tokens: make(map[[sha256.Size]byte]*agentEntry),
		byID:   make(map[protocol.AgentID]*agentEntry),
	}
	s.mcp = s.mcpHandler()
	s.http = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
	}
	return s, nil
}

// SetDiscovery gives the discovery tool its source. Until it is called the tool answers that discovery is
// unavailable. Safe to call while serving.
//
// UNTESTED.
func (s *Server) SetDiscovery(source DiscoverySource) {
	s.mu.Lock()
	s.discovery = source
	s.mu.Unlock()
}

func (s *Server) discoverySource() DiscoverySource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.discovery
}

// Addr is the address the server is listening on, as "127.0.0.1:<port>".
func (s *Server) Addr() string { return s.addr }

// AddAgent mints a token for one agent and builds its MCP server.
//
// The token is returned once and never stored in clear: the map is keyed by its
// SHA-256, so a presented token is looked up by hash and a partial match tells an
// attacker nothing. An empty token means the agent was not registered - either an
// empty ID or name, a token could not be generated, or that ID already has one.
// The last case matters: a second registration must not silently rotate a token
// that a running harness already holds.
//
// UNTESTED.
func (s *Server) AddAgent(id protocol.AgentID, name string, peers []string) (token string) {
	return s.addAgent(id, name, "", peers, false)
}

// AddConnectedAgent mints a token for one agent on a gateway-backed network.
//
// It differs from AddAgent in exactly one way: the agent's MCP server is built in
// connected mode, so its instructions and results speak in network names. The token,
// the maps, the duplicate refusal and the hook file stem still use the local name.
//
// UNTESTED.
func (s *Server) AddConnectedAgent(id protocol.AgentID, name, address string, peers []string) (token string) {
	if address == "" {
		return ""
	}
	return s.addAgent(id, name, address, peers, true)
}

// addAgent is the one registration path: validate, mint, install, refuse a duplicate.
func (s *Server) addAgent(id protocol.AgentID, name, address string, peers []string, connected bool) (token string) {
	if id == "" || name == "" {
		return ""
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token = hex.EncodeToString(raw)
	key := sha256.Sum256([]byte(token))

	entry := &agentEntry{
		id:   id,
		name: name,
		srv:  newAgentServer(id, name, address, peers, connected, s.inbox, s.discoverySource),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[id]; exists {
		return ""
	}
	s.tokens[key] = entry
	s.byID[id] = entry
	return token
}

// Serve runs the server until Shutdown. It returns nil after a clean shutdown.
//
// UNTESTED.
func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("server: serve: %w", err)
	}
	return nil
}

// Shutdown stops accepting, waits for handlers up to ctx, and is idempotent.
//
// UNTESTED.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.shutdownErr = s.http.Shutdown(ctx) //nolint:wrapcheck // returned as is to the caller.
	})
	return s.shutdownErr
}

// routes is the whole surface: the two guards, then authentication, then the
// route. An unknown path is answered only to a caller that presented a valid
// token, so probing the port without one learns nothing.
func (s *Server) routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr {
			// DNS rebinding: a page that resolves its own name to 127.0.0.1 would
			// arrive with a Host we never handed out.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("Origin") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		entry, ok := s.authenticate(r.Header.Get("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		r = r.WithContext(withAgent(r.Context(), entry.id))
		r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxBodyBytes)

		switch {
		case r.URL.Path == "/mcp":
			s.mcp.ServeHTTP(w, r)
		case r.URL.Path == "/hook" && r.Method == http.MethodPost:
			s.serveHook(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// authenticate resolves a bearer token to an agent. The comparison is a map
// lookup on the token's SHA-256, so its cost does not depend on how much of a
// guess is right.
func (s *Server) authenticate(header string) (*agentEntry, bool) {
	token, ok := bearerToken(header)
	if !ok {
		return nil, false
	}
	key := sha256.Sum256([]byte(token))

	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, found := s.tokens[key]
	return entry, found
}

// bearerToken parses an Authorization header. The scheme is matched
// case-insensitively, as HTTP specifies, and nothing else is accepted.
func bearerToken(header string) (string, bool) {
	const scheme = "bearer "
	if len(header) <= len(scheme) {
		return "", false
	}
	if len(header) < len(scheme) || !equalFoldASCII(header[:len(scheme)], scheme) {
		return "", false
	}
	token := header[len(scheme):]
	if token == "" {
		return "", false
	}
	return token, true
}

// equalFoldASCII compares two ASCII strings without allocating, and without
// unicode folding that no HTTP scheme needs.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// isLoopbackHost reports whether a host is the IPv4 or IPv6 loopback literal.
func isLoopbackHost(host string) bool {
	if host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// agentContextKey types the context value that carries the authenticated agent.
type agentContextKey struct{}

func withAgent(ctx context.Context, id protocol.AgentID) context.Context {
	return context.WithValue(ctx, agentContextKey{}, id)
}

// agentFromContext returns the agent that the request's token resolved to. It is
// the only source of a sender's identity; a tool argument never names it.
func agentFromContext(ctx context.Context) (protocol.AgentID, bool) {
	id, ok := ctx.Value(agentContextKey{}).(protocol.AgentID)
	return id, ok && id != ""
}

// protocolKind reports whether an event name is one this build understands.
// Unknown names are accepted and ignored, so a newer harness hook cannot break
// an older server.
func protocolKind(event string) (protocol.HookKind, bool) {
	switch kind := protocol.HookKind(event); kind {
	case protocol.HookSessionStart,
		protocol.HookUserPromptSubmit,
		protocol.HookStop,
		protocol.HookPermissionRequest,
		protocol.HookPostToolUse:
		return kind, true
	default:
		return "", false
	}
}
