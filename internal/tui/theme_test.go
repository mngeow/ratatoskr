package tui

// Authored but UNRUN under GR-3. Pure styling/fake UI messages; nothing rendered.
import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func themedFixture(t *testing.T) *Model {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	t.Setenv("LC_ALL", "C") // ASCII symbols keep the plain/colour comparison exact.
	m, _, _, _ := daemonFixture(t, true)
	installNetworkBody(m, networkFixtureBody(time.Unix(1700000000, 0)), 1)
	return m
}

// Round2 deliberately substitutes the active left padding with > when colour
// is off. Normalize only that known tab cell, never reported/title text.
func withoutActiveTabMarker(m *Model, text string) string {
	lines := strings.Split(ansi.Strip(text), "\n")
	if len(lines) == 0 {
		return ""
	}
	_, _, hits := m.tabLayout(m.width)
	row := []rune(lines[0])
	for _, hit := range hits {
		if hit.kind == m.activeTab() && hit.start < len(row) {
			row[hit.start] = ' '
		}
	}
	lines[0] = string(row)
	return strings.Join(lines, "\n")
}

func TestDaemonThemeProfilesPreserveCellsAndTabHits(t *testing.T) {
	m := themedFixture(t)
	// Profile values are pinned by colorprofile v0.4.3: 2 ASCII, 3 ANSI,
	// 4 ANSI256, 5 TrueColor. No direct colorprofile dependency is introduced.
	for _, profile := range []byte{2, 3, 4, 5} {
		msg := tea.ColorProfileMsg{}
		switch profile {
		case 2:
			msg.Profile = 2
		case 3:
			msg.Profile = 3
		case 4:
			msg.Profile = 4
		case 5:
			msg.Profile = 5
		}
		m.Update(msg)
		row := strings.Split(m.View().Content, "\n")[0]
		if ansi.StringWidth(row) != m.width {
			t.Fatal(profile, "title width", ansi.StringWidth(row))
		}
		if profile > 2 && !strings.Contains(row, "\x1b[") {
			t.Fatal(profile, "no style sequences")
		}
		for x := 0; x < m.width; x++ {
			kind, hit := m.tabAt(x)
			m.theme.noColor = true
			m.Update(msg)
			plainKind, plainHit := m.tabAt(x)
			if kind != plainKind || hit != plainHit {
				t.Fatal("profile changed hit cells", x)
			}
			m.theme.noColor = false
			m.Update(msg)
		}
		coloured := m.View().Content
		m.theme.noColor = true
		m.Update(msg)
		plain := m.View().Content
		if withoutActiveTabMarker(m, coloured) != withoutActiveTabMarker(m, plain) {
			t.Fatal(profile, "styling changed plain cells")
		}
		m.theme.noColor = false
	}
}

func TestNoColorNonemptyAndEmptyOverride(t *testing.T) {
	for _, value := range []string{"", "1", "0", "anything"} {
		t.Setenv("NO_COLOR", value)
		t.Setenv("LC_ALL", "en_US.UTF-8")
		m, _, _, _ := daemonFixture(t, true)
		m.Update(tea.ColorProfileMsg{Profile: 5})
		if m.theme.colours != (value == "") {
			t.Fatal(value, "NO_COLOR policy", m.theme.colours)
		}
	}
}

func TestThemeDoesNotRestyleHarnessFrames(t *testing.T) {
	m, p, _, _ := daemonFixture(t, true)
	p.frame.Content = "\x1b[2;31mchild stays faint red\x1b[0m"
	m.View()
	box, _ := m.boxFor(0)
	before := strings.Join(m.paneLines(0, box.width, box.rows), "\n")
	m.Update(tea.ColorProfileMsg{Profile: 5})
	m.View()
	after := strings.Join(m.paneLines(0, box.width, box.rows), "\n")
	if before != after || !strings.Contains(after, "\x1b[2;31m") {
		t.Fatal("UI theme changed harness content")
	}
}

func TestStyledNetworkKeepsUnknownAndSanitizesReportedText(t *testing.T) {
	m := themedFixture(t)
	m.networkMode = true
	m.network.view.Local.Sessions[0].Name = "claude\x1b]52;c;payload\a\u202eevil"
	m.network.view.Local.Sessions[0].State = "new-state"
	m.network.view.Snapshot.Sessions[0].Reported.State = protocol.SessionStateNeedsYou
	m.Update(tea.ColorProfileMsg{Profile: 5})
	text := m.View().Content
	if strings.Contains(text, "\x1b]") || strings.Contains(text, "\u202e") {
		t.Fatal("reported controls escaped into styled output")
	}
	plain := ansi.Strip(text)
	if !strings.Contains(plain, "unknown to this build") || !strings.Contains(plain, "needs you") {
		t.Fatal(plain)
	}
	for _, line := range strings.Split(text, "\n") {
		if ansi.StringWidth(line) != m.width {
			t.Fatal("styled row overflow")
		}
	}
}

func TestPlainLocalRenderingDoesNotChangeOnProfile(t *testing.T) {
	m, _, _, _ := newModel(t)
	before := m.View().Content
	m.Update(tea.ColorProfileMsg{Profile: 5})
	if m.View().Content != before {
		t.Fatal("daemon theme changed plain rtk")
	}
}

