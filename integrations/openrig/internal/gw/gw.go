// Package gw is the participant API client of ratatoskr-openrig
// (docs/implementation/openrig-bridge-contract.md section 8.1; docs/spec/participant-api).
//
// HTTPS is pinned: the leaf certificate's DER SHA-256 is compared with the configured pin on every new
// connection, before a byte of HTTP is written. InsecureSkipVerify is set only together with that
// VerifyConnection callback. Plain HTTP exists only when the caller asks for it and has no pin. There is no
// proxy and no redirect.
//
// Errors never carry a credential, a pairing code or a request body.
//
// UNTESTED. Under GR-3 nothing here has been run.
package gw

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"time"
)

// Wire constants of the participant API.
const (
	ContractHeader = "X-Ratatoskr-Contract"
	Contract       = "1.0"
	ContractMajor  = "1"
	apiPrefix      = "/participant/v1"

	// Timeouts (contract section 8.1).
	PollWait       = 20
	pollTimeout    = 25 * time.Second
	sendTimeout    = 10 * time.Second
	requestTimeout = 10 * time.Second

	maxAnswerBytes = 4 << 20
)

// ErrPinMismatch is returned (wrapped) when the certificate does not match the configured pin.
var ErrPinMismatch = errors.New("the server certificate does not match the configured pin")

// Kind says what went wrong with a request.
type Kind int

const (
	KindNetwork  Kind = iota // no usable answer: connect, TLS, timeout, reset
	KindPin                  // the certificate did not match the pin
	KindRevoked              // 401
	KindRefused              // another 4xx
	KindServer               // 5xx
	KindProtocol             // an answer that is not what the contract says
)

// Error is a failed request.
type Error struct {
	Kind   Kind
	Status int    // HTTP status, 0 if none
	Code   string // the API's error code, if any
	Msg    string
	// Wrote is true when the request was fully written to the connection, so the gateway may have acted on it.
	Wrote bool
}

func (e *Error) Error() string { return e.Msg }

// Options configure a Client.
type Options struct {
	Scheme     string // "https" or "http"
	Host       string // host:port
	Pin        [32]byte
	Credential string // empty for discovery and pair
}

// Client talks to one participant listener.
type Client struct {
	base       string
	credential string
	api        *http.Client // everything but the long poll
	poll       *http.Client // the long poll, on its own transport so an ack never queues behind it
}

func newTransport(o Options) *http.Transport {
	t := &http.Transport{
		Proxy:               nil,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// An empty map keeps net/http from negotiating HTTP/2.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	if o.Scheme == "https" {
		pin := o.Pin
		t.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
			// Verification is replaced, not skipped: VerifyConnection below is the only trust decision.
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return ErrPinMismatch
				}
				sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
				if subtle.ConstantTimeCompare(sum[:], pin[:]) != 1 {
					return ErrPinMismatch
				}
				return nil
			},
		}
	}
	return t
}

// New builds a client. It makes no request.
func New(o Options) *Client {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		base:       o.Scheme + "://" + o.Host,
		credential: o.Credential,
		api:        &http.Client{Transport: newTransport(o), CheckRedirect: noRedirect},
		poll:       &http.Client{Transport: newTransport(o), CheckRedirect: noRedirect},
	}
}

// Close drops idle connections.
func (c *Client) Close() {
	c.api.CloseIdleConnections()
	c.poll.CloseIdleConnections()
}

