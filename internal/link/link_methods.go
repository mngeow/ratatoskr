package link

// The router.Link implementation, plus the three plain request forms slice 4b will
// call. Every method makes one attempt unless it is documented as retrying: a
// message is never resent by this client.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No message, ack or report has
// been sent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/router"
)

// messageIDPattern is the id shape the gateway accepts.
var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// ackRetryWaits are the gaps between the at most three attempts an ack or a report
// makes. They honour the caller's context.
var ackRetryWaits = []time.Duration{ // UNTESTED guess
	200 * time.Millisecond,
	400 * time.Millisecond,
}

// The compiler checks that this package implements the router's seam.
var _ router.Link = (*Client)(nil)

// Send posts one message and reports what the gateway decided.
//
// The classification is the whole point, so it is spelled out here:
//
//   - a transport error before the request reached the wire (httptrace never saw
//     WroteRequest) wraps router.ErrNotSent: nothing was stored;
//   - a transport error after the request was written is a plain error: the
//     message may have been stored;
//   - a 2xx whose body is not a usable SendResponse for THIS id with a known,
//     decided result is a plain error (uncertain), never a failure;
//   - a 5xx is a plain error (uncertain);
//   - a 3xx or 4xx is a refusal: the error wraps router.ErrNotSent and a
//     *RefusedError, so the caller can map it to a definite failure and still show
//     why.
//
// UNTESTED.
func (c *Client) Send(ctx context.Context, req protocol.SendRequest) (protocol.SendResponse, error) {
	// The send budget has ONE owner, protocol.SendWait, which is longer than the
	// gateway's own offer wait (protocol.AckWait). There is no response-header
	// timeout underneath: the gateway answers this request only when that wait
	// ends. A timeout here, after the request was written, is uncertain.
	ctx, cancel := context.WithTimeout(ctx, protocol.SendWait)
	defer cancel()

	status, wrote, payload, err := c.do(ctx, http.MethodPost, "/v1/messages", req, nil)

	if err != nil && status == 0 {
		if ctxErr := ctx.Err(); ctxErr != nil && !wrote {
			return protocol.SendResponse{}, fmt.Errorf("%w: %v", router.ErrNotSent, ctxErr)
		}
		if !wrote {
			// The cause is reduced to its kind: a transport error's own text can
			// carry the address it was talking to.
			return protocol.SendResponse{}, fmt.Errorf("%w: no connection to the gateway: %s", router.ErrNotSent, causeKind(err))
		}
		return protocol.SendResponse{}, errors.New("no complete answer from the gateway: the message may have been stored")
	}

	switch {
	case status >= 200 && status < 300:
		var response protocol.SendResponse
		if err != nil || json.Unmarshal(payload, &response) != nil {
			return protocol.SendResponse{}, fmt.Errorf("%w: the message may have been stored", errUnreadableAnswer)
		}
		if response.ID != req.ID || !decidedResult(response.Result) {
			return protocol.SendResponse{}, fmt.Errorf("%w: the message may have been stored", errUnreadableAnswer)
		}
		return response, nil

	case status >= 300 && status < 500:
		refusal := refusalFrom(status, payload)
		return protocol.SendResponse{}, fmt.Errorf("%w: %w", router.ErrNotSent, refusal)

	default:
		return protocol.SendResponse{}, fmt.Errorf(
			"the gateway failed while handling the message (status %d): it may have been stored", status)
	}
}

// decidedResult reports whether a gateway result is one a sender can act on. The
// eight protocol.SendResult values minus "sending", which is not an outcome.
func decidedResult(result protocol.SendResult) bool {
	switch result {
	case protocol.SendGatewayUnreachable,
		protocol.SendGatewayUncertain,
		protocol.SendUnknownRecipient,
		protocol.SendDestinationUnreachable,
		protocol.SendSessionGone,
		protocol.SendDeliveryUncertain,
		protocol.SendHostStored:
		return true
	default:
		return false
	}
}

// Ack answers an offer. It retries at most twice more, and only when nothing was
// decided: a transport error or a 5xx. A 2xx is success, and a 3xx or 4xx is the
// gateway's final answer.
//
// UNTESTED.
func (c *Client) Ack(ctx context.Context, id string, saved bool, reason string) error {
	if !messageIDPattern.MatchString(id) {
		return errors.New("link: ack: invalid message id")
	}
	return c.retrying(ctx, http.MethodPost, "/v1/messages/"+id+"/ack", protocol.AckRequest{Saved: saved, Reason: reason})
}

// Report sends one delivery report. It retries on the same terms as Ack: reports
// are idempotent and are resent after a reconnect.
//
// UNTESTED.
func (c *Client) Report(ctx context.Context, id string, rep protocol.StatusReport) error {
	if !messageIDPattern.MatchString(id) {
		return errors.New("link: report: invalid message id")
	}
	return c.retrying(ctx, http.MethodPost, "/v1/messages/"+id+"/status", rep)
}

