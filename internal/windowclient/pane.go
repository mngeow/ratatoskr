package windowclient

// The Pane adapter: one daemon session presented as a tui.Pane.
//
// Everything it reports comes from a cached wire.PaneState or from an atomic, so the TUI can
// poll it at its own rate without touching the socket. Everything it sends goes through the
// client's single writer, in the order the TUI queued it, and the daemon decides whether this
// window may type.
//
// UNTESTED: see doc.go.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// The wire's mouse bits, in the order wire.MouseX10..wire.MouseSGR declares them. The daemon and
// this client agree on this order; the pane package's unexported constants are not available to
// me, and I must not import them anyway.
const (
	mouseX10         uint32 = 1 << 0
	mouseNormal      uint32 = 1 << 1
	mouseHighlight   uint32 = 1 << 2
	mouseButtonEvent uint32 = 1 << 3
	mouseAnyEvent    uint32 = 1 << 4
	mouseSGR         uint32 = 1 << 5
)

// paneSurface is tui.Pane, copied here so the method set is checked without importing the tui
// package: a window client may not depend on it.
type paneSurface interface {
	Resize(width, height int) error
	RequestResize(width, height int) error
	Snapshot() pane.Frame
	SendKey(key uv.KeyPressEvent) error
	Paste(text string) error
	SendMouse(event uv.MouseEvent) error
	MouseEnabled(event uv.MouseEvent) bool
	InputReady() bool
	Err() error
}

// Pane is one session as the TUI sees it.
var _ paneSurface = (*Pane)(nil)

type Pane struct {
	client *Client
	agent  protocol.AgentID
	name   string
	addr   string

	state      atomic.Pointer[wire.PaneState]
	stateMu    sync.Mutex
	highestRev atomic.Uint64
	ended      atomic.Bool

	// desired is the newest size this window asked for, packed. It survives while the window is
	// a viewer, so becoming the holder applies the size it wants rather than an old one.
	desired atomic.Uint64
	// resizeWake is a one-slot signal for the per-pane resize worker.
	resizeWake chan struct{}

	opMu  sync.Mutex
	opErr *operationError
}

// newPane builds a pane from one attached slot.
func newPane(client *Client, slot wire.Slot) *Pane {
	p := &Pane{
		client:     client,
		agent:      slot.Agent,
		name:       slot.Name,
		addr:       slot.Address,
		resizeWake: make(chan struct{}, 1),
	}
	p.desired.Store(packSize(slot.Pane.Cols, slot.Pane.Rows))
	p.ended.Store(slot.Pane.Ended)
	p.highestRev.Store(slot.Pane.Rev)
	state := copyPaneState(slot.Pane)
	p.state.Store(&state)

	client.wg.Add(1)
	go p.resizeLoop()
	return p
}

// Agent is this pane's stable session ID.
func (p *Pane) Agent() protocol.AgentID { return p.agent }

// Name is the session's display name.
func (p *Pane) Name() string { return p.name }

// Address is the session's network name.
func (p *Pane) Address() string { return p.addr }

// State is the complete cached pane state, by value.
func (p *Pane) State() wire.PaneState {
	if state := p.state.Load(); state != nil {
		return copyPaneState(*state)
	}
	return wire.PaneState{Agent: p.agent}
}

// Ended reports whether the session has ended. It is a permanent latch.
func (p *Pane) Ended() bool { return p.ended.Load() }

// Snapshot is the last frame the daemon sent. There is no earlier scrollback.
func (p *Pane) Snapshot() pane.Frame { return p.State().Frame }

// InputReady reports whether input can still reach the child. It is the transport's answer only,
// never the ownership gate's.
func (p *Pane) InputReady() bool {
	state := p.State()
	return state.InputReady && !state.Ended && p.client.Err() == nil
}

// Err reports why this pane cannot be used, or nil.
//
// The transport's own error for this pane comes first, then the client's disconnect, then a
// latched operation error. An ended session is not an error by itself: the UI shows it as ended.
func (p *Pane) Err() error {
	state := p.State()
	if state.Err != "" {
		return errors.New(state.Err)
	}
	if err := p.client.Err(); err != nil {
		return err
	}
	if err := p.OperationError(); err != nil {
		return err
	}
	return nil
}

