package tui

// F3 run history and full message detail. Every read here is requested through a
// tea.Cmd and carries the identity of what it asked for, so a slow answer can
// never land under the wrong run, the wrong message or a closed log.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No body has ever been
// rendered and no read has ever returned.

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// runsListLimit is how many past runs the log asks for and lists.
	runsListLimit = 200 // UNTESTED guess

	// runHeaderRows is the most rows the run header may take, so a narrow
	// terminal still shows message rows at minimum height.
	runHeaderRows = 2

	// detailTabWidth is how a tab is expanded in a message body.
	detailTabWidth = 4
)

// historyState is everything F3 needs that the model did not already hold.
//
// The message list itself stays in the model's log/logLoaded/logErr fields: one
// list, one owner. This struct adds the run selection, the list selection and
// the detail view, and nothing here duplicates that list.
type historyState struct {
	runs        []protocol.RunView
	runIndex    int // 0 is the newest run; -1 means "current run, metadata unknown"
	runLabel    string
	runsPending bool

	selected   int    // index into the model's log; -1 means nothing selected
	selectedID string // the message the user was looking at, across refreshes

	detailPending bool
	detailLoaded  bool
	detailErr     error
	detail        protocol.MessageDetail
	detailRunID   string
	detailID      string
	detailScroll  int
}

// Messages the history view sends itself. Each carries the identity of its
// request so that a stale answer can be recognised and dropped.
type (
	runsMsg struct {
		runs       []protocol.RunView
		err        error
		generation uint64
		keepRunID  string
	}
	detailMsg struct {
		detail     protocol.MessageDetail
		err        error
		generation uint64
		runID      string
		id         string
	}
)

// currentRunID is the run the log is showing, empty meaning "the current run"
// when the metadata is unknown.
func (m *Model) currentRunID() string {
	if m.history.runIndex >= 0 && m.history.runIndex < len(m.history.runs) {
		return m.history.runs[m.history.runIndex].ID
	}
	return ""
}

// runLabel names the run in the header, without trusting anything the store did
// not give us.
func (m *Model) runLabel() string {
	if m.history.runIndex >= 0 && m.history.runIndex < len(m.history.runs) {
		return m.history.runs[m.history.runIndex].ID
	}
	if m.history.runLabel != "" {
		return m.history.runLabel
	}
	return "current run"
}

// viewingCurrentRun reports whether the log is showing the run that is still
// being written. Live refresh is only allowed then.
func (m *Model) viewingCurrentRun() bool {
	return m.history.runIndex <= 0
}

// enterHistory opens F3: clear any previous view, then load the run list.
func (m *Model) enterHistory() tea.Cmd {
	m.mouse = mouseState{owner: -1}
	m.historyGeneration++
	m.logMode = true
	m.logLoaded = false
	m.logErr = nil
	m.log = nil
	m.historyPending = false
	m.history = historyState{runIndex: -1, selected: -1}
	return m.fetchRuns("")
}

// leaveHistory closes F3 and drops everything the view held.
func (m *Model) leaveHistory() {
	m.historyGeneration++
	m.logMode = false
	m.logLoaded = false
	m.logErr = nil
	m.log = nil
	m.historyPending = false
	m.history = historyState{selected: -1}
}

// fetchRuns asks for the run list, remembering which run the user wants to keep.
func (m *Model) fetchRuns(keepRunID string) tea.Cmd {
	router := m.router
	if router == nil {
		return nil
	}
	m.history.runsPending = true
	m.historyPending = false // any previous-generation history was superseded
	generation := m.historyGeneration
	return func() tea.Msg {
		runs, err := router.Runs(runsListLimit)
		return runsMsg{runs: runs, err: err, generation: generation, keepRunID: keepRunID}
	}
}

// fetchHistoryFor asks for one run's messages. An empty runID means the current
// run, which is also the fallback when the run list is unavailable.
func (m *Model) fetchHistoryFor(runID string) tea.Cmd {
	router := m.router
	if router == nil {
		return nil
	}
	query := protocol.HistoryQuery{RunID: runID, Limit: historyQueryLimit}
	m.historyPending = true
	m.historyDirty = false
	m.historyLast = time.Now()
	generation := m.historyGeneration
	return func() tea.Msg {
		messages, err := router.History(query)
		return historyMsg{messages: trimMessages(messages), err: err, generation: generation, runID: runID}
	}
}