// retrying is the shared retry loop: at most len(ackRetryWaits)+1 attempts, waiting
// between them, and stopping at once when the context ends.
func (c *Client) retrying(ctx context.Context, method, path string, body any) error {
	var lastErr error

	for attempt := 0; attempt <= len(ackRetryWaits); attempt++ {
		status, wrote, payload, err := c.doBounded(ctx, method, path, body)

		switch {
		case err != nil && status == 0:
			if !wrote {
				lastErr = fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
			} else {
				lastErr = errors.New("link: no complete answer from the gateway")
			}
		case err != nil:
			lastErr = errors.New("link: the gateway's answer could not be read")
		case status >= 200 && status < 300:
			return nil
		case status >= 500:
			lastErr = fmt.Errorf("link: the gateway failed (status %d)", status)
		default:
			// A 3xx or 4xx is final: retrying a refusal would only repeat it.
			return refusalFrom(status, payload)
		}

		if attempt < len(ackRetryWaits) {
			if waitErr := c.wait(ctx, ackRetryWaits[attempt]); waitErr != nil {
				return waitErr
			}
		}
	}
	return lastErr
}

// Lookup asks the gateway for its current view of one message, so an uncertain
// record can be corrected.
//
// UNTESTED.
func (c *Client) Lookup(ctx context.Context, id string) (protocol.MessageInfo, error) {
	if !messageIDPattern.MatchString(id) {
		return protocol.MessageInfo{}, errors.New("link: lookup: invalid message id")
	}

	status, wrote, payload, err := c.doBounded(ctx, http.MethodGet, "/v1/messages/"+id, nil)
	if err != nil && status == 0 {
		if !wrote {
			return protocol.MessageInfo{}, fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
		}
		return protocol.MessageInfo{}, errors.New("link: no complete answer from the gateway")
	}

	switch {
	case status == http.StatusNotFound:
		return protocol.MessageInfo{}, ErrUnknownMessage
	case status >= 200 && status < 300:
		var info protocol.MessageInfo
		if err != nil || json.Unmarshal(payload, &info) != nil {
			return protocol.MessageInfo{}, errUnreadableAnswer
		}
		return info, nil
	case status >= 300 && status < 500:
		return protocol.MessageInfo{}, refusalFrom(status, payload)
	default:
		return protocol.MessageInfo{}, fmt.Errorf("link: the gateway could not answer (status %d)", status)
	}
}

// Address is the network name for one of this host's agents.
func (c *Client) Address(local string) string {
	return local + "@" + c.pairing.Host
}

// Register announces this run's agents and returns the sessions and directory the
// gateway answered with. One attempt.
//
// UNTESTED.
func (c *Client) Register(ctx context.Context, agents []string) (protocol.SessionsResponse, error) {
	status, wrote, payload, err := c.doBounded(ctx, http.MethodPost, "/v1/sessions", protocol.SessionsRequest{Agents: agents})
	if err != nil && status == 0 {
		if !wrote {
			return protocol.SessionsResponse{}, fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
		}
		return protocol.SessionsResponse{}, errors.New("link: no complete answer from the gateway")
	}

	switch {
	case status >= 200 && status < 300:
		var response protocol.SessionsResponse
		if err != nil || json.Unmarshal(payload, &response) != nil {
			return protocol.SessionsResponse{}, errUnreadableAnswer
		}
		return response, nil
	case status >= 300 && status < 500:
		return protocol.SessionsResponse{}, refusalFrom(status, payload)
	default:
		return protocol.SessionsResponse{}, fmt.Errorf("link: the gateway could not register this runtime (status %d)", status)
	}
}

// Unregister ends this run's sessions. One attempt; any 2xx is success.
//
// UNTESTED.
func (c *Client) Unregister(ctx context.Context) error {
	status, wrote, payload, err := c.doBounded(ctx, http.MethodDelete, "/v1/sessions", nil)
	if err != nil && status == 0 {
		if !wrote {
			return fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
		}
		return errors.New("link: no complete answer from the gateway")
	}

	switch {
	case status >= 200 && status < 300:
		return nil
	case status >= 300 && status < 500:
		return refusalFrom(status, payload)
	default:
		return fmt.Errorf("link: the gateway could not unregister this runtime (status %d)", status)
	}
}

// Heartbeat records that this runtime is still here. The body carries only a
// timestamp; the gateway stores its own receive time and ignores this one.
//
// UNTESTED.
func (c *Client) Heartbeat(ctx context.Context) error {
	status, wrote, payload, err := c.doBounded(ctx, http.MethodPost, protocol.HeartbeatPath,
		protocol.HeartbeatRequest{At: c.opts.Now()})
	if err != nil && status == 0 {
		if !wrote {
			return fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
		}
		return errors.New("link: no complete answer from the gateway")
	}

	switch {
	case status >= 200 && status < 300:
		return nil
	case status >= 300 && status < 500:
		return refusalFrom(status, payload)
	default:
		return fmt.Errorf("link: the gateway could not take the heartbeat (status %d)", status)
	}
}

// doBounded is do with the short call budget every request except a send uses. A
// send is bounded by protocol.SendWait instead, which is longer than the gateway's
// offer wait; no transport has a response-header timeout, so these budgets are the
// only bounds a request has.
func (c *Client) doBounded(ctx context.Context, method, path string, body any) (status int, wrote bool, payload []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return c.do(ctx, method, path, body, nil)
}
