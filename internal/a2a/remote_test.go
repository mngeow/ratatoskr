package a2a

// Untested: under GR-3 nothing here has been run. No socket is opened: the factory returns an
// *http.Client whose Transport is a fake in this file.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
)

// fakeAnswer is one canned reply, or one canned failure.
type fakeAnswer struct {
	status  int
	body    string
	err     error
	written bool // invoke WroteRequest before returning err, i.e. fail after the write
}

// fakeTransport answers from a queue and records what it was asked.
type fakeTransport struct {
	mu      sync.Mutex
	answers []fakeAnswer
	seen    []*http.Request
	bodies  [][]byte
}

// RoundTrip answers one request.
func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	var answer fakeAnswer
	if len(f.answers) > 0 {
		answer = f.answers[0]
		f.answers = f.answers[1:]
	}
	f.seen = append(f.seen, req.Clone(req.Context()))
	if req.Body != nil {
		// The SDK owns the body; reading it here is what lets a test see what was sent.
		body, err := io.ReadAll(req.Body)
		if err == nil {
			f.bodies = append(f.bodies, body)
		}
		req.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	f.mu.Unlock()

	if answer.status == 0 && answer.body == "" && answer.err == nil {
		// No answer queued: an empty 200 is the least surprising default.
		answer = fakeAnswer{status: http.StatusOK, body: "{}"}
	}

	if answer.err != nil {
		if answer.written {
			if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.WroteRequest != nil {
				trace.WroteRequest(httptrace.WroteRequestInfo{})
			}
		}
		return nil, answer.err
	}

	return &http.Response{
		StatusCode: answer.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(answer.body)),
		Request:    req,
	}, nil
}

// requests returns what the fake was asked.
func (f *fakeTransport) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.seen...)
}

// fakeFactory builds a Remote whose factory returns a client over the given fake.
func fakeFactory(t *testing.T, fake *fakeTransport, factoryErr error) *Remote {
	t.Helper()
	return &Remote{Factory: func(ctx context.Context, dest Destination) (*http.Client, error) {
		if factoryErr != nil {
			return nil, factoryErr
		}
		return &http.Client{Transport: fake}, nil
	}}
}

// cardJSON is a valid card pointing at destinationURL.
func cardJSON(destinationURL string) string {
	return fmt.Sprintf(`{
		"name": "reference",
		"description": "counts text",
		"version": "1",
		"supportedInterfaces": [{"url": %q, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}],
		"capabilities": {},
		"defaultInputModes": ["text/plain"],
		"defaultOutputModes": ["text/plain"],
		"skills": [{"id": "count", "name": "Count text", "description": "counts"}]
	}`, destinationURL)
}

// testDestination is one registered destination.
var testDestination = Destination{URL: "http://127.0.0.1:7790/"}

// TestFetchCardValid: a good card is returned canonical, declaring no authentication.
func TestFetchCardValid(t *testing.T) {
	fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: cardJSON(testDestination.URL)}}}
	remote := fakeFactory(t, fake, nil)

	card, auth, err := remote.FetchCard(context.Background(), testDestination)
	if err != nil {
		t.Fatalf("FetchCard: %v", err)
	}
	if auth != CardAuthNone {
		t.Fatal("a card without security declared authentication")
	}
	if !strings.Contains(card, `"name":"reference"`) {
		t.Fatalf("card = %q, want canonical JSON", card)
	}

	requests := fake.requests()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want 1", len(requests))
	}
	if got := requests[0].URL.Path; got != "/.well-known/agent-card.json" {
		t.Fatalf("path = %q, want the well-known card path", got)
	}
	if got := requests[0].URL.Host; got != "127.0.0.1:7790" {
		t.Fatalf("host = %q", got)
	}
}

