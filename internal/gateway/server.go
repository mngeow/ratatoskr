package gateway

// The gateway's HTTP surface: the version and credential checks every request
// passes, and the four routes this slice owns.
//
// The stream, the hub, message routing and report frames are slice 3b-ii; this
// file deliberately carries none of them. Nothing here binds a socket unless Serve
// is called, and Serve is only compiled in this slice.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No request has been served.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// maxPairBodyBytes and maxSessionsBodyBytes bound the two small bodies. A
	// pairing code and a list of names fit in a few hundred bytes.
	maxPairBodyBytes     = 4 << 10 // UNTESTED guess
	maxSessionsBodyBytes = 4 << 10 // UNTESTED guess

	// maxHeartbeatBodyBytes is how much of a heartbeat body is read and thrown
	// away. The gateway ignores its content entirely.
	maxHeartbeatBodyBytes = 1 << 10 // UNTESTED guess

	readHeaderTimeout = 5 * time.Second  // UNTESTED guess
	idleTimeout       = 60 * time.Second // UNTESTED guess
	maxHeaderBytes    = 16 << 10         // UNTESTED guess

	// defaultMaxBodyBytes is the ceiling for any request body, checked before a
	// decoder sees it.
	defaultMaxBodyBytes = 512 << 10 // UNTESTED guess

	// The stream's defaults. They exist so the unrun tests can use tiny values.
	defaultPingEvery     = 5 * time.Second // UNTESTED guess
	defaultWriteDeadline = 5 * time.Second // UNTESTED guess
	defaultQueueSize     = 64              // UNTESTED guess

	// defaultAckWait is how long an inline send waits for the recipient host to
	// acknowledge the offer. The contract calls this the offer wait.
	defaultAckWait = protocol.AckWait // owned by the protocol package: a runtime waits protocol.SendWait for this answer

	// maxMessageBody is the largest body a runtime may send. The runtime enforces
	// the same 64 KiB on the raw body, so this is a second boundary rather than a
	// second policy.
	maxMessageBody = 64 << 10 // UNTESTED guess
)

// Options tunes the server. Zero fields take the defaults.
type Options struct {
	Now func() time.Time
	// NewID mints session ids. The default is 16 random bytes in hex.
	NewID func() (string, error)
	// MaxBodyBytes bounds any request body.
	MaxBodyBytes int64
	// PingEvery is how often a stream is pinged, and therefore how often a host's
	// pairing is rechecked.
	PingEvery time.Duration
	// WriteDeadline bounds one stream frame write.
	WriteDeadline time.Duration
	// QueueSize is the stream's frame queue depth.
	QueueSize int
	// AckWait is how long a send waits for the recipient host's acknowledgement
	// before it records delivery_uncertain.
	AckWait time.Duration
}

// Server is the gateway's HTTP surface. It holds no listener of its own: Serve
// takes one, so nothing here binds a socket by being constructed.
//
// UNTESTED.
type Server struct {
	store *Store
	opts  Options

	// streamOpen reports whether a host has an open stream. It is always false
	// until slice 3b-ii replaces it, because this slice has no hub; keeping the
	// seam here means liveness is decided in one place from the start.
	streamOpen func(host string) bool

	// onDirectoryChanged is called after a registration or an unregistration so a
	// later slice can push a new directory to open streams. Nil means nothing to
	// tell.
	onDirectoryChanged func()

	// hub is the set of open streams. It decides nothing except whether a stream
	// exists.
	hub *hub

	http *http.Server
}