func TestBlockTabsAndSpanBackgroundCoverage(t *testing.T) {
	m := themedFixture(t)
	m.Update(tea.ColorProfileMsg{Profile: 5})
	for _, active := range []bool{false, true} {
		m.networkMode = active
		for _, width := range []int{5, 6, 19, 20, 40, 100} {
			styled, plain, hits := m.tabLayout(width)
			if ansi.Strip(styled) != plain {
				t.Fatal("tab styling changed labels")
			}
			for _, hit := range hits {
				if hit.start < 0 || hit.end > width || hit.end <= hit.start {
					t.Fatal(hit)
				}
			}
			if width >= 27 && !strings.Contains(plain, " Terminals ") {
				t.Fatal(plain)
			}
			if width < 27 && !strings.Contains(plain, " T ") {
				t.Fatal(plain)
			}
		}
	}
	row := m.renderSpans([]uiSpan{{text: "x", fg: colText}}, 10, colSelBG)
	padding := m.theme.style(strings.Repeat(" ", 9), colText, colSelBG, false)
	if !strings.HasSuffix(row, padding) || ansi.StringWidth(row) != 10 {
		t.Fatal("selected padding unstyled")
	}
	bar := m.daemonTitleRow(80)
	if ansi.StringWidth(bar) != 80 || !strings.HasSuffix(bar, "\x1b[0m\x1b]8;;\x1b\\") {
		t.Fatal("bar did not reset its style")
	}
}

func TestColourOffActiveTabKeepsAttributesWithoutColours(t *testing.T) {
	m := themedFixture(t)
	m.theme.noColor = true
	m.Update(tea.ColorProfileMsg{Profile: 2})
	for _, label := range []string{">Terminals ", ">N "} {
		active := m.tabSegment(label, true, true)
		if ansi.Strip(active) != label || ansi.StringWidth(active) != ansi.StringWidth(label) {
			t.Fatal("attributes changed cells")
		}
		if !strings.Contains(active, "\x1b[1;7m") && !strings.Contains(active, "\x1b[7;1m") {
			t.Fatal("active tab lacks bold/reverse", active)
		}
		if strings.Contains(active, "38;") || strings.Contains(active, "48;") {
			t.Fatal("colour in attribute-only path")
		}
		if inactive := m.tabSegment(label, false, true); inactive != label {
			t.Fatal("inactive tab gained attributes")
		}
	}
	if plain := m.theme.style("other UI", colOK, colTermBG, true); plain != "other UI" {
		t.Fatal("theme.style colour-off behavior changed")
	}
}

func TestRoundTwoTabTextPaddingAndProfileStableHits(t *testing.T) {
	m := themedFixture(t)
	for _, network := range []bool{false, true} {
		m.networkMode = network
		for _, width := range []int{5, 6, 19, 20, 40} {
			m.width = width
			for _, coloured := range []bool{false, true} {
				m.theme.noColor = !coloured
				m.Update(tea.ColorProfileMsg{Profile: 5})
				styled, plain, hits := m.tabLayout(width)
				want := " Terminals  Network  Graph "
				if width < 27 {
					want = " T  N  G "
				}
				if !coloured {
					if width < 27 {
						if network {
							want = " T >N  G "
						} else {
							want = ">T  N  G "
						}
					} else {
						if network {
							want = " Terminals >Network  Graph "
						} else {
							want = ">Terminals  Network  Graph "
						}
					}
				}
				if plain != want || ansi.Strip(styled) != want {
					t.Fatal(width, network, coloured, plain)
				}
				boundary, third, end := 11, 20, 27
				if width < 27 {
					boundary, third, end = 3, 6, 9
				}
				for x := 0; x <= width; x++ {
					kind, hit := m.tabAt(x)
					if x >= end || x >= width {
						if hit {
							t.Fatal("invisible cell clickable", x)
						}
					} else {
						wantKind := tabTerminals
						if x >= boundary {
							wantKind = tabNetwork
						}
						if x >= third {
							wantKind = tabGraph
						}
						if !hit || kind != wantKind {
							t.Fatal("padding/gap changed target", x, hits)
						}
					}
				}
			}
		}
	}
}

func TestMonokaiUnknownAndDownsampledActiveContrast(t *testing.T) {
	if stateTint(protocol.SessionStateUnknown, false) != colUnknown || stateTint("new-state", false) != colUnknown || inputTint(protocol.InputUnknown, false) != colUnknown {
		t.Fatal("unknown not purple")
	}
	if stateTint(protocol.SessionStateUnknown, true) != colWarn {
		t.Fatal("old unknown lost warning")
	}
	m := themedFixture(t)
	for _, msg := range []tea.ColorProfileMsg{{Profile: 3}, {Profile: 4}} {
		m.Update(msg)
		bg := m.theme.convertColour(colOK)
		bar := m.theme.convertColour(colBarBG)
		if bg == bar || bg == m.theme.convertColour(colTermBG) || m.theme.convertColour(colDim) == bar {
			t.Fatal("active or muted merged with bar")
		}
	}
}
