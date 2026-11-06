package link

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// No sockets: every client test injects a fake RoundTripper.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/router"
)

// fakeTransport answers requests from a queue of canned answers and records every
// request it saw.
type fakeTransport struct {
	mu       sync.Mutex
	requests []recordedRequest
	// answers is consumed in order; the last one repeats if a test makes more
	// requests than answers.
	answers []fakeAnswer
}

type recordedRequest struct {
	method string
	path   string
	header http.Header
	body   string
}

type fakeAnswer struct {
	status  int
	body    string
	err     error
	wroteIt bool
}

// RoundTrip implements http.RoundTripper.
func (f *fakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)

	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		method: request.Method,
		path:   request.URL.Path,
		header: request.Header.Clone(),
		body:   string(body),
	})
	answer := fakeAnswer{}
	if len(f.answers) > 0 {
		index := len(f.requests) - 1
		if index >= len(f.answers) {
			index = len(f.answers) - 1
		}
		answer = f.answers[index]
	}
	f.mu.Unlock()

	if answer.wroteIt {
		if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteRequest != nil {
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}
	}
	if answer.err != nil {
		return nil, answer.err
	}

	return &http.Response{
		StatusCode: answer.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(answer.body)),
		Request:    request,
	}, nil
}

// count is how many requests the fake has seen.
func (f *fakeTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// last is the most recent recorded request.
func (f *fakeTransport) last(t *testing.T) recordedRequest {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no request was made")
	}
	return f.requests[len(f.requests)-1]
}

// fakeWait records the waits a retry loop asked for.
type fakeWait struct {
	mu    sync.Mutex
	waits []time.Duration
	// stopAt cancels the context on the given attempt index, simulating a context
	// that ends during a wait.
	failAfter int
}

func (w *fakeWait) wait(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.waits = append(w.waits, d)
	attempt := len(w.waits)
	w.mu.Unlock()

	if w.failAfter > 0 && attempt >= w.failAfter {
		return context.Canceled
	}
	return ctx.Err()
}

func (w *fakeWait) recorded() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.waits...)
}

// testPairing is a valid pairing for the fake client.
func testPairing() Pairing {
	return Pairing{Gateway: "127.0.0.1:7777", Host: "laptop", Credential: "abcDEF012_-"}
}