// NewServer builds the HTTP surface. A nil store is a wiring bug and is documented
// rather than checked, because the signature has no error to return.
//
// UNTESTED.
func NewServer(store *Store, opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = randomID
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	if opts.PingEvery == 0 {
		opts.PingEvery = defaultPingEvery
	}
	if opts.WriteDeadline == 0 {
		opts.WriteDeadline = defaultWriteDeadline
	}
	if opts.QueueSize == 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.AckWait == 0 {
		opts.AckWait = defaultAckWait
	}

	server := &Server{
		store: store,
		opts:  opts,
		hub:   newHub(),
	}
	// Liveness reads the hub, and every registration pushes a directory.
	server.streamOpen = server.hub.open
	server.onDirectoryChanged = server.pushDirectory
	server.http = &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		IdleTimeout:       idleTimeout,
		// No ReadTimeout and no WriteTimeout: the stream route in slice 3b-ii
		// must not be cut by a response deadline, and a buffering wrapper is not
		// allowed either.
		//
		// HTTP/2 is never enabled: a non-nil empty map stops the TLS path from
		// configuring it, and ALPN offers only http/1.1, so the stream behaves as it
		// does on the tailnet.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	return server
}

// randomID mints a 128-bit id in hex from crypto/rand.
func randomID() (string, error) {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("gateway: new id: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}

// Handler returns the middleware chain and the router.
//
// Order: the protocol version first, for every request; then the router, where
// every route but pairing also requires a credential.
//
// UNTESTED.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	mux.HandleFunc("POST /v1/sessions", s.withAuth(s.handleSessions))
	mux.HandleFunc("DELETE /v1/sessions", s.withAuth(s.handleEndSessions))
	mux.HandleFunc("POST /v1/heartbeat", s.withAuth(s.handleHeartbeat))
	mux.HandleFunc("GET /v1/stream", s.withAuth(s.handleStream))
	mux.HandleFunc("POST /v1/messages", s.withAuth(s.handleSend))
	mux.HandleFunc("POST /v1/messages/{id}/ack", s.withAuth(s.handleAck))
	mux.HandleFunc("POST /v1/messages/{id}/status", s.withAuth(s.handleStatus))
	mux.HandleFunc("GET /v1/messages/{id}", s.withAuth(s.handleGetMessage))

	return s.withVersion(rewriteBareErrors(mux))
}

// Serve runs the server until Shutdown.
//
// UNTESTED.
func (s *Server) Serve(listener net.Listener) error {
	err := s.http.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gateway: serve: %w", err)
	}
	return nil
}

// Shutdown stops the server and is idempotent.
//
// UNTESTED.
func (s *Server) Shutdown(ctx context.Context) error {
	// Streams first: each handler is parked in its own loop, and cancelling is
	// what gets it out so the http server is not waiting on it.
	s.hub.closeAll()
	if err := s.http.Shutdown(ctx); err != nil {
		return fmt.Errorf("gateway: shutdown: %w", err)
	}
	return nil
}

// withVersion refuses a request that does not carry this gateway's protocol
// version. It runs before routing and before authentication, including for
// pairing, because a client that speaks the wrong version cannot be served at all.
func (s *Server) withVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(protocol.WireVersionHeader) != strconv.Itoa(protocol.WireVersion) {
			writeWireError(w, http.StatusBadRequest, protocol.WireErrVersion, fmt.Sprintf(
				"this gateway speaks protocol version %d; send it in the %s header",
				protocol.WireVersion, protocol.WireVersionHeader))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostContextKey carries the authenticated host name. It is unexported so nothing
// outside this package can forge it.
type hostContextKey struct{}

// withAuth requires a bearer credential and puts the authenticated host in the
// request context.
//
// Authentication reads the database on every request and caches nothing, which is
// what makes `unpair` refuse the very next request. A failure never says whether
// the credential existed.
func (s *Server) withAuth(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credential, ok := bearerCredential(r.Header.Get("Authorization"))
		if !ok {
			writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, "a bearer credential is required")
			return
		}

		host, err := s.store.Authenticate(r.Context(), credential)
		switch {
		case errors.Is(err, ErrUnauthorized):
			writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, "the credential was not accepted")
			return
		case err != nil:
			// A failed lookup is a server problem, not a statement about the
			// credential: say nothing about either.
			writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), hostContextKey{}, host)), host)
	}
}