// fetchDetail asks for one message in full.
func (m *Model) fetchDetail(id string) tea.Cmd {
	router := m.router
	if router == nil {
		return nil
	}
	m.historyGeneration++
	m.historyPending = false
	m.history.runsPending = false
	generation := m.historyGeneration
	runID := m.currentRunID()
	m.history.detailPending = true
	if m.history.detailID != id {
		// A different message: drop the old one now, so a slow read can never
		// show the previous message under the new identity.
		m.history.detail = protocol.MessageDetail{}
		m.history.detailLoaded = false
		m.history.detailErr = nil
		m.history.detailScroll = 0
	}
	m.history.detailID = id
	m.history.detailRunID = runID
	return func() tea.Msg {
		detail, err := router.MessageDetail(id)
		return detailMsg{detail: detail, err: err, generation: generation, runID: runID, id: id}
	}
}

// installRuns replaces the run list after a fetch, keeping the run the user was
// on when it is still there.
func (m *Model) installRuns(msg runsMsg) tea.Cmd {
	m.history.runsPending = false
	if msg.err != nil {
		m.feedback = "run list unavailable: " + sanitize(msg.err.Error())
		if len(m.history.runs) > 0 {
			// Keep what is on screen: a failed refresh must not erase history the
			// user can still read.
			return nil
		}
		m.history.runs = nil
		m.history.runIndex = -1
		m.history.runLabel = "current run (metadata unavailable)"
		return m.fetchHistoryFor("")
	}

	// Copy: the store's slice is not ours to keep or mutate.
	m.history.runs = append([]protocol.RunView(nil), msg.runs...)
	m.history.runLabel = ""

	if len(m.history.runs) == 0 {
		// No runs to list yet: fall back to the current run rather than showing
		// an empty screen, and say the metadata is unknown.
		m.history.runIndex = -1
		m.history.runLabel = "current run (metadata unavailable)"
		return m.fetchHistoryFor("")
	}

	m.history.runIndex = indexOfRun(m.history.runs, msg.keepRunID)
	if m.history.runIndex < 0 {
		m.history.runIndex = 0
		if msg.keepRunID != "" {
			m.feedback = "that run is no longer listed; showing the newest"
		}
	}
	return m.fetchHistoryFor(m.currentRunID())
}

// installHistory replaces the message list for the selected run.
func (m *Model) installHistory(msg historyMsg) {
	m.historyPending = false

	if msg.err != nil {
		// Keep the last readable list and report the problem on the feedback row.
		m.feedback = "history error: " + sanitize(msg.err.Error())
		if !m.logLoaded {
			m.logErr = msg.err
		}
		return
	}

	m.logLoaded = true
	m.logErr = nil
	m.log = append([]protocol.MessageView(nil), msg.messages...)
	m.history.detail = protocol.MessageDetail{}
	m.history.detailLoaded = false
	m.history.detailErr = nil
	m.history.detailID = ""
	m.history.detailScroll = 0
	m.selectAfterRefresh()
}

// selectAfterRefresh keeps the message the user was reading, or falls back to the
// newest one and says so.
func (m *Model) selectAfterRefresh() {
	if len(m.log) == 0 {
		m.history.selected = -1
		m.history.selectedID = ""
		return
	}

	if m.history.selectedID != "" {
		for i, message := range m.log {
			if message.ID == m.history.selectedID {
				m.history.selected = i
				return
			}
		}
		m.feedback = "the selected message is no longer in this run; selected the newest"
	}

	// Chronological order, newest last.
	m.history.selected = len(m.log) - 1
	m.history.selectedID = m.log[m.history.selected].ID
}

