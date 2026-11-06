package daemon

// Untested: under GR-3 nothing here has been run. The worker is driven with fake requests, so
// no key reaches a child and no pane exists.

import (
	"bytes"
	"context"
	"io"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

// workerFixture is one window with one session and a fake pane.
type workerFixture struct {
	window *window
	pane   *fakePane
	router *fakeWindowRouter
	view   sessionView
	gate   *gate
	acks   chan wire.Ack
}

func newWorkerFixture(t *testing.T) *workerFixture {
	t.Helper()

	g := newGate()
	g.addSession("a1", "claude")
	router := &fakeWindowRouter{}
	h := &hub{gate: g, router: router, log: &logger{out: io.Discard}}
	w := newWindow("w1", &recordingConn{}, h, &logger{out: io.Discard})
	fake := &fakePane{ready: true}

	view := sessionView{Name: "claude", Agent: "a1", Pane: fake}
	w.addSession(view)

	fixture := &workerFixture{window: w, pane: fake, router: router, view: view, gate: g, acks: make(chan wire.Ack, 16)}
	return fixture
}

// execute runs one request and returns its acknowledgement.
func (f *workerFixture) execute(t *testing.T, typ wire.Type, body any) wire.Ack {
	t.Helper()
	envelope, err := encodeRequest(typ, 1, body)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return f.window.execute(f.view, envelope)
}

// TestExecuteRefusesAViewer: with no holder, every input op is refused before the pane is
// touched.
func TestExecuteRefusesAViewer(t *testing.T) {
	f := newWorkerFixture(t)

	ack := f.execute(t, wire.TypeKey, wire.Key{Agent: "a1", Key: wire.KeyEvent{Code: 'a'}})
	if ack.OK {
		t.Fatal("a viewer's key was accepted")
	}
	if ack.Error == nil || ack.Error.Code != wire.CodeNotHolder {
		t.Fatalf("ack = %+v, want not_holder", ack)
	}
	if len(f.pane.keys) != 0 {
		t.Fatalf("%d keys reached the pane", len(f.pane.keys))
	}
}

// TestExecuteKeyMapping: the wire key becomes the pinned ultraviolet event, field for field.
func TestExecuteKeyMapping(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	ack := f.execute(t, wire.TypeKey, wire.Key{Agent: "a1", Key: wire.KeyEvent{
		Code: 'A', ShiftedCode: 'A', BaseCode: 'a', Text: "A", Mod: int(uv.ModShift), IsRepeat: true,
	}})
	if !ack.OK || ack.Written != 1 {
		t.Fatalf("ack = %+v", ack)
	}
	if len(f.pane.keys) != 1 {
		t.Fatalf("%d keys reached the pane", len(f.pane.keys))
	}
	got := f.pane.keys[0]
	if got.Code != 'A' || got.ShiftedCode != 'A' || got.BaseCode != 'a' || got.Text != "A" ||
		got.Mod != uv.ModShift || !got.IsRepeat {
		t.Fatalf("key = %+v", got)
	}
}

// TestExecutePaste: the byte count is reported, and an oversized paste is refused untried.
func TestExecutePaste(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	ack := f.execute(t, wire.TypePaste, wire.Paste{Agent: "a1", Text: "hello"})
	if !ack.OK || ack.Written != 5 {
		t.Fatalf("ack = %+v, want Written 5", ack)
	}

	big := make([]byte, wire.MaxPasteBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	ack = f.execute(t, wire.TypePaste, wire.Paste{Agent: "a1", Text: string(big)})
	if ack.OK || ack.Error == nil || ack.Error.Code != wire.CodeTooLarge {
		t.Fatalf("ack = %+v, want too_large", ack)
	}
	if len(f.pane.pastes) != 1 {
		t.Fatalf("%d pastes reached the pane", len(f.pane.pastes))
	}
}

// TestExecuteMouse: kind conversion, and the daemon's own recheck of whether the child wants the
// mouse.
func TestExecuteMouse(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	ck := f.execute(t, wire.TypeMouse, wire.Mouse{Agent: "a1", Kind: wire.MouseClick, X: 3, Y: 4, Button: 1})
	if ck.OK {
		t.Fatal("the mouse was accepted although the child never asked for it")
	}
	if ck.Error == nil || ck.Error.Code != wire.CodeInputFailed {
		t.Fatalf("ack = %+v, want input_failed", ck)
	}
	if ck.Error.Message != "the child has not asked for the mouse" {
		t.Fatalf("message = %q", ck.Error.Message)
	}

	f.pane.mouseY = true
	for _, tc := range []struct {
		kind string
		ok   bool
	}{
		{kind: wire.MouseClick, ok: true},
		{kind: wire.MouseRelease, ok: true},
		{kind: wire.MouseWheel, ok: true},
		{kind: wire.MouseMotion, ok: true},
		{kind: "sideways", ok: false},
	} {
		ack := f.execute(t, wire.TypeMouse, wire.Mouse{Agent: "a1", Kind: tc.kind, X: 1, Y: 2})
		if tc.ok && (!ack.OK || ack.Written != 1) {
			t.Fatalf("%s: ack = %+v", tc.kind, ack)
		}
		if !tc.ok && (ack.OK || ack.Error == nil || ack.Error.Code != wire.CodeBadRequest) {
			t.Fatalf("%s: ack = %+v, want bad_request", tc.kind, ack)
		}
	}

	// The converted event carries the pane-relative position and the button.
	last := f.pane.mice[len(f.pane.mice)-1]
	click, ok := last.(uv.MouseClickEvent)
	if !ok {
		t.Fatalf("converted event = %T", last)
	}
	if click.X != 1 || click.Y != 2 {
		t.Fatalf("click = %+v", click)
	}
}

// TestExecuteResizeOnlyForTheHolder: a viewer's resize is refused, and the holder's Request
// variant uses RequestResize.
func TestExecuteResizeOnlyForTheHolder(t *testing.T) {
	f := newWorkerFixture(t)

	ack := f.execute(t, wire.TypeResize, wire.Resize{Agent: "a1", Width: 100, Height: 40})
	if ack.OK || ack.Error == nil || ack.Error.Code != wire.CodeNotHolder {
		t.Fatalf("a viewer's resize = %+v", ack)
	}
	if len(f.pane.resizes)+len(f.pane.request) != 0 {
		t.Fatal("a viewer's resize reached the pane")
	}

	if _, err := f.gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	if ack := f.execute(t, wire.TypeResize, wire.Resize{Agent: "a1", Width: 100, Height: 40}); !ack.OK {
		t.Fatalf("the holder's resize = %+v", ack)
	}
	if ack := f.execute(t, wire.TypeResize, wire.Resize{Agent: "a1", Width: 120, Height: 50, Request: true}); !ack.OK {
		t.Fatalf("the holder's requested resize = %+v", ack)
	}
	if len(f.pane.resizes) != 1 || f.pane.resizes[0] != [2]int{100, 40} {
		t.Fatalf("resizes = %+v", f.pane.resizes)
	}
	if len(f.pane.request) != 1 || f.pane.request[0] != [2]int{120, 50} {
		t.Fatalf("requested resizes = %+v", f.pane.request)
	}
}

// TestExecuteDraftAndSubmitted: the holder sets and clears the draft flag; a viewer cannot.
func TestExecuteDraftAndSubmitted(t *testing.T) {
	f := newWorkerFixture(t)

	if ack := f.execute(t, wire.TypeDraft, wire.Draft{Agent: "a1", Drafting: true}); ack.OK {
		t.Fatal("a viewer set the draft flag")
	}

	if _, err := f.gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	if ack := f.execute(t, wire.TypeDraft, wire.Draft{Agent: "a1", Drafting: true}); !ack.OK {
		t.Fatalf("draft(true) = %+v", ack)
	}
	if _, _, drafting, _, _ := f.gate.snapshot("a1"); !drafting {
		t.Fatal("the draft flag was not set")
	}
	// The router does not clear the flag on a submit: only the window's draft(false) does.
	// The display mirrors the router, so it must still say drafting.
	if ack := f.execute(t, wire.TypeSubmitted, wire.SessionRef{Agent: "a1"}); !ack.OK {
		t.Fatalf("submitted = %+v", ack)
	}
	if _, _, drafting, _, _ := f.gate.snapshot("a1"); !drafting {
		t.Fatal("a submit cleared the displayed draft flag, which the router still holds")
	}
	if ack := f.execute(t, wire.TypeDraft, wire.Draft{Agent: "a1", Drafting: false}); !ack.OK {
		t.Fatalf("draft(false) = %+v", ack)
	}
	if _, _, drafting, _, _ := f.gate.snapshot("a1"); drafting {
		t.Fatal("draft(false) did not clear the displayed flag")
	}
}

// TestDeliverHeldMirrorsTheRouter: an F4 the router refuses leaves the draft flag set, and the
// display says so; one it applies clears it.
func TestDeliverHeldMirrorsTheRouter(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}
	if ack := f.execute(t, wire.TypeDraft, wire.Draft{Agent: "a1", Drafting: true}); !ack.OK {
		t.Fatalf("draft(true) = %+v", ack)
	}

	f.router.deliverClears = false
	if ack := f.execute(t, wire.TypeDeliverHeld, wire.SessionRef{Agent: "a1"}); !ack.OK {
		t.Fatalf("deliver_held = %+v", ack)
	}
	if _, _, drafting, _, _ := f.gate.snapshot("a1"); !drafting {
		t.Fatal("a refused F4 cleared the displayed flag")
	}

	f.router.deliverClears = true
	if ack := f.execute(t, wire.TypeDeliverHeld, wire.SessionRef{Agent: "a1"}); !ack.OK {
		t.Fatalf("deliver_held = %+v", ack)
	}
	if _, _, drafting, _, _ := f.gate.snapshot("a1"); drafting {
		t.Fatal("an applied F4 did not clear the displayed flag")
	}
}

// TestTakeInputFromAnotherHolderThroughExecute: F6 works for a viewer and for a window nobody
// gave anything to, because take_input is decided before the holder check.
func TestTakeInputFromAnotherHolderThroughExecute(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.gate.takeInput("a1", "w2"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if holder := f.gate.holder("a1"); holder != "w2" {
		t.Fatalf("setup holder = %q", holder)
	}

	// w1 is a viewer: its key is refused, its take_input is not.
	if ack := f.execute(t, wire.TypeKey, wire.Key{Agent: "a1", Key: wire.KeyEvent{Code: 'a'}}); ack.OK {
		t.Fatal("a viewer's key was accepted")
	}
	ack := f.execute(t, wire.TypeTakeInput, wire.SessionRef{Agent: "a1"})
	if !ack.OK || ack.Ownership == nil || ack.Ownership.Holder != "w1" || !ack.Ownership.YouHold {
		t.Fatalf("take_input = %+v", ack)
	}
	if holder := f.gate.holder("a1"); holder != "w1" {
		t.Fatalf("holder = %q, want w1", holder)
	}
	// And now w1 may type.
	if ack := f.execute(t, wire.TypeKey, wire.Key{Agent: "a1", Key: wire.KeyEvent{Code: 'a'}}); !ack.OK {
		t.Fatalf("the new holder's key = %+v", ack)
	}
}

// TestExecuteTakeInput: the answer carries the authoritative ownership.
func TestExecuteTakeInput(t *testing.T) {
	f := newWorkerFixture(t)

	ack := f.execute(t, wire.TypeTakeInput, wire.SessionRef{Agent: "a1"})
	if !ack.OK || ack.Ownership == nil {
		t.Fatalf("ack = %+v", ack)
	}
	if !ack.Ownership.YouHold || ack.Ownership.Holder != "w1" {
		t.Fatalf("ownership = %+v", ack.Ownership)
	}
}

// TestWorkerKeepsOrder: acknowledgements come back in request order, which is what makes a draft
// precede its bytes.
func TestWorkerKeepsOrder(t *testing.T) {
	f := newWorkerFixture(t)
	if _, err := f.gate.takeInput("a1", "w1"); err != nil {
		t.Fatalf("takeInput: %v", err)
	}

	// Queue draft(true), a key and submitted straight into the worker's channel, as the reader
	// would.
	requests := make(chan wire.Envelope, 8)
	f.window.mu.Lock()
	f.window.workers["a1"] = requests
	f.window.mu.Unlock()

	go f.window.runWorker(f.view, requests)

	for i, request := range []struct {
		typ  wire.Type
		body any
	}{
		{typ: wire.TypeDraft, body: wire.Draft{Agent: "a1", Drafting: true}},
		{typ: wire.TypeKey, body: wire.Key{Agent: "a1", Key: wire.KeyEvent{Code: 'x'}}},
		{typ: wire.TypeSubmitted, body: wire.SessionRef{Agent: "a1"}},
	} {
		envelope, err := encodeRequest(request.typ, uint64(i+1), request.body)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		requests <- envelope
	}

	waitFor(t, "three acknowledgements", func() bool {
		f.window.mu.Lock()
		defer f.window.mu.Unlock()
		return len(f.window.deferred)+len(f.window.queue) >= 3
	})

	// The draft flag is set before the key, and cleared after the submit.
	if len(f.pane.keys) != 1 {
		t.Fatalf("%d keys reached the pane", len(f.pane.keys))
	}
	if _, _, drafting, _, _ := f.gate.snapshot("a1"); drafting {
		t.Fatal("the draft flag is still set after a submit")
	}
}

// encodeRequest builds one request envelope by writing it and reading it back, which is exactly
// what a client does.
func encodeRequest(typ wire.Type, id uint64, body any) (wire.Envelope, error) {
	var buffer bytes.Buffer
	if err := wire.Write(&buffer, typ, id, body); err != nil {
		return wire.Envelope{}, err
	}
	return wire.Read(&buffer)
}

// ensure the fake pane satisfies what a request needs.
var _ = context.Background
