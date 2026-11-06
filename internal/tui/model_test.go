package tui

// Authored but UNRUN under GR-3. These tests use stubs only; no real harness,
// terminal, app binary or subprocess is launched.

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

type fakeRouter struct {
	snap      protocol.Snapshot
	drafts    []bool
	submitted int
	delivered []protocol.AgentID
	pending   int
	snapshots int

	// Connected read seam seeds.
	sendViews   []protocol.SendView
	sendsErr    error
	sendDetails map[string]protocol.SendDetail
}

func (r *fakeRouter) UserDraft(_ protocol.AgentID, b bool) { r.drafts = append(r.drafts, b) }
func (r *fakeRouter) UserSubmitted(protocol.AgentID)       { r.submitted++ }
func (r *fakeRouter) DeliverHeld(id protocol.AgentID)      { r.delivered = append(r.delivered, id) }
func (r *fakeRouter) Snapshot() protocol.Snapshot          { r.snapshots++; return r.snap }
func (r *fakeRouter) History(protocol.HistoryQuery) ([]protocol.MessageView, error) {
	return r.snap.Messages, nil
}

// The two read methods F3 needs. They return empty values: this fake exists to
// satisfy the interface for compile-only drafts, and the senior makes it
// run-aware when tests may run.
func (r *fakeRouter) Runs(int) ([]protocol.RunView, error) { return nil, nil }
func (r *fakeRouter) MessageDetail(string) (protocol.MessageDetail, error) {
	return protocol.MessageDetail{}, nil
}
func (r *fakeRouter) PendingOnQuit() int { return r.pending }

// The optional connected read seam. The default is an empty list and an unknown
// send, so a local-mode test needs no setup and a connected test can seed them.
func (r *fakeRouter) Sends(int) ([]protocol.SendView, error) {
	return r.sendViews, r.sendsErr
}

func (r *fakeRouter) SendDetail(id string) (protocol.SendDetail, error) {
	detail, ok := r.sendDetails[id]
	if !ok {
		return protocol.SendDetail{}, errors.New("fakeRouter: no such send")
	}
	return detail, nil
}

type fakePane struct {
	keys      []uv.KeyPressEvent
	pastes    []string
	mouse     []uv.MouseEvent
	resizes   [][2]int
	frame     pane.Frame
	sendErr   error
	before    func()
	snapshots int
	// dead makes InputReady false; mouseOn makes MouseEnabled true. Both default
	// to the quiet case so existing tests are unaffected.
	dead    bool
	mouseOn bool
}

func (p *fakePane) Resize(int, int) error { return nil }
func (p *fakePane) Snapshot() pane.Frame  { p.snapshots++; return p.frame }
func (p *fakePane) SendKey(k uv.KeyPressEvent) error {
	if p.before != nil {
		p.before()
	}
	p.keys = append(p.keys, k)
	return p.sendErr
}
func (p *fakePane) Paste(s string) error { p.pastes = append(p.pastes, s); return p.sendErr }
func (p *fakePane) Err() error           { return nil }

