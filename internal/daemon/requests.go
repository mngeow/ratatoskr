package daemon

// Per-(window, session) request workers: one goroutine each, executing in the order the
// requests arrived and acknowledging in that same order.
//
// The order is what makes input correct: a draft flag has to precede its bytes, and a submit
// has to follow the Enter it belongs to. One worker per session also means a slow paste cannot
// delay a keypress for another session in the same window.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No key has been forwarded.

import (
	"context"
	"errors"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// mirrorTimeout bounds one read of the router's draft flag.
const mirrorTimeout = 2 * time.Second // UNTESTED guess

// runWorker executes one session's requests in order until the window closes.
func (w *window) runWorker(view sessionView, requests chan wire.Envelope) {
	for {
		select {
		case <-w.done:
			return
		case envelope := <-requests:
			if envelope.ID == 0 {
				// Nothing to answer, and nothing to do: a request without an id cannot be
				// reported on.
				continue
			}
			w.ack(envelope.ID, w.execute(view, envelope))
		}
	}
}

// execute runs one request and returns its acknowledgement.
//
// Every input operation asks the gate first and does nothing else when the gate refuses.
func (w *window) execute(view sessionView, envelope wire.Envelope) wire.Ack {
	refuse := func(err *wire.Error) wire.Ack { return wire.Ack{OK: false, Error: err} }

	// take_input is the one operation that is not gated by holding: a viewer, or a window
	// nobody has given anything to, must be able to ask. takeInput does its own checks for
	// an unknown or ended session.
	if envelope.Type == wire.TypeTakeInput {
		ownership, err := w.hub.gate.takeInput(view.Agent, w.id)
		if err != nil {
			return refuse(err)
		}
		return wire.Ack{OK: true, Ownership: &ownership}
	}

	if err := w.hub.gate.admit(view.Agent, w.id); err != nil {
		return refuse(err)
	}
	if view.Pane == nil {
		return refuse(&wire.Error{Code: wire.CodeInputFailed, Message: "that session has no pane"})
	}

	switch envelope.Type {
	case wire.TypeKey:
		var request wire.Key
		if err := decodeInto(envelope, &request); err != nil {
			return refuse(badRequest())
		}
		if err := view.Pane.SendKey(keyPress(request.Key)); err != nil {
			return inputFailed(err)
		}
		return wire.Ack{OK: true, Written: 1}

	case wire.TypePaste:
		var request wire.Paste
		if err := decodeInto(envelope, &request); err != nil {
			return refuse(badRequest())
		}
		if len(request.Text) > wire.MaxPasteBytes {
			return refuse(&wire.Error{Code: wire.CodeTooLarge, Message: "that paste is too large"})
		}
		if err := view.Pane.Paste(request.Text); err != nil {
			return inputFailed(err)
		}
		return wire.Ack{OK: true, Written: len(request.Text)}

	case wire.TypeMouse:
		var request wire.Mouse
		if err := decodeInto(envelope, &request); err != nil {
			return refuse(badRequest())
		}
		event, ok := mouseEvent(request)
		if !ok {
			return refuse(&wire.Error{Code: wire.CodeBadRequest, Message: "that mouse event is not one this build knows"})
		}
		// The daemon rechecks: whether the child wants the mouse is a decision the window
		// cannot be trusted to remember.
		if !view.Pane.MouseEnabled(event) {
			return refuse(&wire.Error{Code: wire.CodeInputFailed, Message: "the child has not asked for the mouse"})
		}
		if err := view.Pane.SendMouse(event); err != nil {
			if errors.Is(err, pane.ErrMouseDisabled) {
				return refuse(&wire.Error{Code: wire.CodeInputFailed, Message: "the child has not asked for the mouse"})
			}
			return inputFailed(err)
		}
		return wire.Ack{OK: true, Written: 1}

	case wire.TypeResize:
		var request wire.Resize
		if err := decodeInto(envelope, &request); err != nil {
			return refuse(badRequest())
		}
		var err error
		if request.Request {
			err = view.Pane.RequestResize(request.Width, request.Height)
		} else {
			err = view.Pane.Resize(request.Width, request.Height)
		}
		if err != nil {
			return inputFailed(err)
		}
		return wire.Ack{OK: true}

	case wire.TypeDraft:
		var request wire.Draft
		if err := decodeInto(envelope, &request); err != nil {
			return refuse(badRequest())
		}
		w.hub.router.UserDraft(view.Agent, request.Drafting)
		w.mirrorDrafting(view.Agent)
		return wire.Ack{OK: true}

	case wire.TypeSubmitted:
		// UserSubmitted does not touch the draft flag: only the window knows when the pane is
		// quiet, and it sends draft(false) then. The mirror below shows whatever the router
		// decided, so the display cannot say "not drafting" over a flag the router still holds.
		w.hub.router.UserSubmitted(view.Agent)
		w.mirrorDrafting(view.Agent)
		return wire.Ack{OK: true}

	case wire.TypeDeliverHeld:
		w.hub.router.DeliverHeld(view.Agent)
		w.mirrorDrafting(view.Agent)
		return wire.Ack{OK: true}

	}

	return refuse(badRequest())
}

// mirrorDrafting copies the router's draft flag for one agent into the gate, which is what the
// windows are shown. The read goes through the router's actor after the request just enqueued,
// so it sees that request's effect. A failed read leaves the last value and is logged: the
// request itself still succeeded.
func (w *window) mirrorDrafting(agent protocol.AgentID) {
	ctx, cancel := context.WithTimeout(context.Background(), mirrorTimeout)
	defer cancel()

	drafting, err := w.hub.router.Drafting(ctx, agent)
	if err != nil {
		w.log.logf("window %s: reading the router's draft flag failed: %v", w.id, err)
		return
	}
	w.hub.gate.setDrafting(agent, drafting)
}

// badRequest is the one answer for a body this build cannot read.
func badRequest() *wire.Error {
	return &wire.Error{Code: wire.CodeBadRequest, Message: "that request could not be read"}
}

// inputFailed wraps a transport error, naming it but not quoting anything else.
func inputFailed(err error) wire.Ack {
	message := err.Error()
	if len(message) > 400 {
		message = message[:400]
	}
	return wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeInputFailed, Message: message}}
}

