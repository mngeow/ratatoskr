package tui

// Authored but UNRUN under GR-3. Stub-backed UI messages only; no real terminal,
// application, harness, PTY or test binary has been executed.

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func ctrlKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }
func assertQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not tea.Quit")
	}
}

func TestMouseTargetsFollowLayoutAtOddAndEvenWidths(t *testing.T) {
	m, _, _, _ := newModel(t)
	for _, width := range []int{100, 101} {
		m.resize(width, 30)
		left, right := paneWidths(width)
		for _, tc := range []struct {
			x, y, pane int
			header     bool
		}{
			{0, screenTop() - 1, 0, true}, {left - 1, screenTop(), 0, false}, {left, screenTop(), -1, false},
			{left + 1, screenTop(), 1, false}, {left + right, screenTop(), 1, false}, {width, screenTop(), -1, false},
			{0, 0, -1, false}, {0, screenTop() + m.screenRows(), -1, false},
		} {
			got := m.mouseTargetAt(tc.x, tc.y)
			if got.pane != tc.pane || (tc.pane >= 0 && got.header != tc.header) {
				t.Fatalf("width %d point %d,%d got %+v", width, tc.x, tc.y, got)
			}
		}
	}
}

func TestFocusOnlyMouseConsumesMatchingReleaseWithoutDraft(t *testing.T) {
	m, _, a, b := newModel(t)
	a.mouseOn = true
	b.mouseOn = true
	left, _ := paneWidths(m.width)
	if cmd := m.handleMouse(tea.MouseClickMsg{X: left + 2, Y: screenTop(), Button: tea.MouseLeft}); cmd != nil {
		t.Fatal("focus click forwarded")
	}
	if m.focus != 1 || m.anyDraft() {
		t.Fatal("focus click marked draft or missed focus")
	}
	m.handleMouse(tea.MouseReleaseMsg{X: 0, Y: screenTop(), Button: tea.MouseRight})
	if !m.mouse.focusConsumed {
		t.Fatal("unrelated release consumed focus gesture")
	}
	m.handleMouse(tea.MouseReleaseMsg{X: 0, Y: screenTop(), Button: tea.MouseLeft})
	if len(a.mouse)+len(b.mouse) != 0 || m.mouse.focusConsumed {
		t.Fatal("focus release leaked")
	}
}

func TestMouseTrackingDraftAndDragStayWithPressOwner(t *testing.T) {
	m, r, a, b := newModel(t)
	if cmd := m.handleMouse(tea.MouseClickMsg{X: 2, Y: screenTop(), Button: tea.MouseLeft}); cmd != nil || m.anyDraft() {
		t.Fatal("disabled mouse created draft")
	}
	a.mouseOn = true
	b.mouseOn = true
	first := m.handleMouse(tea.MouseClickMsg{X: 2, Y: screenTop(), Button: tea.MouseLeft})
	m.handleMouse(tea.MouseMotionMsg{X: 99, Y: 99, Button: tea.MouseLeft})
	m.handleMouse(tea.MouseReleaseMsg{X: 99, Y: 99, Button: tea.MouseLeft})
	if !m.drafts["a"] || m.drafts["b"] {
		t.Fatal("mouse drafting target")
	}
	runInputCommand(t, m, first)
	if len(a.mouse) != 3 || len(b.mouse) != 0 || len(r.drafts) == 0 {
		t.Fatal("drag changed owner")
	}
	left, _ := paneWidths(m.width)
	for _, event := range a.mouse {
		point := event.Mouse()
		if point.X < 0 || point.X >= left || point.Y < 0 || point.Y >= m.screenRows() {
			t.Fatalf("bad relative mouse %+v", point)
		}
	}
}

func TestWheelToUnfocusedPaneHasNoDraftOrEpoch(t *testing.T) {
	m, r, _, b := newModel(t)
	b.mouseOn = true
	left, _ := paneWidths(m.width)
	cmd := m.handleMouse(tea.MouseWheelMsg{X: left + 2, Y: screenTop(), Button: tea.MouseWheelDown})
	if m.focus != 0 || m.anyDraft() || m.draftEpoch[1] != 0 {
		t.Fatal("wheel changed focus/draft")
	}
	runInputCommand(t, m, cmd)
	if len(b.mouse) != 1 || len(r.drafts) != 0 {
		t.Fatal("wheel missed target or touched router draft")
	}
	m.input[1] = make([]inputAction, inputQueueLimit)
	m.feedback = "keep"
	m.handleMouse(tea.MouseWheelMsg{X: left + 2, Y: screenTop(), Button: tea.MouseWheelDown})
	if m.feedback != "keep" || m.draftEpoch[1] != 0 {
		t.Fatal("wheel overflow was noisy or drafted")
	}
}

func TestMouseDialogsDeadPaneAndHeaderDoNotForward(t *testing.T) {
	for _, mode := range []string{"quit", "log", "small", "dead", "header"} {
		t.Run(mode, func(t *testing.T) {
			m, _, a, _ := newModel(t)
			a.mouseOn = true
			y := screenTop()
			switch mode {
			case "quit":
				m.quitArmed = true
			case "log":
				m.logMode = true
			case "small":
				m.width = 10
			case "dead":
				a.dead = true
			case "header":
				y--
			}
			if cmd := m.handleMouse(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft}); cmd != nil || m.anyDraft() {
				t.Fatal("intercepted mouse forwarded/drafted")
			}
		})
	}
}

