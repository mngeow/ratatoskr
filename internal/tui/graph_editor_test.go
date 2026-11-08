package tui

// Authored but UNRUN under GR-3. These exercise state with no daemon or terminal.
import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/windowclient"
)

func TestCardAdoptPreservesEvidenceByOmittingFoundLabel(t *testing.T) {
	e := cardEditor{}
	v := protocol.CardView{Schema: protocol.CardViewSchema, Token: "one"}
	v.Working.Card.Skills = []protocol.CardSkill{{ID: "existing", Name: "Owner text", Tags: []string{"tag"}}}
	e.adopt(v)
	if len(e.skills) != 1 || e.skills[0].FoundLabel != "" || e.skills[0].ID != "existing" {
		t.Fatal("unchanged entry must defer evidence preservation to the daemon")
	}
	e.skills[0].Tags[0] = "changed"
	if v.Working.Card.Skills[0].Tags[0] != "tag" {
		t.Fatal("editor changed the cached card")
	}
}

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

func TestCardConflictAndDirtyPreviewKeepHandEdits(t *testing.T) {
	m := &Model{}
	e := &m.graph.editor
	v := protocol.CardView{Schema: protocol.CardViewSchema, Token: "one"}
	e.adopt(v)
	e.name, e.dirty, e.generation = "hand edited", true, 7
	current := v
	current.Token = "two"
	m.applyCard(cardMsg{generation: 7, view: current, err: &windowclient.CardOpError{Code: wire.CodeCardConflict, Message: "conflict"}})
	if e.name != "hand edited" || !e.blocked || e.baseToken != "one" || e.view.Token != "two" {
		t.Fatal("conflict overwrote fields or rebased silently")
	}
	e.blocked, e.revision = false, 3
	current.Token = "one"
	current.Preview = &protocol.CardRevision{N: 3, Card: protocol.HostCard{Name: "old"}}
	m.applyCard(cardMsg{generation: 7, view: current, op: wire.CardOpHistory})
	if !e.previewReady || e.name != "hand edited" || !e.dirty {
		t.Fatal("history preview lost hand edits")
	}
}

func TestRollbackCannotProceedWithoutFullPreview(t *testing.T) {
	m := &Model{}
	e := &m.graph.editor
	e.mode, e.confirm = "confirm", wire.CardOpRollback
	if cmd := m.cardModalKey(key(tea.KeyEnter)); cmd != nil || !strings.Contains(e.problem, "preview") {
		t.Fatal("rollback accepted without the full revision")
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

func TestLocalCardURLUsesListenerAndOffIsExplicit(t *testing.T) {
	m := &Model{}
	m.graph.selected = graphLocalKey
	e := &m.graph.editor
	e.have = true
	e.view.Listener.State = protocol.CardListenerOn
	e.view.Listener.URL = "http://localhost:9000/card"
	// The stored working card intentionally has no served URL.
	m.openCardURL()
	if !m.graph.urlPanel || m.graph.url != e.view.Listener.URL {
		t.Fatal("local listener URL was not exposed")
	}
	e.view.Listener.State = protocol.CardListenerOff
	m.graph.urlPanel = false
	m.openCardURL()
	if m.graph.urlPanel || m.feedback != "listener off" {
		t.Fatal("off listener opened a URL panel")
	}
	text := strings.Join(cardListenerLines(e.view), " ")
	if !strings.Contains(text, "daemon starts") || !strings.Contains(text, "ends every session") || !strings.Contains(text, "cannot be toggled here") {
		t.Fatal("missing listener configuration consequences", text)
	}
}

func TestGraphLetterShortcutsAcceptShiftAndLockModifiers(t *testing.T) {
	for _, letter := range []rune{'r', 'R', 'e', 'E', 'c', 'C'} {
		m := &Model{}
		m.graph.selected = graphLocalKey
		m.graph.editor.have = true
		m.graph.editor.view.Listener.State = protocol.CardListenerOn
		m.graph.editor.view.Listener.URL = "https://localhost/card"
		m.graphKey(tea.KeyPressMsg{Code: letter, Mod: tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock | tea.ModShift})
		switch letter {
		case 'r', 'R':
			if !m.graph.refresh {
				t.Fatal("refresh ignored", letter)
			}
		case 'e', 'E':
			if !m.graph.editor.open {
				t.Fatal("edit ignored", letter)
			}
		case 'c', 'C':
			if !m.graph.urlPanel {
				t.Fatal("URL ignored", letter)
			}
		}
	}
}

func TestCardRequiredFieldsAndTagRemoval(t *testing.T) {
	m := &Model{}
	e := &m.graph.editor
	e.have = true
	m.cardOperation(wire.CardOpPublish)
	if !strings.Contains(e.problem, "card description") {
		t.Fatal(e.problem)
	}
	e.skills = []wire.CardSkillEdit{{ID: "skill", Tags: []string{"declared"}}}
	m.cardOperation(wire.CardOpEdit)
	if !strings.Contains(e.problem, "description you wrote") {
		t.Fatal(e.problem)
	}
	e.skills[0].Description = "Owner written"
	e.setField(5, "")
	if hasCardTag(e.skills[0].Tags) || !strings.Contains(e.problem, "at least one tag") {
		t.Fatal("empty tags were hidden or restored")
	}
}

func TestFoundSkillDefaultsDoNotCopyFrontMatterDescription(t *testing.T) {
	m := &Model{}
	e := &m.graph.editor
	e.have, e.open, e.mode = true, true, "found"
	e.view.Skills.Found = []protocol.FoundSkill{{Harness: "codex", Label: "scan", DirName: "skill", Name: "Found", Description: "front matter"}}
	m.cardModalKey(key(tea.KeyEnter))
	if len(e.skills) != 1 || e.skills[0].Description != "" || strings.Join(e.skills[0].Tags, ",") != "codex,detected" || e.mode != "" || e.field != 4 {
		t.Fatal("found selection must focus blank owner description with editable defaults", e)
	}
}

func TestInventoryExplainsUndocumentedLocationsAndSymlinks(t *testing.T) {
	v := protocol.CardView{}
	v.Skills.Found = []protocol.FoundSkill{{Name: "found", Label: "skill"}}
	text := strings.Join(cardSectionLines(&protocol.HostCard{}, nil, &v, cardSectionSkills, 200), " ")
	if !strings.Contains(text, "undocumented") || !strings.Contains(text, "symlinked skill folders") || strings.Contains(text, "not confirmed") || strings.Contains(text, "unconfirmed") {
		t.Fatal(text)
	}
}