// bearerCredential parses "Bearer <credential>" exactly: the scheme, one space,
// and a non-empty token. Anything else is not a credential.
func bearerCredential(header string) (string, bool) {
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

// handlePair exchanges a one-time code for a credential.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var request protocol.PairRequest
	if !decodeBody(w, r, maxPairBodyBytes, &request) {
		return
	}

	// The host name is optional on the wire: `rtk runtime pair` has no host flag,
	// so the code itself says which host it was minted for. A host that is sent
	// must still match.
	var credential string
	var err error
	host := request.Host
	if host == "" {
		credential, host, err = s.store.RedeemCodeForAnyHost(r.Context(), request.Code, s.opts.Now())
	} else {
		credential, err = s.store.RedeemCode(r.Context(), request.Code, host, s.opts.Now())
	}
	switch {
	case errors.Is(err, ErrBadCode), errors.Is(err, ErrBadHostName):
		// One answer for every way of being wrong: an unknown code, a code for
		// another host, a used code and an expired one are indistinguishable.
		writeWireError(w, http.StatusForbidden, protocol.WireErrBadCode, "that pairing code is not valid")
		return
	case err != nil:
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	}

	writeJSON(w, http.StatusOK, protocol.PairResponse{Host: host, Credential: credential})
}

// handleSessions registers this run's agents.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request, host string) {
	var request protocol.SessionsRequest
	if !decodeBody(w, r, maxSessionsBodyBytes, &request) {
		return
	}

	sessions, err := s.store.StartSessions(r.Context(), host, request.Agents, s.opts.Now(), s.opts.NewID)
	switch {
	case errors.Is(err, ErrBadAgentName), errors.Is(err, ErrBadAgentList), errors.Is(err, ErrBadHostName):
		// The names are the caller's own, so naming the rule is safe and useful.
		writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest, err.Error())
		return
	case err != nil:
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	}

	directory, err := s.directoryEntries(r.Context())
	if err != nil {
		// The registration happened, but answering with an empty directory would
		// tell the runtime that no peers exist. Fail instead.
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	}

	response := protocol.SessionsResponse{
		Host:      host,
		Sessions:  make([]protocol.SessionInfo, 0, len(sessions)),
		Directory: directory,
	}
	for _, session := range sessions {
		response.Sessions = append(response.Sessions, protocol.SessionInfo{
			Name:      session.Name,
			Address:   session.Name + "@" + host,
			SessionID: session.ID,
		})
	}

	writeJSON(w, http.StatusOK, response)
	if s.onDirectoryChanged != nil {
		s.onDirectoryChanged()
	}
}

// handleEndSessions ends this host's open sessions.
func (s *Server) handleEndSessions(w http.ResponseWriter, r *http.Request, host string) {
	if err := s.store.EndSessions(r.Context(), host, s.opts.Now()); err != nil {
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
	if s.onDirectoryChanged != nil {
		s.onDirectoryChanged()
	}
}

// handleHeartbeat records that the gateway heard from this host.
//
// The body is read and discarded: the gateway stores its own receive time, never
// anything the runtime claims.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request, host string) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxHeartbeatBodyBytes))

	// Was the host live before this beat? A host that has just connected (or was
	// away for over 15 s) has an open stream but a stale heartbeat, so it reads as
	// not live to everyone until this first beat lands. Its directory entry was
	// pushed when the stream attached, before this beat, so it would stay
	// `Live: false` for the other hosts until the next push: this beat is that push.
	// Liveness is still decided only by HostLive.
	now := s.opts.Now()
	previous, _, lastErr := s.store.LastHeartbeat(r.Context(), host)
	wasLive := lastErr == nil && HostLive(previous, s.hub.open(host), now)

	err := s.store.TouchHeartbeat(r.Context(), host, now)
	switch {
	case errors.Is(err, ErrNoSuchHost):
		// The host was unpaired between the authentication check and here.
		writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, "the credential was not accepted")
		return
	case err != nil:
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
	if !wasLive && s.hub.open(host) {
		s.directoryChanged()
	}
}

