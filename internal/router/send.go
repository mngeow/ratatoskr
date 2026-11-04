package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// SendError reasons, as the snake_case vocabulary of design section 8. They are
// what the sending agent is told, and what the server maps onto its response.
const (
	ReasonUnknownRecipient   = "unknown_recipient"
	ReasonUnknownReplyTo     = "unknown_reply_to"
	ReasonRecipientOffline   = "recipient_offline"
	ReasonBudgetExhausted    = "budget_exhausted"
	ReasonTooLarge           = "too_large"
	ReasonStorageUnavailable = "storage_unavailable"
	ReasonEmptyBody          = "empty_body"
)

// SendError is a message that was refused. It is a refusal by contract, not a
// wiring failure, so callers can type-assert it and show Reason.
type SendError struct {
	Reason string
}

func (e *SendError) Error() string { return "router: message rejected: " + e.Reason }

// SendReason lets the server map a refusal to its tool error without importing
// this package: it looks for an error with this method.
func (e *SendError) SendReason() string { return e.Reason }

// Send accepts one message from one agent to another.
//
// It returns only after the message is committed to the store, so a caller that
// has an id knows the row exists. Delivery happens afterwards, from the actor:
// the sender never waits for the recipient.
//
// UNTESTED.
func (r *Router) Send(ctx context.Context, from protocol.AgentID, to, body, replyTo string) (string, error) {
	type result struct {
		id  string
		err error
	}

	done := make(chan result, 1)
	if err := r.submit(ctx, func() {
		id, recipient, err := r.send(from, to, body, replyTo)
		done <- result{id: id, err: err}
		// The reply is already on its way: delivery happens after it, so the
		// sender never waits on the recipient or on the delivery commits.
		if recipient != nil {
			r.pump(recipient, false)
		}
	}); err != nil {
		return "", err
	}

	select {
	case res := <-done:
		return res.id, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.done:
		return "", ErrClosed
	}
}

// send is the actor-side half of Send. The checks run in the order design
// section 1 fixes, so a message that breaks two rules is refused for the first
// one the contract names.
func (r *Router) send(from protocol.AgentID, to, body, replyTo string) (string, *agent, error) {
	if r.storageDown {
		return "", nil, &SendError{Reason: ReasonStorageUnavailable}
	}

	sender, ok := r.agents[from]
	if !ok {
		// A wiring bug, not a message rejection: the server should never accept
		// a send from an agent it did not register.
		return "", nil, fmt.Errorf("router: send: unknown sender %q", from)
	}

	// Recipients are addressed by display name; RegisterAgent rejects duplicates,
	// so a name resolves to at most one agent.
	recipient, ok := r.byName[to]
	if !ok || recipient == sender {
		return "", nil, &SendError{Reason: ReasonUnknownRecipient}
	}

	if recipient.state == protocol.AgentExited {
		return "", nil, &SendError{Reason: ReasonRecipientOffline}
	}

	// Empty is decided after sanitising: a body of ESC or spaces carries nothing.
	if isEmptyBody(body) {
		return "", nil, &SendError{Reason: ReasonEmptyBody}
	}

	// The size limit is on the raw body, because that is what is stored; only
	// the frame is sanitised.
	if len(body) > r.cfg.MaxBody {
		return "", nil, &SendError{Reason: ReasonTooLarge}
	}

	if replyTo != "" {
		exists, err := r.cfg.Store.MessageExists(r.ctx, r.cfg.RunID, replyTo)
		switch {
		case err != nil && errors.Is(err, store.ErrUnavailable):
			r.setStorageDown(err)
			return "", nil, &SendError{Reason: ReasonStorageUnavailable}
		case err != nil:
			return "", nil, fmt.Errorf("router: send: check reply-to: %w", err)
		case !exists:
			return "", nil, &SendError{Reason: ReasonUnknownReplyTo}
		}
	}

	if r.used >= r.cfg.BudgetLimit {
		return "", nil, &SendError{Reason: ReasonBudgetExhausted}
	}

	id, err := r.cfg.NewID()
	if err != nil {
		return "", nil, fmt.Errorf("router: send: new message id: %w", err)
	}

	at := r.now()
	record := store.Message{
		ID:        id,
		RunID:     r.cfg.RunID,
		From:      sender.id,
		To:        recipient.id,
		ReplyTo:   replyTo,
		Body:      body,
		CreatedAt: at,
	}
	if _, err := r.cfg.Store.InsertMessage(r.ctx, record); err != nil {
		if errors.Is(err, store.ErrUnavailable) {
			r.setStorageDown(err)
			return "", nil, &SendError{Reason: ReasonStorageUnavailable}
		}
		return "", nil, fmt.Errorf("router: send: insert message: %w", err)
	}

	m := &msg{
		id:      id,
		from:    sender,
		to:      recipient,
		replyTo: replyTo,
		body:    body,
		state:   protocol.MessageQueued,
		at:      at,
	}
	r.used++
	r.appendLog(m)
	recipient.queue = append(recipient.queue, m)
	r.emit(protocol.Event{Message: r.messageViewPtr(m), Budget: r.budgetViewPtr()})
	return id, recipient, nil
}
