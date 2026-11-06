package tui

// Authored but UNRUN under GR-3; pure model/stub checks only.
import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// UNRUN under GR-3: all state/flag combinations and existing width policy.
func TestLANPlainHTTPConnectionLabel(t *testing.T) {
	for _, tc := range []struct {
		state protocol.ConnState
		want  string
	}{
		{protocol.ConnConnecting, labelConnecting},
		{protocol.ConnConnected, labelConnected},
		{protocol.ConnReconnecting, labelReconnecting},
		{protocol.ConnRefused, labelRefused},
		{protocol.ConnState("unknown"), labelUnavailable},
	} {
		for _, plain := range []bool{false, true} {
			want := tc.want
			if plain && tc.state == protocol.ConnConnected {
				want = "Gateway connected (plain HTTP)"
			}
			if got := connectionLabel(&protocol.ConnectionView{State: tc.state, LANPlainHTTP: plain}); got != want {
				t.Errorf("state=%s plain=%t: %q, want %q", tc.state, plain, got, want)
			}
		}
	}
	if connectionLabel(nil) != labelUnavailable {
		t.Fatal("nil label changed")
	}
	m, _, _, _ := newModel(t)
	localTitle := m.titleLine()
	m.applyConnection(&protocol.ConnectionView{State: protocol.ConnConnected, LANPlainHTTP: true})
	if m.titleLine() != localTitle {
		t.Fatal("local title changed")
	}
	m.applySnapshot(protocol.Snapshot{Runtime: true, Connection: &protocol.ConnectionView{State: protocol.ConnConnected, LANPlainHTTP: true}})
	const label = "Gateway connected (plain HTTP)"
	if ansi.StringWidth(label) != 30 || ansi.StringWidth(label) > minUsableWidth {
		t.Fatal("label does not fit minimum width")
	}
	for _, width := range []int{0, 1, 29, 30, 39, minUsableWidth, 80} {
		got := fitPlain(m.titleLine(), width)
		if ansi.StringWidth(got) != width {
			t.Errorf("width %d: %q", width, got)
		}
		if width >= 30 && !strings.HasPrefix(got, label) {
			t.Errorf("label truncated at width %d", width)
		}
	}
	m.resize(minUsableWidth, minUsableHeight)
	if !strings.HasPrefix(m.render()[0], label) {
		t.Fatal("normal title lost label")
	}
	m.resize(30, minUsableHeight)
	if !strings.HasPrefix(m.renderMinimal(30, minUsableHeight)[0], label) {
		t.Fatal("minimal title lost label")
	}
}

func TestConnectedResultMeaning(t *testing.T) {
	for _, tc := range []struct {
		result protocol.SendResult
		want   string
	}{
		{protocol.SendSending, "sending"},
		{protocol.SendGatewayUnreachable, "gateway unreachable — not sent"},
		{protocol.SendGatewayUncertain, "gateway uncertain — may still arrive"},
		{protocol.SendUnknownRecipient, "unknown recipient — not delivered"},
		{protocol.SendDestinationUnreachable, "destination unreachable — not delivered"},
		{protocol.SendSessionGone, "session ended — not delivered"},
		{protocol.SendDeliveryUncertain, "delivery uncertain — may still arrive"},
		{protocol.SendHostStored, "saved by recipient host — delivery pending"},
	} {
		if got := sendResultLabel(tc.result); got != tc.want {
			t.Errorf("%s: %q", tc.result, got)
		}
	}
	line := sendLine(protocol.SendView{Result: protocol.SendHostStored, Report: protocol.MessageFailed, ReportReason: "write failed"})
	if !strings.Contains(line, "delivery pending — recipient reports failed (write failed)") {
		t.Fatal(line)
	}
}

