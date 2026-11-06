package wire

// Untested: under GR-3 nothing here has been run. Framing is tested in memory only; no
// socket is opened.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// TestRoundTrip: every message type survives an encode and a decode.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
		id   uint64
		body any
	}{
		{name: "hello", typ: TypeHello, body: Hello{Build: "1.2.3", Role: RoleWindow, PID: 42}},
		{name: "hello_ack", typ: TypeHelloAck, body: HelloAck{Build: "1.2.3", PID: 42, WindowID: "abcd1234"}},
		{name: "refused", typ: TypeRefused, body: Refusal{Code: CodeVersionMismatch, Message: "stop the daemon"}},
		{name: "attach", typ: TypeAttach, body: Attach{Dir: "/tmp", Start: []string{"claude"}, Width: 80, Height: 24}},
		{name: "attached", typ: TypeAttached, body: Attached{WindowID: "abcd1234", Pending: 3}},
		{name: "key", typ: TypeKey, body: Key{Agent: "a1", Key: KeyEvent{Code: 97, Text: "a"}}},
		{name: "paste", typ: TypePaste, body: Paste{Agent: "a1", Text: "hello"}},
		{name: "mouse", typ: TypeMouse, body: Mouse{Agent: "a1", Kind: MouseClick, X: 3, Y: 4, Button: 1}},
		{name: "resize", typ: TypeResize, body: Resize{Agent: "a1", Width: 100, Height: 40}},
		{name: "draft", typ: TypeDraft, body: Draft{Agent: "a1", Drafting: true}},
		{name: "session ref", typ: TypeTakeInput, body: SessionRef{Agent: "a1"}},
		{name: "query", typ: TypeQuery, body: Query{Kind: QueryPending}},
		{name: "result", typ: TypeResult, body: Result{Status: &Status{PID: 7, Build: "x", Sessions: []SessionStatus{{Name: "claude"}}}}},
		{name: "stop_session", typ: TypeStopSession, body: StopSession{Name: "claude"}},
		{name: "ack", typ: TypeAck, body: Ack{OK: true, Written: 12}},
		{name: "pane", typ: TypePane, body: PaneState{Agent: "a1", Rev: 9, Cols: 80, Rows: 24}},
		{name: "event", typ: TypeEvent, body: protocol.Event{Resync: true}},
		{name: "bye", typ: TypeBye, body: Bye{Code: CodeShuttingDown, Message: "stopping"}},
		{name: "status request has no body", typ: TypeStatus, body: nil},
		{name: "detach has no body", typ: TypeDetach, body: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			if err := Write(&buffer, tc.typ, tc.id, tc.body); err != nil {
				t.Fatalf("Write: %v", err)
			}

			envelope, err := Read(&buffer)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if envelope.Type != tc.typ {
				t.Fatalf("type = %q, want %q", envelope.Type, tc.typ)
			}
			if envelope.ID != tc.id {
				t.Fatalf("id = %d, want %d", envelope.ID, tc.id)
			}
			if tc.body == nil {
				if len(envelope.Body) != 0 {
					t.Fatalf("a bodyless message carried %d bytes", len(envelope.Body))
				}
				return
			}
			// Round-trip through generic JSON: the shape a peer would see.
			var got, want map[string]any
			if err := json.Unmarshal(envelope.Body, &got); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			raw, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal the expectation: %v", err)
			}
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatalf("unmarshal the expectation: %v", err)
			}
			if !sameJSON(got, want) {
				t.Fatalf("body = %s, want %s", envelope.Body, raw)
			}
		})
	}
}

func sameJSON(a, b map[string]any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

// TestWriteRefusesAnOversizeMessage: a body too large for a frame is refused, not written.
func TestWriteRefusesAnOversizeMessage(t *testing.T) {
	big := bytes.Repeat([]byte("x"), MaxMessageBytes+64)
	var buffer bytes.Buffer

	err := Write(&buffer, TypePaste, 0, Paste{Agent: "a1", Text: string(big)})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Write = %v, want ErrTooLarge", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("%d bytes were written for a refused message", buffer.Len())
	}
}

// TestReadRefusesAnOversizeFrame: a declared length over the limit is refused before any
// of the body is read or allocated.
func TestReadRefusesAnOversizeFrame(t *testing.T) {
	var header [headerBytes]byte
	binary.BigEndian.PutUint32(header[:], MaxMessageBytes+1)

	// A reader that would report any read past the header: none may happen.
	reader := &countingReader{data: header[:]}
	if _, err := Read(reader); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Read = %v, want ErrTooLarge", err)
	}
	if reader.reads != 1 {
		t.Fatalf("Read made %d read calls, want 1 (the header only)", reader.reads)
	}
}

// TestReadRefusesAZeroLengthFrame: a zero-length frame is not a message.
func TestReadRefusesAZeroLengthFrame(t *testing.T) {
	var header [headerBytes]byte
	reader := &countingReader{data: header[:]}
	if _, err := Read(reader); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Read = %v, want ErrTooLarge", err)
	}
}

// TestReadRefusesAMessageWithoutAType.
func TestReadRefusesAMessageWithoutAType(t *testing.T) {
	var buffer bytes.Buffer
	payload := []byte(`{"id":4}`)
	var header [headerBytes]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	buffer.Write(header[:])
	buffer.Write(payload)

	if _, err := Read(&buffer); err == nil {
		t.Fatal("Read accepted a message with no type")
	}
}

// TestDecodeAcceptsAMissingBody: a bodyless message decodes to the zero value.
func TestDecodeAcceptsAMissingBody(t *testing.T) {
	var hello Hello
	if err := Decode(Envelope{Type: TypeHello}, &hello); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if hello != (Hello{}) {
		t.Fatalf("decoded %+v, want the zero value", hello)
	}
}

// TestDecodeRefusesAMalformedBody.
func TestDecodeRefusesAMalformedBody(t *testing.T) {
	var hello Hello
	err := Decode(Envelope{Type: TypeHello, Body: json.RawMessage(`{"build":`)}, &hello)
	if err == nil {
		t.Fatal("Decode accepted a malformed body")
	}
}

// TestReadTruncatedFrame: a short read is an error, never a partial message.
func TestReadTruncatedFrame(t *testing.T) {
	var buffer bytes.Buffer
	if err := Write(&buffer, TypeStatus, 0, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	full := buffer.Bytes()
	if _, err := Read(bytes.NewReader(full[:len(full)-2])); err == nil {
		t.Fatal("Read accepted a truncated frame")
	}
}

// countingReader records how many Read calls were made.
type countingReader struct {
	data  []byte
	at    int
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.at >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.at:])
	r.at += n
	return n, nil
}
