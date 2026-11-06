// Package wire is what a window and the daemon say to each other over the daemon's
// Unix socket: framing, message types and error codes.
//
// It imports only the standard library, internal/protocol and internal/pane (for
// pane.Frame, which crosses as it is). It must never import app, router, store,
// server or link: a window is a client of the daemon and holds none of them.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No message has crossed a
// socket. The design is docs/implementation/daemon-design.md, "Wire surface".
package wire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxMessageBytes bounds one framed message in either direction, header excluded.
	// A paste is limited to MaxPasteBytes of text, which JSON escaping can inflate
	// several times over, so the frame limit is larger than the paste limit.
	MaxMessageBytes = 8 << 20 // UNTESTED guess

	// MaxPasteBytes is the largest paste text a window may send. It matches the
	// 1 MiB limit the input path already has.
	MaxPasteBytes = 1 << 20

	headerBytes = 4
)

// ErrTooLarge reports a message over MaxMessageBytes, read or written.
var ErrTooLarge = errors.New("wire: message too large")

// Type names one message. Requests carry an ID that the daemon echoes in its answer.
type Type string

// Window to daemon.
const (
	// TypeHello is always first. Its body is Hello.
	TypeHello Type = "hello"
	// TypeAttach asks to become a window. Body: Attach. Answer: TypeAttached.
	TypeAttach Type = "attach"
	// Input operations, one per tui.Pane or tui.Router method. Each is acknowledged
	// with TypeAck in the order it was sent for that session.
	TypeKey         Type = "key"          // body Key
	TypePaste       Type = "paste"        // body Paste
	TypeMouse       Type = "mouse"        // body Mouse
	TypeResize      Type = "resize"       // body Resize
	TypeDraft       Type = "draft"        // body Draft
	TypeSubmitted   Type = "submitted"    // body SessionRef
	TypeDeliverHeld Type = "deliver_held" // body SessionRef
	TypeTakeInput   Type = "take_input"   // body SessionRef; the answer carries ownership
	// TypeQuery is a read. Body: Query. Answer: TypeResult.
	TypeQuery Type = "query"
	// TypeDetach says goodbye. The daemon closes the connection and keeps every session.
	TypeDetach Type = "detach"

	// Control requests, for `rtk daemon status` and `rtk daemon stop`.
	TypeStatus      Type = "status"       // no body; answer TypeResult with Status
	TypeStop        Type = "stop"         // no body; answer TypeAck, then the daemon stops
	TypeStopSession Type = "stop_session" // body StopSession; answer TypeAck
)

// Daemon to window.
const (
	TypeHelloAck Type = "hello_ack" // body HelloAck
	TypeRefused  Type = "refused"   // body Refusal; the connection closes after it
	TypeAttached Type = "attached"  // body Attached
	TypeAck      Type = "ack"       // body Ack, in answer to one request
	TypeResult   Type = "result"    // body Result, in answer to one query or status
	// TypePane is a complete pane state, coalesced: the daemon keeps only the latest
	// per pane per window and replaces it, never queues it. Body PaneState.
	TypePane Type = "pane"
	// The next four are reliable: they queue in a bounded buffer, and a window that
	// overflows it is disconnected.
	TypeEvent        Type = "event"         // body protocol.Event
	TypeOwnership    Type = "ownership"     // body Ownership
	TypeSessionEnded Type = "session_ended" // body SessionEnded
	TypePending      Type = "pending"       // body Pending
	// TypeBye is the last message before the daemon closes a connection. Body Bye.
	TypeBye Type = "bye"
)

// Envelope is the one framed unit: a type, a request ID where the type is a request
// or an answer, and a type-specific body.
type Envelope struct {
	Type Type            `json:"type"`
	ID   uint64          `json:"id,omitempty"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Write frames one message: a 4-byte big-endian length, then the JSON envelope, in a
// single Write call so a deadline applies to the whole message. A nil body is omitted.
func Write(w io.Writer, typ Type, id uint64, body any) error {
	envelope := Envelope{Type: typ, ID: id}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("wire: encode %s: %w", typ, err)
		}
		envelope.Body = raw
	}

	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("wire: encode %s: %w", typ, err)
	}
	if len(payload) > MaxMessageBytes {
		return ErrTooLarge
	}

	buffer := make([]byte, headerBytes+len(payload))
	binary.BigEndian.PutUint32(buffer, uint32(len(payload)))
	copy(buffer[headerBytes:], payload)
	written, err := w.Write(buffer)
	if err != nil {
		return fmt.Errorf("wire: write %s: %w", typ, err)
	}
	if written < len(buffer) {
		return fmt.Errorf("wire: write %s: %w", typ, io.ErrShortWrite)
	}
	return nil
}

// Read reads one framed message. A length over MaxMessageBytes is refused before any
// of it is read or allocated, so a peer cannot make the reader allocate.
func Read(r io.Reader) (Envelope, error) {
	var header [headerBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Envelope{}, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxMessageBytes {
		return Envelope{}, ErrTooLarge
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Envelope{}, fmt.Errorf("wire: read message: %w", err)
	}

	var envelope Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("wire: decode envelope: %w", err)
	}
	if envelope.Type == "" {
		return Envelope{}, errors.New("wire: message without a type")
	}
	return envelope, nil
}

// Decode unpacks an envelope's body. A missing body decodes to the zero value.
func Decode(envelope Envelope, into any) error {
	if len(envelope.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Body, into); err != nil {
		return fmt.Errorf("wire: decode %s: %w", envelope.Type, err)
	}
	return nil
}
