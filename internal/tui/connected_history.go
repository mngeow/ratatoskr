package tui

// The F3 outbound source: the current run's sends, read from the router's store and
// never from the actor or the gateway.
//
// It is a source inside the existing log view, reached with Tab in a runtime run, so
// the received history and its rules stay exactly as they are.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. Nothing has been read or drawn.

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// outboundState is the outbound source's state. It is separate from historyState so
// the two sources can never share a selection, a detail or a generation.
type outboundState struct {
	// source is true while F3 is showing outbound sends rather than received
	// messages. Opening F3 always starts on the received source.
	source bool

	loaded  bool
	err     error
	list    []protocol.SendView
	pending bool
	dirty   bool
	last    time.Time

	selected   int
	selectedID string

	detail        protocol.SendDetail
	detailLoaded  bool
	detailErr     error
	detailID      string
	detailScroll  int
	detailVisible int
	detailPending bool

	generation uint64
}

const (
	// outboundRowReserve leaves room for the source header.
	outboundHeaderRows = 1
)

// toggleOutbound switches F3 between the received history and outbound sends.
//
// Both sources' in-flight answers are invalidated: a generation bump is the only way
// to guarantee that an answer for the source just left cannot land in the one just
// entered. Local mode has no outbound source, so Tab is consumed and changes nothing
// there - which is what Tab did before this slice.
func (m *Model) toggleOutbound() tea.Cmd {
	if !m.runtimeMode() {
		return nil
	}
	if _, ok := outboundReaderFor(m.router); !ok {
		m.feedback = "outbound history unavailable"
		return nil
	}

	m.outbound.source = !m.outbound.source
	m.historyGeneration++
	m.outbound.generation++
	m.historyPending = false
	m.history.runsPending = false
	m.outbound.pending = false

	// Selections and details belong to one source; none may survive a switch.
	m.history.detail = protocol.MessageDetail{}
	m.history.detailLoaded = false
	m.history.detailErr = nil
	m.history.detailID = ""
	m.history.detailScroll = 0
	m.outbound.detail = protocol.SendDetail{}
	m.outbound.detailLoaded = false
	m.outbound.detailErr = nil
	m.outbound.detailID = ""
	m.outbound.detailScroll = 0
	m.outbound.detailPending = false
	m.feedback = ""

	if m.outbound.source {
		m.outbound.selected = -1
		m.outbound.selectedID = ""
		return m.fetchOutboundSends()
	}
	return m.fetchHistoryFor(m.currentRunID())
}

// outboundKey handles input while the outbound source is open. The log consumes
// everything, so no key reaches a child from here.
func (m *Model) outboundKey(k tea.KeyPressMsg) tea.Cmd {
	if m.outbound.detailID != "" {
		return m.outboundDetailKey(k)
	}

	switch {
	case isKey(k, tea.KeyTab):
		return m.toggleOutbound()
	case isKey(k, tea.KeyEscape), isKey(k, tea.KeyF3):
		m.leaveHistory()
	case isKey(k, tea.KeyF1):
		m.leaveHistory()
		m.focus = 0
	case isKey(k, tea.KeyF2):
		if m.single() {
			return nil
		}
		m.leaveHistory()
		m.focus = 1
	case isKey(k, tea.KeyUp):
		m.moveOutboundSelection(-1)
	case isKey(k, tea.KeyDown):
		m.moveOutboundSelection(1)
	case isKey(k, tea.KeyPgUp):
		m.moveOutboundSelection(-m.outboundPage())
	case isKey(k, tea.KeyPgDown):
		m.moveOutboundSelection(m.outboundPage())
	case isKey(k, tea.KeyHome):
		m.moveOutboundSelection(-len(m.outbound.list))
	case isKey(k, tea.KeyEnd):
		m.moveOutboundSelection(len(m.outbound.list))
	case isKey(k, tea.KeyEnter):
		return m.openOutboundDetail()
	case isRefreshKey(k):
		return m.fetchOutboundSends()
	case isKey(k, tea.KeyLeft), isKey(k, tea.KeyRight):
		// The received source uses these to switch runs; the outbound source is
		// bound to the current run, so they are spent.
	}
	return nil
}

