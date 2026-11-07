package tui

// Drawing the Network view: the sanitizer, the row model, and the lines.
//
// Everything drawn here is cleaned display input: a host name, a session name, a build string or a
// reason is untrusted text, and a bidi override or a zero-width mark in any of them must not be able
// to reorder the screen. The raw identities stay in the row keys, which are never drawn.
//
// UNTESTED: see network.go.

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Row kinds.
const (
	networkRowLocal   = "local"
	networkRowRemote  = "remote"
	networkRowHost    = "host"
	networkRowGateway = "gateway"
)

// rowKey is a row's stable identity across answers.
//
// A local row is identified by its session id when it has one and its local id otherwise; a remote
// row by the gateway's session id on its host; a host row by the host alone.
type rowKey struct {
	kind string
	host string
	id   string
}

// networkRow is one line of the view, with its identity kept separate from its text.
type networkRow struct {
	key        rowKey
	selectable bool
	text       string
	detail     string
	old        bool
	spans      []uiSpan
}

// networkRows builds the rows in the contract's order: local first, then a gateway summary, then
// every paired host and its sessions.
//
// UNTESTED.
func (m *Model) networkRows() []networkRow {
	rows := make([]networkRow, 0, 8)
	cache := &m.network
	if !cache.have {
		text := "waiting for the first answer"
		if cache.errText != "" {
			text = cleanNetworkText(cache.errText)
		}
		return []networkRow{{text: text}}
	}
	rows = append(rows, networkRow{text: "Local daemon  " + notAvailable(cleanNetworkText(cache.view.Local.Host)) + "  build " + notAvailable(cleanNetworkText(cache.view.Local.Daemon.Build))})

	// Local sessions, or the one honest sentence that says there are none yet.
	if len(cache.view.Local.Sessions) == 0 {
		rows = append(rows, networkRow{
			key:        rowKey{kind: networkRowLocal, id: "empty"},
			selectable: false,
			text:       "local  no local sessions",
		})
	}

	for _, session := range cache.view.Local.Sessions {
		key := rowKey{kind: networkRowLocal, host: cache.view.Local.Host, id: session.SessionID}
		if key.id == "" {
			key.id = session.LocalID
		}
		name := cleanNetworkText(session.Name)
		if name == "" {
			name = cleanNetworkText(session.LocalID)
		}
		name = notAvailable(name)
		state := networkStateCell(m, session.State)
		input := networkInputText(session.Input)

		rows = append(rows, networkRow{
			key:        key,
			selectable: true,
			old:        cache.oldBody,
			text:       fmt.Sprintf("local  %s  %s  %s", name, state, input),
			spans:      []uiSpan{{text: "local  ", fg: colDim}, {text: name, fg: m.harnessTint(session.Harness, session.Name)}, {text: "  " + state, fg: stateTint(session.State, cache.oldBody)}, {text: "  " + input, fg: inputTint(session.Input, cache.oldBody)}},
			detail: fmt.Sprintf("local session %s on %s; observed by this daemon at %s",
				name, notAvailable(cleanNetworkText(cache.view.Local.Host)), notAvailable(cleanNetworkText(session.StateAt))),
		})
	}

	// The gateway summary, one row, so the state and the fetched age are always visible.
	gateway := "gateway  " + networkGatewayStateText(cache.view.Gateway.State)
	if cache.view.Gateway.Error != "" {
		gateway += ": " + cleanNetworkText(cache.view.Gateway.Error)
	}
	gateway += "  snapshot " + networkFetchedText(m, cache)
	rows = append(rows, networkRow{
		key:        rowKey{kind: networkRowGateway},
		selectable: false,
		text:       gateway,
		old:        cache.oldBody || cache.view.Gateway.State != protocol.GatewayConnected,
	})

	snapshot := cache.view.Snapshot
	if snapshot == nil {
		rows = append(rows, networkRow{
			key:        rowKey{kind: networkRowGateway, id: "empty"},
			selectable: false,
			text:       "no snapshot yet; directory peers follow",
		})
		for _, peer := range m.directoryPeers() {
			rows = append(rows, networkRow{
				key:        rowKey{kind: networkRowRemote, id: peer},
				selectable: false,
				text:       "peer  " + cleanNetworkText(peer),
				detail:     "known only from the gateway directory; no report and no liveness",
			})
		}
		return rows
	}
	if snapshot.Schema != protocol.NetworkSchema {
		return append(rows, networkRow{text: "remote data unavailable: snapshot schema unsupported by this build"})
	}

	for _, host := range snapshot.Hosts {
		hostName := cleanNetworkText(host.Host)
		status := notAvailable(cleanNetworkText(host.ReportStatus))
		switch host.ReportStatus {
		case protocol.ReportStatusNone:
			status = "no report"
		case protocol.ReportStatusStale:
			status = "report stale"
		}
		live := "not live"
		if host.Connection.Live {
			live = "live"
		}
		rows = append(rows, networkRow{
			key:        rowKey{kind: networkRowHost, host: host.Host},
			selectable: true,
			text:       fmt.Sprintf("%s  %s  %s  %s", notAvailable(hostName), live, status, networkReportAge(m, host)),
			spans:      m.hostSpans(host, notAvailable(hostName), live, status),
			detail:     networkHostDetail(host),
			old:        networkHostOld(m, host),
		})
	}

	for _, session := range snapshot.Sessions {
		if m.localShadowed(session) {
			// This host's own session already appears in the local section: the snapshot row is the
			// same session seen from the gateway, so drawing both would duplicate it. Any other
			// disagreement between the sections is kept, because it is information.
			continue
		}
		key := rowKey{kind: networkRowRemote, host: session.Host, id: session.SessionID}
		address := cleanNetworkText(session.Address)
		if address == "" {
			address = notAvailable("")
		}
		state := ""
		input := ""
		age := ""
		old := false
		if session.Reported != nil {
			state = networkStateCell(m, session.Reported.State)
			input = networkInputText(session.Reported.Input)
			age = networkRowAge(m, session.Reported)
			old = session.Reported.Stale
		}
		registered := "unregistered"
		if session.Directory.Registered {
			registered = "registered"
		}

		rows = append(rows, networkRow{
			key:        key,
			selectable: true,
			text:       fmt.Sprintf("%s  %s  %s  %s  %s", address, reportedStateCell(session.Reported, state), reportedCell(session.Reported, input), registered, reportedCell(session.Reported, age)),
			spans:      m.sessionSpans(session, address, reportedStateCell(session.Reported, state), reportedCell(session.Reported, input), registered, reportedCell(session.Reported, age)),
			detail:     networkSessionDetail(m, session),
			old:        networkSessionOld(m, session) || old,
		})
	}

	return rows
}

