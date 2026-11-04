package router

import (
	"errors"
	"fmt"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// The concrete store must satisfy the interface this package asks for. This
// catches interface drift where the code lives, rather than in whoever wires the
// two together.
var _ Store = (*store.Store)(nil)

// ErrMessageNotFound reports a message id that the store does not hold. It is a
// sentinel so the UI can say "that message is gone" without parsing text.
var ErrMessageNotFound = errors.New("router: message not found")

// runsLimit is the default number of runs the log lists when a caller passes no
// limit.
const runsLimit = 50 // UNTESTED guess

// Runs lists past runs, newest first, for the log view.
//
// It reads the store directly rather than asking the actor: history and recovery
// must work from any run and while writes are latched down, so neither method
// touches actor-owned state.
//
// UNTESTED.
func (r *Router) Runs(limit int) ([]protocol.RunView, error) {
	if limit <= 0 {
		limit = runsLimit
	}

	rows, err := r.cfg.Store.Runs(r.ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("router: runs: %w", err)
	}

	views := make([]protocol.RunView, 0, len(rows))
	for _, row := range rows {
		view := protocol.RunView{
			ID:          row.ID,
			StartedAt:   row.StartedAt,
			EndReason:   stripControl(row.EndReason),
			Interrupted: row.Interrupted,
		}
		// A copy, never the store's pointer: the view outlives the row it came
		// from, and nothing outside this function may write through it.
		if row.EndedAt != nil {
			ended := *row.EndedAt
			view.EndedAt = &ended
		}
		views = append(views, view)
	}
	return views, nil
}

// MessageDetail returns one message in full: its whole body, sanitized for
// display, and its recorded state history.
//
// There is no resend, so this is how a user recovers an interrupted message. An
// unknown id is ErrMessageNotFound; anything else from the store is wrapped and
// returned as it is.
//
// UNTESTED.
func (r *Router) MessageDetail(id string) (protocol.MessageDetail, error) {
	row, err := r.cfg.Store.Message(r.ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return protocol.MessageDetail{}, fmt.Errorf("%w: %s", ErrMessageNotFound, id)
		}
		return protocol.MessageDetail{}, fmt.Errorf("router: message %s: %w", id, err)
	}

	events, err := r.cfg.Store.MessageEvents(r.ctx, id)
	if err != nil {
		// A message without its history is worse than an error: the history is
		// what tells a user whether their message was ever pasted.
		return protocol.MessageDetail{}, fmt.Errorf("router: message %s events: %w", id, err)
	}

	detail := protocol.MessageDetail{
		// The list row keeps using preview for its short form; the detail keeps
		// the whole body, so a multi-line message stays readable.
		MessageView: storeMessageRowToView(row),
		ReplyTo:     stripControl(row.ReplyTo),
		Body:        stripControl(row.Body),
		Events:      make([]protocol.MessageEventView, 0, len(events)),
	}
	for _, event := range events {
		detail.Events = append(detail.Events, protocol.MessageEventView{
			At:     event.At,
			From:   event.From,
			To:     event.To,
			Reason: stripControl(event.Reason),
		})
	}
	return detail, nil
}
