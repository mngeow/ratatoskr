package daemon

// What a window's requests and queries reach on the router.
//
// It is an interface so the daemon's tests need no live router, and so the daemon cannot reach
// anything on the router that is not part of the wire surface.

import (
	"context"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// windowRouter is the router as the window side sees it.
type windowRouter interface {
	UserDraft(id protocol.AgentID, drafting bool)
	UserSubmitted(id protocol.AgentID)
	DeliverHeld(id protocol.AgentID)
	// Drafting is the router's own draft flag for one agent: the single source the windows
	// are shown, read after every draft-related request.
	Drafting(ctx context.Context, id protocol.AgentID) (bool, error)

	Snapshot() protocol.Snapshot
	PendingOnQuit() int

	History(q protocol.HistoryQuery) ([]protocol.MessageView, error)
	Runs(limit int) ([]protocol.RunView, error)
	MessageDetail(id string) (protocol.MessageDetail, error)
	Sends(limit int) ([]protocol.SendView, error)
	SendDetail(id string) (protocol.SendDetail, error)
}
