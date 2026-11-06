package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// Nothing here binds a socket: every request goes through
// server.Handler().ServeHTTP with httptest.NewRecorder.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

var gatewayNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// newGatewayServer opens a store, pairs one host, and returns a server whose clock
// is fixed.
func newGatewayServer(t *testing.T, opts Options) (*Server, *Store, string) {
	t.Helper()

	store, _ := openStore(t, false)
	credential := pairHostFrom(t, store, "laptop", gatewayNow)

	if opts.Now == nil {
		opts.Now = func() time.Time { return gatewayNow }
	}
	server := NewServer(store, opts)
	return server, store, credential
}

// call sends one request through the handler and returns the recorder.
func call(server *Server, method, path, version, credential, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}

	request := httptest.NewRequest(method, path, reader)
	if version != "" {
		request.Header.Set(protocol.WireVersionHeader, version)
	}
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// version is the header value this gateway accepts.
func version() string { return strconv.Itoa(protocol.WireVersion) }

// wireCode decodes the error code from a response body.
func wireCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var body protocol.WireError
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not a wire error: %v (%q)", err, recorder.Body.String())
	}
	return body.Code
}

func TestVersionHeaderRequiredEverywhere(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/pair"},
		{http.MethodPost, "/v1/sessions"},
		{http.MethodDelete, "/v1/sessions"},
		{http.MethodPost, "/v1/heartbeat"},
		{http.MethodGet, "/v1/unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			server, _, credential := newGatewayServer(t, Options{})

			for _, header := range []string{"", "99", "v1"} {
				recorder := call(server, tc.method, tc.path, header, credential, `{}`)
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("version %q: status = %d, want 400", header, recorder.Code)
				}
				if code := wireCode(t, recorder); code != protocol.WireErrVersion {
					t.Fatalf("version %q: code = %q, want %q", header, code, protocol.WireErrVersion)
				}
			}
		})
	}
}

func TestAuthenticationRequiredWithIdenticalBodies(t *testing.T) {
	authenticated := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/sessions"},
		{http.MethodDelete, "/v1/sessions"},
		{http.MethodPost, "/v1/heartbeat"},
	}

	headers := []string{"", "Bearer", "bearer abc", "Basic abc", "Bearer not-a-credential"}

	for _, tc := range authenticated {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			server, _, _ := newGatewayServer(t, Options{})

			var bodies []string
			for _, header := range headers {
				request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
				request.Header.Set(protocol.WireVersionHeader, version())
				if header != "" {
					request.Header.Set("Authorization", header)
				}

				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, request)

				if recorder.Code != http.StatusUnauthorized {
					t.Fatalf("header %q: status = %d, want 401", header, recorder.Code)
				}
				if code := wireCode(t, recorder); code != protocol.WireErrUnauthorized {
					t.Fatalf("header %q: code = %q", header, code)
				}
				bodies = append(bodies, recorder.Body.String())
			}

			// Every refusal looks the same: nothing says whether it nearly worked.
			for _, body := range bodies[1:] {
				if body != bodies[0] {
					t.Fatalf("refusal bodies differ: %q vs %q", bodies[0], body)
				}
			}
		})
	}
}

// TestAuthenticationReadsEveryTime is the contract's promise: unpair takes effect
// on the next request, without a restart and without a cache.
func TestAuthenticationReadsEveryTime(t *testing.T) {
	server, store, credential := newGatewayServer(t, Options{})
	ctx := context.Background()

	if recorder := call(server, http.MethodPost, "/v1/heartbeat", version(), credential, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("heartbeat before unpair = %d, want 204", recorder.Code)
	}

	if err := store.Unpair(ctx, "laptop", gatewayNow); err != nil {
		t.Fatalf("Unpair: %v", err)
	}

	if recorder := call(server, http.MethodPost, "/v1/heartbeat", version(), credential, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("heartbeat after unpair = %d, want 401", recorder.Code)
	}
}