// do performs one request and decodes a 200 answer into out (nil to ignore the body).
func (c *Client) do(ctx context.Context, hc *http.Client, timeout time.Duration, method, path string, in, out any) (http.Header, *Error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, &Error{Kind: KindProtocol, Msg: "cannot encode the request"}
		}
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wrote := false
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote = true }})
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Msg: "cannot build the request"}
	}
	req.Header.Set(ContractHeader, Contract)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.credential)
	}

	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, ErrPinMismatch) {
			return nil, &Error{Kind: KindPin, Msg: ErrPinMismatch.Error()}
		}
		return nil, &Error{Kind: KindNetwork, Wrote: wrote, Msg: "the request did not complete: " + netReason(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	if err != nil || len(raw) > maxAnswerBytes {
		return nil, &Error{Kind: KindNetwork, Wrote: true, Status: resp.StatusCode, Msg: "the answer was cut off or too large"}
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		if out == nil {
			return resp.Header, nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, &Error{Kind: KindProtocol, Wrote: true, Status: 200, Msg: "the gateway answered something that is not the expected JSON"}
		}
		return resp.Header, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, &Error{Kind: KindRevoked, Wrote: true, Status: 401, Code: "unauthorized", Msg: "the gateway refused this credential: it is revoked or unknown (401 unauthorized)"}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, &Error{Kind: KindProtocol, Wrote: true, Status: resp.StatusCode, Msg: fmt.Sprintf("unexpected HTTP %d: no redirect is followed", resp.StatusCode)}
	}
	var apiErr struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &apiErr)
	e := &Error{Wrote: true, Status: resp.StatusCode, Code: safeCode(apiErr.Code)}
	if resp.StatusCode >= 500 {
		e.Kind = KindServer
	} else {
		e.Kind = KindRefused
	}
	e.Msg = fmt.Sprintf("the gateway answered HTTP %d", resp.StatusCode)
	if e.Code != "" {
		e.Msg += " " + e.Code
	}
	return nil, e
}

// safeCode keeps an API error code only if it looks like one.
func safeCode(code string) string {
	if len(code) == 0 || len(code) > 40 {
		return ""
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z' || r == '_') {
			return ""
		}
	}
	return code
}

// netReason names a network failure without echoing addresses or certificate text.
func netReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return "timed out"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	return "connection failed"
}

// Discovery is the answer of GET /participant/v1.
type Discovery struct {
	Contract string   `json:"contract"`
	Versions []string `json:"versions"`
}

// Discover checks the contract major before anything else is sent.
func (c *Client) Discover(ctx context.Context) (Discovery, *Error) {
	var d Discovery
	header, e := c.do(ctx, c.api, requestTimeout, http.MethodGet, apiPrefix, nil, &d)
	if e != nil {
		return d, e
	}
	if header.Get(ContractHeader) == "" {
		return d, &Error{Kind: KindProtocol, Wrote: true, Status: 200, Msg: "discovery omitted the " + ContractHeader + " response header"}
	}
	for _, v := range d.Versions {
		if v == ContractMajor {
			return d, nil
		}
	}
	return d, &Error{Kind: KindProtocol, Wrote: true, Status: 200, Msg: fmt.Sprintf("the gateway does not offer participant contract major %s", ContractMajor)}
}

// Pair redeems a single-use pairing code. It sends no credential. The caller must treat an error with Wrote
// set as "the code may or may not have been redeemed".
func (c *Client) Pair(ctx context.Context, code, participant string) (credential string, e *Error) {
	var out struct {
		Participant string `json:"participant"`
		Credential  string `json:"credential"`
	}
	_, e = c.do(ctx, c.api, requestTimeout, http.MethodPost, apiPrefix+"/pair",
		map[string]string{"code": code, "participant": participant}, &out)
	if e != nil {
		if e.Kind == KindRefused && e.Code == "bad_pairing_code" {
			e.Msg = "the gateway refused the pairing code: it is unknown, already used or expired"
		}
		return "", e
	}
	if out.Credential == "" || out.Participant != participant {
		return "", &Error{Kind: KindProtocol, Wrote: true, Status: 200, Msg: "pairing answered without a credential or with another participant name"}
	}
	return out.Credential, nil
}

// DirectoryEntry is one address in the directory.
type DirectoryEntry struct {
	Address string `json:"address"`
	Host    string `json:"host"`
	Live    bool   `json:"live"`
}

// SessionInfo is the registered session.
type SessionInfo struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	SessionID string `json:"session_id"`
}

