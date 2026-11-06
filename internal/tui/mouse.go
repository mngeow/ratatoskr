package tui

// Mouse routing between the two panes.
//
// A click is only ever forwarded, never interpreted: this file decides which pane
// receives an event and in which coordinates, and nothing else. It never sends,
// pastes or acts on a message body.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// mouseState is drag ownership. Once a press is forwarded to a pane, that pane
// keeps the rest of the gesture, even if the pointer crosses the separator: a
// drag that changed owner mid-gesture would be two half gestures in two children.
type mouseState struct {
	owner int // pane index, or -1 when no drag is in progress
	// focusConsumed marks a press that only moved focus, so its release is
	// swallowed rather than delivered as a click nobody made.
	focusConsumed bool
	button        uv.MouseButton
}

// mouseTarget says where a terminal cell falls.
type mouseTarget struct {
	pane   int // -1 when the cell is not inside a pane
	header bool
	body   bool
	x, y   int // pane-relative, when body is true
}

// mouseTargetAt maps a screen cell to a pane region, using the active layout.
//
// It keeps the shape the existing tests use. With two panes it is the split those
// tests describe; with one pane it is the full-width rectangle, because the
// geometry comes from the model's active count rather than a global rule.
func (m *Model) mouseTargetAt(x, y int) mouseTarget {
	target := mouseTarget{pane: -1}

	box, ok := m.boxAt(x, y)
	if !ok {
		return target
	}

	target.pane = box.index
	target.header = box.inHeader(x, y)
	target.body = box.inBody(x, y)
	if target.body {
		target.x, target.y = box.relative(x, y)
	}
	return target
}

// mouseEventFromTea copies a Bubble Tea mouse message into the emulator's event
// type field by field, keeping which kind of event it is.
func mouseEventFromTea(msg tea.MouseMsg) uv.MouseEvent {
	if msg == nil {
		return nil
	}

	mouse := msg.Mouse()
	event := uv.Mouse{
		X:      mouse.X,
		Y:      mouse.Y,
		Button: uv.MouseButton(mouse.Button),
		Mod:    uv.KeyMod(mouse.Mod),
	}

	switch msg.(type) {
	case tea.MouseClickMsg:
		return uv.MouseClickEvent(event)
	case tea.MouseReleaseMsg:
		return uv.MouseReleaseEvent(event)
	case tea.MouseWheelMsg:
		return uv.MouseWheelEvent(event)
	case tea.MouseMotionMsg:
		return uv.MouseMotionEvent(event)
	default:
		return nil
	}
}

// handleMouse routes one mouse message. Everything it does not forward is
// consumed, so a click can never leak into a child as a keystroke.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	event := mouseEventFromTea(msg)
	if event == nil {
		return nil
	}

	// States where the log, the quit warning or the size warning owns the screen:
	// consume everything and forget any gesture in progress, so a drag cannot
	// resume into a child when the dialog closes.
	if m.quitArmed || m.logMode || m.tooSmall() {
		m.mouse = mouseState{owner: -1}
		return nil
	}

	switch msg.(type) {
	case tea.MouseWheelMsg:
		return m.wheelMouse(event)
	case tea.MouseMotionMsg:
		return m.dragMouse(event)
	case tea.MouseReleaseMsg:
		return m.releaseMouse(event)
	case tea.MouseClickMsg:
		return m.pressMouse(event)
	}
	return nil
}

// pressMouse decides whether a press focuses a pane or is forwarded to one.
func (m *Model) pressMouse(event uv.MouseEvent) tea.Cmd {
	point := event.Mouse()
	target := m.mouseTargetAt(point.X, point.Y)
	if target.pane < 0 {
		return nil
	}

	slot := m.slots[target.pane]

	leftButton := point.Button == uv.MouseLeft

	if target.pane != m.focus {
		// Only a left press moves focus; it is consumed, and its release is
		// swallowed later so the child sees no orphan click.
		if !leftButton {
			return nil
		}
		if !target.header && !target.body {
			return nil
		}
		m.setFocus(target.pane)
		m.mouse = mouseState{owner: -1, focusConsumed: true, button: point.Button}
		return nil
	}

	// The focused pane: its header is the application's, its body is the child's.
	if slot.Pane == nil || target.header || !target.body {
		return nil
	}
	if !m.paneInputReady(target.pane) {
		// Nothing may be queued for a pane that cannot receive input; the
		// feedback row already says so.
		return nil
	}
	if !slot.Pane.MouseEnabled(event) {
		return nil
	}

	// A mouse coordinate the child expects is relative to its own screen.
	forwarded := withMousePosition(event, target.x, target.y)
	m.clearInterrupt()
	if len(m.input[target.pane]) < inputQueueLimit {
		m.mouse = mouseState{owner: target.pane, button: point.Button}
	} else {
		m.mouse = mouseState{owner: -1}
	}
	return m.enqueueInputFor(target.pane, inputAction{
		mouse:      true,
		mouseEvent: forwarded,
		drafting:   true,
		priorDraft: m.drafts[slot.ID],
	}, false)
}

