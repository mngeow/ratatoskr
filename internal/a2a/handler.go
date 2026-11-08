package a2a

// The HTTP side: authentication, routing to a participant, and the SDK's JSON-RPC handler.
//
// Order of a POST to /a2a/agents/{id}: bearer credential -> principal (401 if not accepted, 403 if a host
// credential names no current session) -> target (404 unknown, 409 ambiguous alias with candidates) ->
// the SDK handler, with the principal and target in the context. 401 and 403 come before JSON-RPC, as the
// spec leaves authentication failures to HTTP. The agent card is the one public route.
//
// A2A-Version: the SDK compares Major.Minor and reads an empty header as "0.3", so an empty header is
// refused with VersionNotSupportedError (-32009). That is the rule this server wants (section 6), and a
// test pins it so an SDK change cannot loosen it unnoticed.
//
// UNTESTED.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// SessionHeader names the acting session on a request authenticated by a host credential. The daemon sets
// it; the gateway checks that it is a CURRENT session of that host before believing it.
const SessionHeader = "X-Ratatoskr-Session"

// maxRequestBytes bounds a JSON-RPC request body.
const maxRequestBytes = 256 << 10

// SessionInfo is one live managed session.
type SessionInfo struct {
	ID   string
	Host string
	Name string
}

// SessionDirectory is the gateway's view of sessions, supplied by the gateway.
type SessionDirectory interface {
	// Resolve finds the live sessions a reference names: an exact session id, or a friendly name. More
	// than one result means the reference is ambiguous and is refused with the candidates.
	Resolve(ctx context.Context, ref string) ([]SessionInfo, error)
	// Current reports whether sessionID is a current (not ended) session of host.
	Current(ctx context.Context, host, sessionID string) (bool, error)
}

// HostAuthenticator checks a paired host's credential.
type HostAuthenticator interface {
	Authenticate(ctx context.Context, credential string) (host string, ok bool, err error)
}

// Config wires the handler.
type Config struct {
	Store    Store
	Service  *Service
	Sessions SessionDirectory
	Hosts    HostAuthenticator
	// BaseURL is the gateway's own URL as clients reach it, written into endpoint cards. It comes from
	// configuration, never from a request header; empty means cards are not served (503).
	BaseURL string
}

// Handler serves everything under AgentsPath.
type Handler struct {
	cfg     Config
	jsonrpc http.Handler
}

// NewHandler builds the handler. The SDK's User/Authenticator is left unset; scoping reads the principal
// from the context this package sets.
func NewHandler(cfg Config) (*Handler, error) {
	enforcer, err := a2asrv.NewAgentCardEnforcer(&a2a.AgentCard{
		Capabilities:       a2a.AgentCapabilities{},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
	})
	if err != nil {
		return nil, err
	}
	executor := &Executor{Svc: cfg.Service}
	tasks := NewSDKStore(cfg.Store)
	request := a2asrv.NewHandler(executor,
		a2asrv.WithTaskStore(tasks),
		a2asrv.WithCallInterceptors(enforcer))
	// SendMessage runs here, not in the SDK's execution manager (send.go). The InterceptedHandler stays,
	// so the A2A-Version check and the card enforcer still run first; every other method is the SDK's.
	intercepted, ok := request.(*a2asrv.InterceptedHandler)
	if !ok {
		return nil, fmt.Errorf("a2a: the SDK handler is a %T, not an *InterceptedHandler", request)
	}
	intercepted.Handler = &sendHandler{RequestHandler: intercepted.Handler, agent: executor, tasks: tasks}
	return &Handler{cfg: cfg, jsonrpc: a2asrv.NewJSONRPCHandler(request)}, nil
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, AgentsPath)
	if !ok || rest == "" {
		http.NotFound(w, r)
		return
	}
	if id, isCard := strings.CutSuffix(rest, CardSuffix); isCard {
		h.serveCard(w, r, id)
		return
	}
	if strings.Contains(rest, "/") {
		http.NotFound(w, r)
		return
	}
	h.serveRPC(w, r, rest)
}

