package tui

// Authored but UNRUN under GR-3. Stub queries and pure rendering; no socket.
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
)

func networkFixtureBody(at time.Time) protocol.NetworkView {
	live := true
	return protocol.NetworkView{Schema: 1, FetchedAt: protocol.FormatTime(at),
		Local:   protocol.NetworkLocal{Host: "local", Sessions: []protocol.ReportSession{{Name: "claude", LocalID: "local-a", State: "unknown", Input: "held"}}},
		Gateway: protocol.NetworkGatewayView{State: protocol.GatewayConnected},
		Snapshot: &protocol.NetworkSnapshot{Schema: 1, Gateway: protocol.NetworkGateway{ReportStaleAfterMS: 30000},
			Hosts:    []protocol.NetworkHost{{Host: "remote", Connection: protocol.NetworkConnection{Live: true}, ReportStatus: "fresh", Report: &protocol.HostReport{AgeMS: 2000}}},
			Sessions: []protocol.NetworkSession{{Host: "remote", Name: "codex", SessionID: "remote-a", Address: "codex@remote", Directory: protocol.DirectoryFacts{Registered: true, Live: &live}, Reported: &protocol.ReportedSession{State: "busy", Input: "free", AgeMS: 2000}}}}}
}

func installNetworkBody(m *Model, view protocol.NetworkView, version uint64) {
	m.network.generation++
	m.applyNetwork(networkMsg{generation: m.network.generation, view: view, version: version})
}

func TestNetworkFreshnessUsesEveryAnswer(t *testing.T) {
	m, _, _, _ := daemonFixture(t, true)
	base := time.Unix(1700000000, 0).UTC()
	now := base.Add(10 * time.Second)
	m.now = func() time.Time { return now }
	view := networkFixtureBody(base)
	installNetworkBody(m, view, 7)
	if got := networkRowAge(m, m.network.view.Snapshot.Sessions[0].Reported); got != "12s" {
		t.Fatal(got)
	}
	view.FetchedAt = protocol.FormatTime(base.Add(8 * time.Second))
	view.Snapshot.Sessions[0].Reported.AgeMS = 10000
	view.Local.Sessions[0].State = "busy"
	installNetworkBody(m, view, 7)
	if m.network.view.FetchedAt != view.FetchedAt || m.network.view.Local.Sessions[0].State != "busy" {
		t.Fatal("same-version body discarded")
	}
	now = base.Add(40 * time.Second)
	if !networkSessionOld(m, m.network.view.Snapshot.Sessions[0]) {
		t.Fatal("age growth did not mark report old")
	}
	view.Gateway.State = protocol.GatewayUnreachable
	installNetworkBody(m, view, 8)
	if !networkSessionOld(m, m.network.view.Snapshot.Sessions[0]) || m.network.view.FetchedAt != view.FetchedAt {
		t.Fatal("unreachable snapshot freshness reset")
	}
}

func TestNetworkOneQueryAndNoFetchUntilOpened(t *testing.T) {
	m, _, _, _ := daemonFixture(t, true)
	now := time.Unix(1700000000, 0).UTC()
	m.now = func() time.Time { return now }
	var requests []wire.NetworkRequest
	m.daemon.network = func(_ context.Context, r wire.NetworkRequest) (protocol.NetworkView, uint64, error) {
		requests = append(requests, r)
		return networkFixtureBody(now), 1, nil
	}
	if m.networkTick() != nil || len(requests) != 0 {
		t.Fatal("fetched before F5")
	}
	first := m.key(key(tea.KeyF5))
	if first == nil || !m.network.pending {
		t.Fatal("F5 did not request")
	}
	if m.key(key('r')) != nil || m.networkTick() != nil {
		t.Fatal("wait overlapped")
	}
	m.key(key(tea.KeyF5))
	m.applyNetwork(first().(networkMsg))
	if m.network.pending || m.networkTick() != nil {
		t.Fatal("hidden view chained query")
	}
	if requests[0].FirstWaitMS != 8000 || requests[0].WaitMS != 0 {
		t.Fatal(requests)
	}
	now = now.Add(time.Second)
	next := m.key(key(tea.KeyF5))
	if next == nil {
		t.Fatal("reopen did not refetch")
	}
	m.applyNetwork(next().(networkMsg))
	if requests[1].Since != 0 || requests[1].WaitMS != 0 || requests[1].FirstWaitMS != 0 {
		t.Fatal("R/reopen was not immediate", requests)
	}
	now = now.Add(time.Second)
	wait := m.networkTick()
	if wait == nil {
		t.Fatal("recurring poll missing")
	}
	wait()
	if requests[2].Since != 1 || requests[2].WaitMS != 8000 || requests[2].FirstWaitMS != 0 {
		t.Fatal(requests)
	}
}

