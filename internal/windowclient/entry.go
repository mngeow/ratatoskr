package windowclient

// Entry: asking the daemon what pressing Enter on one Network row should do.
//
// The answer is a plan, and the plan carries the only command that may be run - built by the daemon
// from the owner's host map, never by this client. The client copies it, and never builds, edits or
// launches anything.
//
// UNTESTED: see doc.go.

import (
	"context"
	"errors"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

// RefusalError is the daemon refusing a window or its attach, with the wire code kept so a caller can
// act on it.
//
// Its Error is the daemon's own message and nothing else: the dial paths wrap it with their existing
// prefix, so the text a user reads is unchanged, while errors.As finds the code through the wrap.
type RefusalError struct {
	Code    wire.Code
	Message string
}

// Error is the daemon's message.
func (e *RefusalError) Error() string { return e.Message }

// RefusalCode is the wire code the daemon refused with.
func (e *RefusalError) RefusalCode() wire.Code { return e.Code }

// Enter asks the daemon what one Network row means.
//
// It is a query like any other: it shares the mux, the semaphore and the timeout, and it runs no
// command. A missing plan is an error, because a caller with nothing to show or run has nothing.
//
// UNTESTED.
func (c *Client) Enter(ctx context.Context, request wire.EnterRequest) (wire.EnterPlan, error) {
	result, err := c.queryContext(ctx, wire.Query{Kind: wire.QueryEnter, Enter: &request})
	if err != nil {
		return wire.EnterPlan{}, err
	}
	if result.Plan == nil {
		return wire.EnterPlan{}, errors.New("windowclient: the daemon answered the enter request without a plan")
	}

	plan := *result.Plan
	// The argv is the one part a later stage runs, so the caller gets its own copy.
	plan.Argv = append([]string(nil), result.Plan.Argv...)
	return plan, nil
}