// TestFetchCardClassifiesAuth: only an HTTP Bearer requirement the gateway can meet is Bearer; every other
// scheme is unsupported; no security is none. The card carries the standard JSON names (httpAuthSecurityScheme,
// securityRequirements[].schemes.<name>).
func TestFetchCardClassifiesAuth(t *testing.T) {
	withSecurity := func(security string) string {
		return strings.Replace(cardJSON(testDestination.URL), `"capabilities": {},`, `"capabilities": {}, `+security+`,`, 1)
	}
	bearer := `"securitySchemes": {"bearer": {"httpAuthSecurityScheme": {"scheme": "Bearer"}}}`
	tests := []struct {
		name string
		body string
		want CardAuth
	}{
		{"none", cardJSON(testDestination.URL), CardAuthNone},
		{"bearer with a requirement", withSecurity(bearer + `, "securityRequirements": [{"schemes": {"bearer": {}}}]`), CardAuthBearer},
		{"bearer, lower-case scheme name", withSecurity(`"securitySchemes": {"b": {"httpAuthSecurityScheme": {"scheme": "bearer"}}}, "securityRequirements": [{"schemes": {"b": {}}}]`), CardAuthBearer},
		{"bearer declared but never required", withSecurity(bearer), CardAuthUnsupported},
		{"basic", withSecurity(`"securitySchemes": {"b": {"httpAuthSecurityScheme": {"scheme": "Basic"}}}, "securityRequirements": [{"schemes": {"b": {}}}]`), CardAuthUnsupported},
		{"api key", withSecurity(`"securitySchemes": {"k": {"apiKeySecurityScheme": {"location": "header", "name": "X-Key"}}}, "securityRequirements": [{"schemes": {"k": {}}}]`), CardAuthUnsupported},
		{"oauth2", withSecurity(`"securitySchemes": {"o": {"oauth2SecurityScheme": {"flows": {}}}}, "securityRequirements": [{"schemes": {"o": {"list": ["read"]}}}]`), CardAuthUnsupported},
		{"mtls", withSecurity(`"securitySchemes": {"m": {"mtlsSecurityScheme": {}}}, "securityRequirements": [{"schemes": {"m": {}}}]`), CardAuthUnsupported},
		{"bearer AND api key together cannot be met", withSecurity(bearer + `, "securityRequirements": [{"schemes": {"bearer": {}, "k": {}}}]`), CardAuthUnsupported},
		{"an OR whose second option is bearer", withSecurity(bearer + `, "securityRequirements": [{"schemes": {"k": {}}}, {"schemes": {"bearer": {}}}]`), CardAuthBearer},
		{"a requirement naming an undefined scheme", withSecurity(`"securityRequirements": [{"schemes": {"ghost": {}}}]`), CardAuthUnsupported},
		{"bearer with scopes", withSecurity(bearer + `, "securityRequirements": [{"schemes": {"bearer": {"list": ["x"]}}}]`), CardAuthUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: tc.body}}}
			card, got, err := fakeFactory(t, fake, nil).FetchCard(context.Background(), testDestination)
			if err != nil || card == "" {
				t.Fatalf("FetchCard: %q %v", card, err)
			}
			if got != tc.want {
				t.Fatalf("auth = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestServiceAuthRefusalIsDefiniteAndCarriesNoSecret: a 401 or 403 to a send is ErrServiceAuth wrapped in
// ErrNotDelivered (never "uncertain"), a 401 or 403 to a read is ErrServiceAuth, and no error text contains
// the credential or the header, even when the transport's own error would.
func TestServiceAuthRefusalIsDefiniteAndCarriesNoSecret(t *testing.T) {
	const secret = "s3cr3t-outbound-VALUE"
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		fake := &fakeTransport{answers: []fakeAnswer{{status: status, body: "nope " + secret}}}
		remote := fakeFactory(t, fake, nil)

		_, err := remote.Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "hi"})
		if !errors.Is(err, ErrServiceAuth) || !errors.Is(err, ErrNotDelivered) || errors.Is(err, ErrUncertain) {
			t.Fatalf("send %d = %v, want ErrServiceAuth+ErrNotDelivered and not uncertain", status, err)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(strings.ToLower(err.Error()), "authorization") {
			t.Fatalf("send %d error leaks: %q", status, err)
		}

		fake = &fakeTransport{answers: []fakeAnswer{{status: status, body: "nope " + secret}}}
		_, err = fakeFactory(t, fake, nil).Get(context.Background(), testDestination, "r-1")
		if !errors.Is(err, ErrServiceAuth) || strings.Contains(err.Error(), secret) {
			t.Fatalf("get %d = %v", status, err)
		}
	}

	// A transport error that itself quotes the header must not reach the caller.
	fake := &fakeTransport{answers: []fakeAnswer{{err: errors.New(`Post "http://x": Authorization: Bearer ` + secret), written: true}}}
	_, err := fakeFactory(t, fake, nil).Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "hi"})
	if !errors.Is(err, ErrUncertain) || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error = %v", err)
	}
}