// directoryPeers is the cached peer list as bare addresses, which is all it is.
func (m *Model) directoryPeers() []string {
	if m.snap.Connection == nil {
		return nil
	}
	return append([]string(nil), m.snap.Connection.Peers...)
}

// networkStateLines is the short form of the Network view: the header and the local or waiting state,
// for a window too small to show the rows.
//
// It says only what is known, in the same words the full view uses.
//
// UNTESTED.
func (m *Model) networkStateLines(width int) []string {
	lines := []string{m.networkHeaderLine()}

	cache := &m.network
	if !cache.have {
		lines = append(lines, "waiting for the first answer")
		return lines
	}
	if len(cache.view.Local.Sessions) == 0 {
		lines = append(lines, "no local sessions")
		return lines
	}
	for _, session := range cache.view.Local.Sessions {
		lines = append(lines, fmt.Sprintf("%s  %s  %s",
			notAvailable(cleanNetworkText(session.Name)),
			networkStateText(session.State),
			networkInputText(session.Input)))
	}
	return lines
}

// networkHeaderLine is the one line above the rows: what this is, whose answer it is, and how old.
func (m *Model) networkHeaderLine() string {
	cache := &m.network
	if !cache.have {
		if cache.errText != "" {
			return "Network  " + cleanNetworkText(cache.errText)
		}
		return "Network  waiting for the first answer"
	}
	header := "Network  " + networkGatewayStateText(cache.view.Gateway.State)
	if cache.view.Gateway.Error != "" {
		header += ": " + cleanNetworkText(cache.view.Gateway.Error)
	}
	header += "  " + networkFetchedText(m, cache)
	if cache.oldBody || cache.view.Gateway.State != protocol.GatewayConnected {
		header += "  (old)"
	}
	if cache.errText != "" {
		header = "Network  " + cleanNetworkText(cache.errText) + "  previous data old"
	}
	return header
}