// OperationError is the latched failure for this session, or nil.
func (p *Pane) OperationError() error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if p.opErr == nil {
		return nil
	}
	return p.opErr
}

// latch records a failure, keeping the first cause for as long as the latch stands.
//
// Your rule: the first refusal wins, so a later failure cannot overwrite the cause a user is
// being shown. A recoverable latch clears only on a successful take_input.
func (p *Pane) latch(err error, recoverable bool) {
	if err == nil {
		return
	}

	p.opMu.Lock()
	first := p.opErr == nil
	if first {
		var coded interface{ Code() string }
		if errors.As(err, &coded) && coded.Code() == string(wire.CodeInputFailed) {
			recoverable = false
		}
		if p.client.Err() != nil {
			recoverable = false
		}
		p.opErr = &operationError{cause: err, recoverable: recoverable}
	}
	latched := p.opErr
	p.opMu.Unlock()

	if first {
		p.client.notify(WindowEvent{Kind: KindOperationError, Agent: p.agent, Error: latched})
	}
}

// clearRecoverableLatch drops a recoverable failure after a successful take_input. A transport or
// disconnect failure is never cleared this way.
func (p *Pane) clearRecoverableLatch() {
	p.opMu.Lock()
	cleared := p.opErr != nil && p.opErr.recoverable
	if cleared {
		p.opErr = nil
	}
	p.opMu.Unlock()

	if cleared {
		p.client.notify(WindowEvent{Kind: KindOperationError, Agent: p.agent})
	}
}

// apply installs one complete pane state.
//
// The ownership fields apply only at a revision at least as high as the highest already applied,
// so a frame that was sent before an ownership change cannot undo it. The ended latch applies
// whatever its revision is, and once latched it never clears.
func (p *Pane) apply(state wire.PaneState) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if state.Agent == "" {
		state.Agent = p.agent
	}

	p.ended.Store(p.ended.Load() || state.Ended)

	current := p.State()
	merged := state
	merged.Ended = p.ended.Load()
	if merged.Rev < p.highestRev.Load() {
		// A stale frame: keep the ownership fields the window already applied, and take only the
		// parts that are not revisioned.
		merged.Rev = current.Rev
		merged.Holder = current.Holder
		merged.YouHold = current.YouHold
		merged.Drafting = current.Drafting
	} else {
		p.highestRev.Store(merged.Rev)
	}
	if p.ended.Load() {
		merged.Holder = ""
		merged.YouHold = false
	}
	if merged.ExitCode == nil && current.ExitCode != nil {
		merged.ExitCode = current.ExitCode
	}
	if current.Ended {
		if current.ExitCode != nil {
			merged.ExitCode = current.ExitCode
		}
		if current.StartError != "" {
			merged.StartError = current.StartError
		}
	}
	merged = copyPaneState(merged)

	p.state.Store(&merged)
	if merged.YouHold && !current.YouHold {
		p.requestResizeNow()
	}
}

// applyOwnership installs an authoritative ownership change.
func (p *Pane) applyOwnership(ownership wire.Ownership) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.ended.Load() {
		// Once a session has ended, ownership is meaningless: it stays ended and holder-less.
		return
	}

	current := p.State()
	if ownership.Rev < p.highestRev.Load() {
		return
	}
	p.highestRev.Store(ownership.Rev)

	current.Rev = ownership.Rev
	current.Holder = ownership.Holder
	current.YouHold = ownership.YouHold
	p.state.Store(&current)

	// Becoming the holder applies the size this window wants; nothing else changes.
	if ownership.YouHold {
		p.requestResizeNow()
	}
}

// markEnded latches the end from a session_ended message, so a window sees the end even if the
// final frame is lost.
func (p *Pane) markEnded(rev uint64, exitCode *int) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.ended.Store(true)

	current := p.State()
	if rev > p.highestRev.Load() {
		p.highestRev.Store(rev)
	}
	current.Rev = p.highestRev.Load()
	current.Ended = true
	current.Holder = ""
	current.YouHold = false
	if exitCode != nil {
		current.ExitCode = exitCode
	}
	current = copyPaneState(current)
	p.state.Store(&current)

	p.client.notify(WindowEvent{Kind: KindSessionEnded, Agent: p.agent})
}

