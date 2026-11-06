package router

import (
	"context"
	"errors"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// This file only declares the seam a connected run (`rtk runtime`) plugs into.
// Behaviour comes in later slices; nothing here is called yet. UNTESTED.
//
// A Router with Config.Link == nil is in local mode and behaves exactly as it
// always has. With a Link it is connected: every send goes through the gateway,
// including a send to an agent on the same machine, and there is no local bypass.

// ErrUnknownMessage is what Link.Lookup returns, possibly wrapped, when the gateway
// never stored the message. The router maps it to a definite gateway_unreachable for
// a send that was still uncertain.
var ErrUnknownMessage = errors.New("router: the gateway has no such message")

// ErrNotSent is what Link.Send returns (possibly wrapped, so errors.Is finds it)
// when the message certainly was not stored: no connection, it failed before the
// request could be written, or the gateway answered with a refusal (a 3xx, a 400
// or a 401/403, including a protocol version mismatch) before storing anything.
// The router maps it to gateway_unreachable and shows the error text as the
// detail, so the user sees WHY (for example "the gateway refused this host:
// unauthorized"). It never reads as host_stored. Any other error from Send (a
// timeout or reset after the request was written, a 5xx, or an answer that cannot
// be parsed) means the request may have reached the gateway and may even have been
// delivered: the router maps it to gateway_uncertain, never to a definite failure.
var ErrNotSent = errors.New("router: link: nothing was sent")

// Link is the router's connection to a gateway. Send, Ack, Report and Lookup block
// on the network, so the router calls them only from its own goroutines, never from
// the actor. Address is the exception: it is pure string composition, must never
// block or touch the network, and the actor calls it (when it builds an agent's
// view). Nobody may add a network call to Address. An implementation is safe for
// concurrent use. Nothing it sends carries hook
// payloads, MCP tokens or terminal contents.
type Link interface {
	// Send posts one message and waits for the gateway's result, bounded by ctx.
	Send(ctx context.Context, req protocol.SendRequest) (protocol.SendResponse, error)
	// Ack answers an offer: the message is saved in the inbox, or refused.
	Ack(ctx context.Context, id string, saved bool, reason string) error
	// Report sends one delivery report for a message this host received. It is
	// idempotent and may be sent again after a reconnect.
	Report(ctx context.Context, id string, rep protocol.StatusReport) error
	// Lookup asks the gateway for its current view of one message, to correct an
	// uncertain record.
	Lookup(ctx context.Context, id string) (protocol.MessageInfo, error)
	// Address returns the network name (name@host) for a local display name.
	Address(local string) string
}