// TestFetchCardInvalid: every strict rule, always as ErrCardInvalid.
func TestFetchCardInvalid(t *testing.T) {
	with := func(replace func(string) string) string { return replace(cardJSON(testDestination.URL)) }

	tests := []struct {
		name string
		body string
	}{
		{name: "no name", body: with(func(s string) string { return strings.Replace(s, `"name": "reference"`, `"name": ""`, 1) })},
		{name: "no description", body: with(func(s string) string {
			return strings.Replace(s, `"description": "counts text"`, `"description": ""`, 1)
		})},
		{name: "no input modes", body: with(func(s string) string {
			return strings.Replace(s, `"defaultInputModes": ["text/plain"],`, `"defaultInputModes": [],`, 1)
		})},
		{name: "another host", body: cardJSON("http://127.0.0.1:9999/")},
		{name: "another scheme", body: cardJSON("https://127.0.0.1:7790/")},
		{name: "no jsonrpc interface", body: with(func(s string) string {
			return strings.Replace(s, `"protocolBinding": "JSONRPC"`, `"protocolBinding": "GRPC"`, 1)
		})},
		{name: "another protocol version", body: with(func(s string) string {
			return strings.Replace(s, `"protocolVersion": "1.0"`, `"protocolVersion": "2.0"`, 1)
		})},
		{name: "a skill without a name", body: with(func(s string) string {
			return strings.Replace(s, `"name": "Count text"`, `"name": ""`, 1)
		})},
		{name: "not a card", body: `{"nonsense": true}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: tc.body}}}
			_, _, err := fakeFactory(t, fake, nil).FetchCard(context.Background(), testDestination)
			if !errors.Is(err, ErrCardInvalid) {
				t.Fatalf("error = %v, want ErrCardInvalid", err)
			}
			// A rejected card's text never appears in the error.
			if strings.Contains(err.Error(), tc.body) {
				t.Fatalf("the error quotes the card: %v", err)
			}
		})
	}
}

// TestFetchCardNonHTTPScheme: a file:// destination is refused without any call.
func TestFetchCardNonHTTPScheme(t *testing.T) {
	fake := &fakeTransport{}
	_, _, err := fakeFactory(t, fake, nil).FetchCard(context.Background(), Destination{URL: "file:///etc/passwd"})
	if !errors.Is(err, ErrCardInvalid) {
		t.Fatalf("error = %v, want ErrCardInvalid", err)
	}
	if len(fake.requests()) != 0 {
		t.Fatal("a file:// destination reached the transport")
	}
}

// TestFetchCardOversize: a card over the limit is ErrCardInvalid, not a raw size error.
func TestFetchCardOversize(t *testing.T) {
	big := strings.Repeat("x", remoteMaxBytes+10)
	fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: `{"name":"` + big + `"}`}}}
	_, _, err := fakeFactory(t, fake, nil).FetchCard(context.Background(), testDestination)
	if !errors.Is(err, ErrCardInvalid) {
		t.Fatalf("error = %v, want ErrCardInvalid", err)
	}
}

// TestFetchCardFactoryError: the factory's text is not quoted.
func TestFetchCardFactoryError(t *testing.T) {
	remote := fakeFactory(t, &fakeTransport{}, errors.New("dial tcp 100.64.0.9:7790: refused"))
	_, _, err := remote.FetchCard(context.Background(), testDestination)
	if !errors.Is(err, ErrCardInvalid) {
		t.Fatalf("error = %v, want ErrCardInvalid", err)
	}
	if strings.Contains(err.Error(), "100.64") {
		t.Fatalf("the error quotes an address: %v", err)
	}
}

// TestSendSuccessTask: a task answer is mirrored.
func TestSendSuccessTask(t *testing.T) {
	body := `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_INPUT_REQUIRED","message":{"parts":[{"text":"which format?"}]}},"artifacts":[]}`
	fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"result":` + body + `}`}}}
	remote := fakeFactory(t, fake, nil)

	task, err := remote.Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if task.ID != "t1" || task.ContextID != "c1" {
		t.Fatalf("task = %+v", task)
	}
	if task.State != "INPUT_REQUIRED" {
		t.Fatalf("state = %q, want the bare name", task.State)
	}
	if task.Text != "which format?" {
		t.Fatalf("text = %q", task.Text)
	}

	requests := fake.requests()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want exactly one (never retried)", len(requests))
	}
	for _, header := range []string{"Authorization", "Cookie", "If-None-Match"} {
		if value := requests[0].Header.Get(header); value != "" {
			t.Fatalf("the request carried %s: %q", header, value)
		}
	}
	if version := requests[0].Header.Get("A2A-Version"); version != "1.0" {
		t.Fatalf("A2A-Version = %q, want the SDK's 1.0", version)
	}
	// The destination is the REGISTERED url, never a card's idea of it.
	if got := requests[0].URL.String(); got != testDestination.URL {
		t.Fatalf("requested %q, want the registered url %q", got, testDestination.URL)
	}

	traced := string(fake.bodies[0])
	if !strings.Contains(traced, `"method":"message/send"`) {
		t.Fatalf("body = %q, want a JSON-RPC send", traced)
	}
	if !strings.Contains(traced, `"messageId":"m1"`) {
		t.Fatalf("body = %q, want the caller's message id", traced)
	}
	if !strings.Contains(traced, `"text":"hello"`) {
		t.Fatalf("body = %q, want the text part", traced)
	}
}

// TestSendDirectMessage: a message answer becomes a completed task with no id.
func TestSendDirectMessage(t *testing.T) {
	body := `{"role":"ROLE_AGENT","messageId":"a1","parts":[{"text":"done"}]}`
	fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"result":` + body + `}`}}}
	remote := fakeFactory(t, fake, nil)

	task, err := remote.Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if task.ID != "" || task.State != "COMPLETED" || task.Text != "done" {
		t.Fatalf("task = %+v", task)
	}
}

