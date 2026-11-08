package link

// The one-shot request client: one transport, one credential, no redirects, no
// Client.Timeout, and every call bounded by its context.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No request has been made.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/router"
)

const (
	// dialTimeout bounds the connect step. There is deliberately NO response-header
	// timeout on any transport: POST /v1/messages is answered only when the
	// gateway's offer wait ends (protocol.AckWait), and a header timeout shorter
	// than the call's own budget would give up just as the gateway answers. Every
	// call is bounded by its own context instead: protocol.SendWait for a send and
	// requestTimeout for the rest.
	dialTimeout = 3 * time.Second // UNTESTED guess

	// requestTimeout bounds each short request (ack, report, lookup, register,
	// unregister, heartbeat). It is a call budget, not a header wait. A send does
	// not use it: its budget is protocol.SendWait.
	requestTimeout = 5 * time.Second // UNTESTED guess

	// keepAlive is the idle TCP keepalive for the client's connections.
	keepAlive = 15 * time.Second // UNTESTED guess

	// maxResponseBytes bounds one answer. A gateway answer is small; anything
	// larger is not one.
	maxResponseBytes = 1 << 20

	// httpClientConns caps idle connections. One runtime talks to one gateway
	// about one message at a time.
	httpClientConns = 4 // UNTESTED guess

	// The stream's own defaults. The first four are contract values.
	defaultHeartbeatEvery = 5 * time.Second  // UNTESTED guess; contract value
	defaultDeadAfter      = 15 * time.Second // UNTESTED guess; contract value
	defaultBackoffMin     = 1 * time.Second  // UNTESTED guess; contract value
	defaultBackoffMax     = 15 * time.Second // UNTESTED guess; contract value
	defaultMaxFrameBytes  = 1 << 20          // UNTESTED guess

	// heartbeatRequestTimeout bounds one heartbeat request inside the loop.
	heartbeatRequestTimeout = 5 * time.Second // UNTESTED guess

	// unregisterTimeout bounds the DELETE /v1/sessions a Close sends.
	unregisterTimeout = 2 * time.Second // UNTESTED guess
)

// ErrUnknownMessage reports a message the gateway never stored. It is the router's
// sentinel, so a caller can test for it without importing this package.
var ErrUnknownMessage = router.ErrUnknownMessage

// errUnreadableAnswer reports an answer that arrived but could not be understood.
// Callers map it to "uncertain": the request may have been handled.
var errUnreadableAnswer = errors.New("link: the gateway's answer could not be understood")

// Options tunes the client. Zero fields take the defaults.
type Options struct {
	DialTimeout time.Duration
	// Transport replaces the built transport. Tests only.
	Transport http.RoundTripper
	// Now is the clock heartbeats are stamped with.
	Now func() time.Time

	// HeartbeatEvery is how often the stream posts a heartbeat while it is open.
	HeartbeatEvery time.Duration
	// DeadAfter is how long with no frame at all - ping included - before the
	// connection is treated as dead.
	DeadAfter time.Duration
	// BackoffMin and BackoffMax bound the redial delay, which doubles from the
	// first to the second.
	BackoffMin time.Duration
	BackoffMax time.Duration
	// MaxFrameBytes caps one stream frame line.
	MaxFrameBytes int
}

// Client talks to one gateway as one paired host.
//
// UNTESTED.
type Client struct {
	pairing Pairing
	baseURL string
	// tls is the pinned configuration the built transports use, or nil for a plain
	// Tailscale or loopback pairing. It is kept so a future stream or pair path cannot
	// build a transport without it.
	tls  *tls.Config
	http *http.Client
	// streamHTTP is the client for the one long-lived stream. It exists separately
	// because a stream and a request want opposite keep-alive and deadline
	// behaviour.
	streamHTTP *http.Client
	opts       Options

	mu        sync.Mutex
	runCancel context.CancelFunc
	runDone   chan struct{}
	ran       atomic.Bool
	closed    atomic.Bool
	closeOnce sync.Once

	// wait sleeps between retries. It is a field so tests can observe the waits and
	// prove a cancelled context returns at once.
	wait func(ctx context.Context, d time.Duration) error
}

