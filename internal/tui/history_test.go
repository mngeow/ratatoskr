package tui

// F3 tests are authored but UNRUN under GR-3. All reads use map-backed stubs;
// no terminal, application, harness or test binary has been executed.

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

type historyRouter struct {
	fakeRouter
	runs                          []protocol.RunView
	messages                      map[string][]protocol.MessageView
	details                       map[string]protocol.MessageDetail
	runErr, historyErr, detailErr error
	lastRun, lastMessage          string
}

func (r *historyRouter) Runs(int) ([]protocol.RunView, error) { return r.runs, r.runErr }
func (r *historyRouter) History(q protocol.HistoryQuery) ([]protocol.MessageView, error) {
	r.lastRun = q.RunID
	return r.messages[q.RunID], r.historyErr
}
func (r *historyRouter) MessageDetail(id string) (protocol.MessageDetail, error) {
	r.lastMessage = id
	return r.details[id], r.detailErr
}

// readCommands advances only commands returned by explicit log operations. It
// does not start Init/ticks or event waits and never starts a real process.
func readCommands(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			readCommands(t, m, child)
		}
		return
	}
	_, next := m.Update(msg)
	if next != nil {
		readCommands(t, m, next)
	}
}

func newHistoryModel(t *testing.T) (*Model, *historyRouter, *fakePane) {
	t.Helper()
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ended := at.Add(-time.Hour)
	current := protocol.MessageView{ID: "new-message", RunID: "new-run", Preview: "new preview", State: protocol.MessageAccepted, At: at}
	old := protocol.MessageView{ID: "old-message", RunID: "old-run", Preview: "old preview", State: protocol.MessageInterrupted, At: at.Add(-2 * time.Hour)}
	r := &historyRouter{runs: []protocol.RunView{{ID: "new-run", StartedAt: at}, {ID: "old-run", StartedAt: at.Add(-3 * time.Hour), EndedAt: &ended, EndReason: "crashed", Interrupted: 1}}, messages: map[string][]protocol.MessageView{"new-run": {current}, "old-run": {old}}, details: map[string]protocol.MessageDetail{
		"old-message": {MessageView: old, Body: "WHOLE BODY\n\t界 text\x1b[2J\x9b\n" + strings.Repeat("long unbroken body ", 50) + "TAIL", Events: []protocol.MessageEventView{{At: ended, From: protocol.MessageHeld, To: protocol.MessageInterrupted, Reason: "run ended"}}},
		"new-message": {MessageView: current, Body: "new whole body"},
	}}
	p := &fakePane{}
	m, err := New(r, nil, []Slot{{ID: "a", Pane: p}, {ID: "b", Pane: &fakePane{}}})
	if err != nil {
		t.Fatal(err)
	}
	m.resize(100, 30)
	return m, r, p
}

func TestF3NavigatesOlderRunsAndFullDetail(t *testing.T) {
	m, r, p := newHistoryModel(t)
	readCommands(t, m, m.key(key(tea.KeyF3)))
	readCommands(t, m, m.key(key(tea.KeyLeft)))
	if r.lastRun != "old-run" {
		t.Fatalf("selected run %q", r.lastRun)
	}
	view := m.View().Content
	if !strings.Contains(view, "crashed") || !strings.Contains(view, "old preview") {
		t.Fatal("older metadata/list absent")
	}
	readCommands(t, m, m.key(key(tea.KeyEnter)))
	if r.lastMessage != "old-message" {
		t.Fatalf("detail %q", r.lastMessage)
	}
	view = m.View().Content
	if !strings.Contains(view, "WHOLE BODY") || strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x9b") {
		t.Fatal("full body absent or unsafe")
	}
	m.key(key(tea.KeyEnd))
	view = m.View().Content
	if !strings.Contains(view, "run ended") || !strings.Contains(view, "interrupted") {
		t.Fatal("last state history unreachable")
	}
	m.key(key(tea.KeyEscape))
	if !m.logMode {
		t.Fatal("detail Escape closed entire log")
	}
	m.key(key(tea.KeyEscape))
	if m.logMode {
		t.Fatal("list Escape did not close")
	}
	if len(p.keys)+len(p.pastes) != 0 {
		t.Fatal("log input reached child")
	}
}