// networkLines renders the body: the visible rows, then the selected row's detail.
func (m *Model) networkLines(width, rows int) []string {
	all := m.networkRows()
	if len(all) == 0 {
		return []string{fitPlain("no network data yet", width)}
	}

	visible := rows
	if visible < 1 {
		visible = 1
	}
	detailRows := 1
	listRows := visible - detailRows
	if listRows < 1 {
		listRows = 1
	}

	top := m.network.scrollTop
	index := m.selectedNetworkIndex()
	if index < top {
		top = index
	}
	if index >= top+listRows {
		top = index - listRows + 1
	}
	if top > len(all)-1 {
		top = len(all) - 1
	}
	if top < 0 {
		top = 0
	}

	lines := make([]string, 0, visible)
	for i := 0; i < listRows; i++ {
		index := top + i
		if index >= len(all) {
			lines = append(lines, m.uiLine("", width, colText, colTermBG, false))
			continue
		}
		row := all[index]
		lines = append(lines, m.renderNetworkRow(row, width))
	}

	detail := ""
	if index := m.selectedNetworkIndex(); index >= 0 && index < len(all) && all[index].detail != "" {
		detail = all[index].detail
	}
	lines = append(lines, m.uiLine(detail, width, colText, colDetailBG, false))

	for len(lines) < rows {
		lines = append(lines, fitPlain("", width))
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return lines
}

// renderNetworkRow draws one row, styled when colour is on.
//
// The plain text is built first, exactly as before, and the styling only wraps it: a selected row gets
// its surface and focus marker, an ordinary row the mock's dark surface, a heading is muted and an old
// row is drawn in the warning colour with its "old" word kept. Colours never carry a signal on their
// own - the words and the marker are always there.
//
// UNTESTED.
func (m *Model) renderNetworkRow(row networkRow, width int) string {
	selected := row.selectable && row.key == m.network.selection
	bg := colTermBG
	marker := "  "
	if selected {
		bg = colSelBG
		marker = "> "
	}
	spans := []uiSpan{{text: marker, fg: colFocus, bold: selected}}
	if row.old {
		spans = append(spans, uiSpan{text: "old  ", fg: colWarn})
	}
	if len(row.spans) > 0 {
		spans = append(spans, row.spans...)
	} else {
		fg := stateTint("", row.old)
		if row.key.kind == networkRowHost {
			fg = colAccent
		}
		if row.key.kind == networkRowGateway && row.key.id == "" {
			if m.network.view.Gateway.State == protocol.GatewayConnected {
				fg = colOK
			} else {
				fg = colWarn
				if m.network.view.Gateway.State == protocol.GatewayUnreachable {
					fg = colBad
				}
			}
		}
		spans = append(spans, uiSpan{text: row.text, fg: fg})
	}
	return m.renderSpans(spans, width, bg)
}

func (m *Model) hostSpans(host protocol.NetworkHost, name, live, status string) []uiSpan {
	old := networkHostOld(m, host)
	fg := colOK
	if old {
		fg = colWarn
	}
	if host.Report == nil {
		status = "- no report"
	}
	return []uiSpan{{text: name, fg: colAccent, bold: true}, {text: "  " + live, fg: fg}, {text: "  " + status + "  " + networkReportAge(m, host), fg: stateTint("", old)}}
}

func (m *Model) sessionSpans(session protocol.NetworkSession, address, state, input, registered, age string) []uiSpan {
	old := networkSessionOld(m, session)
	harness, rawState, rawInput := "", "", ""
	if session.Reported != nil {
		harness = session.Reported.Harness
		rawState = session.Reported.State
		rawInput = session.Reported.Input
	}
	return []uiSpan{{text: address, fg: m.harnessTint(harness, session.Name)}, {text: "  " + state, fg: stateTint(rawState, old)}, {text: "  " + input, fg: inputTint(rawInput, old)}, {text: "  " + registered + "  " + age, fg: stateTint("", old)}}
}

// networkFetchedText says how old the snapshot is, from the answer's own fields.
func networkFetchedText(m *Model, cache *networkCache) string {
	if !cache.have || cache.view.FetchedAt == "" {
		return "age not available"
	}
	fetched, ok := protocol.ParseTime(cache.view.FetchedAt)
	if !ok {
		return "age not available"
	}
	elapsed := m.now().Sub(fetched)
	if elapsed < 0 {
		elapsed = 0
	}
	return "fetched " + shortAge(elapsed)
}

// networkReportAge renders a host report's age: the gateway's age plus the time since we received it.
func networkReportAge(m *Model, host protocol.NetworkHost) string {
	if host.Report == nil {
		return ""
	}
	return ageWithElapsed(m, host.Report.AgeMS)
}

// networkRowAge renders one reported session's age the same way.
func networkRowAge(m *Model, reported *protocol.ReportedSession) string {
	if reported == nil {
		return ""
	}
	return ageWithElapsed(m, reported.AgeMS)
}

// ageWithElapsed adds this window's elapsed time to the gateway's age.
//
// The gateway's age is the truth about when it received the report; the elapsed part is only how
// long ago this window received that answer. A missing or unreadable FetchedAt means no age at all,
// never zero.
func ageWithElapsed(m *Model, ageMS int64) string {
	total, ok := networkAge(m, ageMS)
	if !ok {
		return "age not available"
	}
	return shortAge(total)
}

func networkAge(m *Model, ageMS int64) (time.Duration, bool) {
	const maxAge = time.Duration(1<<63 - 1)
	if ageMS < 0 || ageMS > int64(maxAge/time.Millisecond) {
		return 0, false
	}
	fetched, ok := protocol.ParseTime(m.network.view.FetchedAt)
	if !ok {
		return 0, false
	}
	elapsed := max(time.Duration(0), m.now().Sub(fetched))
	base := time.Duration(ageMS) * time.Millisecond
	if elapsed > maxAge-base {
		return 0, false
	}
	return base + elapsed, true
}

func networkSnapshotOld(m *Model) bool {
	_, valid := protocol.ParseTime(m.network.view.FetchedAt)
	return m.network.oldBody || !valid || m.network.view.Gateway.State != protocol.GatewayConnected || m.network.view.Snapshot == nil || m.network.view.Snapshot.Gateway.ReportStaleAfterMS <= 0
}

func networkAgeOld(m *Model, ageMS int64) bool {
	if networkSnapshotOld(m) {
		return true
	}
	age, ok := networkAge(m, ageMS)
	return !ok || age.Milliseconds() > m.network.view.Snapshot.Gateway.ReportStaleAfterMS
}

func networkHostOld(m *Model, host protocol.NetworkHost) bool {
	return networkSnapshotOld(m) || !host.Connection.Live || host.ReportStatus != protocol.ReportStatusFresh || host.Report == nil || networkAgeOld(m, host.Report.AgeMS)
}

func networkSessionOld(m *Model, session protocol.NetworkSession) bool {
	if session.Reported == nil || session.Reported.Stale || networkAgeOld(m, session.Reported.AgeMS) {
		return true
	}
	if session.Directory.Live != nil && !*session.Directory.Live {
		return true
	}
	for _, host := range m.network.view.Snapshot.Hosts {
		if host.Host == session.Host {
			return networkHostOld(m, host)
		}
	}
	return true // no host facts, never infer current readiness
}

// shortAge renders an age for a narrow column.
func shortAge(age time.Duration) string {
	switch {
	case age < time.Second:
		return "0s"
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours())/24)
	}
}