// The input surface the bug task added: readiness, mouse eligibility, mouse
// delivery and the non-blocking resize request.
func (p *fakePane) InputReady() bool { return !p.dead }
func (p *fakePane) MouseEnabled(uv.MouseEvent) bool {
	return p.mouseOn
}
func (p *fakePane) SendMouse(event uv.MouseEvent) error {
	if p.before != nil {
		p.before()
	}
	p.mouse = append(p.mouse, event)
	return p.sendErr
}
func (p *fakePane) RequestResize(width, height int) error {
	p.resizes = append(p.resizes, [2]int{width, height})
	return nil
}
func newModel(t *testing.T) (*Model, *fakeRouter, *fakePane, *fakePane) {
	t.Helper()
	r := &fakeRouter{snap: protocol.Snapshot{Agents: []protocol.AgentView{{ID: "a", State: protocol.AgentIdle}, {ID: "b", State: protocol.AgentIdle}}}}
	a, b := &fakePane{}, &fakePane{}
	m, err := New(r, nil, []Slot{{ID: "a", Name: "Claude", Pane: a}, {ID: "b", Name: "Codex", Pane: b}})
	if err != nil {
		t.Fatal(err)
	}
	m.resize(100, 30)
	return m, r, a, b
}
func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func TestInputDraftPrecedesTransportAndEnterCompletion(t *testing.T) {
	m, r, a, _ := newModel(t)
	a.before = func() {
		if len(r.drafts) == 0 || !r.drafts[len(r.drafts)-1] {
			t.Fatal("draft not held before write")
		}
	}
	cmd := m.forward(key(tea.KeyEnter))
	if r.submitted != 0 || len(a.keys) != 0 {
		t.Fatal("input ran inside Update")
	}
	done := cmd().(inputDone)
	if r.submitted != 1 {
		t.Fatal("submission was not reported after actual write inside command")
	}
	m.inputCompleted(done)
	if r.submitted != 1 || m.drafts["a"] {
		t.Fatal("completed Enter did not clear sole draft")
	}
}

func TestQueuedLaterKeyKeepsDraftAfterEnter(t *testing.T) {
	m, r, _, _ := newModel(t)
	enter := m.forward(key(tea.KeyEnter))
	if cmd := m.forward(tea.KeyPressMsg{Code: 'x', Text: "x"}); cmd != nil {
		t.Fatal("two concurrent input commands")
	}
	next := m.inputCompleted(enter().(inputDone))
	if r.submitted != 1 || !m.drafts["a"] || next == nil {
		t.Fatal("lost later draft or next input")
	}
	m.inputCompleted(next().(inputDone))
	if !m.drafts["a"] {
		t.Fatal("ordinary key cleared draft")
	}
}

func TestFailedEnterNeverSubmits(t *testing.T) {
	m, r, a, _ := newModel(t)
	a.sendErr = errors.New("write failed")
	m.inputCompleted(m.forward(key(tea.KeyEnter))().(inputDone))
	if r.submitted != 0 || !m.drafts["a"] || !strings.Contains(m.feedback, "failed") {
		t.Fatal("failed write cleared state")
	}
}

func TestQueueOverflowIsVisibleAndConservative(t *testing.T) {
	m, _, _, _ := newModel(t)
	first := m.forward(key(tea.KeyEnter))
	for range inputQueueLimit {
		m.forward(tea.KeyPressMsg{Code: 'a', Text: "a"})
	}
	if len(m.input[0]) != inputQueueLimit || !strings.Contains(m.feedback, "queue full") {
		t.Fatal("unbounded or silent overflow")
	}
	m.inputCompleted(first().(inputDone))
	if !m.drafts["a"] {
		t.Fatal("overflow draft lost")
	}
}

func TestGlobalKeysAndLogNeverReachChild(t *testing.T) {
	m, r, a, b := newModel(t)
	m.key(key(tea.KeyF2))
	if m.focus != 1 {
		t.Fatal("focus")
	}
	runInputCommand(t, m, m.key(key(tea.KeyF4)))
	if len(r.delivered) != 1 || r.delivered[0] != "b" {
		t.Fatal("delivery focus")
	}
	m.key(key(tea.KeyF3))
	m.key(key('x'))
	m.paste("private")
	if len(a.keys)+len(b.keys)+len(b.pastes) != 0 {
		t.Fatal("log leaked input")
	}
	m.key(key(tea.KeyEscape))
	cmd := m.key(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+C intercepted")
	}
	m.inputCompleted(cmd().(inputDone))
	if len(b.keys) != 1 {
		t.Fatal("Ctrl+C not forwarded")
	}
}

