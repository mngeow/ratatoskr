package router

import (
	"context"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Drafting reports whether the router currently holds one agent's deliveries because the
// user is mid-composition in its pane.
//
// It is a read through the actor, so it sees every UserDraft, UserSubmitted and DeliverHeld
// that was enqueued before it. The daemon calls it after each of those to mirror the router's
// own flag for the windows to show, so the display can never disagree with what the router
// decides. An unknown agent is not drafting.
//
// UNTESTED.
func (r *Router) Drafting(ctx context.Context, id protocol.AgentID) (bool, error) {
	var drafting bool
	err := r.call(ctx, func() error {
		if a, ok := r.agents[id]; ok {
			drafting = a.drafting
		}
		return nil
	})
	return drafting, err
}