// directoryEntries renders the directory for the wire.
//
// Liveness is decided only by HostLive, with the hub seam standing in for "has an
// open stream"; no other code tests the 15 s window.
func (s *Server) directoryEntries(ctx context.Context) ([]protocol.DirectoryEntry, error) {
	rows, err := s.store.Directory(ctx)
	if err != nil {
		return nil, err
	}

	now := s.opts.Now()
	entries := make([]protocol.DirectoryEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, protocol.DirectoryEntry{
			Address: row.Name + "@" + row.Host,
			Host:    row.Host,
			Live:    HostLive(row.LastHeartbeat, s.streamOpen(row.Host), now),
		})
	}
	return entries, nil
}

// pushDirectory sends a fresh directory frame to every attached stream.
//
// It returns immediately: the read and the pushes happen in one goroutine, so a
// registration never waits on another host's socket. A failed read pushes nothing,
// and a host whose queue is full is cancelled rather than served a stale directory
// for ever.
//
// UNTESTED.
func (s *Server) pushDirectory() {
	hosts := s.hub.hosts()
	if len(hosts) == 0 {
		// Nobody is streaming: no read, no frame, nothing to do.
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), directoryReadTimeout)
		defer cancel()

		entries, err := s.directoryEntries(ctx)
		if err != nil {
			return
		}
		frame := protocol.StreamFrame{
			Type:      protocol.FrameDirectory,
			Directory: entries,
			At:        s.opts.Now(),
		}

		for _, host := range hosts {
			if err := s.hub.enqueue(host, frame); errors.Is(err, errQueueFull) {
				// The host is not reading its own frames; ending the stream is
				// the honest answer.
				s.hub.cancel(host)
			}
		}
	}()
}

// decodeBody reads one JSON body under the route's limit.
//
// Unknown fields are allowed, trailing data is not, and every failure is the same
// generic bad_request: a decoder message would describe the caller's own input
// back to it, which is not worth the risk of echoing a credential that a confused
// client put in the wrong place.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, destination any) bool {
	reader := http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(reader)

	if err := decoder.Decode(destination); err != nil {
		writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest, "the request body is not valid JSON for this route")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest, "the request body must hold exactly one JSON value")
		return false
	}
	return true
}

// writeJSON writes a JSON response with the headers every route uses.
func writeJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		// Marshalling our own response type cannot fail; if it somehow does, the
		// response says nothing about why.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal","message":"the request could not be completed"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// writeWireError writes one protocol error. The text is ours, never a Go error's.
func writeWireError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.WireError{Code: code, Message: message})
}

// bareErrorWriter turns the ServeMux's plain-text 404 and 405 into wire errors.
//
// A bare mux answers an unknown path or method with text/plain, which would break
// the contract that every response is JSON. Our own handlers always set the JSON
// content type before writing, so a 404 or 405 without it can only have come from
// the mux.
type bareErrorWriter struct {
	http.ResponseWriter
	rewritten bool
}

func (w *bareErrorWriter) WriteHeader(status int) {
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) &&
		!strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		code, message := protocol.WireErrNotFound, "no such route"
		if status == http.StatusMethodNotAllowed {
			code, message = protocol.WireErrBadRequest, "that method is not allowed on this route"
		}
		w.rewritten = true
		writeWireError(w.ResponseWriter, status, code, message)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the real writer, so the stream route
// can set write deadlines and flush through this wrapper. Without it the wrapper
// would hide both (DevOps review: wrappers hide Flusher).
func (w *bareErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush passes through for code that asserts http.Flusher directly.
func (w *bareErrorWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *bareErrorWriter) Write(payload []byte) (int, error) {
	if w.rewritten {
		// The mux's own text body is dropped; ours has already been written.
		return len(payload), nil
	}
	return w.ResponseWriter.Write(payload) //nolint:wrapcheck // pass-through.
}

// rewriteBareErrors wraps a handler so a bare mux 404 or 405 becomes JSON.
func rewriteBareErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&bareErrorWriter{ResponseWriter: w}, r)
	})
}