func TestNetworkInputSuppressionAndLocalF5(t *testing.T) {
	m, p, c, r := daemonFixture(t, true)
	m.networkMode = true
	m.Update(tea.PasteStartMsg{})
	m.Update(tea.PasteMsg{Content: "not child input"})
	m.Update(tea.MouseClickMsg{X: 4, Y: screenTop(), Button: uv.MouseLeft})
	m.key(key(tea.KeyEnter))
	m.key(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.anyDraft() || len(m.input[0]) != 0 || len(r.drafts) != 0 || len(p.keys) != 0 || len(p.pastes) != 0 || len(p.mouse) != 0 {
		t.Fatal("Network leaked pane input")
	}
	f6 := m.key(key(tea.KeyF6))
	if f6 == nil {
		t.Fatal("F6 swallowed")
	}
	m.applyTakeInputDone(f6().(takeInputDone))
	if c.takes != 1 {
		t.Fatal("F6 did not target terminal")
	}
	assertDetach(t, m.key(tea.KeyPressMsg{Code: 'c', Mod: uv.ModCtrl}))
	local, _, left, _ := newModel(t)
	cmd := local.key(key(tea.KeyF5))
	if cmd == nil {
		t.Fatal("local F5 intercepted")
	}
	cmd()
	if len(left.keys) != 1 || left.keys[0].Code != tea.KeyF5 {
		t.Fatal("local F5 changed")
	}
}

func TestNetworkSchemasSanitizationSelectionAndPureRender(t *testing.T) {
	m, _, _, _ := daemonFixture(t, true)
	now := time.Unix(1700000000, 0)
	m.now = func() time.Time { return now }
	view := networkFixtureBody(now)
	installNetworkBody(m, view, 1)
	want := rowKey{kind: networkRowRemote, host: "remote", id: "remote-a"}
	m.network.selection = want
	view.Snapshot.Sessions = append([]protocol.NetworkSession{{Host: "other", SessionID: "new", Address: "new@other"}}, view.Snapshot.Sessions...)
	installNetworkBody(m, view, 2)
	if m.network.selection != want {
		t.Fatal("refresh lost raw identity")
	}
	before := m.network.scrollTop
	m.networkLines(40, 3)
	if m.network.scrollTop != before {
		t.Fatal("View mutated scroll")
	}
	if got := cleanNetworkText("\x1b[31mhost\x1b[0m\u202e\u200b\u034f\ufe0f\u2028"); got != "host" {
		t.Fatal(got)
	}
	view.Snapshot.Schema = 2
	installNetworkBody(m, view, 3)
	var text []string
	for _, row := range m.networkRows() {
		text = append(text, row.text)
	}
	if strings.Contains(strings.Join(text, "\n"), "codex@remote") || !strings.Contains(strings.Join(text, "\n"), "schema unsupported") {
		t.Fatal("new nested schema rendered")
	}
	view.Schema = 2
	view.Local.Sessions[0].Name = "do-not-render-new-schema"
	installNetworkBody(m, view, 4)
	if strings.Contains(m.networkHeaderLine(), "do-not-render") || m.network.errText == "" || !m.network.oldBody {
		t.Fatal("new outer schema accepted")
	}
}

func TestNetworkMissingReportAndFailureRetainLocal(t *testing.T) {
	m, _, _, _ := daemonFixture(t, true)
	now := time.Unix(1700000000, 0)
	m.now = func() time.Time { return now }
	view := networkFixtureBody(now)
	view.Gateway.State = protocol.GatewayNotConfigured
	view.Snapshot = nil
	installNetworkBody(m, view, 1)
	if len(m.networkRows()) < 2 || !strings.Contains(m.networkRows()[1].text, "unknown") {
		t.Fatal("local unknown unavailable without gateway")
	}
	m.applyNetwork(networkMsg{generation: m.network.generation, err: errors.New("read failed")})
	if !m.network.have || !m.network.oldBody || !strings.Contains(m.networkHeaderLine(), "read failed") {
		t.Fatal("failed refresh discarded content")
	}
	view = networkFixtureBody(now)
	view.Snapshot.Hosts[0].Report = nil
	view.Snapshot.Hosts[0].ReportStatus = protocol.ReportStatusNone
	installNetworkBody(m, view, 2)
	var found bool
	for _, row := range m.networkRows() {
		if row.key.kind == networkRowHost {
			found = strings.Contains(row.text, "no report")
		}
	}
	if !found {
		t.Fatal("no report became empty/idle")
	}
}