func TestPairThenSessions(t *testing.T) {
	store, _ := openStore(t, false)
	server := NewServer(store, Options{Now: func() time.Time { return gatewayNow }})

	code, err := store.CreatePairingCode(context.Background(), "desktop", gatewayNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	body, _ := json.Marshal(protocol.PairRequest{Code: code, Host: "desktop"})
	recorder := call(server, http.MethodPost, "/v1/pair", version(), "", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("pair = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var paired protocol.PairResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &paired); err != nil {
		t.Fatalf("parse pair response: %v", err)
	}
	if paired.Host != "desktop" || paired.Credential == "" {
		t.Fatalf("pair response = %+v", paired)
	}

	// The credential works on the next call.
	sessionsBody, _ := json.Marshal(protocol.SessionsRequest{Agents: []string{"claude", "codex"}})
	recorder = call(server, http.MethodPost, "/v1/sessions", version(), paired.Credential, string(sessionsBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("sessions = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	var response protocol.SessionsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse sessions response: %v", err)
	}
	if response.Host != "desktop" {
		t.Fatalf("host = %q, want desktop", response.Host)
	}
	if len(response.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want two", response.Sessions)
	}
	if response.Sessions[0].Address != "claude@desktop" {
		t.Fatalf("address = %q, want claude@desktop", response.Sessions[0].Address)
	}
	// The directory lists both, and neither is live without a stream.
	if len(response.Directory) != 2 {
		t.Fatalf("directory = %+v, want two entries", response.Directory)
	}
	for _, entry := range response.Directory {
		if entry.Live {
			t.Fatalf("entry %+v is live although no stream exists", entry)
		}
	}
}

func TestPairFailuresLookIdentical(t *testing.T) {
	store, _ := openStore(t, false)
	server := NewServer(store, Options{Now: func() time.Time { return gatewayNow }})
	ctx := context.Background()

	code, err := store.CreatePairingCode(ctx, "desktop", gatewayNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	expired, err := store.CreatePairingCode(ctx, "old", gatewayNow.Add(-2*codeTTL))
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	bodies := []string{
		mustJSON(t, protocol.PairRequest{Code: "AAAA-BBBB-CCCC", Host: "desktop"}),
		mustJSON(t, protocol.PairRequest{Code: code, Host: "someone-else"}),
		mustJSON(t, protocol.PairRequest{Code: expired, Host: "old"}),
		mustJSON(t, protocol.PairRequest{Code: "BAD HOST", Host: "Bad Host"}),
	}

	var first string
	for i, body := range bodies {
		recorder := call(server, http.MethodPost, "/v1/pair", version(), "", body)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("case %d: status = %d, want 403", i, recorder.Code)
		}
		if code := wireCode(t, recorder); code != protocol.WireErrBadCode {
			t.Fatalf("case %d: code = %q, want %q", i, code, protocol.WireErrBadCode)
		}
		if i == 0 {
			first = recorder.Body.String()
			continue
		}
		if recorder.Body.String() != first {
			t.Fatalf("case %d body %q differs from %q", i, recorder.Body.String(), first)
		}
	}
}

func TestSessionsEndsEarlierAndTakesHostFromCredential(t *testing.T) {
	server, store, credential := newGatewayServer(t, Options{})
	ctx := context.Background()

	first, _ := json.Marshal(protocol.SessionsRequest{Agents: []string{"claude"}})
	recorder := call(server, http.MethodPost, "/v1/sessions", version(), credential, string(first))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first registration = %d: %s", recorder.Code, recorder.Body.String())
	}
	var firstResponse protocol.SessionsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &firstResponse); err != nil {
		t.Fatalf("parse: %v", err)
	}
	firstID := firstResponse.Sessions[0].SessionID

	// The body tries to name another host; the credential decides.
	second, _ := json.Marshal(map[string]any{"agents": []string{"claude"}, "host": "somebody-else"})
	recorder = call(server, http.MethodPost, "/v1/sessions", version(), credential, string(second))
	if recorder.Code != http.StatusOK {
		t.Fatalf("second registration = %d: %s", recorder.Code, recorder.Body.String())
	}
	var secondResponse protocol.SessionsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &secondResponse); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if secondResponse.Host != "laptop" {
		t.Fatalf("host = %q, want the credential's laptop", secondResponse.Host)
	}
	if secondResponse.Sessions[0].SessionID == firstID {
		t.Fatal("a new registration reused the earlier session id")
	}

	var ended int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id=? AND ended_at IS NOT NULL`, firstID).Scan(&ended); err != nil {
		t.Fatalf("read earlier session: %v", err)
	}
	if ended != 1 {
		t.Fatal("the earlier session was not ended by the new registration")
	}
}

func TestSessionsRejectsBadAgentNames(t *testing.T) {
	server, _, credential := newGatewayServer(t, Options{})

	tests := []struct {
		name   string
		agents []string
	}{
		{name: "uppercase", agents: []string{"Claude"}},
		{name: "too long", agents: []string{strings.Repeat("a", 33)}},
		{name: "empty name", agents: []string{""}},
		{name: "no names", agents: nil},
		{name: "duplicate", agents: []string{"claude", "claude"}},
		{name: "nine names", agents: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(protocol.SessionsRequest{Agents: tc.agents})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			recorder := call(server, http.MethodPost, "/v1/sessions", version(), credential, string(body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if code := wireCode(t, recorder); code != protocol.WireErrBadRequest {
				t.Fatalf("code = %q, want %q", code, protocol.WireErrBadRequest)
			}
		})
	}
}

// TestDirectoryLiveness is the only place liveness is exercised from the wire: it
// must come from HostLive, with the hub seam deciding whether a stream exists.
func TestDirectoryLiveness(t *testing.T) {
	store, _ := openStore(t, false)
	credential := pairHostFrom(t, store, "laptop", gatewayNow)
	ctx := context.Background()

	streamOpen := false
	server := NewServer(store, Options{
		Now:   func() time.Time { return gatewayNow },
		NewID: func() (string, error) { return "session-1", nil },
	})
	server.streamOpen = func(string) bool { return streamOpen }

	body, _ := json.Marshal(protocol.SessionsRequest{Agents: []string{"claude"}})
	if recorder := call(server, http.MethodPost, "/v1/sessions", version(), credential, string(body)); recorder.Code != http.StatusOK {
		t.Fatalf("registration = %d: %s", recorder.Code, recorder.Body.String())
	}

	// A stale heartbeat is not live, even with a stream open.
	if err := store.TouchHeartbeat(ctx, "laptop", gatewayNow.Add(-liveWindow-time.Second)); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	server.streamOpen = func(string) bool { return true }
	request := httptest.NewRequest(http.MethodPost, "/v1/sessions", atJSONBody(t, []string{"claude"}))
	request.Header.Set(protocol.WireVersionHeader, version())
	request.Header.Set("Authorization", "Bearer "+credential)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	var response protocol.SessionsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(response.Directory) != 1 || response.Directory[0].Live {
		t.Fatalf("a stale heartbeat was reported live: %+v", response.Directory)
	}

	// A fresh heartbeat with a stream open is live.
	if err := store.TouchHeartbeat(ctx, "laptop", gatewayNow); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/sessions", atJSONBody(t, []string{"claude"}))
	request.Header.Set(protocol.WireVersionHeader, version())
	request.Header.Set("Authorization", "Bearer "+credential)
	server.Handler().ServeHTTP(recorder, request)
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(response.Directory) != 1 || !response.Directory[0].Live {
		t.Fatalf("a fresh heartbeat with a stream was not live: %+v", response.Directory)
	}

	// No stream means not live, however fresh the heartbeat is.
	server.streamOpen = func(string) bool { return false }
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/sessions", atJSONBody(t, []string{"claude"}))
	request.Header.Set(protocol.WireVersionHeader, version())
	request.Header.Set("Authorization", "Bearer "+credential)
	server.Handler().ServeHTTP(recorder, request)
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(response.Directory) != 1 || response.Directory[0].Live {
		t.Fatalf("a host without a stream was reported live: %+v", response.Directory)
	}
}

func TestHeartbeatUsesTheGatewayClock(t *testing.T) {
	server, store, credential := newGatewayServer(t, Options{})
	ctx := context.Background()

	// The body claims a time; the gateway must ignore it.
	body := `{"at":"1999-01-01T00:00:00Z"}`
	if recorder := call(server, http.MethodPost, "/v1/heartbeat", version(), credential, body); recorder.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d, want 204: %s", recorder.Code, recorder.Body.String())
	}

	hosts, err := store.Hosts(ctx)
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("hosts = %+v", hosts)
	}
	if !hosts[0].LastHeartbeat.Equal(gatewayNow) {
		t.Fatalf("heartbeat = %v, want the gateway's clock %v", hosts[0].LastHeartbeat, gatewayNow)
	}
}

func TestEndSessionsRoute(t *testing.T) {
	server, store, credential := newGatewayServer(t, Options{})
	ctx := context.Background()

	body, _ := json.Marshal(protocol.SessionsRequest{Agents: []string{"claude"}})
	if recorder := call(server, http.MethodPost, "/v1/sessions", version(), credential, string(body)); recorder.Code != http.StatusOK {
		t.Fatalf("registration = %d", recorder.Code)
	}

	if recorder := call(server, http.MethodDelete, "/v1/sessions", version(), credential, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", recorder.Code)
	}

	if _, ok, err := store.CurrentSession(ctx, "laptop", "claude"); err != nil {
		t.Fatalf("CurrentSession: %v", err)
	} else if ok {
		t.Fatal("a session is still open after DELETE /v1/sessions")
	}
}

func TestOversizeBodyRejected(t *testing.T) {
	server, _, credential := newGatewayServer(t, Options{MaxBodyBytes: 16})

	big := `{"agents":["claude"],"padding":"` + strings.Repeat("x", 64) + `"}`
	recorder := call(server, http.MethodPost, "/v1/sessions", version(), credential, big)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversize body = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if code := wireCode(t, recorder); code != protocol.WireErrBadRequest {
		t.Fatalf("code = %q, want %q", code, protocol.WireErrBadRequest)
	}
}

// TestUnknownRouteAndMethodAreJSON: the mux's bare 404 and 405 must still be wire
// errors, because every response is JSON.
func TestUnknownRouteAndMethodAreJSON(t *testing.T) {
	server, _, credential := newGatewayServer(t, Options{})

	recorder := call(server, http.MethodGet, "/v1/nope", version(), credential, "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown route = %d, want 404", recorder.Code)
	}
	if code := wireCode(t, recorder); code != protocol.WireErrNotFound {
		t.Fatalf("code = %q, want %q", code, protocol.WireErrNotFound)
	}

	recorder = call(server, http.MethodGet, "/v1/heartbeat", version(), credential, "")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method = %d, want 405", recorder.Code)
	}
	if code := wireCode(t, recorder); code != protocol.WireErrBadRequest {
		t.Fatalf("code = %q, want %q", code, protocol.WireErrBadRequest)
	}
}

// TestErrorBodiesCarryNoSecrets: a refusal must not quote the credential, the
// code, or a database word.
func TestErrorBodiesCarryNoSecrets(t *testing.T) {
	server, store, credential := newGatewayServer(t, Options{})
	ctx := context.Background()

	code, err := store.CreatePairingCode(ctx, "desktop", gatewayNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	bodies := []string{
		call(server, http.MethodPost, "/v1/heartbeat", version(), "wrong-credential", "").Body.String(),
		call(server, http.MethodPost, "/v1/pair", version(), "", mustJSON(t, protocol.PairRequest{Code: "AAAA-BBBB-CCCC", Host: "desktop"})).Body.String(),
		call(server, http.MethodPost, "/v1/pair", version(), "", "not json").Body.String(),
	}

	for _, body := range bodies {
		for _, secret := range []string{credential, code, normaliseCode(code), "sql", "SQL"} {
			if strings.Contains(body, secret) {
				t.Fatalf("error body %q contains %q", body, secret)
			}
		}
	}
}

// mustJSON marshals a request body or fails the test.
func mustJSON(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// atJSONBody renders a sessions request body.
func atJSONBody(t *testing.T, agents []string) *bytes.Reader {
	t.Helper()
	return bytes.NewReader([]byte(mustJSON(t, protocol.SessionsRequest{Agents: agents})))
}

// TestRewriteWrapperDoesNotHideFlushOrDeadlines: the stream route (slice 3b-ii)
// flushes and sets write deadlines through http.ResponseController. A wrapper
// that hides the real writer would silently disable both, so the wrapper must
// unwrap and pass Flush through.
func TestRewriteWrapperDoesNotHideFlushOrDeadlines(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapper := &bareErrorWriter{ResponseWriter: recorder}

	if wrapper.Unwrap() != http.ResponseWriter(recorder) {
		t.Fatal("Unwrap does not return the underlying writer")
	}
	if _, ok := any(wrapper).(http.Flusher); !ok {
		t.Fatal("the wrapper does not implement http.Flusher")
	}

	if err := http.NewResponseController(wrapper).Flush(); err != nil {
		t.Fatalf("Flush through the wrapper: %v", err)
	}
	if !recorder.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
}

// TestAFirstHeartbeatPushesTheDirectory: a host that has just connected has an open
// stream but a stale heartbeat, so it is not live until its first beat. The beat
// must push a directory so other hosts see Live flip to true; a beat from a host
// that was already live must not push again.
func TestAFirstHeartbeatPushesTheDirectory(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	now := gatewayNow
	alpha := pairHostFrom(t, store, "alpha", now)
	beta := pairHostFrom(t, store, "beta", now)
	if _, err := store.StartSessions(ctx, "alpha", []string{"claude"}, now, idGenerator("a")); err != nil {
		t.Fatalf("StartSessions: %v", err)
	}
	if _, err := store.StartSessions(ctx, "beta", []string{"codex"}, now, idGenerator("b")); err != nil {
		t.Fatalf("StartSessions: %v", err)
	}

	server := NewServer(store, Options{
		Now: func() time.Time { return now }, PingEvery: time.Hour, WriteDeadline: time.Second, QueueSize: 8,
	})
	betaStream := attachStreamValue(server, "beta", 8)
	attachStreamValue(server, "alpha", 8)

	pushes := 0
	server.onDirectoryChanged = func() { pushes++ }

	// alpha has an open stream and no heartbeat yet: its first beat is a transition.
	if recorder := call(server, http.MethodPost, protocol.HeartbeatPath, version(), alpha, "{}"); recorder.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", recorder.Code)
	}
	if pushes != 1 {
		t.Fatalf("%d directory pushes after the first beat, want 1", pushes)
	}

	// A second beat from a host that is already live pushes nothing.
	if recorder := call(server, http.MethodPost, protocol.HeartbeatPath, version(), alpha, "{}"); recorder.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", recorder.Code)
	}
	if pushes != 1 {
		t.Fatalf("%d pushes after a beat from a live host, want still 1", pushes)
	}

	// A host with no open stream is not live even after a beat, so nothing is pushed.
	server.hub.detach("beta", betaStream)
	if recorder := call(server, http.MethodPost, protocol.HeartbeatPath, version(), beta, "{}"); recorder.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", recorder.Code)
	}
	if pushes != 1 {
		t.Fatalf("%d pushes for a host with no stream, want still 1", pushes)
	}
}
