package tui

// Ctrl+C policy, decided by the user and deliberately isolated here so it is one file to
// revise.
//
// The user reported that Ctrl+C did not exit rtk. A harness pane wants Ctrl+C to
// interrupt the child, so quitting on the first press would break the pane; but a
// pane that cannot receive input has no such claim. The rule below is: forward
// once, quit on a second press within a short window, and quit immediately when
// nothing can receive the key.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"time"

	"charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// interruptWindow is how long a second Ctrl+C keeps its meaning. It is a guess,
// not a measured value.
const interruptWindow = 1 * time.Second // UNTESTED guess

// ctrlCAgainFeedback is shown after the first forwarded Ctrl+C.
const ctrlCAgainFeedback = "Ctrl+C again to quit rtk"

// interruptState is the armed first press.
type interruptState struct {
	armed bool
	pane  int
	at    time.Time
	// draftBefore is the draft state from before the first press marked one, so
	// the warning on the second press describes the user's situation rather than
	// this feature's own side effect.
	draftBefore bool
}

// clearInterrupt disarms the sequence, and removes the "again" hint if that is
// what the feedback row is showing.
func (m *Model) clearInterrupt() {
	m.interrupt = interruptState{}
	if m.feedback == ctrlCAgainFeedback {
		// Only the hint this feature wrote is removed; any other message stays.
		m.feedback = ""
	}
}

// expireInterrupt is called from the tick: a stale "again" hint must not linger
// after the second press has stopped meaning anything.
func (m *Model) expireInterrupt() {
	if !m.interrupt.armed {
		return
	}
	if m.now().Sub(m.interrupt.at) >= 0 && m.now().Sub(m.interrupt.at) <= interruptWindow {
		return
	}
	m.clearInterrupt()
}

// canReceiveInput reports whether the focused pane could take a keystroke right
// now. When it cannot, Ctrl+C has nobody to interrupt and quits instead.
func (m *Model) canReceiveInput() bool {
	if m.logMode || m.tooSmall() {
		return false
	}
	slot := m.slots[m.focus]
	if slot.Pane == nil {
		return false
	}
	return m.paneInputReady(m.focus)
}

// inputUnavailableLine is the one line that outranks every other message: a pane
// that cannot receive input is the reason keystrokes appear to do nothing.
func (m *Model) inputUnavailableLine() string {
	for _, i := range m.activeSlots() {
		slot := m.slots[i]
		if slot.Pane == nil {
			continue
		}
		if m.paneInputReady(i) {
			continue
		}
		return "INPUT UNAVAILABLE: " + m.slotName(i) + " cannot receive input"
	}
	return ""
}

// ctrlC applies the decided policy and returns a command when it decided to
// quit or to forward the press.
func (m *Model) ctrlC(key tea.KeyPressMsg) tea.Cmd {
	// Second press: same pane, inside the window, nothing else happened. Quit,
	// using the draft state from before the first press so this feature's own
	// draft does not count as a reason to warn.
	if m.interrupt.armed && m.interrupt.pane == m.focus &&
		m.now().Sub(m.interrupt.at) >= 0 && m.now().Sub(m.interrupt.at) <= interruptWindow {
		draft := m.interrupt.draftBefore
		m.clearInterrupt()
		return m.requestQuitWithDraft(draft)
	}

	// Nothing can receive it: one press is enough.
	if !m.canReceiveInput() {
		m.clearInterrupt()
		return m.requestQuitWithDraft(m.anyDraft())
	}

	// A live pane gets the press, and the next one quits.
	room := len(m.input[m.focus]) < inputQueueLimit
	draftBefore := m.anyDraft()
	m.interrupt = interruptState{
		armed:       true,
		pane:        m.focus,
		at:          m.now(),
		draftBefore: draftBefore,
	}

	command := m.enqueueInputFor(m.focus, inputAction{
		key:        toKeyPress(key),
		drafting:   true,
		priorDraft: draftBefore,
	}, false)

	if room {
		m.feedback = ctrlCAgainFeedback
	}
	return command
}

// isCtrlC reports Ctrl+C and nothing else: Ctrl+Shift+C is a different key and
// belongs to the child.
func isCtrlC(key tea.KeyPressMsg) bool {
	mod := key.Mod &^ (uv.ModCapsLock | uv.ModNumLock | uv.ModScrollLock)
	return (key.Code == 'c' && mod == uv.ModCtrl) || (key.Code == 3 && mod == 0)
}

// A blocked router/third-party pane call stays isolated in one Cmd. Once its
// operation window expires, it is visible and Ctrl+C takes the quit path.
const inputCommandLimit = 3 * time.Second // UNTESTED UI watchdog, not a preemptive goroutine cancel.
func (m *Model) paneInputReady(i int) bool {
	p := m.slots[i].Pane
	if p == nil || !p.InputReady() {
		return false
	}
	return !m.inputRunning[i] || m.now().Sub(m.inputStarted[i]) <= inputCommandLimit
}
