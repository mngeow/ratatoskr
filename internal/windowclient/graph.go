package windowclient

// UNTESTED under GR-3. Window-only graph reads use the existing bounded mux.
import (
	"context"
	"errors"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func (c *Client) Graph(ctx context.Context, req wire.GraphRequest) (protocol.GraphView, uint64, error) {
	if req.WaitMS < 0 || req.WaitMS > wire.MaxGraphWaitMS || req.FirstWaitMS < 0 || req.FirstWaitMS > wire.MaxGraphFirstMS || req.WaitMS > 0 && req.FirstWaitMS > 0 {
		return protocol.GraphView{}, 0, errors.New("windowclient: invalid graph wait bounds")
	}
	result, err := c.queryContext(ctx, wire.Query{Kind: wire.QueryGraph, Graph: &req})
	if err != nil {
		return protocol.GraphView{}, 0, err
	}
	if result.Graph == nil {
		return protocol.GraphView{}, 0, errors.New("windowclient: graph answer has no body")
	}
	return *result.Graph, result.GraphVersion, nil
}