// Resize applies a size, waiting for the daemon's answer. Only the holder's size is applied, so a
// viewer's is refused locally without a frame.
func (p *Pane) Resize(width, height int) error {
	if err := validSize(width, height); err != nil {
		return err
	}
	if err := p.checkInputable(); err != nil {
		return err
	}
	if !p.State().YouHold {
		return notHolderError()
	}
	p.desired.Store(packSize(width, height))
	return p.client.resize(p.agent, width, height, false)
}

// RequestResize asks for a size without waiting. The newest request wins, and nothing is sent
// while this window is a viewer.
func (p *Pane) RequestResize(width, height int) error {
	if err := validSize(width, height); err != nil {
		return err
	}
	if err := p.client.Err(); err != nil {
		return err
	}
	if p.ended.Load() {
		return endedError()
	}
	p.desired.Store(packSize(width, height))
	p.requestResizeNow()
	return nil
}

// requestResizeNow signals the resize worker.
func (p *Pane) requestResizeNow() {
	select {
	case p.resizeWake <- struct{}{}:
	default:
	}
}

// resizeLoop sends the newest requested size whenever this window becomes the holder.
//
// One worker per pane, so there is no goroutine per request, and only the newest size is ever
// sent.
func (p *Pane) resizeLoop() {
	defer p.client.wg.Done()
	for {
		select {
		case <-p.client.done:
			return
		case <-p.resizeWake:
			if p.ended.Load() || !p.State().YouHold || p.OperationError() != nil {
				continue
			}
			cols, rows := unpackSize(p.desired.Load())
			if cols < 1 || rows < 1 {
				continue
			}
			if err := p.client.resize(p.agent, cols, rows, true); err != nil {
				p.latch(err, true)
			}
		}
	}
}

// SendKey forwards one keypress.
func (p *Pane) SendKey(key uv.KeyPressEvent) error {
	if err := p.checkInputable(); err != nil {
		return err
	}

	body := wire.Key{
		Agent: p.agent,
		Key: wire.KeyEvent{
			Code:        int32(key.Code),
			ShiftedCode: int32(key.ShiftedCode),
			BaseCode:    int32(key.BaseCode),
			Text:        key.Text,
			Mod:         int(key.Mod),
			IsRepeat:    key.IsRepeat,
		},
	}
	written, err := p.client.input(wire.TypeKey, body, 1)
	if err != nil {
		p.latch(err, true)
		return err
	}
	_ = written
	return nil
}

// Paste forwards text, after the size check the daemon also applies.
func (p *Pane) Paste(text string) error {
	if err := p.checkInputable(); err != nil {
		return err
	}
	if len(text) > wire.MaxPasteBytes {
		err := errors.New("windowclient: that paste is too large")
		p.latch(err, true)
		return err
	}

	_, err := p.client.input(wire.TypePaste, wire.Paste{Agent: p.agent, Text: text}, len(text))
	if err != nil {
		p.latch(err, true)
		return err
	}
	return nil
}

// SendMouse forwards one mouse event, carrying its real kind.
func (p *Pane) SendMouse(event uv.MouseEvent) error {
	if err := p.checkInputable(); err != nil {
		return err
	}
	if event == nil {
		return errors.New("windowclient: send mouse: nil event")
	}
	if !p.MouseEnabled(event) {
		err := errors.New("the child has not asked for the mouse")
		return err
	}

	mouse, ok := wireMouse(event)
	if !ok {
		return fmt.Errorf("windowclient: %T is not a mouse event kind a window sends", event)
	}
	mouse.Agent = p.agent

	if _, err := p.client.input(wire.TypeMouse, mouse, 1); err != nil {
		p.latch(err, true)
		return err
	}
	return nil
}

