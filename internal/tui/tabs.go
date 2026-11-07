package tui

// The daemon window's tab strip: Terminals and Network, always on the title row.
//
// The user asked for a clickable tab that is always visible, not a dashboard. So the strip shares the
// row the title already used: no new row, no change to chromeRows, screenTop, the header row or any
// pane box, and therefore no PTY resize when a tab is switched. A plain (local) window draws exactly
// what it drew before.
//
// The strip is the only new mouse surface, and it is deliberately narrow: a click on it switches
// tabs, and nothing else about it can become input. A gesture a child already owns is never taken
// away from it, even when the pointer crosses the strip.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No tab has been drawn or clicked.

import (
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// tabKind names one tab.
// tabHit is one tab's rectangle on row 0, in cells: [start, end).
type tabHit struct {
	kind  tabKind
	start int
	end   int
}

type tabKind int

const (
	tabTerminals tabKind = iota
	tabNetwork
)

// tabMarker is the plain-text mark on the active tab.
//
// Styling alone is not enough: a terminal with colours off, a pipe, or a user with a monochrome theme
// would lose the only sign of which tab is showing. It replaces one existing
// padding cell when colour is off; it never adds a cell to the geometry.
const tabMarker = ">"

// tabLabels are the visible labels for one active tab, in the full form.
//
// Round 2 made the tabs Herdr-style blocks: no brackets, one padding cell on each side of the label,
// and the blocks touch (there is no gap to draw or click). The active tab is marked by its block -
// colour when there is colour, and a '>' that REPLACES its first padding cell when there is not, so the
// strip's width never depends on the profile or on which tab is active.
//
// UNTESTED.
func tabLabels(active tabKind, colour bool) (labels [2]string) {
	names := [2]string{"Terminals", "Network"}
	for index, name := range names {
		labels[index] = " " + name + " "
	}
	labels = markActiveTab(labels, active, colour)
	return labels
}

// markActiveTab gives the active block its plain cue when colour is off.
//
// The marker overwrites the block's first padding cell rather than being inserted, which is what keeps
// the cell count - and so every hit box - identical with and without colour.
//
// UNTESTED.
func markActiveTab(labels [2]string, active tabKind, colour bool) [2]string {
	if colour {
		return labels
	}
	block := labels[active]
	if strings.HasPrefix(block, " ") && len(block) > 1 {
		labels[active] = tabMarker + block[1:]
	}
	return labels
}

// compactLabels are the narrow form: the same blocks, one letter each.
//
// UNTESTED.
func compactLabels(active tabKind, colour bool) (labels [2]string) {
	names := [2]string{"T", "N"}
	for index, name := range names {
		labels[index] = " " + name + " "
	}
	labels = markActiveTab(labels, active, colour)
	return labels
}

// tabGap is the plain space between the two blocks.
//
// Round 2 set it to zero: the blocks touch, as Herdr's do. The constant stays so the geometry is still
// written in one place.
const tabGap = 0

// tabStripWidth is the width of one form of the strip.
func tabStripWidth(labels [2]string) int {
	return ansi.StringWidth(labels[0]) + tabGap + ansi.StringWidth(labels[1])
}

// tabLayout is the one description of the strip: what to draw, and where it is.
//
// Geometry comes from the plain labels and never from the theme, so a hit box is identical with and
// without colour; only the styling of the segments changes. Everything is measured on the plain text,
// so a hit box can never drift from the cells that are drawn.
//
// UNTESTED.
func (m *Model) tabLayout(width int) (styled string, plain string, hits []tabHit) {
	if width < 1 {
		return "", "", nil
	}

	// The full labels are used whenever they fit; below that the compact form, so the abbreviation
	// appears exactly where the full strip stops fitting.
	// The drawn labels carry the plain marker when colour is off, so geometry is measured on exactly
	// what is drawn and never on stripped text.
	colour := m.theme.colours
	labels := tabLabels(m.activeTab(), colour)
	if width < tabStripWidth(labels) {
		labels = compactLabels(m.activeTab(), colour)
	}

	var styledBuilder, plainBuilder strings.Builder
	for index, label := range labels {
		start := ansi.StringWidth(plainBuilder.String())
		plainBuilder.WriteString(label)
		end := ansi.StringWidth(plainBuilder.String())

		// A tab clipped by the terminal keeps only the cells that are actually drawn.
		hitStart, hitEnd := start, end
		if hitStart >= width {
			hitStart, hitEnd = width, width
		} else if hitEnd > width {
			hitEnd = width
		}
		if hitEnd > hitStart {
			hits = append(hits, tabHit{kind: tabKind(index), start: hitStart, end: hitEnd})
		}

		styledBuilder.WriteString(m.tabSegment(label, tabKind(index) == m.activeTab(), start < width))
		if index < len(labels)-1 && tabGap > 0 {
			// The gap is the bar, never a tab's surface. With tabGap 0 there is nothing to draw.
			styledBuilder.WriteString(m.theme.style(strings.Repeat(" ", tabGap), colTabIdle, colBarBG, false))
			plainBuilder.WriteString(strings.Repeat(" ", tabGap))
		}
	}

	return styledBuilder.String(), plainBuilder.String(), hits
}

// tabSegment styles one whole tab block.
//
// Colour on: the active block is bold dark-on-green and inactive blocks are their own colour on the
// bar; there is no reverse and no marker, because the block itself says which tab is showing. Colour
// off: the active block keeps bold and reverse with NO colour setters, and its plain marker (already in
// the label) is the second cue. Either way the block is one style run, so no unstyled blank follows a
// reset.
//
// UNTESTED.
func (m *Model) tabSegment(label string, active bool, visible bool) string {
	if !visible {
		return m.theme.style(label, colTabIdle, colBarBG, false)
	}

	if !active {
		return m.theme.style(label, colTabIdle, colBarBG, false)
	}

	if m.theme.colours {
		// Bold text colour on the green block: the two roles are named explicitly, and colTabSel is
		// left alone because it also supplies the focused header's foreground.
		return m.theme.style(label, colTermBG, colOK, true)
	}
	return lipgloss.NewStyle().Bold(true).Reverse(true).Render(label)
}

// daemonTitleRow draws row 0 of a daemon window: the strip, then as much title as fits.
//
// The title takes remaining space. Below six columns even compact tabs clip;
// their hit boxes are clipped to exactly the cells that remain visible.
//
// UNTESTED.
func (m *Model) daemonTitleRow(width int) string {
	styled, plain, _ := m.tabLayout(width)

	remaining := width - ansi.StringWidth(plain)
	row := styled
	if remaining > 0 {
		// One space of the bar, then the title cleaned and cut BEFORE it is styled, so a reported
		// string can never carry an escape of its own into the row.
		// One bar cell, so the title never touches the last block.
		row += m.theme.style(" ", colTabIdle, colBarBG, false)
		if remaining > 1 {
			title := fitPlain(m.titleLine(), remaining-1)
			row += m.theme.style(title, colDim, colBarBG, false)
		}
	}
	return fitStyled(row, width)
}

// activeTab is the tab the window is showing.
//
// The log is a subview of Terminals, so it belongs to that tab: opening F3 does not move the strip.
func (m *Model) activeTab() tabKind {
	if m.networkMode {
		return tabNetwork
	}
	return tabTerminals
}

// tabStrip is the plain text of the strip, for tests and for a caller that only wants the words.
func (m *Model) tabStrip(width int) string {
	_, plain, _ := m.tabLayout(width)
	return plain
}

// tabAt reports which tab a cell on row 0 belongs to.
func (m *Model) tabAt(x int) (tabKind, bool) {
	_, _, hits := m.tabLayout(m.width)
	for _, hit := range hits {
		if x >= hit.start && x < hit.end {
			return hit.kind, true
		}
	}
	return 0, false
}

// selectTab switches tabs.
//
// Network opens through the existing path, which leaves the log itself; Terminals leaves both the log
// and the Network view. Selecting the tab already active does nothing.
func (m *Model) selectTab(kind tabKind) tea.Cmd {
	switch kind {
	case tabNetwork:
		if m.networkMode {
			return nil
		}
		return m.openNetwork()
	default:
		if m.logMode {
			m.leaveHistory()
		}
		if m.networkMode {
			m.closeNetwork()
		}
		return nil
	}
}

// tabGesture is a strip press waiting for its release.
//
// It is deliberately separate from mouseState.focusConsumed: a press that only moved focus keeps its
// own release-swallowing behaviour, and a strip press keeps its own, so the two can never be
// mistaken for each other.
type tabGesture struct {
	pending bool
	button  uv.MouseButton
}

// tabMouse handles the mouse over the strip.
//
// It reports handled=false for everything it does not own, which is the important half: a gesture a
// child already owns goes through the existing drag and release paths unchanged, even when the pointer
// is over the strip, and the Network view's mouse consumption is untouched for everything but the
// strip itself.
//
// UNTESTED.
func (m *Model) tabMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	if msg == nil || m.daemon == nil {
		return nil, false
	}

	// A child owns a gesture: the strip never takes one away, so its motion and release keep going
	// to that pane even over row 0.
	if m.mouse.owner >= 0 {
		return nil, false
	}
	if m.mouse.focusConsumed {
		return nil, false
	}

	event := msg.Mouse()

	switch typed := msg.(type) {
	case tea.MouseClickMsg:
		if event.Y != 0 {
			break
		}
		kind, onTab := m.tabAt(event.X)
		if !onTab {
			break
		}
		if typed.Button != uv.MouseLeft {
			// A non-left press on the strip is consumed and changes nothing.
			return nil, true
		}
		if m.quitArmed {
			// The quit warning owns the screen; the press is swallowed so the warning stays.
			return nil, true
		}

		m.tabGesture = tabGesture{pending: true, button: typed.Button}
		return m.selectTab(kind), true

	case tea.MouseReleaseMsg:
		if !m.tabGesture.pending {
			break
		}
		// The strip press's own release is swallowed wherever it lands, and only for the same button:
		// an unrelated release must not be eaten.
		if typed.Button != uv.MouseNone && typed.Button != m.tabGesture.button {
			break
		}
		m.tabGesture = tabGesture{}
		return nil, true

	case tea.MouseMotionMsg:
		if event.Y == 0 {
			// Nothing is dragged from the strip; a stray motion over it is consumed so it cannot start
			// a gesture somewhere else.
			return nil, true
		}

	case tea.MouseWheelMsg:
		if _, onTab := m.tabAt(event.X); onTab && event.Y == 0 {
			// A wheel turn over the strip scrolls nothing.
			return nil, true
		}
	}

	return nil, false
}
