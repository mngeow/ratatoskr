package link

// The graph API from the gateway client's side: reading the aggregated graph snapshot.
//
// Like FetchNetwork it neither retries nor queues, and the caller's context is the only bound (the
// daemon gives a fetch the wait plus a margin, which doBounded's short budget would cut off). The
// graph response is capped by the gateway at protocol.MaxGraphBytes, under this client's 1 MiB
// answer limit, so no limit here changes.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No graph has been fetched.

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
	// ErrGraphUnsupported reports a gateway that has no graph route: an older build. The daemon shows
	// it as its own state, gateway_too_old, and Network keeps working.
	ErrGraphUnsupported = errors.New("link: the gateway does not support the graph route")

	// ErrGraphSchemaTooNew reports a snapshot whose schema this daemon does not understand. It is
	// treated as unavailable rather than guessed at.
	ErrGraphSchemaTooNew = errors.New("link: the gateway's graph data is newer than this daemon understands")
)

// GraphFetch is one answer to a graph fetch. Unchanged is the gateway's 204.
type GraphFetch struct {
	Snapshot  *protocol.GraphSnapshot
	Unchanged bool
}

// FetchGraph reads the graph, waiting at most wait for a change since the caller's revision. A
// revision of zero means "no since": the gateway answers at once. The wait is clamped to the
// protocol's maximum.
//
// UNTESTED.
func (c *Client) FetchGraph(ctx context.Context, since uint64, wait time.Duration) (GraphFetch, error) {
	if wait > protocol.MaxGraphWait {
		wait = protocol.MaxGraphWait
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
	path := protocol.GraphPath
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	status, wrote, payload, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil && status == 0 {
		if !wrote {
			return GraphFetch{}, fmt.Errorf("link: no connection to the gateway: %s", causeKind(err))
		}
		return GraphFetch{}, errors.New("link: no complete answer from the gateway")
	}

	switch {
	case status == http.StatusNoContent:
		return GraphFetch{Unchanged: true}, nil
	case status >= 200 && status < 300:
		var snapshot protocol.GraphSnapshot
		if err != nil || json.Unmarshal(payload, &snapshot) != nil {
			return GraphFetch{}, errUnreadableAnswer
		}
		if snapshot.Schema > protocol.GraphSchema {
			return GraphFetch{}, ErrGraphSchemaTooNew
		}
		return GraphFetch{Snapshot: &snapshot}, nil
	case status == http.StatusNotFound:
		return GraphFetch{}, ErrGraphUnsupported
	case status >= 300 && status < 500:
		return GraphFetch{}, refusalFrom(status, payload)
	default:
		return GraphFetch{}, fmt.Errorf("link: the gateway could not answer the graph request (status %d)", status)
	}
}