// dragMouse keeps a gesture with its owner, clamped into that pane's body.
func (m *Model) dragMouse(event uv.MouseEvent) tea.Cmd {
	if m.mouse.owner < 0 {
		return nil
	}

	slot := m.slots[m.mouse.owner]
	if slot.Pane == nil || !m.paneInputReady(m.mouse.owner) || !slot.Pane.MouseEnabled(event) {
		return nil
	}

	m.clearInterrupt()
	forwarded := m.clampToPane(m.mouse.owner, event)
	return m.enqueueInputFor(m.mouse.owner, inputAction{
		mouse:      true,
		mouseEvent: forwarded,
		drafting:   true,
		priorDraft: m.drafts[slot.ID],
	}, false)
}

// releaseMouse ends a gesture, forwarding the release to the pane that owns it.
func (m *Model) releaseMouse(event uv.MouseEvent) tea.Cmd {
	button := event.Mouse().Button
	if button != uv.MouseNone && m.mouse.button != uv.MouseNone && button != m.mouse.button {
		return nil
	}
	if m.mouse.focusConsumed {
		// The press only moved focus; the child never saw it, so it must not see
		// the release either.
		m.mouse = mouseState{owner: -1}
		return nil
	}

	owner := m.mouse.owner
	if owner < 0 {
		return nil
	}
	m.mouse = mouseState{owner: -1}

	slot := m.slots[owner]
	if slot.Pane == nil || !m.paneInputReady(owner) || !slot.Pane.MouseEnabled(event) {
		return nil
	}

	forwarded := m.clampToPane(owner, event)
	m.clearInterrupt()
	return m.enqueueInputFor(owner, inputAction{
		mouse:      true,
		mouseEvent: forwarded,
		drafting:   true,
		priorDraft: m.drafts[slot.ID],
	}, false)
}

// wheelMouse sends a wheel turn to the pane under the pointer without changing
// focus and without touching drafts: scrolling is not typing.
func (m *Model) wheelMouse(event uv.MouseEvent) tea.Cmd {
	point := event.Mouse()
	target := m.mouseTargetAt(point.X, point.Y)
	if target.pane < 0 || !target.body {
		return nil
	}

	slot := m.slots[target.pane]
	if slot.Pane == nil || !m.paneInputReady(target.pane) || !slot.Pane.MouseEnabled(event) {
		return nil
	}

	forwarded := withMousePosition(event, target.x, target.y)
	// A full queue drops a wheel turn silently: losing a scroll is better than
	// shouting at someone whose child is simply slow.
	return m.enqueueInputFor(target.pane, inputAction{
		mouse:      true,
		mouseEvent: forwarded,
	}, true)
}

// clampToPane keeps a drag inside its owner's body, whatever the pointer does.
func (m *Model) clampToPane(index int, event uv.MouseEvent) uv.MouseEvent {
	box, ok := m.boxFor(index)
	if !ok {
		return event
	}

	point := event.Mouse()
	x, y := box.relative(point.X, point.Y)
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if box.width > 0 && x > box.width-1 {
		x = box.width - 1
	}
	if box.rows > 0 && y > box.rows-1 {
		y = box.rows - 1
	}
	return withMousePosition(event, x, y)
}

// withMousePosition replaces an event's coordinates, keeping its kind.
func withMousePosition(event uv.MouseEvent, x, y int) uv.MouseEvent {
	mouse := event.Mouse()
	mouse.X, mouse.Y = x, y

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

// setFocus moves focus and disarms the Ctrl+C sequence: a focus change means the
// user is somewhere else now.
func (m *Model) setFocus(index int) {
	if index < 0 || index >= m.activeCount() {
		return
	}
	if m.focus == index {
		return
	}
	m.focus = index
	m.clearInterrupt()
}