// newFakeClient builds a client whose transport is the fake.
func newFakeClient(t *testing.T, answers ...fakeAnswer) (*Client, *fakeTransport, *fakeWait) {
	t.Helper()

	transport := &fakeTransport{answers: answers}
	client, err := NewClient(testPairing(), Options{
		Transport: transport,
		Now:       func() time.Time { return gatewayNow() },
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	wait := &fakeWait{}
	client.wait = wait.wait
	return client, transport, wait
}

// gatewayNow is a fixed clock for heartbeat assertions.
func gatewayNow() time.Time {
	return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
}

// sendResponseJSON renders a SendResponse body.
func sendResponseJSON(t *testing.T, response protocol.SendResponse) string {
	t.Helper()

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// TestSendClassification is the whole classification table.
func TestSendClassification(t *testing.T) {
	request := protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta", Body: "hi"}

	t.Run("not written is not sent", func(t *testing.T) {
		client, _, _ := newFakeClient(t, fakeAnswer{err: errors.New("dial failed"), wroteIt: false})
		_, err := client.Send(context.Background(), request)
		if !errors.Is(err, router.ErrNotSent) {
			t.Fatalf("error = %v, want router.ErrNotSent", err)
		}
		if strings.Contains(err.Error(), "dial failed") {
			t.Fatalf("the transport error's own text leaked into %q", err.Error())
		}
	})

	t.Run("written then failed is uncertain", func(t *testing.T) {
		client, _, _ := newFakeClient(t, fakeAnswer{err: errors.New("reset"), wroteIt: true})
		_, err := client.Send(context.Background(), request)
		if errors.Is(err, router.ErrNotSent) {
			t.Fatal("a written request was reported as not sent")
		}
		if !strings.Contains(err.Error(), "may have been stored") {
			t.Fatalf("error = %q, want it to say the message may have been stored", err)
		}
	})

	for _, result := range []protocol.SendResult{
		protocol.SendGatewayUnreachable,
		protocol.SendGatewayUncertain,
		protocol.SendUnknownRecipient,
		protocol.SendDestinationUnreachable,
		protocol.SendSessionGone,
		protocol.SendDeliveryUncertain,
		protocol.SendHostStored,
	} {
		t.Run("200 "+string(result), func(t *testing.T) {
			body := sendResponseJSON(t, protocol.SendResponse{ID: "m1", Result: result, Detail: "d"})
			client, transport, _ := newFakeClient(t, fakeAnswer{status: http.StatusOK, body: body})

			response, err := client.Send(context.Background(), request)
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if response.Result != result {
				t.Fatalf("result = %q, want %q", response.Result, result)
			}
			if transport.count() != 1 {
				t.Fatalf("%d requests, want exactly 1", transport.count())
			}
		})
	}

	uncertain := []struct {
		name  string
		reply fakeAnswer
	}{
		{name: "wrong id", reply: fakeAnswer{status: http.StatusOK, body: `{"id":"other","result":"host_stored"}`}},
		{name: "empty body", reply: fakeAnswer{status: http.StatusOK, body: ""}},
		{name: "malformed json", reply: fakeAnswer{status: http.StatusOK, body: "{"}},
		{name: "result sending", reply: fakeAnswer{status: http.StatusOK, body: `{"id":"m1","result":"sending"}`}},
		{name: "unknown result", reply: fakeAnswer{status: http.StatusOK, body: `{"id":"m1","result":"maybe"}`}},
		{name: "500", reply: fakeAnswer{status: http.StatusInternalServerError, body: ""}},
		{name: "502", reply: fakeAnswer{status: http.StatusBadGateway, body: ""}},
		{name: "503", reply: fakeAnswer{status: http.StatusServiceUnavailable, body: ""}},
	}

	for _, tc := range uncertain {
		t.Run(tc.name+" is uncertain", func(t *testing.T) {
			client, transport, _ := newFakeClient(t, tc.reply)
			_, err := client.Send(context.Background(), request)
			if err == nil {
				t.Fatal("Send accepted an unusable answer")
			}
			if errors.Is(err, router.ErrNotSent) {
				t.Fatalf("an uncertain answer was reported as not sent: %v", err)
			}
			if transport.count() != 1 {
				t.Fatalf("%d requests, want exactly 1", transport.count())
			}
		})
	}

	refusals := []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{name: "redirect", status: http.StatusFound, body: "", code: "redirect"},
		{name: "bad request", status: http.StatusBadRequest, body: `{"code":"bad_request","message":"invalid to"}`, code: "bad_request"},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"code":"unauthorized","message":"no"}`, code: "unauthorized"},
		{name: "forbidden", status: http.StatusForbidden, body: `{"code":"bad_pairing_code","message":"no"}`, code: "bad_pairing_code"},
		{name: "not found", status: http.StatusNotFound, body: `{"code":"not_found","message":"no"}`, code: "not_found"},
		{name: "teapot", status: http.StatusTeapot, body: "not json", code: "http_418"},
	}

	for _, tc := range refusals {
		t.Run(tc.name+" is a refusal", func(t *testing.T) {
			client, transport, _ := newFakeClient(t, fakeAnswer{status: tc.status, body: tc.body})

			_, err := client.Send(context.Background(), request)
			if !errors.Is(err, router.ErrNotSent) {
				t.Fatalf("error = %v, want router.ErrNotSent", err)
			}
			refusal, ok := Refusal(err)
			if !ok {
				t.Fatalf("Refusal did not find a refusal in %v", err)
			}
			if refusal.Code != tc.code {
				t.Fatalf("code = %q, want %q", refusal.Code, tc.code)
			}
			if !strings.Contains(err.Error(), "refused this host") {
				t.Fatalf("error text = %q, want it to read like a refusal", err)
			}
			if transport.count() != 1 {
				t.Fatalf("%d requests, want exactly 1 (a redirect must not be followed)", transport.count())
			}
		})
	}

	t.Run("cancelled before the request", func(t *testing.T) {
		client, _, _ := newFakeClient(t, fakeAnswer{err: context.Canceled})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := client.Send(ctx, request)
		if !errors.Is(err, router.ErrNotSent) {
			t.Fatalf("error = %v, want router.ErrNotSent", err)
		}
	})
}

// TestAckAndReportRetries covers the retry rule and the wait durations.
func TestAckAndReportRetries(t *testing.T) {
	t.Run("503 then 204", func(t *testing.T) {
		client, transport, wait := newFakeClient(t,
			fakeAnswer{status: http.StatusServiceUnavailable},
			fakeAnswer{status: http.StatusNoContent},
		)

		if err := client.Ack(context.Background(), "m1", true, ""); err != nil {
			t.Fatalf("Ack: %v", err)
		}
		if transport.count() != 2 {
			t.Fatalf("%d requests, want 2", transport.count())
		}
		waits := wait.recorded()
		if len(waits) != 1 || waits[0] != 200*time.Millisecond {
			t.Fatalf("waits = %v, want one 200 ms wait", waits)
		}
	})

	t.Run("503 three times", func(t *testing.T) {
		client, transport, wait := newFakeClient(t, fakeAnswer{status: http.StatusServiceUnavailable})

		if err := client.Ack(context.Background(), "m1", true, ""); err == nil {
			t.Fatal("Ack accepted three 503s")
		}
		if transport.count() != 3 {
			t.Fatalf("%d requests, want 3", transport.count())
		}
		if waits := wait.recorded(); len(waits) != 2 || waits[0] != 200*time.Millisecond || waits[1] != 400*time.Millisecond {
			t.Fatalf("waits = %v, want 200 ms then 400 ms", waits)
		}
	})

	t.Run("400 is final", func(t *testing.T) {
		client, transport, wait := newFakeClient(t, fakeAnswer{status: http.StatusBadRequest, body: `{"code":"bad_request","message":"no"}`})

		err := client.Ack(context.Background(), "m1", true, "")
		if err == nil {
			t.Fatal("Ack accepted a 400")
		}
		if _, ok := Refusal(err); !ok {
			t.Fatalf("Ack's 400 = %v, want a refusal", err)
		}
		if transport.count() != 1 {
			t.Fatalf("%d requests, want 1", transport.count())
		}
		if len(wait.recorded()) != 0 {
			t.Fatal("a refusal was retried")
		}
	})

	t.Run("transport error then 204", func(t *testing.T) {
		client, transport, _ := newFakeClient(t,
			fakeAnswer{err: errors.New("reset"), wroteIt: true},
			fakeAnswer{status: http.StatusNoContent},
		)

		if err := client.Report(context.Background(), "m1", protocol.StatusReport{State: protocol.MessageHeld}); err != nil {
			t.Fatalf("Report: %v", err)
		}
		if transport.count() != 2 {
			t.Fatalf("%d requests, want 2", transport.count())
		}
	})

	t.Run("cancelled during the wait", func(t *testing.T) {
		client, transport, wait := newFakeClient(t, fakeAnswer{status: http.StatusServiceUnavailable})
		wait.failAfter = 1

		err := client.Ack(context.Background(), "m1", true, "")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if transport.count() != 1 {
			t.Fatalf("%d requests, want 1: a cancelled wait must not send again", transport.count())
		}
	})

	t.Run("bad id makes no request", func(t *testing.T) {
		client, transport, _ := newFakeClient(t, fakeAnswer{status: http.StatusNoContent})

		if err := client.Ack(context.Background(), "bad id", true, ""); err == nil {
			t.Fatal("Ack accepted an invalid id")
		}
		if transport.count() != 0 {
			t.Fatalf("%d requests, want 0", transport.count())
		}
	})
}

// TestRequestsCarryVersionAndCredential: every request is authenticated and
// versioned, and no error text ever echoes the credential.
func TestRequestsCarryVersionAndCredential(t *testing.T) {
	client, transport, _ := newFakeClient(t,
		fakeAnswer{status: http.StatusOK, body: `{"id":"m1","result":"host_stored"}`},
		fakeAnswer{status: http.StatusNoContent},
	)

	request := protocol.SendRequest{ID: "m1", From: "claude", To: "codex@beta", Body: "hi"}
	_, err := client.Send(context.Background(), request)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	recorded := transport.last(t)
	if recorded.method != http.MethodPost || recorded.path != "/v1/messages" {
		t.Fatalf("request = %s %s", recorded.method, recorded.path)
	}
	if got := recorded.header.Get(protocol.WireVersionHeader); got != "1" {
		t.Fatalf("version header = %q, want 1", got)
	}
	if got := recorded.header.Get("Authorization"); got != "Bearer "+testPairing().Credential {
		t.Fatalf("Authorization = %q", got)
	}
	// The body is the SendRequest exactly.
	var sent protocol.SendRequest
	if err := json.Unmarshal([]byte(recorded.body), &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent.ID != request.ID || sent.Body != request.Body || sent.To != request.To {
		t.Fatalf("sent body = %+v", sent)
	}

	// No error in this flow mentions the credential.
	if err != nil && strings.Contains(err.Error(), testPairing().Credential) {
		t.Fatalf("error text carries the credential: %v", err)
	}
}

// TestLookupUnknownMessage: a 404 is the sentinel, not a generic failure.
func TestLookupUnknownMessage(t *testing.T) {
	client, _, _ := newFakeClient(t, fakeAnswer{status: http.StatusNotFound, body: `{"code":"not_found","message":"no such message"}`})

	if _, err := client.Lookup(context.Background(), "m1"); !errors.Is(err, ErrUnknownMessage) {
		t.Fatalf("Lookup error = %v, want ErrUnknownMessage", err)
	}
}

// TestConnectAddEndHeartbeat checks methods, paths and bodies of the protocol 2 calls.
func TestConnectAddEndHeartbeat(t *testing.T) {
	client, transport, _ := newFakeClient(t,
		fakeAnswer{status: http.StatusOK, body: `{"host":"laptop","directory":[]}`},
		fakeAnswer{status: http.StatusOK, body: `{"host":"laptop","session":{"name":"claude","address":"claude@laptop","session_id":"s1"},"directory":[]}`},
		fakeAnswer{status: http.StatusNoContent},
		fakeAnswer{status: http.StatusNoContent},
	)

	connected, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if connected.Host != "laptop" {
		t.Fatalf("connect = %+v", connected)
	}
	recorded := transport.last(t)
	if recorded.method != http.MethodPost || recorded.path != protocol.ConnectPath {
		t.Fatalf("connect request = %s %s", recorded.method, recorded.path)
	}
	if recorded.body != "" {
		t.Fatalf("connect sent a body: %q", recorded.body)
	}

	added, err := client.AddSession(context.Background(), "claude")
	if err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	if added.Host != "laptop" || added.Session.SessionID != "s1" || added.Session.Address != "claude@laptop" {
		t.Fatalf("add = %+v", added)
	}
	recorded = transport.last(t)
	if recorded.method != http.MethodPost || recorded.path != protocol.SessionsPath {
		t.Fatalf("add request = %s %s", recorded.method, recorded.path)
	}
	var addBody protocol.AddSessionRequest
	if err := json.Unmarshal([]byte(recorded.body), &addBody); err != nil {
		t.Fatalf("add body is not JSON: %v", err)
	}
	if addBody.Name != "claude" {
		t.Fatalf("add body = %+v", addBody)
	}

	if err := client.EndSession(context.Background(), "claude"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	recorded = transport.last(t)
	if recorded.method != http.MethodDelete || recorded.path != protocol.SessionsPath+"/claude" {
		t.Fatalf("end request = %s %s", recorded.method, recorded.path)
	}

	if err := client.Unregister(context.Background()); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	recorded = transport.last(t)
	if recorded.method != http.MethodDelete || recorded.path != "/v1/sessions" {
		t.Fatalf("unregister request = %s %s", recorded.method, recorded.path)
	}

	if err := client.Heartbeat(context.Background()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	recorded = transport.last(t)
	if recorded.method != http.MethodPost || recorded.path != protocol.HeartbeatPath {
		t.Fatalf("heartbeat request = %s %s", recorded.method, recorded.path)
	}
	// The body carries only the timestamp.
	var heartbeat map[string]any
	if err := json.Unmarshal([]byte(recorded.body), &heartbeat); err != nil {
		t.Fatalf("heartbeat body is not JSON: %v", err)
	}
	if len(heartbeat) != 1 || heartbeat["at"] == nil {
		t.Fatalf("heartbeat body = %v, want only at", heartbeat)
	}
}

// TestConnectAddEndRefusals: a 4xx becomes a refusal with its code, an unreadable answer
// is its own error, and a server error is named by status only.
func TestConnectAddEndRefusals(t *testing.T) {
	t.Run("a refused connect carries the code", func(t *testing.T) {
		client, _, _ := newFakeClient(t, fakeAnswer{
			status: http.StatusForbidden,
			body:   `{"code":"unauthorized","message":"this host is not paired"}`,
		})
		_, err := client.Connect(context.Background())
		refusal, ok := Refusal(err)
		if !ok || refusal.Code == "" {
			t.Fatalf("Connect error = %v, want a refusal", err)
		}
	})

	t.Run("an unreadable add answer is refused", func(t *testing.T) {
		client, _, _ := newFakeClient(t, fakeAnswer{status: http.StatusOK, body: `not json`})
		if _, err := client.AddSession(context.Background(), "claude"); !errors.Is(err, errUnreadableAnswer) {
			t.Fatalf("AddSession error = %v, want errUnreadableAnswer", err)
		}
	})

	t.Run("a server error on end names no body", func(t *testing.T) {
		client, _, _ := newFakeClient(t, fakeAnswer{status: http.StatusInternalServerError, body: `{"code":"internal","message":"x"}`})
		err := client.EndSession(context.Background(), "claude")
		if err == nil {
			t.Fatal("EndSession accepted a 500")
		}
		if strings.Contains(err.Error(), "internal") {
			t.Fatalf("error quotes the body: %v", err)
		}
	})

	t.Run("ending an unknown session is still success", func(t *testing.T) {
		client, _, _ := newFakeClient(t, fakeAnswer{status: http.StatusNoContent})
		if err := client.EndSession(context.Background(), "claude"); err != nil {
			t.Fatalf("EndSession: %v", err)
		}
	})
}

// TestAddress: the network name is the local name on this host.
func TestAddress(t *testing.T) {
	client, _, _ := newFakeClient(t)
	if got := client.Address("claude"); got != "claude@laptop" {
		t.Fatalf("Address = %q, want claude@laptop", got)
	}
	if client.Host() != "laptop" || client.GatewayAddr() != "127.0.0.1:7777" {
		t.Fatalf("host/gateway = %q/%q", client.Host(), client.GatewayAddr())
	}
}

// TestTransportIsLockedDown: no proxy, no redirects, bounded header timeout.
func TestTransportIsLockedDown(t *testing.T) {
	client, err := NewClient(testPairing(), Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", client.http.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("the transport has a proxy function")
	}
	if !transport.DisableKeepAlives {
		t.Fatal("keep-alives are on: a reused connection the gateway closed would fail after the request was written and turn a certain \"not sent\" into \"may have arrived\"")
	}
	if !transport.DisableCompression {
		t.Fatal("compression is enabled")
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("header timeout = %v, want none: POST /v1/messages is answered only when the gateway's offer wait ends, so a header timeout shorter than the send budget gives up just as the gateway answers", transport.ResponseHeaderTimeout)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("HTTP/2 upgrade is enabled")
	}
	if err := client.http.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
	if client.http.Timeout != 0 {
		t.Fatalf("client timeout = %v, want none", client.http.Timeout)
	}

	// A proxy environment variable must not change any of that.
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	other, err := NewClient(testPairing(), Options{})
	if err != nil {
		t.Fatalf("NewClient with HTTP_PROXY set: %v", err)
	}
	otherTransport, ok := other.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", other.http.Transport)
	}
	if otherTransport.Proxy != nil {
		t.Fatal("HTTP_PROXY made the transport use a proxy")
	}
}

// TestBodyLimit: an answer larger than the cap is an error, not a partial decode.
func TestBodyLimit(t *testing.T) {
	big := strings.Repeat("x", maxResponseBytes+10)
	client, _, _ := newFakeClient(t, fakeAnswer{status: http.StatusOK, body: big})

	_, err := client.Lookup(context.Background(), "m1")
	if err == nil {
		t.Fatal("Lookup accepted an oversized answer")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("answer")) {
		t.Fatalf("error = %q", err)
	}
}

// TestSendBudgetOrdering states, in one place, the ordering the send path depends
// on: the runtime's send wait is longer than the gateway's offer wait, with room
// for connect and database time, and no request transport imposes a response-header
// wait of its own. It restates the compile-time check in protocol so a reader of the
// link tests sees the constraint.
func TestSendBudgetOrdering(t *testing.T) {
	if protocol.SendWait <= protocol.AckWait {
		t.Fatalf("SendWait %v must exceed AckWait %v", protocol.SendWait, protocol.AckWait)
	}
	if protocol.SendWait-protocol.AckWait < 2*time.Second {
		t.Fatalf("SendWait %v leaves less than 2 s beyond AckWait %v", protocol.SendWait, protocol.AckWait)
	}

	client, err := NewClient(testPairing(), Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", client.http.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 || transport.ExpectContinueTimeout != 0 {
		t.Fatalf("the request transport has a header wait (%v, %v): a send would give up before the gateway's answer",
			transport.ResponseHeaderTimeout, transport.ExpectContinueTimeout)
	}
	if requestTimeout >= protocol.SendWait {
		t.Fatalf("requestTimeout %v must stay below SendWait %v", requestTimeout, protocol.SendWait)
	}
}

// TestCauseKindTLSRows: handshake failures are named without a URL, an address or a
// certificate. UNTESTED: written, not run.
func TestCauseKindTLSRows(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "a pin mismatch says to re-pair",
			err:  &url.Error{Op: "Post", URL: "https://192.168.1.20:7777/v1/send", Err: errPinMismatch},
			want: "the gateway's certificate does not match the pinned fingerprint: it was regenerated, or something else answered at this address; re-pair after checking the fingerprint on the gateway machine",
		},
		{
			name: "a plain-HTTP answer on a TLS port",
			err:  &url.Error{Op: "Post", URL: "https://192.168.1.20:7777/v1/send", Err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}},
			want: "the gateway did not answer with TLS",
		},
		{
			// What net/http returns when a pinned runtime meets a plain-HTTP gateway: the
			// gateway answers the ClientHello with an HTTP 400, and the client replaces
			// the TLS error with ErrSchemeMismatch.
			name: "a plain-HTTP gateway answering a ClientHello",
			err:  &url.Error{Op: "Post", URL: "https://192.168.1.20:7777/v1/send", Err: http.ErrSchemeMismatch},
			want: "the gateway did not answer with TLS",
		},
		{
			name: "a TLS alert",
			err:  &url.Error{Op: "Post", URL: "https://192.168.1.20:7777/v1/send", Err: tls.AlertError(40)},
			want: "the TLS handshake failed",
		},
		{
			name: "a wrapped handshake text",
			err:  &url.Error{Op: "Post", URL: "https://192.168.1.20:7777/v1/send", Err: errors.New("tls: server selected unsupported protocol version 301")},
			want: "the TLS handshake failed",
		},
		{
			name: "an unrelated error is still opaque",
			err:  errors.New("something else entirely"),
			want: "the connection failed",
		},
		{
			name: "a refusal by connection still reads as before",
			err:  syscall.ECONNREFUSED,
			want: "connection refused",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := causeKind(tc.err)
			if got != tc.want {
				t.Fatalf("causeKind = %q, want %q", got, tc.want)
			}
			for _, forbidden := range []string{"192.168", "https://", "sha256:", "CERTIFICATE"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("causeKind text %q contains %q", got, forbidden)
				}
			}
		})
	}
}

// TestNewClientBuildsThePinnedConfig: https when there is a fingerprint, and a
// malformed pin is refused even though a test transport is injected.
func TestNewClientBuildsThePinnedConfig(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("ab", 32)
	leafPairing := Pairing{
		Gateway:     "192.168.1.20:7777",
		Host:        "mac",
		Credential:  "abcDEF012_-",
		Fingerprint: fingerprint,
	}

	t.Run("https pairing carries the pin", func(t *testing.T) {
		client, err := NewClient(leafPairing, Options{})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if client.baseURL != "https://192.168.1.20:7777" {
			t.Fatalf("baseURL = %q, want https", client.baseURL)
		}
		if client.tls == nil || client.tls.VerifyConnection == nil {
			t.Fatal("the client has no pinned configuration")
		}
	})

	t.Run("a malformed pin is refused even with an injected transport", func(t *testing.T) {
		bad := leafPairing
		bad.Fingerprint = "sha256:abc"
		if _, err := NewClient(bad, Options{Transport: &fakeTransport{}}); err == nil {
			t.Fatal("NewClient accepted a malformed fingerprint behind an injected transport")
		}
	})

	t.Run("a plain pairing has no TLS config", func(t *testing.T) {
		client, err := NewClient(Pairing{Gateway: "127.0.0.1:7777", Host: "mac", Credential: "abc"}, Options{})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if client.baseURL != "http://127.0.0.1:7777" {
			t.Fatalf("baseURL = %q, want http", client.baseURL)
		}
		if client.tls != nil {
			t.Fatal("a plain pairing built a TLS configuration")
		}
	})

	t.Run("a LAN plain pairing keeps http", func(t *testing.T) {
		client, err := NewClient(Pairing{Gateway: "192.168.1.20:7777", Host: "mac", Credential: "abc", InsecureHTTP: true}, Options{})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if client.baseURL != "http://192.168.1.20:7777" {
			t.Fatalf("baseURL = %q, want http", client.baseURL)
		}
	})
}
