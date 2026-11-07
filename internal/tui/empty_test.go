package tui

// Authored but UNRUN under GR-3. Empty models, fake router/client and UI messages only.
import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func emptyWindowFixture(t *testing.T) (*Model, *daemonTestRouter, *daemonTestClient) {
	_, _, client, router := daemonFixture(t, true)
	m := allocateModel(router, nil, nil)
	m.daemon = &daemonWindow{client: client}
	m.networkMode = true
	m.applySnapshot(router.snap)
	m.resize(80, 24)
	return m, router, client
}

func TestEmptyDaemonHasNoPaneRoutesAndKeepsLocalValidation(t *testing.T) {
	m, r, c := emptyWindowFixture(t)
	if m.activeCount() != 0 || len(m.boxes()) != 0 || len(m.activeSlots()) != 0 || m.cursor() != nil || m.canReceiveInput() {
		t.Fatal("phantom pane")
	}
	if _, err := New(r, nil, nil); err == nil {
		t.Fatal("local constructor accepted empty slots")
	}
	if _, err := NewRuntime(r, nil, nil); err == nil {
		t.Fatal("runtime constructor accepted empty slots")
	}
	for _, code := range []rune{tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4, tea.KeyF5, tea.KeyF6, 'x', tea.KeyEnter, tea.KeyBackspace} {
		m.key(key(code))
	}
	m.Update(tea.PasteStartMsg{})
	m.Update(tea.PasteMsg{Content: "no child"})
	m.Update(tea.MouseClickMsg{X: 2, Y: screenTop(), Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 2, Y: screenTop(), Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 2, Y: screenTop(), Button: tea.MouseLeft})
	m.Update(tea.MouseWheelMsg{X: 2, Y: screenTop(), Button: tea.MouseWheelDown})
	m.View()
	if m.anyDraft() || len(r.drafts) != 0 || c.takes != 0 || len(m.input[0])+len(m.input[1]) != 0 || c.p.snapshots != 0 {
		t.Fatal("empty window touched a pane")
	}
	assertDetach(t, m.key(ctrlKey()))
}

func TestEmptyTerminalsGuidanceAndNetworkStates(t *testing.T) {
	for _, small := range []bool{false, true} {
		m, _, _ := emptyWindowFixture(t)
		if small {
			m.resize(35, 7)
		}
		m.networkMode = false
		text := ansi.Strip(m.View().Content)
		if !strings.Contains(text, "No sessions in this window.") || !strings.Contains(text, "Terminals ") {
			t.Fatal(text)
		}
		m.networkMode = true
		text = ansi.Strip(m.View().Content)
		if !strings.Contains(text, "waiting for the first answer") {
			t.Fatal(text)
		}
	}
	m, _, _ := emptyWindowFixture(t)
	for _, state := range []string{protocol.GatewayNotConfigured, protocol.GatewayUnsupported, protocol.GatewayUnreachable} {
		view := networkFixtureBody(time.Unix(1700000000, 0))
		view.Local.Sessions = nil
		view.Gateway.State = state
		installNetworkBody(m, view, 1)
		text := ansi.Strip(m.View().Content)
		if !strings.Contains(text, "no local sessions") || strings.TrimSpace(m.networkHeaderLine()) == "" {
			t.Fatal(state, text)
		}
	}
}

func TestDaemonNetworkStartsAtInitAndPollsWhileTerminalsVisible(t *testing.T) {
	m, _, _ := emptyWindowFixture(t)
	m.networkMode = false
	now := time.Unix(1700000000, 0)
	m.now = func() time.Time { return now }
	var requests []wire.NetworkRequest
	m.daemon.network = func(_ context.Context, request wire.NetworkRequest) (protocol.NetworkView, uint64, error) {
		requests = append(requests, request)
		return networkFixtureBody(now), 4, nil
	}
	cmd := m.Init()
	if cmd == nil || !m.network.pending || m.networkTick() != nil {
		t.Fatal("Init did not seed exactly one query")
	}
	// Init batches the tick and waits before the network command; run only its last,
	// fake query command. No timer, event wait or real client is executed here.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatal("missing Init batch")
	}
	m.applyNetwork(batch[len(batch)-1]().(networkMsg))
	now = now.Add(time.Second)
	next := m.networkTick()
	if next == nil {
		t.Fatal("Terminals tab stopped cache updates")
	}
	m.applyNetwork(next().(networkMsg))
	if len(requests) != 2 || requests[0].FirstWaitMS != 8000 || requests[1].Since != 4 || requests[1].WaitMS != 8000 {
		t.Fatal(requests)
	}
}

func TestEmptyWindowStillQueriesRemoteEntryAndLocalNotInWindow(t *testing.T) {
	for _, remote := range []bool{false, true} {
		m, _, _ := emptyWindowFixture(t)
		installNetworkBody(m, networkFixtureBody(time.Unix(1700000000, 0)), 1)
		if remote {
			m.network.selection = rowKey{kind: networkRowRemote, host: "remote", id: "remote-a"}
		} else {
			m.network.selection = rowKey{kind: networkRowLocal, host: "local", id: "local-a"}
		}
		m.network.haveSelection = true
		calls := 0
		m.daemon.enter = func(_ context.Context, request wire.EnterRequest) (wire.EnterPlan, error) {
			calls++
			if remote {
				if request.Host != "remote" || request.SessionID != "remote-a" {
					t.Fatal(request)
				}
				return wire.EnterPlan{Kind: wire.EnterSSH, Argv: []string{"ssh"}}, nil
			}
			if request.LocalID != "local-a" {
				t.Fatal(request)
			}
			return wire.EnterPlan{Kind: wire.EnterLocal, LocalID: "local-a", InWindow: false}, nil
		}
		cmd := m.enterNetwork()
		if cmd == nil {
			t.Fatal("empty window blocked Network entry")
		}
		m.applyEntry(cmd().(entryMsg))
		if calls != 1 || (remote && m.EntryIntent() == nil) || (!remote && m.feedback != entryNotInWindow) {
			t.Fatal(calls, m.feedback, m.EntryIntent())
		}
	}
}