func TestCtrlCSecondPressUsesDraftBeforeInterrupt(t *testing.T) {
	m, _, _, _ := newModel(t)
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	first := m.key(ctrlKey())
	if first == nil || !m.drafts["a"] || m.feedback != ctrlCAgainFeedback {
		t.Fatal("first interrupt")
	}
	now = now.Add(time.Second / 2)
	assertQuit(t, m.key(ctrlKey()))
	if m.quitArmed || len(m.input[0]) != 1 {
		t.Fatal("artificial draft added third press or second was forwarded")
	}
}

func TestCtrlCPreexistingDraftWarnsAndConfirms(t *testing.T) {
	m, _, _, _ := newModel(t)
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	m.drafts["a"] = true
	m.key(ctrlKey())
	now = now.Add(time.Second / 2)
	if m.key(ctrlKey()) != nil || !m.quitArmed {
		t.Fatal("existing draft lost quit warning")
	}
	assertQuit(t, m.key(ctrlKey()))
}

func TestCtrlCNothingReceivingRequestsQuitEverywhere(t *testing.T) {
	for _, mode := range []string{"nil", "dead", "log", "small"} {
		t.Run(mode, func(t *testing.T) {
			m, _, a, _ := newModel(t)
			switch mode {
			case "nil":
				m.slots[0].Pane = nil
			case "dead":
				a.dead = true
			case "log":
				m.logMode = true
			case "small":
				m.width = 10
			}
			assertQuit(t, m.key(ctrlKey()))
			if len(m.input[0]) != 0 || m.anyDraft() {
				t.Fatal("unavailable interrupt queued/drafted")
			}
		})
	}
}

func TestCtrlCDisarmedByOtherKeyFocusAndExpiry(t *testing.T) {
	for _, why := range []string{"key", "focus", "expiry"} {
		t.Run(why, func(t *testing.T) {
			m, _, _, _ := newModel(t)
			now := time.Unix(100, 0)
			m.now = func() time.Time { return now }
			m.key(ctrlKey())
			switch why {
			case "key":
				m.key(key('x'))
			case "focus":
				m.key(key(tea.KeyF2))
			case "expiry":
				now = now.Add(2 * time.Second)
				m.Update(tickMsg(now))
			}
			if m.interrupt.armed || m.feedback == ctrlCAgainFeedback {
				t.Fatal("interrupt sequence stayed armed")
			}
		})
	}
}

func TestFullInputQueueCannotSwallowSecondCtrlC(t *testing.T) {
	m, _, _, _ := newModel(t)
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	m.input[0] = make([]inputAction, inputQueueLimit)
	m.key(ctrlKey())
	now = now.Add(time.Second / 2)
	assertQuit(t, m.key(ctrlKey()))
}

type stalledRouter struct {
	fakeRouter
	entered, release chan struct{}
}

func (r *stalledRouter) UserDraft(_ protocol.AgentID, b bool) {
	if b {
		close(r.entered)
		<-r.release
	}
}

func TestBlockedRouterCommandCannotBlockViewOrQuit(t *testing.T) {
	r := &stalledRouter{entered: make(chan struct{}), release: make(chan struct{})}
	m, err := New(r, nil, []Slot{{ID: "a", Pane: &fakePane{}}, {ID: "b", Pane: &fakePane{}}})
	if err != nil {
		t.Fatal(err)
	}
	m.resize(100, 30)
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	cmd := m.forward(key('x'))
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-r.entered:
	case <-time.After(time.Second):
		t.Fatal("input command did not start")
	}
	t.Cleanup(func() {
		close(r.release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("stub command did not finish")
		}
	})
	now = now.Add(inputCommandLimit + time.Second)
	if !strings.Contains(m.View().Content, "INPUT UNAVAILABLE") {
		t.Fatal("blocked command invisible")
	}
	if m.key(ctrlKey()) != nil || !m.quitArmed {
		t.Fatal("blocked input swallowed quit warning")
	}
	assertQuit(t, m.key(key(tea.KeyF10)))
}

func TestDeadInputVisibilityAndResizeAreNonblockingRequests(t *testing.T) {
	m, _, a, _ := newModel(t)
	a.dead = true
	m.feedback = "old feedback"
	m.banner = "old banner"
	if !strings.Contains(m.headerText(0), "INPUT UNAVAILABLE") || !strings.Contains(m.feedbackLine(), "INPUT UNAVAILABLE") {
		t.Fatal("dead input hidden")
	}
	if cmd := m.paste("x"); cmd != nil || m.anyDraft() {
		t.Fatal("dead input paste queued")
	}
	m.Update(tea.PasteStartMsg{})
	if m.anyDraft() {
		t.Fatal("dead input paste start drafted")
	}
	m.resize(40, 9)
	if !strings.Contains(m.View().Content, "INPUT UNAVAILABLE") {
		t.Fatal("small terminal hid dead input")
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse capture absent")
	}
}