func TestConnectionCacheAndLocalDisplay(t *testing.T) {
	m, _, _, _ := newModel(t)
	localTitle, localHints := m.titleLine(), m.historyHints()
	for _, tc := range []struct {
		state protocol.ConnState
		want  string
	}{
		{protocol.ConnConnecting, labelConnecting}, {protocol.ConnConnected, labelConnected},
		{protocol.ConnReconnecting, labelReconnecting}, {protocol.ConnRefused, labelRefused},
	} {
		connection := &protocol.ConnectionView{State: tc.state, Err: "credential removed", Peers: []string{"codex@host"}}
		m.applySnapshot(protocol.Snapshot{Runtime: true, Connection: connection})
		connection.Peers[0] = "mutated"
		if !strings.HasPrefix(m.titleLine(), tc.want) || m.conn.Peers[0] != "codex@host" {
			t.Fatal("state or shared cache changed")
		}
	}
	m.applySnapshot(protocol.Snapshot{})
	if m.titleLine() != localTitle || m.historyHints() != localHints || m.conn != nil {
		t.Fatal("local view changed")
	}
	m.logMode = true
	m.historyKey(key(tea.KeyTab))
	if m.outbound.source {
		t.Fatal("local Tab enabled outbound")
	}
}

func TestOutboundReopenRejectsOldReadAndShowsHints(t *testing.T) {
	m, _, _, _ := newModel(t)
	m.applySnapshot(protocol.Snapshot{Runtime: true})
	m.enterHistory()
	m.toggleOutbound()
	old := m.outbound.generation
	m.leaveHistory()
	m.enterHistory()
	if !strings.Contains(m.historyHints(), "Tab outbound") {
		t.Fatal("outbound source undiscoverable")
	}
	m.toggleOutbound()
	m.installOutboundSends(outboundSendsMsg{generation: old, sends: []protocol.SendView{{ID: "stale"}}})
	if len(m.outbound.list) != 0 {
		t.Fatal("stale read landed after reopening")
	}
	if !strings.Contains(m.historyHints(), "Tab received") {
		t.Fatal("wrong active-source hints")
	}
}

func TestOutboundErrorRetainsBodyAndRendererIsPure(t *testing.T) {
	m, _, _, _ := newModel(t)
	m.applySnapshot(protocol.Snapshot{Runtime: true})
	m.logMode = true
	m.outbound = outboundState{source: true, detailID: "s", detailLoaded: true, generation: 8, detailScroll: 999,
		detail: protocol.SendDetail{SendView: protocol.SendView{ID: "s"}, Body: "retained body"}}
	m.installOutboundDetail(outboundDetailMsg{id: "s", generation: 8, err: errors.New("read failed")})
	if !strings.Contains(strings.Join(m.outboundDetailContent(80), "\n"), "retained body") {
		t.Fatal("reload hid body")
	}
	m.outboundDetailScreen(80, 5)
	if m.outbound.detailScroll != 999 {
		t.Fatal("render mutated scroll")
	}
	m.closeOutboundDetail()
	m.installOutboundDetail(outboundDetailMsg{id: "s", generation: 8})
	if m.outbound.detailLoaded {
		t.Fatal("closed detail response landed")
	}
}

func TestRecentSendUpdatesAndResync(t *testing.T) {
	m, _, _, _ := newModel(t)
	at := time.Unix(10, 0)
	m.applySnapshot(protocol.Snapshot{Runtime: true, Messages: []protocol.MessageView{{ID: "same", At: at}}, Sends: []protocol.SendView{{ID: "same", At: at, Result: protocol.SendSending}}})
	m.applyEvent(protocol.Event{Send: &protocol.SendView{ID: "same", At: at, Result: protocol.SendDeliveryUncertain}})
	rows := m.recentRows()
	if len(rows) != 2 || rows[0].kind != 0 || !strings.Contains(rows[1].text, "may still arrive") {
		t.Fatalf("rows: %+v", rows)
	}
	m.logMode = true
	m.outbound.source = true
	m.applySnapshot(protocol.Snapshot{Runtime: true})
	if !m.outbound.dirty || len(m.sends) != 0 {
		t.Fatal("resync failed to refresh outbound")
	}
}