func TestF3RejectsStaleReadAfterRunSwitchOrClose(t *testing.T) {
	m, r, _ := newHistoryModel(t)
	readCommands(t, m, m.key(key(tea.KeyF3)))
	old := m.key(key(tea.KeyLeft))
	newer := m.key(key(tea.KeyRight))
	readCommands(t, m, newer)
	readCommands(t, m, old)
	if !strings.Contains(m.View().Content, "new preview") {
		t.Fatal("stale old-run read replaced current")
	}
	detail := m.key(key(tea.KeyEnter))
	m.key(key(tea.KeyF3))
	readCommands(t, m, detail)
	if m.logMode {
		t.Fatal("stale detail reopened log")
	}
	_ = r
}

func TestF3ReadErrorsRemainInUI(t *testing.T) {
	m, r, _ := newHistoryModel(t)
	readCommands(t, m, m.key(key(tea.KeyF3)))
	r.detailErr = errors.New("disk\x1b[2J failure")
	readCommands(t, m, m.key(key(tea.KeyEnter)))
	if !m.logMode || !strings.Contains(m.feedback, "failure") || strings.Contains(m.feedback, "\x1b") {
		t.Fatal("detail error not safely reported")
	}
	r.detailErr = nil
	m.key(key(tea.KeyEscape))
	r.runErr = errors.New("run read failed")
	readCommands(t, m, m.key(tea.KeyPressMsg{Code: 'r', Text: "r"}))
	if !strings.Contains(m.feedback, "failed") {
		t.Fatal("run error lost")
	}
}

func TestDetailCloseInvalidatesReadAndReleasesRefresh(t *testing.T) {
	m, _, _ := newHistoryModel(t)
	readCommands(t, m, m.key(key(tea.KeyF3)))
	m.historyPending = true
	detail := m.key(key(tea.KeyEnter))
	if m.historyPending {
		t.Fatal("invalidated list read still blocks refresh")
	}
	m.key(key(tea.KeyEscape))
	readCommands(t, m, detail)
	if m.history.detailID != "" || m.historyPending || !m.historyDirty {
		t.Fatal("detail close did not restore list refresh")
	}
}

func TestDetailReloadErrorKeepsLastReadableBody(t *testing.T) {
	m, r, _ := newHistoryModel(t)
	readCommands(t, m, m.key(key(tea.KeyF3)))
	readCommands(t, m, m.key(key(tea.KeyEnter)))
	r.detailErr = errors.New("temporary read error")
	readCommands(t, m, m.key(tea.KeyPressMsg{Code: 'r', Text: "r"}))
	if !strings.Contains(m.View().Content, "new whole body") {
		t.Fatal("failed reload hid existing body")
	}
}

func TestBodyWrappingSanitizesAndCountsWideTabs(t *testing.T) {
	if got := expandTabs("界\tZ", 4); got != "界  Z" {
		t.Fatalf("wide tab %q", got)
	}
	lines := bodyLines("界\tZ\n\x1b[2Jtail\x01", 4)
	for _, line := range lines {
		if strings.ContainsAny(line, "\x1b\x01\t") {
			t.Fatalf("control survived %q", line)
		}
	}
	if !strings.Contains(strings.Join(lines, ""), "tail") {
		t.Fatal("body tail lost")
	}
}

func TestInitialReadErrorAndNarrowHistoryHelp(t *testing.T) {
	m, r, _ := newHistoryModel(t)
	r.runErr = errors.New("no runs")
	r.historyErr = errors.New("history unavailable")
	readCommands(t, m, m.key(key(tea.KeyF3)))
	if !strings.Contains(m.feedback, "unavailable") || m.logErr == nil {
		t.Fatal("initial error shown as endless loading")
	}
	m.resize(40, 10)
	help := strings.Join(m.historyHelpLines(), " ")
	for _, name := range []string{"Enter", "refresh", "F10"} {
		if !strings.Contains(help, name) {
			t.Fatalf("missing narrow help %s", name)
		}
	}
}
