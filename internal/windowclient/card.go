package windowclient

// UNTESTED under GR-3. Local file and generation operations never retry.
import (
	"context"
	"errors"
	"fmt"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func (c *Client) Card(ctx context.Context) (protocol.CardFileView, error) {
	r, e := c.queryContext(ctx, wire.Query{Kind: wire.QueryCard})
	if e != nil {
		return protocol.CardFileView{}, e
	}
	if r.Card == nil {
		return protocol.CardFileView{}, errors.New("windowclient: card answer has no body")
	}
	return *r.Card, nil
}

type CardOpError struct {
	Code      wire.Code
	Message   string
	Uncertain bool
}

func (e *CardOpError) Error() string { return e.Message }
func (c *Client) CardOp(ctx context.Context, op wire.CardOp) (wire.Result, error) {
	switch op.Op {
	case wire.CardOpGet, wire.CardOpReload, wire.CardOpDraftPrepare, wire.CardOpDraftRun, wire.CardOpDraftStatus, wire.CardOpDraftCancel, wire.CardOpDraftAccept:
	default:
		return wire.Result{}, errors.New("windowclient: unknown card operation")
	}
	if err := ctx.Err(); err != nil {
		return wire.Result{}, err
	}
	select {
	case c.querySem <- struct{}{}:
		defer func() { <-c.querySem }()
	default:
		return wire.Result{}, errBusy
	}
	value, err := c.request(ctx, wire.TypeCardOp, op, wire.TypeResult, queryTimeout)
	if err != nil {
		return wire.Result{}, &CardOpError{Message: fmt.Sprintf("card operation outcome unknown: %v", err), Uncertain: true}
	}
	r, ok := value.(wire.Result)
	if !ok {
		return wire.Result{}, &CardOpError{Message: "card operation outcome unknown: invalid answer", Uncertain: true}
	}
	if r.Error != nil {
		return r, &CardOpError{Code: r.Error.Code, Message: r.Error.Message}
	}
	needsCard := op.Op == wire.CardOpGet || op.Op == wire.CardOpReload || op.Op == wire.CardOpDraftAccept
	if needsCard && r.Card == nil {
		return r, &CardOpError{Message: "card operation answer has no file status", Uncertain: true}
	}
	if !needsCard && (r.Draft == nil || r.Draft.ID == "" || (op.DraftID != "" && r.Draft.ID != op.DraftID)) {
		return r, &CardOpError{Message: "card operation answer has no matching draft", Uncertain: true}
	}
	return r, nil
}