// NewClient builds a client for one pairing.
//
// The transport is built once: no proxy (so HTTP_PROXY is never used), bounded dial
// and header timeouts, no compression, no HTTP/2 upgrade, and a redirect policy that
// never follows one - a redirect is returned as a refusal, because it could point
// anywhere. There is no http.Client.Timeout: every call is bounded by its context.
//
// UNTESTED.
func NewClient(p Pairing, opts Options) (*Client, error) {
	if err := validatePairing(p); err != nil {
		return nil, err
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = dialTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	if opts.HeartbeatEvery == 0 {
		opts.HeartbeatEvery = defaultHeartbeatEvery
	}
	if opts.DeadAfter == 0 {
		opts.DeadAfter = defaultDeadAfter
	}
	if opts.BackoffMin == 0 {
		opts.BackoffMin = defaultBackoffMin
	}
	if opts.BackoffMax == 0 {
		opts.BackoffMax = defaultBackoffMax
	}
	if opts.MaxFrameBytes == 0 {
		opts.MaxFrameBytes = defaultMaxFrameBytes
	}

	// The pin is built before anything else, so a malformed fingerprint fails here
	// rather than on the first request. It is built even when a test transport is
	// injected: the seam may replace the dialer, never the pin's validity.
	var pinned *tls.Config
	if p.Fingerprint != "" {
		built, err := pinnedTLSConfig(p.Fingerprint)
		if err != nil {
			return nil, err
		}
		pinned = built
	}

	client := &Client{
		pairing: p,
		baseURL: p.scheme() + "://" + p.Gateway,
		opts:    opts,
		wait:    waitContext,
		tls:     pinned,
	}
	client.http = &http.Client{
		Transport:     pairTransport(opts.Transport, opts, pinned),
		CheckRedirect: noRedirect,
	}
	// The stream gets its own client: one long-lived connection, keep-alives on,
	// no header timeout and no whole-response deadline, behind the same redirect
	// refusal.
	client.streamHTTP = &http.Client{
		Transport:     streamTransport(opts.Transport, opts, pinned),
		CheckRedirect: noRedirect,
	}
	return client, nil
}

// streamTransport builds the transport the stream uses, or returns the injected
// one.
//
// It differs from the request transport in exactly one way: keep-alives stay ON,
// because this is one long connection rather than a request per message. Like the
// request transport it has NO ResponseHeaderTimeout: a gateway that never answers
// is caught by the stream's own watchdog, which is created before the request is
// issued, so the whole-stream budget has one owner.
func streamTransport(injected http.RoundTripper, opts Options, pinned *tls.Config) http.RoundTripper {
	if injected != nil {
		// Tests only: production never sets Options.Transport, so a real client always
		// dials with the configuration below.
		return injected
	}
	return &http.Transport{
		TLSClientConfig: pinned,
		// The handshake gets the same budget as the dial. UNTESTED guess: a pinned
		// self-signed certificate makes the handshake one round trip at most.
		TLSHandshakeTimeout: opts.DialTimeout,
		// HTTP/2 is never enabled, on either side.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
		Proxy:        nil,
		DialContext: (&net.Dialer{
			Timeout:   opts.DialTimeout,
			KeepAlive: keepAlive,
		}).DialContext,
		// No ResponseHeaderTimeout, on purpose: see the comment above.
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
		MaxIdleConns:       1,
		// Keep-alives ON: the stream is one connection for the life of the run.
		DisableKeepAlives: false,
	}
}

// Host is the host name this runtime is paired as.
func (c *Client) Host() string { return c.pairing.Host }

// GatewayAddr is the gateway address this runtime is paired with.
func (c *Client) GatewayAddr() string { return c.pairing.Gateway }

// newTransport builds the transport used for ordinary requests.
func newTransport(opts Options, pinned *tls.Config) *http.Transport {
	return &http.Transport{
		TLSClientConfig:     pinned,
		TLSHandshakeTimeout: opts.DialTimeout,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		// Explicitly nil: a gateway address is never reached through a proxy, and
		// HTTP_PROXY must not be able to redirect a credential.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   opts.DialTimeout,
			KeepAlive: keepAlive,
		}).DialContext,
		// No ResponseHeaderTimeout: see dialTimeout above.
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
		MaxIdleConns:       httpClientConns,
		// One connection per request. A kept-alive connection that the gateway
		// closed (a restart, a sleep) would fail AFTER the request was written, and
		// an honest "may have arrived" would then follow a request that never could
		// have. A fresh connection fails at dial, which is a certain "not sent". The
		// cost is one TCP connect per request on a tailnet. UNTESTED guess.
		DisableKeepAlives: true,
	}
}

// pairTransport returns the injected transport when there is one, and otherwise a
// freshly built one.
func pairTransport(injected http.RoundTripper, opts Options, pinned *tls.Config) http.RoundTripper {
	if injected != nil {
		return injected
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = dialTimeout
	}
	return newTransport(opts, pinned)
}

// rootText walks an error chain to its innermost text.
func rootText(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

// noRedirect refuses to follow a redirect: the response is handed back as it is and
// treated as a refusal.
func noRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// waitContext sleeps for d unless the context ends first.
func waitContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// do performs one request and reports what came back.
//
// It returns the status (0 when nothing came back), whether the request reached the
// wire, the body, and an error for a transport, read or decode failure. The caller
// classifies, because only the caller knows what "uncertain" means for it.
//
// UNTESTED.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) (status int, wrote bool, payload []byte, err error) {
	return c.doHeaders(ctx, method, path, nil, body, out)
}

