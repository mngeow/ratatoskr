package dashboard

// Written under GR-3 and never run. Pure functions of fixed snapshots: no gateway, store or network.

import (
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

func i64(n int64) *int64 { return &n }
func bp(b bool) *bool    { return &b }

func fixture() Data {
	return Data{
		Now: time.Date(2026, 11, 11, 14, 9, 20, 0, time.UTC),
		Network: protocol.NetworkSnapshot{
			Gateway: protocol.NetworkGateway{StartedAt: "2026-11-08T10:00:00Z"},
			Hosts: []protocol.NetworkHost{
				{Host: "mac", Connection: protocol.NetworkConnection{Live: true, HeartbeatAgeMS: i64(5000)}},
				{Host: "vmb", Connection: protocol.NetworkConnection{Live: true, HeartbeatAgeMS: i64(2000)}},
				{Host: "vmc", Connection: protocol.NetworkConnection{HeartbeatAgeMS: i64(900000)}},
				{Host: "vmd"},
			},
			Sessions: []protocol.NetworkSession{
				{Address: "claude@mac", Host: "mac", Name: "claude", SessionID: "ses_c1a4e0", Directory: protocol.DirectoryFacts{Registered: true, Live: bp(true)},
					Reported: &protocol.ReportedSession{State: "idle", Input: "free"}},
				{Address: "codex@vmb", Host: "vmb", Name: "codex", SessionID: "ses_9d31a7", Directory: protocol.DirectoryFacts{Registered: true}},
			},
		},
		Graph: protocol.GraphSnapshot{
			EdgesPartial: true,
			Hosts: []protocol.GraphHost{
				{Name: "mac", Connection: protocol.GraphConnection{Live: true, Source: protocol.GraphSourceStream},
					Card: protocol.GraphCard{State: protocol.CardFresh, Source: protocol.GraphSourcePoll, AgeMS: i64(4000), Revision: "r1",
						Card: &protocol.HostCard{Name: "mac", Description: "<script>alert(1)</script>", Version: "1.0"}}},
				{Name: "vmb", Connection: protocol.GraphConnection{Live: true}, Card: protocol.GraphCard{State: protocol.CardStale, AgeMS: i64(251000)}},
				{Name: "vmc", Connection: protocol.GraphConnection{HeartbeatAgeMS: i64(900000)}, Card: protocol.GraphCard{State: protocol.CardOffline}},
				{Name: "vmd", Card: protocol.GraphCard{State: protocol.CardNone}},
			},
			Edges: []protocol.GraphEdge{
				{From: "mac", To: "vmb", Kind: protocol.GraphEdgeMessagesSeen, Count: 3, HostStored: 2},
				{From: "mac", To: "gone", Kind: protocol.GraphEdgeMessagesSeen, Count: 1},
			},
		},
	}
}

func TestFormatAge(t *testing.T) {
	for ms, want := range map[int64]string{-5: "0 s", 0: "0 s", 4000: "4 s", 251000: "4 m", 7200000: "2 h", 3 * 86400000: "3 d"} {
		if got := FormatAge(ms); got != want {
			t.Errorf("FormatAge(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestOverviewCountsKeepTheFactsApart(t *testing.T) {
	p := BuildOverview(fixture(), Info{})
	if p.HostCount != 4 || p.LiveHostCount != 2 || p.FreshCardCount != 1 || p.StaleCardCount != 1 || p.OfflineCardCount != 1 || p.NoCardCount != 1 ||
		p.SessionCount != 2 || p.ConnectedSessionCount != 1 {
		t.Fatalf("%+v", p)
	}
	// mac is live with a fresh card: the only host that does not need a look.
	if len(p.Attention) != 3 || p.Attention[0].Name != "vmb" {
		t.Fatalf("attention = %+v", p.Attention)
	}
	if !strings.Contains(p.Chrome.OpenAccessText, "plain HTTP") {
		t.Fatalf("the strip must say plain HTTP: %q", p.Chrome.OpenAccessText)
	}
	if h := BuildOverview(fixture(), Info{HTTPS: true}); strings.Contains(h.Chrome.OpenAccessText, "plain HTTP") || !strings.Contains(h.Chrome.OpenAccessText, "no login") {
		t.Fatalf("https strip: %q", h.Chrome.OpenAccessText)
	}
}

func TestGraphIsHubAndSpokeWithNeverSeenDotted(t *testing.T) {
	doc := BuildGraph(fixture())
	classes := map[string]string{}
	var seen []GraphElement
	for _, e := range doc.Elements {
		classes[e.Data.ID] = e.Classes
		if e.Classes == "seen" {
			seen = append(seen, e)
		}
	}
	for id, want := range map[string]string{
		"gateway":   "gateway",
		"host:mac":  "host live card-fresh",
		"spoke:mac": "spoke live",
		"host:vmc":  "host notlive card-offline",
		"spoke:vmc": "spoke notlive",
		"host:vmd":  "host neverseen card-none",
		"spoke:vmd": "spoke neverseen",
		"spoke:vmb": "spoke live",
		"host:vmb":  "host live card-stale",
	} {
		if classes[id] != want {
			t.Errorf("%s: classes %q, want %q", id, classes[id], want)
		}
	}
	// One overlay edge only: the one to a host that is not paired is dropped. No host-to-host connectivity edge.
	if len(seen) != 1 || seen[0].Data.Count != 3 || seen[0].Data.HostStored != 2 || !doc.EdgesPartial {
		t.Fatalf("seen = %+v partial=%v", seen, doc.EdgesPartial)
	}
	for _, e := range doc.Elements {
		if e.Data.Source != "" && e.Classes != "seen" && e.Data.Source != "gateway" {
			t.Fatalf("a connectivity edge that does not start at the gateway: %+v", e)
		}
		if strings.HasPrefix(e.Data.ID, "host:") && (e.Data.SessionCount == nil || e.Data.ConnectionLabel == "") {
			t.Fatalf("a host node without its row facts: %+v", e)
		}
	}
}

func TestCardPartialKeepsSessionsExactAndUnknownEmpty(t *testing.T) {
	card, ok := BuildCard(fixture(), "vmb")
	if !ok || card.CardState != protocol.CardStale || card.CardAge != "4 m" || card.Card != nil || card.CardVersion != "" {
		t.Fatalf("%+v %v", card, ok)
	}
	if len(card.Sessions) != 1 || card.Sessions[0].SessionID != "ses_9d31a7" || card.Sessions[0].DirectoryLiveKnown || card.Sessions[0].State != "" {
		t.Fatalf("sessions = %+v (an unknown directory fact must not become false)", card.Sessions)
	}
	mac, _ := BuildCard(fixture(), "mac")
	if mac.HeartbeatAge != "5 s" || mac.Sessions[0].State != "idle" || !mac.Sessions[0].DirectoryLive || mac.CardVersion != "1.0" || mac.CardRevision != "r1" {
		t.Fatalf("%+v", mac)
	}
	if _, ok := BuildCard(fixture(), "nobody"); ok {
		t.Fatal("an unknown host produced a card")
	}
}

func TestStatusGatewayUptime(t *testing.T) {
	s := BuildStatus(fixture())
	if s.GatewayState != "up 3 d 4 h" || s.HostCount != 4 || s.LiveHostCount != 2 || s.CardNoneCount != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestCardURLEscapesTheHostName(t *testing.T) {
	if got := CardURL("a b/c"); got != "/ui/hosts/a%20b%2Fc/card" {
		t.Fatalf("CardURL = %q", got)
	}
}