func TestQuitWarningInterceptsPasteAndEscape(t *testing.T) {
	m, r, a, _ := newModel(t)
	r.pending = 1
	if m.key(key(tea.KeyF10)) != nil || !m.quitArmed {
		t.Fatal("no pending warning")
	}
	m.paste("do not send")
	m.Update(tea.PasteStartMsg{})
	if len(a.pastes) != 0 || m.drafts["a"] {
		t.Fatal("dialog leaked paste")
	}
	m.key(key(tea.KeyEscape))
	if m.quitArmed || len(a.keys) != 0 {
		t.Fatal("Esc leaked")
	}
	m.key(key(tea.KeyF10))
	if m.key(key(tea.KeyF10)) == nil {
		t.Fatal("second F10 did not quit")
	}
}

func TestSnapshotOwnershipAndResync(t *testing.T) {
	m, r, _, _ := newModel(t)
	shared := []protocol.AgentView{{ID: "a", State: protocol.AgentIdle}}
	m.applySnapshot(protocol.Snapshot{Agents: shared})
	m.applyEvent(protocol.Event{Agent: &protocol.AgentView{ID: "a", State: protocol.AgentBusy}})
	if shared[0].State != protocol.AgentIdle {
		t.Fatal("mutated router snapshot")
	}
	r.snap = protocol.Snapshot{Banner: "resynced"}
	m.Update(eventMsg(protocol.Event{Resync: true}))
	if m.banner != "resynced" {
		t.Fatal("no resync")
	}
}

func TestViewFitsAndSanitizesChrome(t *testing.T) {
	m, _, a, b := newModel(t)
	m.slots[0].Name = "evil\x1b[2J\x9bname\n"
	a.frame = pane.Frame{Content: "\x1b[31m界hello", X: 1, Y: 1}
	b.frame = pane.Frame{Content: "right", Hidden: true}
	view := m.View()
	lines := strings.Split(view.Content, "\n")
	if len(lines) != m.height {
		t.Fatalf("height %d", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != m.width {
			t.Fatalf("row %d width %d", i, w)
		}
	}
	if strings.Contains(view.Content, "\x1b[2J") || strings.Contains(view.Content, "\x9b") {
		t.Fatal("unsafe chrome")
	}
	if a.snapshots != 1 || b.snapshots != 1 {
		t.Fatal("inconsistent repeated frame snapshots")
	}
	for _, size := range [][2]int{{1, 1}, {10, 2}, {39, 9}, {0, 0}} {
		m.resize(size[0], size[1])
		m.View()
	}
}

func TestStaleHistoryCannotReplaceNewLog(t *testing.T) {
	m, _, _, _ := newModel(t)
	m.enterLog()
	old := m.historyGeneration
	m.leaveLog()
	m.enterLog()
	m.Update(historyMsg{messages: []protocol.MessageView{{ID: "stale"}}, generation: old})
	if m.logLoaded || len(m.log) != 0 {
		t.Fatal("stale history installed")
	}
}

func TestF4ClearsOnlyEligibleLocalDraftAndWarns(t *testing.T) {
	m, r, _, _ := newModel(t)
	m.drafts["a"] = true
	runInputCommand(t, m, m.key(key(tea.KeyF4)))
	if m.drafts["a"] || len(r.delivered) != 1 || !strings.Contains(m.feedback, "submits both") {
		t.Fatal("override warning/state")
	}
	m.drafts["a"] = true
	m.upsertAgent(protocol.AgentView{ID: "a", State: protocol.AgentNeedsYou})
	runInputCommand(t, m, m.key(key(tea.KeyF4)))
	if !m.drafts["a"] || len(r.delivered) != 1 {
		t.Fatal("permission prompt overridden")
	}
	if !strings.Contains(holdReason("drafting"), "pastes after any text in the box and submits both") {
		t.Fatal("missing warning")
	}
}

// Runs only stub-backed input commands, not the UI, timers or a harness.
func runInputCommand(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		done, ok := msg.(inputDone)
		if !ok {
			t.Fatalf("unexpected input response %T", msg)
		}
		cmd = m.inputCompleted(done)
	}
}
