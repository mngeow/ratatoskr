package link

// The network API from the gateway client's side: reporting about this host, and reading the
// aggregated snapshot.
//
// Neither method retries or queues anything: the daemon decides when to report and when to fetch,
// because it is the daemon that knows whether anyone is looking. Both take the caller's context as
// the only bound, which is why they call do directly rather than doBounded - the daemon gives a
// fetch the wait plus a margin, and doBounded's short budget would cut it off.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No report has been sent and no snapshot
// fetched.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

var (
	// ErrNetworkUnsupported reports a gateway that has no network routes: an older build. A daemon
	// stops reporting and fetching until its stream reconnects.
	ErrNetworkUnsupported = errors.New("link: the gateway does not support the network routes")

	// ErrNetworkSchemaTooNew reports a snapshot whose schema this daemon does not understand. It is
	// treated as unavailable rather than guessed at.
	ErrNetworkSchemaTooNew = errors.New("link: the gateway's network data is newer than this daemon understands")
)

// NetworkFetch is one answer to a snapshot fetch.
//
// Unchanged is the gateway's 204: nothing moved since the caller's revision, which is not an
// error. Snapshot is the body of a 200.
type NetworkFetch struct {
	Snapshot  *protocol.NetworkSnapshot
	Unchanged bool
}

// ReportSelf sends this host's report about itself. A 2xx is success; the gateway answers 204.
//
// Success means the gateway stored it, not that anyone read it. A 404 means the gateway has no
// network routes, which the caller must treat as "stop asking" until the connection changes.
//
// UNTESTED.
func (c *Client) ReportSelf(ctx context.Context, request protocol.ReportRequest) error {
	status, wrote, payload, err := c.do(ctx, http.MethodPut, protocol.ReportPath, request, nil)
	if err != nil && status == 0 {
		if !wrote {
			return fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
		}
		return errors.New("link: no complete answer from the gateway")
	}

	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusNotFound:
		return ErrNetworkUnsupported
	case status >= 300 && status < 500:
		return refusalFrom(status, payload)
	default:
		return fmt.Errorf("link: the gateway could not store the report (status %d)", status)
	}
}

// FetchNetwork reads the snapshot, waiting at most wait for a change since the caller's revision.
//
// A revision of zero means "no since": the gateway answers at once, which is what a daemon with no
// snapshot yet wants. The wait is clamped to the protocol's maximum so a caller cannot ask for an
// unbounded request.
//
// UNTESTED.
func (c *Client) FetchNetwork(ctx context.Context, since uint64, wait time.Duration) (NetworkFetch, error) {
	if wait > protocol.MaxNetworkWait {
		wait = protocol.MaxNetworkWait
	}
	if wait < 0 {
		wait = 0
	}

	query := url.Values{}
	if since != 0 {
		query.Set("since", strconv.FormatUint(since, 10))
	}
	if wait > 0 {
		query.Set("wait_ms", strconv.FormatInt(wait.Milliseconds(), 10))
	}
	path := protocol.NetworkPath
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	status, wrote, payload, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil && status == 0 {
		if !wrote {
			return NetworkFetch{}, fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
		}
		return NetworkFetch{}, errors.New("link: no complete answer from the gateway")
	}

	switch {
	case status == http.StatusNoContent:
		return NetworkFetch{Unchanged: true}, nil
	case status >= 200 && status < 300:
		var snapshot protocol.NetworkSnapshot
		if err != nil || json.Unmarshal(payload, &snapshot) != nil {
			return NetworkFetch{}, errUnreadableAnswer
		}
		if snapshot.Schema > protocol.NetworkSchema {
			return NetworkFetch{}, ErrNetworkSchemaTooNew
		}
		return NetworkFetch{Snapshot: &snapshot}, nil
	case status == http.StatusNotFound:
		return NetworkFetch{}, ErrNetworkUnsupported
	case status >= 300 && status < 500:
		return NetworkFetch{}, refusalFrom(status, payload)
	default:
		return NetworkFetch{}, fmt.Errorf("link: the gateway could not answer the network request (status %d)", status)
	}
}