// AddSession registers (or replaces) the session.
func (c *Client) AddSession(ctx context.Context, name string) (SessionInfo, []DirectoryEntry, *Error) {
	var out struct {
		Participant string           `json:"participant"`
		Session     SessionInfo      `json:"session"`
		Directory   []DirectoryEntry `json:"directory"`
	}
	_, e := c.do(ctx, c.api, requestTimeout, http.MethodPost, apiPrefix+"/sessions", map[string]string{"name": name}, &out)
	if e != nil {
		return SessionInfo{}, nil, e
	}
	if out.Session.Name != name || out.Session.Address == "" {
		return SessionInfo{}, nil, &Error{Kind: KindProtocol, Wrote: true, Status: 200, Msg: "registration answered with another session"}
	}
	return out.Session, out.Directory, nil
}

// Directory lists the addresses.
func (c *Client) Directory(ctx context.Context) ([]DirectoryEntry, *Error) {
	var out struct {
		Directory []DirectoryEntry `json:"directory"`
	}
	if _, e := c.do(ctx, c.api, requestTimeout, http.MethodGet, apiPrefix+"/directory", nil, &out); e != nil {
		return nil, e
	}
	return out.Directory, nil
}

// Offer is one message on its way to the session.
type Offer struct {
	ID      string          `json:"id"`
	From    string          `json:"from"`
	To      string          `json:"to"`
	ReplyTo string          `json:"reply_to,omitempty"`
	Body    json.RawMessage `json:"body"`
	At      string          `json:"at"`
}

// Inbox is the answer of one long poll.
type Inbox struct {
	Offers   []Offer `json:"offers"`
	Replaced bool    `json:"replaced"`
}

// Poll parks one long poll of PollWait seconds.
func (c *Client) Poll(ctx context.Context) (Inbox, *Error) {
	var in Inbox
	_, e := c.do(ctx, c.poll, pollTimeout, http.MethodGet, apiPrefix+"/inbox?wait="+strconv.Itoa(PollWait), nil, &in)
	return in, e
}

// Ack acknowledges one offer: saved true once stored, or false with a reason to refuse it.
func (c *Client) Ack(ctx context.Context, id string, saved bool, reason string) *Error {
	body := map[string]any{"saved": saved}
	if reason != "" {
		body["reason"] = reason
	}
	_, e := c.do(ctx, c.api, requestTimeout, http.MethodPost, apiPrefix+"/messages/"+url.PathEscape(id)+"/ack", body, nil)
	return e
}

// ReportStatus reports pasted, failed or unconfirmed (never accepted) for an acknowledged offer.
func (c *Client) ReportStatus(ctx context.Context, id, state, reason string) *Error {
	body := map[string]any{"state": state}
	if reason != "" {
		body["reason"] = reason
	}
	_, e := c.do(ctx, c.api, requestTimeout, http.MethodPost, apiPrefix+"/messages/"+url.PathEscape(id)+"/status", body, nil)
	return e
}

// SendRequest is an outbound message.
type SendRequest struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	ReplyTo string `json:"reply_to,omitempty"`
	Body    string `json:"body"`
}

// SendResult is the gateway's answer to a send.
type SendResult struct {
	ID        string           `json:"id"`
	Result    string           `json:"result"`
	Detail    string           `json:"detail,omitempty"`
	Directory []DirectoryEntry `json:"directory,omitempty"`
}

// Send makes one attempt. The same id from the same sender stores once, so the caller may repeat it.
func (c *Client) Send(ctx context.Context, r SendRequest) (SendResult, *Error) {
	var out SendResult
	_, e := c.do(ctx, c.api, sendTimeout, http.MethodPost, apiPrefix+"/messages", r, &out)
	if e != nil {
		return out, e
	}
	if out.ID != r.ID || out.Result == "" {
		return out, &Error{Kind: KindProtocol, Wrote: true, Status: 200, Msg: "the send answer is not for this message"}
	}
	return out, nil
}
