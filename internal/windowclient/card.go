package windowclient

// UNTESTED under GR-3. These operations target only this window's own daemon.
import (
	"context"
	"errors"
	"fmt"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func (c *Client) Card(ctx context.Context) (protocol.CardView, error) {
	result, err := c.queryContext(ctx, wire.Query{Kind: wire.QueryCard})
	if err != nil {
		return protocol.CardView{}, err
	}
	if result.Card == nil {
		return protocol.CardView{}, errors.New("windowclient: card answer has no body")
	}
	return *result.Card, nil
}

// CardOpError distinguishes an explicit refusal from an unknown write outcome.
// A conflict keeps the accompanying current view for review; it never discards edits.
type CardOpError struct {
	Code      wire.Code
	Message   string
	Uncertain bool
}

func (e *CardOpError) Error() string { return e.Message }

func (c *Client) CardOp(ctx context.Context, op wire.CardOp) (protocol.CardView, error) {
	switch op.Op {
	case wire.CardOpGet, wire.CardOpEdit, wire.CardOpValidate, wire.CardOpPublish, wire.CardOpHistory, wire.CardOpRollback, wire.CardOpRescan:
	default:
		return protocol.CardView{}, errors.New("windowclient: unknown card operation")
	}
	if err := ctx.Err(); err != nil {
		return protocol.CardView{}, err
	}
	select {
	case c.querySem <- struct{}{}:
		defer func() { <-c.querySem }()
	default:
		return protocol.CardView{}, errBusy
	}
	value, err := c.request(ctx, wire.TypeCardOp, op, wire.TypeResult, queryTimeout)
	if err != nil {
		return protocol.CardView{}, &CardOpError{Message: fmt.Sprintf("card operation outcome unknown; reload before another write: %v", err), Uncertain: true}
	}
	result, ok := value.(wire.Result)
	if !ok {
		return protocol.CardView{}, &CardOpError{Message: "card operation outcome unknown: invalid answer", Uncertain: true}
	}
	view := protocol.CardView{}
	if result.Card != nil {
		view = *result.Card
	}
	if result.Error != nil {
		return view, &CardOpError{Code: result.Error.Code, Message: result.Error.Message}
	}
	if result.Card == nil {
		return view, &CardOpError{Message: "card operation outcome unknown: answer has no card", Uncertain: true}
	}
	return view, nil
}