// outboundDetailKey handles input in one send's full view.
func (m *Model) outboundDetailKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case isKey(k, tea.KeyEscape):
		m.closeOutboundDetail()
	case isKey(k, tea.KeyTab):
		return m.toggleOutbound()
	case isKey(k, tea.KeyF3):
		m.leaveHistory()
	case isKey(k, tea.KeyF1):
		m.leaveHistory()
		m.focus = 0
	case isKey(k, tea.KeyF2):
		if m.single() {
			return nil
		}
		m.leaveHistory()
		m.focus = 1
	case isKey(k, tea.KeyUp):
		m.scrollOutboundDetail(-1)
	case isKey(k, tea.KeyDown):
		m.scrollOutboundDetail(1)
	case isKey(k, tea.KeyPgUp):
		m.scrollOutboundDetail(-m.outboundDetailPage())
	case isKey(k, tea.KeyPgDown):
		m.scrollOutboundDetail(m.outboundDetailPage())
	case isKey(k, tea.KeyHome):
		m.outbound.detailScroll = 0
	case isKey(k, tea.KeyEnd):
		m.outbound.detailScroll = m.outboundDetailHeight()
	case isRefreshKey(k):
		if m.outbound.detailID != "" {
			return m.fetchOutboundDetail(m.outbound.detailID)
		}
	}
	return nil
}

// openOutboundDetail reads the selected send in full.
func (m *Model) openOutboundDetail() tea.Cmd {
	if m.outbound.selected < 0 || m.outbound.selected >= len(m.outbound.list) {
		m.feedback = "no send selected"
		return nil
	}
	m.feedback = ""
	m.outbound.detailErr = nil
	return m.fetchOutboundDetail(m.outbound.list[m.outbound.selected].ID)
}

// closeOutboundDetail returns to the outbound list.
func (m *Model) closeOutboundDetail() {
	m.outbound.generation++
	m.outbound.pending = false
	m.outbound.detail = protocol.SendDetail{}
	m.outbound.detailLoaded = false
	m.outbound.detailErr = nil
	m.outbound.detailID = ""
	m.outbound.detailScroll = 0
	m.outbound.detailPending = false
	m.feedback = ""
}

// moveOutboundSelection moves the list selection inside the list.
func (m *Model) moveOutboundSelection(delta int) {
	if len(m.outbound.list) == 0 {
		m.feedback = "no outbound sends in this run"
		return
	}

	next := m.outbound.selected
	if next < 0 {
		next = len(m.outbound.list) - 1
	}
	next += delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.outbound.list) {
		next = len(m.outbound.list) - 1
	}

	m.outbound.selected = next
	m.outbound.selectedID = m.outbound.list[next].ID
}

// outboundPage is how many rows a page key moves by.
func (m *Model) outboundPage() int {
	rows := m.screenRows() - outboundHeaderRows
	if rows < 1 {
		rows = 1
	}
	return rows
}

// scrollOutboundDetail moves the detail window, clamped to its content.
func (m *Model) scrollOutboundDetail(delta int) {
	maximum := m.outboundDetailHeight() - m.outboundDetailPage()
	if maximum < 0 {
		maximum = 0
	}

	next := m.outbound.detailScroll + delta
	if next < 0 {
		next = 0
	}
	if next > maximum {
		next = maximum
	}
	m.outbound.detailScroll = next
}

// outboundDetailHeight is how many lines the detail body has at this width.
func (m *Model) outboundDetailHeight() int {
	return len(m.outboundDetailContent(m.width))
}

// outboundDetailPage is the number of detail rows a page key moves by.
func (m *Model) outboundDetailPage() int {
	if m.tooSmall() {
		return max(1, m.height-3)
	}
	return max(1, m.screenRows())
}

// outboundLines renders the outbound source into the F3 screen area.
func (m *Model) outboundLines(width, rows int) []string {
	if rows < 1 {
		return nil
	}
	if m.outbound.detailID != "" {
		return m.outboundDetailScreen(width, rows)
	}
	return m.outboundListScreen(width, rows)
}

