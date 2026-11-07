package tui

// Authored but UNRUN under GR-3. Stub entry queries only; no daemon or SSH.
import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func TestEntryManualKeepsDaemonPrefix(t *testing.T) {
	got := entryManualText(wire.EnterPlan{Reason: "unmapped", Manual: "on vmb: rtk attach --expect-session id"})
	if got != "not opened: unmapped on vmb: rtk attach --expect-session id" || strings.Contains(got, "on that host:") {
		t.Fatal(got)
	}
}

func entryFixture(t *testing.T) *Model {
	m, _, _, _ := daemonFixture(t, true)
	m.networkMode = true
	installNetworkBody(m, networkFixtureBody(time.Unix(1700000000, 0)), 1)
	return m
}

func TestEntryLocalUsesLocalIDWithGatewayRegistration(t *testing.T) {
	m := entryFixture(t)
	m.network.view.Local.Sessions[0].SessionID = "gateway-registration"
	m.network.selection = rowKey{kind: networkRowLocal, host: "local", id: "gateway-registration"}
	m.network.haveSelection = true
	var request wire.EnterRequest
	m.daemon.enter = func(_ context.Context, req wire.EnterRequest) (wire.EnterPlan, error) {
		request = req
		return wire.EnterPlan{Kind: wire.EnterLocal, LocalID: "a", InWindow: true}, nil
	}
	cmd := m.enterNetwork()
	if cmd == nil {
		t.Fatal("no query")
	}
	if second := m.enterNetwork(); second != nil {
		t.Fatal("overlapped entry query")
	}
	m.applyEntry(cmd().(entryMsg))
	if request.LocalID != "local-a" || request.Host != "" || request.SessionID != "" {
		t.Fatal("local identity replaced by gateway ID", request)
	}
	if m.networkMode {
		t.Fatal("local plan did not focus terminals")
	}
}

func TestEntryClosedViewDropsReplyButReleasesPending(t *testing.T) {
	m := entryFixture(t)
	m.network.selection = rowKey{kind: networkRowRemote, host: "remote", id: "remote-a"}
	m.network.haveSelection = true
	m.daemon.enter = func(context.Context, wire.EnterRequest) (wire.EnterPlan, error) {
		return wire.EnterPlan{Kind: wire.EnterSSH, Argv: []string{"ssh"}}, nil
	}
	cmd := m.enterNetwork()
	m.closeNetwork()
	m.ResumeNetwork(NetworkBookmark{Kind: networkRowRemote, Host: "remote", ID: "remote-a"})
	if m.applyEntry(cmd().(entryMsg)) != nil || m.EntryIntent() != nil || m.entryPending {
		t.Fatal("closed view reply applied or pending wedged")
	}
	m.feedback = "unchanged"
	m.applyEntry(entryMsg{generation: m.entryGeneration, viewEpoch: m.entryViewEpoch,
		selection: rowKey{kind: networkRowRemote, id: "different"}, err: errors.New("old failure")})
	if m.feedback != "unchanged" {
		t.Fatal("stale error changed feedback")
	}
}

func TestEntrySecondHopRefusedBeforeQueryAndSSHIntentCopied(t *testing.T) {
	m := entryFixture(t)
	m.network.selection = rowKey{kind: networkRowRemote, host: "remote", id: "remote-a"}
	m.network.haveSelection = true
	calls := 0
	argv := []string{"ssh", "-t", "-o", "ConnectTimeout=5", "--", "remote", "rtk attach --expect-session id"}
	m.daemon.enter = func(context.Context, wire.EnterRequest) (wire.EnterPlan, error) {
		calls++
		return wire.EnterPlan{Kind: wire.EnterSSH, Argv: argv}, nil
	}
	m.SetEnteredViaSSH(true)
	if m.enterNetwork() != nil || calls != 0 || m.feedback != entryOtherHostBlocked {
		t.Fatal("second hop not refused locally")
	}
	m.SetEnteredViaSSH(false)
	cmd := m.enterNetwork()
	if cmd == nil {
		t.Fatal("entry query missing")
	}
	if m.applyEntry(cmd().(entryMsg)) == nil || m.WindowError() != nil {
		t.Fatal("no intentional quit")
	}
	argv[0] = "changed"
	intent := m.EntryIntent()
	if intent.Plan.Argv[0] != "ssh" || intent.Address != "codex@remote" {
		t.Fatal(intent)
	}
	intent.Plan.Argv[0] = "caller mutation"
	if m.EntryIntent().Plan.Argv[0] != "ssh" {
		t.Fatal("intent aliases caller")
	}
}

func TestNetworkDuplicateAndNoReportWording(t *testing.T) {
	m := entryFixture(t)
	m.network.view.Local.Sessions[0].SessionID = "matching"
	m.network.view.Snapshot.Sessions = []protocol.NetworkSession{
		{Host: "local", SessionID: "matching", Address: "duplicate"},
		{Host: "local", SessionID: "unmatched", Address: "disagreement"},
		{Host: "remote", SessionID: "matching", Address: "other-host"},
	}
	remote := 0
	for _, row := range m.networkRows() {
		if row.key.kind != networkRowRemote {
			continue
		}
		remote++
		if strings.Contains(row.text, "duplicate") || !strings.Contains(row.text, "no report") || strings.Contains(row.text, "not available not available") {
			t.Fatal(row.text)
		}
	}
	if remote != 2 {
		t.Fatal("lost disagreement or remote record", remote)
	}
}