// MouseEnabled reproduces the pinned pane's rule from the wire mask alone.
//
// Tracking is the highest of 1003, 1002, 1001, 1000 and 9 that is set. Motion needs 1003, or 1002
// with a button held; a release is not eligible under X10; a wheel needs any tracking at all; a
// press needs any tracking at all.
func (p *Pane) MouseEnabled(event uv.MouseEvent) bool {
	if event == nil {
		return false
	}
	tracking := trackingMode(p.State().Mouse)
	if tracking == 0 {
		return false
	}

	switch event.(type) {
	case uv.MouseMotionEvent:
		if tracking == mouseAnyEvent {
			return true
		}
		return tracking == mouseButtonEvent && event.Mouse().Button != uv.MouseNone
	case uv.MouseReleaseEvent:
		return tracking != mouseX10
	case uv.MouseWheelEvent:
		return true
	default:
		return true
	}
}

// TakeInput asks for input ownership. A success is the only thing that clears a recoverable
// operation latch, and it is never blocked by one: the request must reach the daemon.
func (p *Pane) TakeInput(ctx context.Context) error {
	if p.ended.Load() {
		return endedError()
	}

	ownership, err := p.client.takeInput(ctx, p.agent)
	if err != nil {
		return err
	}

	p.applyOwnership(ownership)
	p.clearRecoverableLatch()
	p.requestResizeNow()
	p.client.notify(WindowEvent{Kind: KindOwnership, Agent: p.agent, Ownership: ownership})
	return nil
}

// checkInputable is the local gate: an ended session and a latched failure stop input before a
// frame is written. The daemon rechecks ownership regardless.
func (p *Pane) checkInputable() error {
	if p.ended.Load() {
		return endedError()
	}
	if err := p.OperationError(); err != nil {
		return err
	}
	if p.client.Err() != nil {
		return p.client.Err()
	}
	if !p.State().YouHold {
		return notHolderError()
	}
	return nil
}

// trackingMode is the highest tracking mode the mask carries.
func trackingMode(mask uint32) uint32 {
	switch {
	case mask&mouseAnyEvent != 0:
		return mouseAnyEvent
	case mask&mouseButtonEvent != 0:
		return mouseButtonEvent
	case mask&mouseHighlight != 0:
		return mouseHighlight
	case mask&mouseNormal != 0:
		return mouseNormal
	case mask&mouseX10 != 0:
		return mouseX10
	}
	return 0
}

// wireMouse converts an ultraviolet event to the wire's, keeping its real kind.
func wireMouse(event uv.MouseEvent) (wire.Mouse, bool) {
	switch typed := event.(type) {
	case uv.MouseClickEvent:
		return wire.Mouse{Kind: wire.MouseClick, X: typed.X, Y: typed.Y, Button: int(typed.Button), Mod: int(typed.Mod)}, true
	case uv.MouseReleaseEvent:
		return wire.Mouse{Kind: wire.MouseRelease, X: typed.X, Y: typed.Y, Button: int(typed.Button), Mod: int(typed.Mod)}, true
	case uv.MouseWheelEvent:
		return wire.Mouse{Kind: wire.MouseWheel, X: typed.X, Y: typed.Y, Button: int(typed.Button), Mod: int(typed.Mod)}, true
	case uv.MouseMotionEvent:
		return wire.Mouse{Kind: wire.MouseMotion, X: typed.X, Y: typed.Y, Button: int(typed.Button), Mod: int(typed.Mod)}, true
	}
	return wire.Mouse{}, false
}

// packSize packs a size for the atomic.
func packSize(width, height int) uint64 {
	return uint64(uint32(width))<<32 | uint64(uint32(height)) //nolint:gosec // validated sizes.
}

// unpackSize reads a packed size.
func unpackSize(packed uint64) (width, height int) {
	return int(packed >> 32), int(packed & 0xffffffff) //nolint:gosec // packed from two sizes.
}

func copyPaneState(state wire.PaneState) wire.PaneState {
	if state.ExitCode != nil {
		code := *state.ExitCode
		state.ExitCode = &code
	}
	return state
}

func validSize(width, height int) error {
	if width < 1 || height < 1 || width > 65535 || height > 65535 {
		return errors.New("windowclient: pane size must be between 1 and 65535")
	}
	return nil
}