// outboundListScreen renders the header and the list.
func (m *Model) outboundListScreen(width, rows int) []string {
	lines := make([]string, 0, rows)
	lines = append(lines, fitPlain(m.outboundHeaderText(), width))

	listRows := rows - outboundHeaderRows
	if listRows < 1 {
		return lines
	}

	switch {
	case m.outbound.err != nil && !m.outbound.loaded:
		lines = append(lines, fitPlain("outbound history unavailable: "+sanitize(m.outbound.err.Error()), width))
	case !m.outbound.loaded:
		lines = append(lines, fitPlain("loading outbound history…", width))
	case len(m.outbound.list) == 0:
		lines = append(lines, fitPlain("no outbound sends in this run", width))
	default:
		start := m.outboundWindowStart(listRows)
		end := start + listRows
		if end > len(m.outbound.list) {
			end = len(m.outbound.list)
		}
		for i := start; i < end; i++ {
			marker := "  "
			if i == m.outbound.selected {
				marker = "> "
			}
			lines = append(lines, fitPlain(marker+outboundListLine(m.outbound.list[i]), width))
		}
	}

	for len(lines) < rows {
		lines = append(lines, strings.Repeat(" ", width))
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return lines
}

// outboundHeaderText names the source and, at the bound, says so.
func (m *Model) outboundHeaderText() string {
	if len(m.outbound.list) >= outboundSendsLimit {
		return fmt.Sprintf("outbound sends — current run — newest %d sends only", outboundSendsLimit)
	}
	return "outbound sends — current run"
}

// outboundListLine is one list row: time, addresses and the result wording.
func outboundListLine(view protocol.SendView) string {
	return view.At.Local().Format("15:04:05") + " " + sendLine(view)
}

// outboundWindowStart keeps the selection inside the visible window.
func (m *Model) outboundWindowStart(listRows int) int {
	total := len(m.outbound.list)
	if total <= listRows {
		return 0
	}

	selected := m.outbound.selected
	if selected < 0 {
		selected = total - 1
	}

	start := selected - listRows + 1
	if start < 0 {
		start = 0
	}
	if maximum := total - listRows; start > maximum {
		start = maximum
	}
	return start
}

// outboundDetailScreen renders one send in full.
func (m *Model) outboundDetailScreen(width, rows int) []string {
	content := m.outboundDetailContent(width)

	maximum := len(content) - rows
	if maximum < 0 {
		maximum = 0
	}
	start := min(max(0, m.outbound.detailScroll), maximum)
	end := start + rows
	if end > len(content) {
		end = len(content)
	}

	lines := make([]string, 0, rows)
	for _, line := range content[start:end] {
		lines = append(lines, fitPlain(line, width))
	}
	for len(lines) < rows {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines
}

// outboundDetailContent builds every line of one send's detail.
func (m *Model) outboundDetailContent(width int) []string {
	lines := make([]string, 0, 32)

	if m.outbound.detailErr != nil {
		lines = append(lines, splitWrapped("send detail unavailable: "+sanitize(m.outbound.detailErr.Error()), width)...)
		lines = append(lines, "Esc back to the list, R to retry")
		if !m.outbound.detailLoaded {
			return lines
		}
	}
	if !m.outbound.detailLoaded {
		return append(lines, "loading send…")
	}

	detail := m.outbound.detail
	lines = append(lines, splitWrapped("id "+sanitize(detail.ID), width)...)
	lines = append(lines, splitWrapped("from "+sanitize(detail.From)+"  to "+sanitize(detail.To), width)...)
	lines = append(lines, splitWrapped("sent "+detail.At.Local().Format("2006-01-02 15:04:05"), width)...)
	if detail.ReplyTo != "" {
		lines = append(lines, splitWrapped("reply-to "+sanitize(detail.ReplyTo), width)...)
	}

	result := sendResultLabel(detail.Result)
	if suffix := sendReportSuffix(detail.SendView); suffix != "" {
		result += suffix
	}
	lines = append(lines, splitWrapped("result "+result, width)...)

	lines = append(lines, "", "body")
	lines = append(lines, bodyLines(detail.Body, width)...)

	lines = append(lines, "", "events")
	if len(detail.Events) == 0 {
		lines = append(lines, "  (none recorded)")
	}
	for _, event := range detail.Events {
		entry := "  " + event.At.Local().Format("2006-01-02 15:04:05") + "  "
		if event.Reported {
			entry += "recipient reports " + sanitize(string(event.Report))
		} else {
			entry += sendResultLabel(event.Result)
		}
		if reason := sanitize(event.Reason); reason != "" {
			entry += " (" + reason + ")"
		}
		lines = append(lines, splitWrapped(entry, width)...)
	}
	return lines
}

// outboundHints is the hint row for the outbound source.
func (m *Model) outboundHints() string {
	if m.outbound.detailID != "" {
		return "Esc list   Tab received   F3 exit   ↑/↓ scroll   PgUp/PgDn page   Home/End   R reload   F10 quit"
	}
	return "Esc/F3 exit   Tab received   ↑/↓ select   PgUp/PgDn page   Enter detail   R refresh   F10 quit"
}