// doHeaders is do with extra request headers (the A2A tools name the acting session in one).
func (c *Client) doHeaders(ctx context.Context, method, path string, extra map[string]string, body any, out any) (status int, wrote bool, payload []byte, err error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, false, nil, fmt.Errorf("link: encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, false, nil, errors.New("link: build request failed")
	}
	request.Header.Set(protocol.WireVersionHeader, strconv.Itoa(protocol.WireVersion))
	request.Header.Set("Authorization", "Bearer "+c.pairing.Credential)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range extra {
		request.Header.Set(name, value)
	}

	// WroteRequest runs on the transport's own goroutine, not this one, so the
	// flag is atomic. It also fires when a write FAILS part-way: that still counts
	// as "reached the wire", which is the safe side (uncertain, never "not sent").
	var wroteRequest atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest.Store(true) },
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))

	response, err := c.http.Do(request)
	if err != nil {
		// wroteRequest is the only evidence of whether anything reached the wire.
		return 0, wroteRequest.Load(), nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	payload, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return response.StatusCode, true, nil, errors.New("link: reading the gateway's answer failed")
	}
	if len(payload) > maxResponseBytes {
		return response.StatusCode, true, nil, errors.New("link: the gateway's answer was too large")
	}

	if out != nil && response.StatusCode >= 200 && response.StatusCode < 300 && len(payload) > 0 {
		if decodeErr := json.Unmarshal(payload, out); decodeErr != nil {
			return response.StatusCode, true, payload, errUnreadableAnswer
		}
	}
	return response.StatusCode, true, payload, nil
}

// RefusedError is the gateway refusing this host: a version mismatch, an unknown
// credential, a malformed request or a message the caller may not see.
//
// UNTESTED.
type RefusedError struct {
	Status  int
	Code    string
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("the gateway refused this host: %s: %s", e.Code, e.Message)
}

// Refusal finds a refusal in an error chain.
func Refusal(err error) (*RefusedError, bool) {
	var refusal *RefusedError
	if errors.As(err, &refusal) {
		return refusal, true
	}
	return nil, false
}

// refusalFrom builds a refusal from a status and whatever body came with it.
//
// The code and message are the gateway's own when it sent a WireError, sanitised;
// otherwise they describe what happened without quoting the body back.
func refusalFrom(status int, payload []byte) *RefusedError {
	refusal := &RefusedError{
		Status:  status,
		Code:    "http_" + strconv.Itoa(status),
		Message: "unexpected answer",
	}

	var wire protocol.WireError
	if err := json.Unmarshal(payload, &wire); err == nil && wire.Code != "" {
		refusal.Code = sanitizeText(wire.Code)
		if wire.Message != "" {
			refusal.Message = sanitizeText(wire.Message)
		}
	}

	if status >= 300 && status < 400 {
		// A redirect is never followed, so it is a refusal with its own wording.
		refusal.Code = "redirect"
		refusal.Message = "the gateway answered with a redirect; redirects are never followed"
	}
	return refusal
}

// causeKind reduces a transport error to a short kind, so no URL, host or
// credential can appear in an error text.
func causeKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "host unreachable"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "cannot resolve the gateway address"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out"
	}

	// TLS failures, in order of what the user has to do about them. None of these
	// texts quotes a URL, an address or a certificate.
	if errors.Is(err, errPinMismatch) {
		return "the gateway's certificate does not match the pinned fingerprint: it was regenerated, or something else answered at this address; re-pair after checking the fingerprint on the gateway machine"
	}

	// A plain-HTTP gateway answers a ClientHello with an HTTP 400, and net/http turns
	// that into ErrSchemeMismatch before the TLS error is seen. Nothing was written
	// either way.
	var recordErr tls.RecordHeaderError
	if errors.Is(err, http.ErrSchemeMismatch) || errors.As(err, &recordErr) {
		return "the gateway did not answer with TLS"
	}

	var alertErr tls.AlertError
	if errors.As(err, &alertErr) {
		return "the TLS handshake failed"
	}

	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return "the TLS handshake failed"
	}

	// The last catch, and only a text prefix: net/http wraps a handshake failure
	// inside *url.Error, and crypto/tls does not export a single error type for all
	// of them. The root text is the innermost error's, so a wrapper cannot name an
	// address.
	if strings.HasPrefix(rootText(err), "tls: ") {
		return "the TLS handshake failed"
	}
	return "the connection failed"
}