// networkStateCell renders a state as symbol and word.
//
// The word is always there, so colour and glyph are never the only signal; the symbol comes from the
// theme's glyph policy, and the ASCII form is one cell so a row's width does not move when glyphs turn
// off.
//
// UNTESTED.
func networkStateCell(m *Model, state string) string {
	word := networkStateText(state)
	if state == protocol.SessionStateNeedsYou {
		word = "needs you"
	}
	return stateSymbol(m, state) + " " + word
}

// stateSymbol is the marker for one state, or "" for a value this build does not know.
func stateSymbol(m *Model, state string) string {
	key := ""
	switch state {
	case protocol.SessionStateIdle:
		key = "idle"
	case protocol.SessionStateBusy:
		key = "busy"
	case protocol.SessionStateNeedsYou:
		key = "needs"
	case protocol.SessionStateUnknown:
		key = "unknown"
	case protocol.SessionStateExited:
		key = "exited"
	case protocol.SessionStateStarting:
		key = "starting"
	}
	if key == "" {
		return "?"
	}
	if m.theme.glyphs() {
		return stateGlyphsUTF8[key]
	}
	return stateGlyphsASCII[key]
}

// networkStateText renders one state value, and says so when it is not one this build knows.
func networkStateText(state string) string {
	switch state {
	case protocol.SessionStateStarting, protocol.SessionStateIdle, protocol.SessionStateBusy,
		protocol.SessionStateNeedsYou, protocol.SessionStateUnknown, protocol.SessionStateExited:
		return state
	case "":
		return "not available"
	}
	return cleanNetworkText(state) + " (unknown to this build)"
}