// decodeInto decodes one request body.
func decodeInto(envelope wire.Envelope, into any) error {
	return wire.Decode(envelope, into)
}

// keyPress converts the wire's key to the pinned ultraviolet event, field for field.
//
// UNTESTED.
func keyPress(key wire.KeyEvent) uv.KeyPressEvent {
	return uv.KeyPressEvent(uv.Key{
		Code:        rune(key.Code),
		ShiftedCode: rune(key.ShiftedCode),
		BaseCode:    rune(key.BaseCode),
		Text:        key.Text,
		Mod:         uv.KeyMod(key.Mod),
		IsRepeat:    key.IsRepeat,
	})
}

// mouseEvent converts the wire's mouse event to ultraviolet's, or reports that the kind is not
// one this build knows.
//
// UNTESTED.
func mouseEvent(request wire.Mouse) (uv.MouseEvent, bool) {
	switch request.Kind {
	case wire.MouseClick:
		return uv.MouseClickEvent{X: request.X, Y: request.Y, Button: uv.MouseButton(request.Button), Mod: uv.KeyMod(request.Mod)}, true
	case wire.MouseRelease:
		return uv.MouseReleaseEvent{X: request.X, Y: request.Y, Button: uv.MouseButton(request.Button), Mod: uv.KeyMod(request.Mod)}, true
	case wire.MouseWheel:
		return uv.MouseWheelEvent{X: request.X, Y: request.Y, Button: uv.MouseButton(request.Button), Mod: uv.KeyMod(request.Mod)}, true
	case wire.MouseMotion:
		return uv.MouseMotionEvent{X: request.X, Y: request.Y, Button: uv.MouseButton(request.Button), Mod: uv.KeyMod(request.Mod)}, true
	}
	return nil, false
}

// sanitizeMessage keeps an error text short and single-line for a window.
func sanitizeMessage(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 400 {
		text = text[:400]
	}
	return text
}