// TestSendServiceError: a JSON-RPC error object is a RemoteError, not a delivery failure.
func TestSendServiceError(t *testing.T) {
	fake := &fakeTransport{answers: []fakeAnswer{{
		status: http.StatusOK,
		body:   `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"no such task"}}`,
	}}}
	remote := fakeFactory(t, fake, nil)

	_, err := remote.Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "hello"})
	var remoteErr *RemoteError
	if !errors.As(err, &remoteErr) {
		t.Fatalf("error = %v, want a RemoteError", err)
	}
	if remoteErr.Code != -32001 {
		t.Fatalf("code = %d, want -32001", remoteErr.Code)
	}
	if remoteErr.Message != "no such task" {
		t.Fatalf("message = %q", remoteErr.Message)
	}
	// A received refusal is neither sent-unknown nor not-delivered.
	if errors.Is(err, ErrUncertain) || errors.Is(err, ErrNotDelivered) {
		t.Fatalf("a service error was classified as delivery trouble: %v", err)
	}
}

// TestSendTransportFailures: before the write is not-delivered; after it is uncertain.
func TestSendTransportFailures(t *testing.T) {
	t.Run("before the write", func(t *testing.T) {
		fake := &fakeTransport{answers: []fakeAnswer{{err: errors.New("dial tcp 100.64.0.9:7790: refused")}}}
		_, err := fakeFactory(t, fake, nil).Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "x"})
		if !errors.Is(err, ErrNotDelivered) {
			t.Fatalf("error = %v, want ErrNotDelivered", err)
		}
		if errors.Is(err, ErrUncertain) {
			t.Fatal("a provably unwritten request was called uncertain")
		}
		if strings.Contains(err.Error(), "100.64") {
			t.Fatalf("the error quotes an address: %v", err)
		}
	})

	t.Run("after the write", func(t *testing.T) {
		fake := &fakeTransport{answers: []fakeAnswer{{err: errors.New("connection reset"), written: true}}}
		_, err := fakeFactory(t, fake, nil).Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "x"})
		if !errors.Is(err, ErrUncertain) {
			t.Fatalf("error = %v, want ErrUncertain", err)
		}
		if errors.Is(err, ErrNotDelivered) {
			t.Fatal("a written request was called not delivered")
		}
	})

	t.Run("oversize reply", func(t *testing.T) {
		big := strings.Repeat("x", remoteMaxBytes+10)
		fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"result":"` + big + `"}`}}}
		_, err := fakeFactory(t, fake, nil).Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "x"})
		if !errors.Is(err, ErrUncertain) {
			t.Fatalf("error = %v, want ErrUncertain", err)
		}
	})
}

// TestSendPreconditions: nothing is sent when the call could not be made at all.
func TestSendPreconditions(t *testing.T) {
	tests := []struct {
		name string
		in   RemoteSend
	}{
		{name: "no message id", in: RemoteSend{Text: "x"}},
		{name: "empty text", in: RemoteSend{MessageID: "m1"}},
		{name: "oversize text", in: RemoteSend{MessageID: "m1", Text: strings.Repeat("x", remoteMaxSendBytes+1)}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTransport{}
			_, err := fakeFactory(t, fake, nil).Send(context.Background(), testDestination, tc.in)
			if !errors.Is(err, ErrNotDelivered) {
				t.Fatalf("error = %v, want ErrNotDelivered", err)
			}
			if len(fake.requests()) != 0 {
				t.Fatal("a precondition failure still made a request")
			}
		})
	}

	t.Run("the factory failed", func(t *testing.T) {
		remote := fakeFactory(t, &fakeTransport{}, errors.New("pin mismatch"))
		_, err := remote.Send(context.Background(), testDestination, RemoteSend{MessageID: "m1", Text: "x"})
		if !errors.Is(err, ErrNotDelivered) {
			t.Fatalf("error = %v, want ErrNotDelivered", err)
		}
		if strings.Contains(err.Error(), "pin mismatch") {
			t.Fatalf("the error quotes the factory: %v", err)
		}
	})
}

// TestGet: success, a service error, and a transport problem.
func TestGet(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		body := `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"a1","parts":[{"text":"lines: 1"}]},{"artifactId":"a2","parts":[{"data":{"lines":1,"words":1,"characters":1}}]}]}`
		fake := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: `{"jsonrpc":"2.0","id":1,"result":` + body + `}`}}}
		task, err := fakeFactory(t, fake, nil).Get(context.Background(), testDestination, "t1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if task.State != "COMPLETED" || task.ID != "t1" {
			t.Fatalf("task = %+v", task)
		}
		if len(task.Results) != 2 {
			t.Fatalf("results = %#v, want one entry per artifact", task.Results)
		}
		if task.Results[0] != "lines: 1" {
			t.Fatalf("text artifact = %q", task.Results[0])
		}
		if !strings.Contains(task.Results[1], `"lines":1`) {
			t.Fatalf("data artifact = %q, want JSON", task.Results[1])
		}
	})

	t.Run("a service error", func(t *testing.T) {
		fake := &fakeTransport{answers: []fakeAnswer{{
			status: http.StatusOK,
			body:   `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"no such task"}}`,
		}}}
		_, err := fakeFactory(t, fake, nil).Get(context.Background(), testDestination, "t1")
		var remoteErr *RemoteError
		if !errors.As(err, &remoteErr) || remoteErr.Code != -32001 {
			t.Fatalf("error = %v, want a RemoteError with the spec code", err)
		}
	})

	t.Run("a transport problem", func(t *testing.T) {
		fake := &fakeTransport{answers: []fakeAnswer{{err: errors.New("connection reset"), written: true}}}
		_, err := fakeFactory(t, fake, nil).Get(context.Background(), testDestination, "t1")
		if err == nil {
			t.Fatal("Get succeeded with a broken transport")
		}
		// A read is safe to repeat, so it is not classified as uncertain.
		if errors.Is(err, ErrUncertain) {
			t.Fatal("a read was classified as an uncertain send")
		}
	})

	t.Run("no id", func(t *testing.T) {
		if _, err := fakeFactory(t, &fakeTransport{}, nil).Get(context.Background(), testDestination, ""); err == nil {
			t.Fatal("Get accepted an empty id")
		}
	})
}

// TestLimitedBodyNeverPullsMoreThanLimitPlusOne: the read is clamped, so a huge reply is refused after
// limit+1 bytes and nothing beyond that is requested from the connection.
func TestLimitedBodyNeverPullsMoreThanLimitPlusOne(t *testing.T) {
	source := &countingReader{}
	body := &limitedBody{body: io.NopCloser(source)}
	buffer := make([]byte, 1<<20)
	var total int64
	var err error
	for err == nil {
		var n int
		n, err = body.Read(buffer)
		total += int64(n)
	}
	if !errors.Is(err, ErrReplyTooLarge) || source.total != remoteMaxBytes+1 || total != remoteMaxBytes+1 {
		t.Fatalf("err=%v pulled=%d returned=%d, want ErrReplyTooLarge and exactly %d", err, source.total, total, remoteMaxBytes+1)
	}
}

type countingReader struct{ total int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	c.total += int64(len(p))
	return len(p), nil
}