func (h *Handler) serveRPC(w http.ResponseWriter, r *http.Request, ref string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	principal, status := h.authenticate(r)
	if status != 0 {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="a2a"`)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	target, status, candidates := h.resolve(r.Context(), ref)
	if status != 0 {
		writeResolveError(w, status, candidates)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	ctx := WithTarget(WithCaller(r.Context(), principal), target)
	h.jsonrpc.ServeHTTP(w, r.WithContext(ctx))
}

// authenticate derives the principal from the credential. It returns a non-zero HTTP status on failure and
// never says which part failed. A body, a query string or any other header cannot name the principal.
func (h *Handler) authenticate(r *http.Request) (Principal, int) {
	credential, ok := bearer(r.Header.Get("Authorization"))
	if !ok {
		return Principal{}, http.StatusUnauthorized
	}

	participant, found, err := h.cfg.Store.ParticipantByCredential(r.Context(), HashCredential(credential))
	switch {
	case err != nil:
		return Principal{}, http.StatusInternalServerError
	case found && participant.Revoked():
		return Principal{}, http.StatusUnauthorized
	case found:
		return Principal{Kind: KindParticipant, Name: participant.ID}, 0
	}

	host, ok, err := h.cfg.Hosts.Authenticate(r.Context(), credential)
	switch {
	case err != nil:
		return Principal{}, http.StatusInternalServerError
	case !ok:
		return Principal{}, http.StatusUnauthorized
	}
	session := r.Header.Get(SessionHeader)
	if session == "" {
		return Principal{}, http.StatusForbidden
	}
	current, err := h.cfg.Sessions.Current(r.Context(), host, session)
	switch {
	case err != nil:
		return Principal{}, http.StatusInternalServerError
	case !current:
		return Principal{}, http.StatusForbidden
	}
	return Principal{Kind: KindSession, Host: host, Session: session}, 0
}

// resolve finds the participant a route names. A registered, active service wins; otherwise the live
// sessions. status is 0 on success.
func (h *Handler) resolve(ctx context.Context, ref string) (Target, int, []SessionInfo) {
	participant, found, err := h.cfg.Store.GetParticipant(ctx, ref)
	if err != nil {
		return Target{}, http.StatusInternalServerError, nil
	}
	service := found && !participant.Revoked() && participant.URL != ""
	sessions, err := h.cfg.Sessions.Resolve(ctx, ref)
	if err != nil {
		return Target{}, http.StatusInternalServerError, nil
	}
	// A reference that names a service AND a live session is ambiguous: an enrolled name must never
	// silently capture messages meant for a session alias (or the other way round).
	if service && len(sessions) > 0 {
		candidates := append([]SessionInfo{{ID: participant.ID, Name: "(external service)"}}, sessions...)
		return Target{}, http.StatusConflict, candidates
	}
	switch {
	case service:
		return Target{Kind: TargetService, ID: participant.ID}, 0, nil
	case len(sessions) == 1:
		return Target{Kind: TargetSession, ID: sessions[0].ID, Host: sessions[0].Host}, 0, nil
	case len(sessions) > 1:
		return Target{}, http.StatusConflict, sessions
	}
	return Target{}, http.StatusNotFound, nil
}

func writeResolveError(w http.ResponseWriter, status int, candidates []SessionInfo) {
	if status != http.StatusConflict {
		http.Error(w, http.StatusText(status), status)
		return
	}
	type candidate struct {
		ID   string `json:"id"`
		Host string `json:"host"`
		Name string `json:"name"`
	}
	body := struct {
		Error      string      `json:"error"`
		Candidates []candidate `json:"candidates"`
	}{Error: "ambiguous_reference"}
	for _, c := range candidates {
		body.Candidates = append(body.Candidates, candidate{ID: c.ID, Host: c.Host, Name: c.Name})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// serveCard serves the public, read-only endpoint card.
func (h *Handler) serveCard(w http.ResponseWriter, r *http.Request, ref string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.cfg.BaseURL == "" {
		http.Error(w, "the gateway has no public URL configured", http.StatusServiceUnavailable)
		return
	}
	target, status, candidates := h.resolve(r.Context(), ref)
	if status != 0 {
		writeResolveError(w, status, candidates)
		return
	}
	name := target.ID
	if target.Kind == TargetSession {
		sessions, err := h.cfg.Sessions.Resolve(r.Context(), target.ID)
		if err == nil && len(sessions) == 1 {
			name = sessions[0].Name + "@" + sessions[0].Host
		}
	}
	body, err := json.Marshal(EndpointCard(h.cfg.BaseURL, target, name))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// bearer parses "Bearer <credential>" exactly.
func bearer(header string) (string, bool) {
	const scheme = "Bearer "
	if !strings.HasPrefix(header, scheme) {
		return "", false
	}
	token := header[len(scheme):]
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// SameCredential compares two credentials in constant time.
func SameCredential(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
