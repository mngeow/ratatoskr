package pane

// Mouse support for one pane: which tracking the child asked for, whether an
// event may be sent, and the encoder path itself.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No child has ever enabled a
// mouse mode, and no mouse event has ever been encoded.

import (
	"errors"
	"math"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ErrMouseDisabled reports a mouse event the child has not asked for: either no
// tracking mode is on, or the event kind is not one the active mode sends. The
// UI treats it as a stale decision rather than a failure to show.
var ErrMouseDisabled = errors.New("pane: mouse event not enabled by the child")

// Mouse tracking modes, as bits. The numbers are the DEC private modes the child
// sets with CSI ? N h; 1006 is the SGR encoding rather than a tracking mode.
const (
	mouseModeX10         uint32 = 1 << iota // 9: press only
	mouseModeNormal                         // 1000: press and release
	mouseModeHighlight                      // 1001: press, release and highlight
	mouseModeButtonEvent                    // 1002: press, release and button motion
	mouseModeAnyEvent                       // 1003: press, release and all motion
	mouseModeSGR                            // 1006: SGR encoding
)

// mouseTrackingMask covers the five tracking modes; 1006 is not one of them.
const mouseTrackingMask = mouseModeX10 | mouseModeNormal | mouseModeHighlight | mouseModeButtonEvent | mouseModeAnyEvent

// mouseModeBit maps a DEC mode to its bit, or 0 for a mode this package does not
// track.
func mouseModeBit(mode ansi.Mode) uint32 {
	dec, ok := mode.(ansi.DECMode)
	if !ok {
		return 0
	}
	switch dec {
	case ansi.ModeMouseX10:
		return mouseModeX10
	case ansi.ModeMouseNormal:
		return mouseModeNormal
	case ansi.ModeMouseHighlight:
		return mouseModeHighlight
	case ansi.ModeMouseButtonEvent:
		return mouseModeButtonEvent
	case ansi.ModeMouseAnyEvent:
		return mouseModeAnyEvent
	case ansi.ModeMouseExtSgr:
		return mouseModeSGR
	default:
		return 0
	}
}

// trackMode records a mode change reported by the emulator.
//
// The emulator calls this from the goroutine that writes child output into it, so
// the tracker is an atomic bitset rather than a lock: taking a lock here would
// put the reader behind the UI's input path.
//
// UNTESTED.
func (p *Pane) trackMode(mode ansi.Mode, enable bool) {
	bit := mouseModeBit(mode)
	if bit == 0 {
		return
	}
	if enable {
		p.mouseModes.Or(bit)
		return
	}
	p.mouseModes.And(^bit)
}

// trackingMode returns the tracking mode in force. x/vt prefers the highest of
// 1003, 1002, 1001, 1000 and 9 when several are on, so this mirrors that order.
func trackingMode(modes uint32) uint32 {
	switch {
	case modes&mouseModeAnyEvent != 0:
		return mouseModeAnyEvent
	case modes&mouseModeButtonEvent != 0:
		return mouseModeButtonEvent
	case modes&mouseModeHighlight != 0:
		return mouseModeHighlight
	case modes&mouseModeNormal != 0:
		return mouseModeNormal
	case modes&mouseModeX10 != 0:
		return mouseModeX10
	default:
		return 0
	}
}

// MouseEnabled reports whether the child would accept this event.
//
// It reads only an atomic, so the UI may call it while deciding what to forward.
// Motion needs 1003, or 1002 with a button held; X10 sends presses only, so a
// release or a drag is not eligible there, while a wheel is treated as a press;
// a wheel needs some tracking to be on at all.
//
// UNTESTED.
func (p *Pane) MouseEnabled(event uv.MouseEvent) bool {
	if event == nil {
		return false
	}

	tracking := trackingMode(p.mouseModes.Load())
	if tracking == 0 {
		return false
	}

	switch event.(type) {
	case uv.MouseMotionEvent:
		if tracking == mouseModeAnyEvent {
			return true
		}
		return tracking == mouseModeButtonEvent && event.Mouse().Button != uv.MouseNone
	case uv.MouseReleaseEvent:
		return tracking != mouseModeX10
	case uv.MouseWheelEvent:
		return true
	default:
		return true
	}
}

// SendMouse forwards one mouse event to the child.
//
// It takes the same path as a keystroke - input transaction, emulator, pipe flush
// - so a click cannot overtake a keystroke or a paste that was queued before it,
// and there is still exactly one writer. The transport itself is the senior's to
// refine; this draft deliberately does not change it.
//
// A mode change between the UI's decision and this write is not an error: the
// event is refused with ErrMouseDisabled instead of being encoded into a stream
// the child no longer reads.
//
// UNTESTED.
func (p *Pane) SendMouse(event uv.MouseEvent) error {
	if event == nil {
		return errors.New("pane: send mouse: nil event")
	}
	return p.sendEncoded(func() int32 { return 1 }, func() error {
		if !p.MouseEnabled(event) {
			return ErrMouseDisabled
		}
		event = clampMouse(event, p.mouseModes.Load()&mouseModeSGR != 0)
		p.emu.SendMouse(event)
		return nil
	})
}

// clampMouse keeps an event inside the range its encoding can express, without
// changing which kind of event it is.
func clampMouse(event uv.MouseEvent, sgr bool) uv.MouseEvent {
	mouse := event.Mouse()

	maximum := 222
	if sgr {
		maximum = math.MaxInt32
	}
	if mouse.X < 0 {
		mouse.X = 0
	}
	if mouse.Y < 0 {
		mouse.Y = 0
	}
	if mouse.X > maximum {
		mouse.X = maximum
	}
	if mouse.Y > maximum {
		mouse.Y = maximum
	}

	switch event.(type) {
	case uv.MouseClickEvent:
		return uv.MouseClickEvent(mouse)
	case uv.MouseReleaseEvent:
		return uv.MouseReleaseEvent(mouse)
	case uv.MouseWheelEvent:
		return uv.MouseWheelEvent(mouse)
	case uv.MouseMotionEvent:
		return uv.MouseMotionEvent(mouse)
	default:
		return event
	}
}

// InputReady reports whether input can still reach the child: not closed, not
// exited, and the reply drain alive. It is atomics only, so the UI can ask on
// every keystroke.
//
// UNTESTED.
func (p *Pane) InputReady() bool {
	if p.closed.Load() || p.exited.Load() || p.drainDead.Load() || p.emulatorDead.Load() {
		return false
	}
	if since := p.emulatorBusyAt.Load(); since != 0 && time.Since(time.Unix(0, since)) > paneOperationLimit {
		return false
	}
	return true
}
