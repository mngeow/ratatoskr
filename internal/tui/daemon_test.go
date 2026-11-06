package tui

// Authored but UNRUN under GR-3. Fake sessions/router/client only, no sockets.
import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/windowclient"
)

type daemonTestPane struct {
	fakePane
	state wire.PaneState
}

func (p *daemonTestPane) State() wire.PaneState { return p.state }
func (p *daemonTestPane) Ended() bool           { return p.state.Ended }
func (p *daemonTestPane) InputReady() bool      { return p.state.InputReady && !p.state.Ended }

type daemonTestClient struct {
	p              *daemonTestPane
	op, errorState error
	takes          int
}

func (c *daemonTestClient) Err() error                                    { return c.errorState }
func (c *daemonTestClient) OperationError(protocol.AgentID) error         { return c.op }
func (c *daemonTestClient) WindowEvents() <-chan windowclient.WindowEvent { return nil }
func (c *daemonTestClient) TakeInput(context.Context, protocol.AgentID) error {
	c.takes++
	c.p.state.YouHold = true
	c.p.state.Rev++
	c.op = nil
	return nil
}

type daemonTestRouter struct {
	fakeRouter
	draftHook   func(bool)
	submitHook  func()
	deliverHook func()
}

func (r *daemonTestRouter) UserDraft(id protocol.AgentID, b bool) {
	r.fakeRouter.UserDraft(id, b)
	if r.draftHook != nil {
		r.draftHook(b)
	}
}
func (r *daemonTestRouter) UserSubmitted(id protocol.AgentID) {
	r.fakeRouter.UserSubmitted(id)
	if r.submitHook != nil {
		r.submitHook()
	}
}
func (r *daemonTestRouter) DeliverHeld(id protocol.AgentID) {
	r.fakeRouter.DeliverHeld(id)
	if r.deliverHook != nil {
		r.deliverHook()
	}
}

func daemonFixture(t *testing.T, holder bool) (*Model, *daemonTestPane, *daemonTestClient, *daemonTestRouter) {
	t.Helper()
	p := &daemonTestPane{state: wire.PaneState{Agent: "a", Rev: 1, YouHold: holder, InputReady: true}}
	c := &daemonTestClient{p: p}
	r := &daemonTestRouter{fakeRouter: fakeRouter{snap: protocol.Snapshot{Runtime: true, Agents: []protocol.AgentView{{ID: "a", State: protocol.AgentIdle}}}}}
	m, err := NewRuntime(r, nil, []Slot{{ID: "a", Name: "Claude", Pane: p}})
	if err != nil {
		t.Fatal(err)
	}
	m.daemon = &daemonWindow{client: c, lastRev: [2]uint64{1}, lastHolder: [2]bool{holder}}
	m.applySnapshot(r.snap)
	m.resize(100, 30)
	return m, p, c, r
}

func assertDetach(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no detach command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("command did not detach")
	}
}