// installDetail replaces the detail view, guarding against an answer for a
// different message or run.
func (m *Model) installDetail(msg detailMsg) {
	m.history.detailPending = false

	if msg.err != nil {
		m.history.detailErr = msg.err
		m.feedback = "message detail unavailable: " + sanitize(msg.err.Error())
		// Only a failure for this very message keeps what is on screen; the
		// identity guard above already ensures it is the same message.
		return
	}

	m.history.detail = msg.detail
	m.history.detailLoaded = true
	m.history.detailErr = nil
	m.history.detailScroll = 0
	m.history.detailID = msg.id
	m.history.detailRunID = msg.runID
}

// moveRun switches to an older (delta +1) or newer (delta -1) run.
func (m *Model) moveRun(delta int) tea.Cmd {
	if len(m.history.runs) == 0 {
		m.feedback = "run list unavailable; only the current run is readable"
		return nil
	}

	next := m.history.runIndex + delta
	switch {
	case next >= len(m.history.runs):
		m.feedback = "already at the oldest listed run"
		if len(m.history.runs) >= runsListLimit {
			m.feedback = "oldest of the newest 200 runs; older runs are not listed"
		}
		return nil
	case next < 0:
		m.feedback = "already at the newest run"
		return nil
	}

	m.historyGeneration++
	m.history.runsPending = false
	m.history.runIndex = next
	m.history.detail = protocol.MessageDetail{}
	m.history.detailLoaded = false
	m.history.detailErr = nil
	m.history.detailID = ""
	m.history.detailScroll = 0
	m.history.selected = -1
	m.history.selectedID = ""
	m.log = nil
	m.logLoaded = false
	m.logErr = nil
	m.historyPending = false
	m.feedback = ""
	return m.fetchHistoryFor(m.currentRunID())
}

// moveSelection moves the list selection and keeps it inside the list.
func (m *Model) moveSelection(delta int) {
	if len(m.log) == 0 {
		m.feedback = "no messages in this run"
		return
	}

	next := m.history.selected
	if next < 0 {
		next = len(m.log) - 1
	}
	next += delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.log) {
		next = len(m.log) - 1
	}

	m.history.selected = next
	m.history.selectedID = m.log[next].ID
}

// selectedMessage is the message the marker is on.
func (m *Model) selectedMessage() (protocol.MessageView, bool) {
	if m.history.selected < 0 || m.history.selected >= len(m.log) {
		return protocol.MessageView{}, false
	}
	return m.log[m.history.selected], true
}

// openDetail reads the selected message in full.
func (m *Model) openDetail() tea.Cmd {
	message, ok := m.selectedMessage()
	if !ok {
		m.feedback = "no message selected"
		return nil
	}
	m.feedback = ""
	m.history.detailErr = nil
	return m.fetchDetail(message.ID)
}

// reloadDetail re-reads the message the detail view is showing.
func (m *Model) reloadDetail() tea.Cmd {
	if m.history.detailID == "" {
		return nil
	}
	return m.fetchDetail(m.history.detailID)
}

// closeDetail returns from the detail view to the list.
func (m *Model) closeDetail() {
	m.historyGeneration++
	m.historyPending = false
	m.historyDirty = true
	m.history.runsPending = false
	m.history.detail = protocol.MessageDetail{}
	m.history.detailLoaded = false
	m.history.detailErr = nil
	m.history.detailID = ""
	m.history.detailScroll = 0
	m.feedback = ""
}

// rerunList refreshes the run list and the selected run's messages.
func (m *Model) rerunList() tea.Cmd {
	m.feedback = ""
	// A refresh supersedes whatever is in flight, so an older answer cannot land
	// after the newer one.
	m.historyGeneration++
	return m.fetchRuns(m.currentRunID())
}

// historyKey handles input while F3 is open. The log consumes everything: no
// keystroke from here can reach a child.
func (m *Model) historyKey(k tea.KeyPressMsg) tea.Cmd {
	if m.history.detailID != "" {
		return m.detailKey(k)
	}

	switch {
	case isKey(k, tea.KeyEscape), isKey(k, tea.KeyF3):
		m.leaveHistory()
	case isKey(k, tea.KeyF1):
		m.leaveHistory()
		m.focus = 0
	case isKey(k, tea.KeyF2):
		m.leaveHistory()
		m.focus = 1
	case isKey(k, tea.KeyLeft):
		return m.moveRun(1) // older
	case isKey(k, tea.KeyRight):
		return m.moveRun(-1) // newer
	case isKey(k, tea.KeyUp):
		m.moveSelection(-1)
	case isKey(k, tea.KeyDown):
		m.moveSelection(1)
	case isKey(k, tea.KeyPgUp):
		m.moveSelection(-m.listPage())
	case isKey(k, tea.KeyPgDown):
		m.moveSelection(m.listPage())
	case isKey(k, tea.KeyEnter):
		return m.openDetail()
	case isRefreshKey(k):
		return m.rerunList()
	}
	return nil
}

