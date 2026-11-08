package tui

// Authored but UNRUN under GR-3. Pure graph fixtures, no daemon or model calls.
import (
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"strings"
	"testing"
)

func TestGraphTabHitCellsMatchVisibleBlocks(t *testing.T) {
	m := themedFixture(t)
	m.graphMode, m.networkMode = true, false
	for _, width := range []int{5, 9, 26, 27, 80} {
		m.width = width
		_, plain, hits := m.tabLayout(width)
		want := " Terminals  Network >Graph "
		start := 20
		if width < 27 {
			want, start = " T  N >G ", 6
		}
		if plain != want || ansi.StringWidth(plain) != len(want) {
			t.Fatal("graph marker changed tab width", plain)
		}
		for x := start; x < min(width, len(want)); x++ {
			kind, hit := m.tabAt(x)
			if !hit || kind != tabGraph {
				t.Fatal("graph padding is not clickable", x, hits)
			}
		}
	}
}

func TestGraphSameVersionRefreshOwnsBodyAndPreservesHostSlots(t *testing.T) {
	m := &Model{}
	v := protocol.GraphView{Schema: protocol.GraphSchema, Snapshot: &protocol.GraphSnapshot{Schema: protocol.GraphSchema, Hosts: []protocol.GraphHost{{Name: "beta"}, {Name: "gamma"}}}}
	m.applyGraph(graphMsg{view: v, version: 1})
	if len(m.graph.order) != 2 || m.graph.order[0] != "beta" {
		t.Fatal("initial host slots")
	}
	v.Snapshot.Hosts = []protocol.GraphHost{{Name: "alpha"}, {Name: "gamma"}}
	v.FetchedAt = "2026-11-07T15:00:00Z"
	m.applyGraph(graphMsg{view: v, version: 1})
	v.Snapshot.Hosts[1].Name = "mutated caller"
	if m.graph.order[0] != "alpha" || m.graph.order[1] != "gamma" || m.graph.view.FetchedAt != "2026-11-07T15:00:00Z" || m.graph.view.Snapshot.Hosts[1].Name != "gamma" {
		t.Fatal("refresh moved an existing host, ignored the new body, or aliased it")
	}
}

func TestGraphRawCardKeepsEscapesAndTail(t *testing.T) {
	card := &protocol.HostCard{Name: "host", Description: "\x1b[31m\u202e" + strings.Repeat("a", 64000) + "TAIL"}
	poll := &protocol.GraphCard{UnknownFields: 2}
	lines := cardSectionLines(card, poll, nil, cardSectionRaw, 40)
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "what the gateway kept") || !strings.Contains(text, "TAIL") || !strings.Contains(text, "unknown fields ignored: 2") || strings.ContainsRune(text, '\x1b') {
		t.Fatal("raw view lost provenance, escaped controls, or the end of the body")
	}
}
