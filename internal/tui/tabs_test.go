package tui

// Authored but UNRUN under GR-3. UI messages and fake panes only.
import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func tabCell(t *testing.T, m *Model, label string) int {
	t.Helper()
	line := ansi.Strip(strings.Split(m.View().Content, "\n")[0])
	index := strings.Index(line, label)
	if index < 0 {
		t.Fatalf("tab %q absent from row0: %q", label, line)
	}
	return ansi.StringWidth(line[:index]) + 1
}

func TestTabsSwitchBothWaysWithoutInputOrOwnership(t *testing.T) {
	m, p, c, r := daemonFixture(t, true)
	before := p.state
	x := tabCell(t, m, "Network")
	m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
	if !m.networkMode || m.anyDraft() || len(m.input[0]) != 0 || c.takes != 0 || p.state != before || len(r.drafts) != 0 {
		t.Fatal("tab click changed child state")
	}
	m.Update(tea.MouseReleaseMsg{X: 2, Y: screenTop(), Button: tea.MouseLeft})
	if len(p.mouse) != 0 || len(m.input[0]) != 0 {
		t.Fatal("tab release reached child")
	}
	x = tabCell(t, m, "Terminals")
	m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
	if m.networkMode || m.logMode || m.anyDraft() {
		t.Fatal("Terminals tab did not return")
	}
}

func TestTabsStayAtRowZeroInMinimalAndQuitViews(t *testing.T) {
	for _, mode := range []string{"small", "dead", "quit", "log"} {
		m, p, _, _ := daemonFixture(t, true)
		switch mode {
		case "small":
			m.resize(30, 5)
		case "dead":
			p.dead = true
			p.state.InputReady = false
			m.resize(30, 5)
		case "quit":
			m.quitArmed = true
			m.feedback = "warning"
			m.resize(30, 5)
		case "log":
			m.logMode = true
		}
		x := tabCell(t, m, "Network")
		m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
		if mode == "quit" {
			if m.networkMode || !m.quitArmed {
				t.Fatal("quit warning bypassed")
			}
		} else if !m.networkMode {
			t.Fatal(mode, "tab not clickable")
		}
	}
}

func TestOwnedGestureOverStripKeepsClampedRelease(t *testing.T) {
	m, p, _, _ := daemonFixture(t, true)
	p.mouseOn = true
	_, first := m.Update(tea.MouseClickMsg{X: 2, Y: screenTop(), Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 18, Y: 0, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 18, Y: 0, Button: tea.MouseLeft})
	if m.networkMode || m.mouse.owner >= 0 {
		t.Fatal("owned gesture switched tab or stayed owned")
	}
	runInputCommand(t, m, first)
	if len(p.mouse) != 3 {
		t.Fatal("lost owned press/drag/release", len(p.mouse))
	}
	for _, event := range p.mouse {
		if event.Mouse().Y < 0 {
			t.Fatal("unclamped strip coordinate")
		}
	}
}

func TestLocalTitleAndPaneGeometryUnaffectedByTabs(t *testing.T) {
	m, _, _, _ := newModel(t)
	if !strings.HasPrefix(ansi.Strip(m.View().Content), "ratatoskr") {
		t.Fatal("daemon tabs in plain rtk")
	}
	d, _, _, _ := daemonFixture(t, true)
	if screenTop() != 2 || chromeRows != 7 || d.boxes()[0].top != 2 || d.mouseTargetAt(0, 0).pane != -1 {
		t.Fatal("tab strip shifted pane geometry")
	}
}