// detailKey handles input in the detail view. Navigation keys never reach a
// message body: there is no send, no paste and no retry anywhere here.
func (m *Model) detailKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case isKey(k, tea.KeyEscape):
		m.closeDetail()
	case isKey(k, tea.KeyF3):
		m.leaveHistory()
	case isKey(k, tea.KeyF1):
		m.leaveHistory()
		m.focus = 0
	case isKey(k, tea.KeyF2):
		m.leaveHistory()
		m.focus = 1
	case isKey(k, tea.KeyLeft):
		return m.moveRun(1)
	case isKey(k, tea.KeyRight):
		return m.moveRun(-1)
	case isKey(k, tea.KeyUp):
		m.scrollDetail(-1)
	case isKey(k, tea.KeyDown):
		m.scrollDetail(1)
	case isKey(k, tea.KeyPgUp):
		m.scrollDetail(-m.detailPage())
	case isKey(k, tea.KeyPgDown):
		m.scrollDetail(m.detailPage())
	case isKey(k, tea.KeyHome):
		m.history.detailScroll = 0
	case isKey(k, tea.KeyEnd):
		m.history.detailScroll = m.detailContentHeight()
	case isRefreshKey(k):
		return m.reloadDetail()
	}
	return nil
}

// listPage is the number of message rows a page key moves by.
func (m *Model) listPage() int {
	page := m.screenRows() - m.runHeaderHeight(m.width)
	if page < 1 {
		page = 1
	}
	return page
}

// detailPage is the number of detail rows a page key moves by.
func (m *Model) detailPage() int {
	if m.tooSmall() {
		return max(1, m.height-3)
	}
	return max(1, m.screenRows())
}

// scrollDetail moves the detail window, clamped so the last line is reachable
// and the first is the floor.
func (m *Model) scrollDetail(delta int) {
	maximum := m.detailContentHeight() - m.detailPage()
	if maximum < 0 {
		maximum = 0
	}
	next := m.history.detailScroll + delta
	if next < 0 {
		next = 0
	}
	if next > maximum {
		next = maximum
	}
	m.history.detailScroll = next
}

// detailContentHeight is how many lines the detail body has at the current width.
func (m *Model) detailContentHeight() int {
	return len(m.detailLines(m.width))
}

// runHeaderHeight is how many rows the run header uses, at most runHeaderRows and
// never all of them.
func (m *Model) runHeaderHeight(width int) int {
	lines := m.runHeaderLines(width)
	if len(lines) > runHeaderRows {
		return runHeaderRows
	}
	return len(lines)
}

// runHeaderLines describes the selected run, hard-wrapped so a narrow terminal
// cannot lose the facts that matter. The full metadata is also in the detail
// view, so capping this at runHeaderRows loses nothing permanently.
func (m *Model) runHeaderLines(width int) []string {
	position := fmt.Sprintf("msg %d/%d", m.history.selected+1, len(m.log))
	if m.history.runIndex < 0 || m.history.runIndex >= len(m.history.runs) {
		return []string{"current run (metadata unavailable)", position}
	}
	run := m.history.runs[m.history.runIndex]
	first := fmt.Sprintf("%d/%d %s %s %s", m.history.runIndex+1, len(m.history.runs), ansi.Truncate(sanitize(run.ID), 8, "…"), run.StartedAt.Local().Format("2006-01-02 15:04"), position)
	end := "running"
	if run.EndedAt != nil {
		end = "ended"
		if run.EndReason != "" {
			end = sanitize(run.EndReason)
		}
	}
	second := fmt.Sprintf("end: %s; interrupted %d", ansi.Truncate(end, max(4, width-24), "…"), run.Interrupted)
	return []string{first, second}
}