// networkInputText renders one input value the same way.
func networkInputText(input string) string {
	switch input {
	case protocol.InputHeld, protocol.InputFree, protocol.InputUnknown:
		return input
	case "":
		return "not available"
	}
	return cleanNetworkText(input) + " (unknown to this build)"
}

// notAvailable is an empty cell's text, which the Vocabulary allows only where nothing is known.
func notAvailable(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not available"
	}
	return value
}

// networkHostDetail is the selected host row's explanation.
func networkHostDetail(host protocol.NetworkHost) string {
	source := "gateway report"
	if host.ReportStatus == protocol.ReportStatusNone {
		source = "no report from this host"
	}
	return fmt.Sprintf("%s; source %s; gateway clock as of the snapshot", notAvailable(cleanNetworkText(host.Host)), source)
}

// networkSessionDetail is the selected remote session's explanation.
func networkSessionDetail(m *Model, session protocol.NetworkSession) string {
	parts := []string{}
	if networkSessionOld(m, session) {
		parts = append(parts, "old report; not current readiness")
	}
	if session.Reported != nil {
		parts = append(parts, networkRowAge(m, session.Reported))
	}
	parts = append(parts, notAvailable(cleanNetworkText(session.Address)))
	if session.Directory.Registered {
		parts = append(parts, "in the gateway directory")
	} else {
		parts = append(parts, "reported by the host, not in the directory")
	}
	if session.Directory.Live != nil {
		if *session.Directory.Live {
			parts = append(parts, "gateway-reported live as of the snapshot")
		} else {
			parts = append(parts, "gateway-reported not live as of the snapshot")
		}
	}
	if session.Reported != nil {
		if session.Reported.Stale {
			parts = append(parts, "report stale, "+networkRowAge(m, session.Reported))
		} else {
			parts = append(parts, "report "+networkRowAge(m, session.Reported))
		}
		if session.Reported.Harness != "" {
			parts = append(parts, "harness "+cleanNetworkText(session.Reported.Harness))
		}
		if session.Reported.HarnessVersion != "" {
			parts = append(parts, "version "+cleanNetworkText(session.Reported.HarnessVersion))
		}
	} else {
		parts = append(parts, "no report for this session")
	}
	return strings.Join(parts, "; ")
}

// cleanNetworkText removes everything that could rewrite or reorder a terminal line.
//
// It strips ANSI escape sequences, C0 and C1 controls, format characters (which include the bidi
// overrides), zero-width marks, variation selectors and the line and paragraph separators. What
// remains is plain text.
//
// UNTESTED.
func cleanNetworkText(text string) string {
	text = ansi.Strip(text)
	var builder strings.Builder
	builder.Grow(len(text))

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		if r == 0x2028 || r == 0x2029 {
			continue
		}
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		if r == 0x034f || (r >= 0xfe00 && r <= 0xfe0f) || (r >= 0xe0100 && r <= 0xe01ef) {
			continue
		}

		builder.WriteRune(r)
	}

	return strings.TrimSpace(builder.String())
}

func networkGatewayStateText(state string) string {
	switch state {
	case protocol.GatewayConnected, protocol.GatewayUnreachable, protocol.GatewayNotConfigured, protocol.GatewayUnsupported:
		return state
	case "":
		return "not available"
	default:
		return notAvailable(cleanNetworkText(state)) + " (unknown to this build)"
	}
}

// localShadowed reports whether a snapshot row is the same session as one in the local section.
//
// The rule is deliberately narrow: the row must be on this machine's own host AND its non-empty
// session id must match a local session's. Anything else - a different host, a different id, an empty
// id - is kept, because the two sections disagreeing is information, not noise.
//
// UNTESTED.
func (m *Model) localShadowed(session protocol.NetworkSession) bool {
	if session.Host == "" || session.Host != m.network.view.Local.Host {
		return false
	}
	if session.SessionID == "" {
		return false
	}
	for _, local := range m.network.view.Local.Sessions {
		if local.SessionID != "" && local.SessionID == session.SessionID {
			return true
		}
	}
	return false
}

// reportedStateCell is the state cell of a report row: "no report" when there is none.
func reportedStateCell(reported *protocol.ReportedSession, state string) string {
	if reported == nil {
		return "- no report"
	}
	return notAvailable(state)
}

// reportedCell is one cell of a report row.
//
// A session with no report says "no report" once, in the state cell; the other cells stay blank rather
// than repeating a phrase three times.
//
// UNTESTED.
func reportedCell(reported *protocol.ReportedSession, value string) string {
	if reported == nil {
		return ""
	}
	return notAvailable(value)
}
