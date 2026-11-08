package tui

// UNTESTED. Graph is reported metadata only; no skill text creates an edge.
import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"image/color"
	"strings"
)

func graphMarkTint(card protocol.GraphCard) color.Color {
	if card.Restored {
		return colWarn
	}
	switch card.State {
	case protocol.CardFresh:
		return colOK
	case protocol.CardStale:
		return colWarn
	case protocol.CardRejected:
		return colBad
	case protocol.CardOffline, protocol.CardNone:
		return colDim
	}
	return colUnknown
}
func (m *Model) graphLines(width, rows int) []string {
	if rows < 1 {
		return nil
	}
	if m.graph.urlPanel {
		return m.graphURLLines(width, rows)
	}
	if m.graph.editor.open {
		return m.cardEditorLines(width, rows)
	}
	if m.graph.edgeView {
		return m.graphTextViewport(m.graphEdgeLines(), width, rows)
	}
	geo := m.graphGeometry()
	if m.graph.fullDetail {
		return m.graphDetail(width, rows)
	}
	if !geo.wide {
		return m.graphList(width, rows)
	}
	left := make([]string, rows)
	for i := range left {
		left[i] = m.uiLine("", geo.left, colText, colTermBG, false)
	}
	localBG := colTermBG
	if m.graph.selected == graphLocalKey {
		localBG = colSelBG
	}
	left[0] = m.uiLine("Local card / edit (e)   poll source: gateway-poll", geo.left, colAccent, localBG, true)
	for gridRow := 0; gridRow < 4; gridRow++ {
		for line := 0; line < 3; line++ {
			y := 1 + gridRow*3 + line
			if y >= rows {
				continue
			}
			var b strings.Builder
			backbone := " "
			if m.graph.polls {
				backbone = "|"
			}
			b.WriteString(m.uiLine(backbone, 1, colLine, colTermBG, false))
			for col := 0; col < 3; col++ {
				slot := gridRow*3 + col
				name := ""
				if slot < len(m.graph.order) {
					name = m.graph.order[slot]
				}
				h, ok := m.graphHost(name)
				_, visible := geo.nodes[name]
				text := ""
				fg := color.Color(colDim)
				bg := colTermBG
				if ok && visible {
					parts := graphNodeText(h, m.graphAge(h.Card.AgeMS), m.theme.glyphs())
					text = parts[line]
					fg = graphMarkTint(h.Card)
					if line == 0 {
						fg = colAccent
					}
					if name == m.graph.selected {
						bg = colSelBG
					}
				}
				b.WriteString(m.uiLine(text, 18, fg, bg, line == 0))
				if col < 2 {
					spoke := "  "
					if m.graph.polls && ok && visible && line == 1 && h.Card.Source == protocol.GraphSourcePoll && h.Card.LastAttemptAt != "" {
						spoke = "<-"
					}
					b.WriteString(m.uiLine(spoke, 2, colLine, colTermBG, false))
				}
			}
			left[y] = b.String() + m.uiLine("", 1, colText, colTermBG, false)
		}
	}
	extra := []string{m.graphControls(), "Card marks = poll only; connection separate"}
	if m.graph.messages {
		edges := m.graphEdgeLines()
		extra = append(extra, "messages_seen: decided / host_stored (gateway-ledger)")
		extra = append(extra, edges[len(graphLegendLines()):]...)
	} else {
		extra = append(extra, "messages_seen layer hidden")
	}
	for i, line := range extra {
		y := 13 + i
		if y >= rows {
			break
		}
		left[y] = m.uiLine(line, geo.left, colDim, colTermBG, false)
	}
	right := m.graphDetail(geo.detail.width, rows)
	out := make([]string, rows)
	for i := range out {
		out[i] = left[i] + m.uiLine("|", 1, colLine, colTermBG, false) + right[i]
	}
	return out
}
func (m *Model) graphList(width, rows int) []string {
	names := m.graphNames()
	selected := 0
	for i, name := range names {
		if name == m.graph.selected {
			selected = i
		}
	}
	visible := max(1, rows-3)
	top := graphListTop(selected, m.graph.listTop, len(names), visible)
	out := []string{}
	for i := 0; i < visible && len(out) < rows; i++ {
		index := top + i
		text := ""
		fg := color.Color(colText)
		bg := colTermBG
		if index < len(names) {
			name := names[index]
			text = graphKeyName(name)
			if name != graphLocalKey {
				h, _ := m.graphHost(name)
				text += "  " + graphCardMark(h.Card, m.theme.glyphs()) + "  " + m.graphAge(h.Card.AgeMS)
				fg = graphMarkTint(h.Card)
			}
			if name == m.graph.selected {
				bg = colSelBG
				text = "> " + text
			} else {
				text = "  " + text
			}
		}
		out = append(out, m.uiLine(text, width, fg, bg, index == selected))
	}
	entryHint := "Enter: formatted card; edit from Local card row"
	if m.localGraphSelected() {
		entryHint = "Enter: formatted card; e: local editor"
	}
	for _, line := range []string{m.graphControls(), entryHint, "card=poll only; connection separate; edges=gateway-decided sends"} {
		if len(out) < rows {
			out = append(out, m.uiLine(line, width, colDim, colTermBG, false))
		}
	}
	for len(out) < rows {
		out = append(out, m.uiLine("", width, colText, colTermBG, false))
	}
	return out
}
func (m *Model) graphDetail(width, rows int) []string {
	card, poll, local := m.selectedCard()
	lines := []string{graphKeyName(m.graph.selected), "Card | Skills | Security | Capabilities | Raw JSON"}
	if h, ok := m.graphHost(m.graph.selected); ok {
		live := "not connected"
		if h.Connection.Live {
			live = "connected"
		}
		lines = append(lines, graphField("connection", live+"; "+h.Connection.Source+"; heartbeat age "+m.graphAge(h.Connection.HeartbeatAgeMS)))
		lines = append(lines, graphField("card", graphCardMark(h.Card, m.theme.glyphs())+"; source "+h.Card.Source+"; age "+m.graphAge(h.Card.AgeMS)))
		if h.Card.CardOmitted {
			lines = append(lines, "card body omitted by gateway size limit")
		}
	}
	if m.localGraphSelected() {
		lines = append(lines, "e: edit local card (local daemon)")
		if local != nil {
			lines = append(lines, cardListenerLines(*local)...)
		}
	} else {
		lines = append(lines, "read-only here; edit on that host")
	}
	lines = append(lines, cardSectionLines(card, poll, local, m.graph.section, width)...)
	return m.graphTextViewport(lines, width, rows)
}
func (m *Model) graphTextViewport(lines []string, width, rows int) []string {
	return m.graphTextAt(lines, width, rows, m.graph.scroll)
}
func (m *Model) graphTextAt(lines []string, width, rows, scroll int) []string {
	if rows <= 0 {
		return nil
	}
	wrapped := []string{}
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(cleanUISpan(line), max(1, width), true), "\n")...)
	}
	top := min(max(0, scroll), max(0, len(wrapped)-rows))
	out := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		line := ""
		if top+i < len(wrapped) {
			line = wrapped[top+i]
		}
		fg := color.Color(colText)
		if top+i < 2 {
			fg = colAccent
		}
		out = append(out, m.uiLine(line, width, fg, colTermBG, false))
	}
	return out
}
func (m *Model) graphControls() string {
	filter := []string{"all", "fresh", "stale", "offline", "no card", "rejected"}[m.graph.filter]
	return fmt.Sprintf("[poll %t] [messages %t] [filter %s] [edge list] (Tab focus, arrows/Enter)", m.graph.polls, m.graph.messages, filter)
}
func (m *Model) graphEdgeLines() []string {
	lines := append([]string{}, graphLegendLines()...)
	if m.graph.view.Snapshot == nil {
		return append(lines, "No graph snapshot")
	}
	if m.graph.view.Snapshot.EdgesPartial {
		lines = append(lines, "EDGE COUNTS PARTIAL: ledger cap reached")
	}
	for _, edge := range m.graph.view.Snapshot.Edges {
		if edge.Kind != protocol.GraphEdgeMessagesSeen {
			continue
		}
		if !m.graph.edgeView && m.graph.selected != graphLocalKey && edge.From != m.graph.selected && edge.To != m.graph.selected {
			continue
		}
		age := "age unavailable"
		if at, ok := protocol.ParseTime(edge.LastAt); ok {
			elapsed := m.now().Sub(at)
			if elapsed < 0 {
				elapsed = 0
			}
			age = shortAge(elapsed)
		}
		lines = append(lines, fmt.Sprintf("%s -> %s: count %d / host_stored %d; %dh; source %s; last %s", cleanUISpan(edge.From), cleanUISpan(edge.To), edge.Count, edge.HostStored, edge.WindowHours, cleanUISpan(edge.Source), age))
	}
	return lines
}
func (m *Model) graphURLLines(width, rows int) []string {
	lines := []string{"Card URL - native text selection enabled", "Esc: return; Enter: request terminal clipboard (support varies)", "Wrapped for display; no clipboard success is assumed.", m.graph.url}
	return m.graphTextViewport(lines, width, rows)
}
func (m *Model) graphMouse(msg tea.MouseMsg) tea.Cmd {
	if m.graph.urlPanel {
		return nil
	}
	point := msg.Mouse()
	if _, ok := msg.(tea.MouseWheelMsg); ok {
		delta := 1
		if point.Button == tea.MouseWheelUp {
			delta = -1
		}
		m.graph.scroll = max(0, m.graph.scroll+delta)
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || point.Button != tea.MouseLeft {
		return nil
	}
	if m.graph.editor.open {
		return nil
	}
	geo := m.graphGeometry()
	if geo.wide {
		if point.Y == screenTop() && point.X < geo.left {
			m.graph.selected = graphLocalKey
			return nil
		}
		for name, rect := range geo.nodes {
			if rect.contains(point.X, point.Y) {
				m.graph.selected = name
				m.graph.focus = 0
				m.graph.scroll = 0
				return nil
			}
		}
		if geo.detail.contains(point.X, point.Y) {
			m.graph.focus = 1
		}
		return nil
	}
	if !m.graph.fullDetail && !m.graph.edgeView {
		names := m.graphNames()
		selected := 0
		for i, name := range names {
			if name == m.graph.selected {
				selected = i
			}
		}
		top := graphListTop(selected, m.graph.listTop, len(names), max(1, m.screenRows()-3))
		index := top + point.Y - screenTop()
		if point.Y < screenTop() || point.Y >= screenTop()+max(1, m.screenRows()-3) {
			return nil
		}
		if index >= 0 && index < len(names) {
			m.graph.selected = names[index]
			m.graph.fullDetail = true
			m.graph.focus = 1
			m.graph.scroll = 0
		}
	}
	return nil
}

func graphListTop(selected, old, count, visible int) int {
	top := old
	if selected < top {
		top = selected
	}
	if selected >= top+visible {
		top = selected - visible + 1
	}
	return max(0, min(top, max(0, count-visible)))
}