// historyLines renders the whole screen area F3 owns: the run header and then
// either the message list or the full detail.
func (m *Model) historyLines(width, rows int) []string {
	if rows < 1 {
		return nil
	}

	if m.history.detailID != "" {
		return m.detailScreen(width, rows)
	}
	return m.listScreen(width, rows)
}

// listScreen renders the run header and the message list, keeping the selection
// inside the visible window.
func (m *Model) listScreen(width, rows int) []string {
	lines := make([]string, 0, rows)

	header := m.runHeaderLines(width)
	if len(header) > runHeaderRows {
		header = header[:runHeaderRows]
	}
	for _, line := range header {
		lines = append(lines, fitPlain(line, width))
	}

	listRows := rows - len(header)
	if listRows < 1 {
		return lines
	}

	switch {
	case m.logErr != nil:
		lines = append(lines, fitPlain("history error: "+sanitize(m.logErr.Error()), width))
	case !m.logLoaded:
		lines = append(lines, fitPlain("loading history…", width))
	case len(m.log) == 0:
		lines = append(lines, fitPlain("no messages in this run", width))
	default:
		start := m.listWindowStart(listRows)
		end := start + listRows
		if end > len(m.log) {
			end = len(m.log)
		}
		for i := start; i < end; i++ {
			marker := "  "
			if i == m.history.selected {
				marker = "> "
			}
			lines = append(lines, fitPlain(marker+logLine(m.log[i]), width))
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

// listWindowStart returns the first visible message index: the selection is
// always inside the window.
func (m *Model) listWindowStart(listRows int) int {
	total := len(m.log)
	if total <= listRows {
		return 0
	}

	selected := m.history.selected
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

// detailScreen renders the full message: run metadata, the message's fields, its
// whole body with its own newlines, and its state history.
func (m *Model) detailScreen(width, rows int) []string {
	content := m.detailLines(width)

	maximum := len(content) - rows
	if maximum < 0 {
		maximum = 0
	}
	start := max(0, min(m.history.detailScroll, maximum))
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

// detailLines builds every line of the detail view, before scrolling.
func (m *Model) detailLines(width int) []string {
	lines := make([]string, 0, 32)

	// Full run metadata, so nothing here is permanently truncated by the header
	// cap: identity, when it started, when and why it ended, how many messages it
	// lost.
	run := m.runLabel()
	lines = append(lines, splitWrapped("run "+run, width)...)
	if m.history.runIndex >= 0 && m.history.runIndex < len(m.history.runs) {
		selected := m.history.runs[m.history.runIndex]
		lines = append(lines, splitWrapped("start "+selected.StartedAt.Local().Format("2006-01-02 15:04:05"), width)...)
		if selected.EndedAt != nil {
			lines = append(lines, splitWrapped("end "+selected.EndedAt.Local().Format("2006-01-02 15:04:05"), width)...)
		} else {
			lines = append(lines, "end running")
		}
		if reason := sanitize(selected.EndReason); reason != "" {
			lines = append(lines, splitWrapped("end reason "+reason, width)...)
		}
		lines = append(lines, fmt.Sprintf("interrupted %d", selected.Interrupted))
	} else {
		lines = append(lines, "start unknown", "end unknown", "interrupted unknown")
	}

	if m.history.detailErr != nil {
		lines = append(lines, "")
		lines = append(lines, splitWrapped("detail error: "+sanitize(m.history.detailErr.Error()), width)...)
		lines = append(lines, "Esc back to the list, R to retry")
		if !m.history.detailLoaded {
			return lines
		}
		lines = append(lines, "showing last readable detail")
	}

	if !m.history.detailLoaded {
		lines = append(lines, "", "loading message…")
		return lines
	}

	detail := m.history.detail
	lines = append(lines, "")
	lines = append(lines, splitWrapped("id "+sanitize(detail.ID), width)...)
	lines = append(lines, splitWrapped("from "+sanitize(detail.From)+"  to "+sanitize(detail.To), width)...)
	lines = append(lines, splitWrapped("state "+sanitize(string(detail.State)), width)...)
	if reason := sanitize(detail.Reason); reason != "" {
		lines = append(lines, splitWrapped("reason "+reason, width)...)
	}
	if detail.ReplyTo != "" {
		lines = append(lines, splitWrapped("reply-to "+sanitize(detail.ReplyTo), width)...)
	}
	lines = append(lines, "created "+detail.At.Local().Format("2006-01-02 15:04:05"))

	lines = append(lines, "", "body")
	lines = append(lines, bodyLines(detail.Body, width)...)

	lines = append(lines, "", "state history")
	if len(detail.Events) == 0 {
		lines = append(lines, "  (none recorded)")
	}
	for _, event := range detail.Events {
		from := string(event.From)
		if from == "" {
			from = "(new)"
		}
		entry := fmt.Sprintf("  %s  %s -> %s", event.At.Local().Format("2006-01-02 15:04:05"), from, event.To)
		if reason := sanitize(event.Reason); reason != "" {
			entry += "  " + reason
		}
		lines = append(lines, splitWrapped(entry, width)...)
	}
	return lines
}

// bodyLines turns a stored body into display lines: ANSI and control characters
// removed, tabs expanded, then hard-wrapped by cell width so an unbreakable
// token cannot run past the edge and every line stays reachable.
func bodyLines(body string, width int) []string {
	if width < 1 {
		return nil
	}

	stripped := ansi.Strip(body)
	raw := strings.Split(stripped, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		// Sanitize before measuring/wrapping; control bytes must not consume
		// cells or affect ANSI parser state. Keep tabs for explicit expansion.
		var clean strings.Builder
		for _, r := range line {
			if r == '\t' || (r >= 32 && r != 127 && (r < 128 || r > 159)) {
				clean.WriteRune(r)
			}
		}
		expanded := expandTabs(clean.String(), detailTabWidth)
		if expanded == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, strings.Split(ansi.Hardwrap(expanded, width, true), "\n")...)
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

// expandTabs replaces each tab with enough spaces to reach the next stop.
func expandTabs(line string, tabWidth int) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	if tabWidth < 1 {
		tabWidth = 1
	}

	var b strings.Builder
	column := 0
	parts := strings.Split(line, "\t")
	for i, part := range parts {
		b.WriteString(part)
		column += ansi.StringWidth(part)
		if i < len(parts)-1 {
			spaces := tabWidth - column%tabWidth
			b.WriteString(strings.Repeat(" ", spaces))
			column += spaces
		}
	}
	return b.String()
}

// splitWrapped hard-wraps a metadata line by cell width, so no fact is lost off
// the right edge.
func splitWrapped(text string, width int) []string {
	if width < 1 {
		return []string{""}
	}
	return strings.Split(ansi.Hardwrap(sanitize(text), width, true), "\n")
}

// historyHints is the hint row while F3 is open.
func (m *Model) historyHints() string {
	if m.history.detailID != "" {
		return "↑/↓ scroll  PgUp/PgDn page  Home/End  R reload  ←/→ runs  Esc list  F3 exit  F1/F2 panes  F10 quit"
	}
	return "←/→ runs  ↑/↓ select  Enter detail  R refresh  PgUp/PgDn page  Esc/F3 back  F1/F2 panes  F10 quit"
}

func isRefreshKey(k tea.KeyPressMsg) bool {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock | tea.ModShift)
	return mod == 0 && (k.Code == 'r' || k.Code == 'R')
}

// Reuse the two recent-message rows for help while browsing stored history;
// showing live-run previews below an older run would mix their identities.
func (m *Model) historyHelpLines() []string {
	lines := strings.Split(ansi.Wrap(m.historyHints(), max(1, m.width), " "), "\n")
	for len(lines) < recentMessageRows+1 {
		lines = append(lines, "")
	}
	return lines
}

// indexOfRun finds a run by ID, returning -1 when it is not listed.
func indexOfRun(runs []protocol.RunView, id string) int {
	if id == "" {
		return -1
	}
	for i, run := range runs {
		if run.ID == id {
			return i
		}
	}
	return -1
}