func TestDaemonViewerEnqueuesNothingAndCanTakeInput(t *testing.T) {
	m, p, c, r := daemonFixture(t, false)
	p.resizes = nil
	m.forward(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m.paste("hello")
	m.key(key(tea.KeyF4))
	m.resize(90, 25)
	if len(m.input[0]) != 0 || m.anyDraft() || len(r.drafts) != 0 || len(p.resizes) != 0 {
		t.Fatal("viewer generated input/draft/resize")
	}
	if strings.Contains(m.headerText(0), "INPUT UNAVAILABLE") || !strings.Contains(m.headerText(0), "VIEWER") || m.inputUnavailableLine() != "" {
		t.Fatal("viewer mislabeled failure")
	}
	p.state.Drafting = true
	cmd := m.key(key(tea.KeyF6))
	if cmd == nil || len(r.drafts) != 0 {
		t.Fatal("F6 did not schedule cleanly")
	}
	if m.key(key(tea.KeyF6)) != nil {
		t.Fatal("duplicate takeover queued")
	}
	m.applyTakeInputDone(cmd().(takeInputDone))
	if c.takes != 1 || !m.drafts["a"] || len(r.drafts) != 0 || len(p.resizes) == 0 {
		t.Fatal("new holder draft/size not seeded")
	}
}

func TestDaemonDetachKeysAndLocalWatchdog(t *testing.T) {
	for _, viewer := range []bool{true, false} {
		m, _, _, r := daemonFixture(t, !viewer)
		r.pending = 4
		m.drafts["a"] = true
		assertDetach(t, m.key(key(tea.KeyF10)))
		if m.quitArmed || m.WindowError() != nil {
			t.Fatal("detach warned or errored")
		}
	}
	viewer, _, _, _ := daemonFixture(t, false)
	assertDetach(t, viewer.key(tea.KeyPressMsg{Code: 'c', Mod: uv.ModCtrl}))
	holder, _, _, _ := daemonFixture(t, true)
	first := holder.key(tea.KeyPressMsg{Code: 'c', Mod: uv.ModCtrl})
	if first == nil || holder.daemon.detached || holder.feedback != daemonCtrlCAgain {
		t.Fatal("holder first Ctrl+C did not interrupt")
	}
	assertDetach(t, holder.key(tea.KeyPressMsg{Code: 'c', Mod: uv.ModCtrl}))
	m, _, _, _ := daemonFixture(t, true)
	m.inputRunning[0] = true
	m.inputStarted[0] = time.Now().Add(-time.Minute)
	if !m.paneInputReady(0) {
		t.Fatal("daemon used local watchdog")
	}
	local, _, _, _ := newModel(t)
	local.inputRunning[0] = true
	local.inputStarted[0] = time.Now().Add(-time.Minute)
	if local.paneInputReady(0) {
		t.Fatal("local watchdog changed")
	}
}

func TestDaemonAcknowledgementFailureKeepsDraft(t *testing.T) {
	for _, kind := range []string{"draft", "submit", "clear", "f4"} {
		t.Run(kind, func(t *testing.T) {
			m, p, c, r := daemonFixture(t, true)
			m.drafts["a"] = true
			fail := func() { c.op = errors.New("ack refused") }
			var cmd tea.Cmd
			switch kind {
			case "draft":
				r.draftHook = func(bool) { fail() }
				cmd = m.forward(key(tea.KeyEnter))
			case "submit":
				r.submitHook = fail
				cmd = m.forward(key(tea.KeyEnter))
			case "clear":
				r.draftHook = func(bool) { fail() }
				cmd = m.enqueueControl(0, 1)
			case "f4":
				r.deliverHook = fail
				cmd = m.key(key(tea.KeyF4))
			}
			if cmd == nil {
				t.Fatal("no operation")
			}
			done := cmd().(inputDone)
			m.inputCompleted(done)
			if done.err == nil || !m.drafts["a"] || !strings.Contains(m.feedback, "WARNING") {
				t.Fatal("refusal reported success")
			}
			if kind == "draft" && len(p.keys) != 0 {
				t.Fatal("key bypassed draft refusal")
			}
		})
	}
}

func TestDaemonEnterClearsOnlyAfterClearAck(t *testing.T) {
	m, _, _, _ := daemonFixture(t, true)
	enter := m.forward(key(tea.KeyEnter))
	clear := m.inputCompleted(enter().(inputDone))
	if clear == nil || !m.drafts["a"] {
		t.Fatal("Enter cleared draft before draft(false) ack")
	}
	m.inputCompleted(clear().(inputDone))
	if m.drafts["a"] {
		t.Fatal("acknowledged quiet clear did not clear draft")
	}
}

func TestDaemonRapidTakeoverKeepsSingleCommand(t *testing.T) {
	m, p, _, _ := daemonFixture(t, true)
	old := m.forward(tea.KeyPressMsg{Code: 'x', Text: "x"})
	p.state.YouHold = false
	p.state.Rev = 2
	m.reconcileDaemon()
	p.state.YouHold = true
	p.state.Rev = 3
	m.reconcileDaemon()
	if !m.inputRunning[0] {
		t.Fatal("old command slot released before completion")
	}
	if m.forward(tea.KeyPressMsg{Code: 'y', Text: "y"}) != nil {
		t.Fatal("parallel command launched after regain")
	}
	if next := m.inputCompleted(old().(inputDone)); next == nil {
		t.Fatal("new queue did not resume after stale command finished")
	}
}

func TestDaemonConnectionTitleHintsAndDisconnect(t *testing.T) {
	m, _, c, _ := daemonFixture(t, true)
	m.applyConnection(&protocol.ConnectionView{State: protocol.ConnConnected, LANPlainHTTP: true})
	if !strings.HasPrefix(m.titleLine(), labelConnectedLANPlainHTTP) || !strings.Contains(m.titleLine(), reattachHint) {
		t.Fatal("gateway/reattach label missing")
	}
	m.outbound.source = true
	if strings.Contains(m.historyHints(), "F10 quit") || !strings.Contains(m.historyHints(), "F6 take input") {
		t.Fatal("outbound detach hints missing")
	}
	c.errorState = errors.New("socket closed")
	_, cmd := m.Update(tickMsg(time.Now()))
	assertDetach(t, cmd)
	if err := m.WindowError(); err == nil || !strings.Contains(err.Error(), "sessions may still be running; run rtk attach") {
		t.Fatal("disconnect looked like session termination")
	}
}
