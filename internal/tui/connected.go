package tui

// Connected-mode display: gateway state, result wording and the cached outbound
// records the runtime's screens show.
//
// Snapshot.Runtime is the only thing that marks a connected run. Everything here is
// inert in local mode: the caches stay empty and every label is bypassed.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No label has been rendered and
// no outbound read has been made.

import (
	"fmt"
	"sort"
	"time"

	"charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// maxCachedSends bounds the outbound records cached for the recent rows.
	maxCachedSends = 200

	// outboundSendsLimit is what the F3 outbound source asks for.
	outboundSendsLimit = 200

	// connection labels. The em dash is deliberate and used consistently.
	labelConnecting = "Gateway connecting"
	labelConnected  = "Gateway connected"
	// labelConnectedLANPlainHTTP marks a connected run paired to a LAN gateway with
	// --insecure-http: the traffic on this network is unencrypted. Its 30 ASCII
	// cells fit within the minimum usable width; smaller windows truncate normally.
	labelConnectedLANPlainHTTP = "Gateway connected (plain HTTP)"
	labelReconnecting          = "Gateway reconnecting — sends unavailable"
	labelRefused               = "Gateway refused — retrying"
	labelUnavailable           = "Gateway state unavailable"

	// reportSeparator joins an immediate result and the recipient's later report,
	// so neither replaces the other.
	reportSeparator = " — "
)

// outboundReader is the optional read seam a connected router offers. It is asserted
// from Router, so a local-mode router or a fake needs no method for it.
type outboundReader interface {
	Sends(limit int) ([]protocol.SendView, error)
	SendDetail(id string) (protocol.SendDetail, error)
}

// runtimeMode reports whether this run is connected. Snapshot.Runtime is the only
// marker; layout and the constructor never decide it.
func (m *Model) runtimeMode() bool { return m.snap.Runtime }

// connectionLabel names the gateway state for the title.
//
// A Runtime snapshot with no connection, or one carrying a state this build does
// not know, says the state is unavailable rather than guessing "connected".
func connectionLabel(connection *protocol.ConnectionView) string {
	if connection == nil {
		return labelUnavailable
	}

	switch connection.State {
	case protocol.ConnConnecting:
		return labelConnecting
	case protocol.ConnConnected:
		// The flag always comes from the live view, so a later snapshot without it
		// falls back to the plain label by itself.
		if connection.LANPlainHTTP {
			return labelConnectedLANPlainHTTP
		}
		return labelConnected
	case protocol.ConnReconnecting:
		return labelReconnecting
	case protocol.ConnRefused:
		return labelRefused
	default:
		return labelUnavailable
	}
}

// connectionErrLine is the gateway's own reason, shown while the link is not
// connected. A refused link therefore says why without ever calling it a network
// failure, and no configured address or credential is shown.
func (m *Model) connectionErrLine() string {
	if !m.runtimeMode() || m.conn == nil {
		return ""
	}
	if m.conn.State == protocol.ConnConnected {
		return ""
	}
	return sanitize(m.conn.Err)
}

// sendResultLabel is the one wording for an outbound result. It never implies more
// than the result means: host_stored is saved, not delivered.
func sendResultLabel(result protocol.SendResult) string {
	switch result {
	case protocol.SendSending:
		return "sending"
	case protocol.SendGatewayUnreachable:
		return "gateway unreachable — not sent"
	case protocol.SendGatewayUncertain:
		return "gateway uncertain — may still arrive"
	case protocol.SendUnknownRecipient:
		return "unknown recipient — not delivered"
	case protocol.SendDestinationUnreachable:
		return "destination unreachable — not delivered"
	case protocol.SendSessionGone:
		return "session ended — not delivered"
	case protocol.SendDeliveryUncertain:
		return "delivery uncertain — may still arrive"
	case protocol.SendHostStored:
		return "saved by recipient host — delivery pending"
	}

	if result == "" {
		return "result unavailable"
	}
	// A future result is shown as itself: never inferred as success or failure.
	return sanitize(string(result))
}

// sendReportSuffix renders the recipient's later report beside the immediate
// result. "accepted" says the recipient recorded it, never that a model acted.
func sendReportSuffix(view protocol.SendView) string {
	if view.Report == "" {
		return ""
	}

	suffix := reportSeparator + "recipient reports " + sanitize(string(view.Report))
	if reason := sanitize(view.ReportReason); reason != "" {
		suffix += " (" + reason + ")"
	}
	return suffix
}

// sendLine is one outbound row for the recent list and the F3 outbound source.
func sendLine(view protocol.SendView) string {
	from := sanitize(view.From)
	to := sanitize(view.To)
	if from == "" {
		from = "?"
	}
	if to == "" {
		to = "?"
	}

	line := fmt.Sprintf("→ %s→%s %s", from, to, sendResultLabel(view.Result))
	line += sendReportSuffix(view)
	if preview := sanitize(view.Preview); preview != "" {
		line += ": " + preview
	}
	return line
}

// recentRow is one row of the combined recent list.
type recentRow struct {
	at   time.Time
	kind int // 0 for a received message, 1 for an outbound send
	id   string
	text string
}

// recentRows combines received messages and outbound sends, newest first.
//
// Ties are deterministic: an exact timestamp match puts the received message before
// the outbound send, then orders by id. The two sources are deduplicated separately
// by id and never merged into one record, because a received message and a send can
// share an id without being the same thing here.
func (m *Model) recentRows() []recentRow {
	rows := make([]recentRow, 0, len(m.messages)+len(m.sends))
	for _, message := range m.messages {
		rows = append(rows, recentRow{at: message.At, kind: 0, id: message.ID, text: messageLine(message)})
	}
	for _, view := range m.sends {
		rows = append(rows, recentRow{at: view.At, kind: 1, id: view.ID, text: sendLine(view)})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.After(rows[j].at)
		}
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		return rows[i].id < rows[j].id
	})
	return rows
}

// applyConnection caches a copy of the connection view and its peers, so a later
// frame cannot change what this model already showed.
func (m *Model) applyConnection(connection *protocol.ConnectionView) {
	if connection == nil {
		m.conn = nil
		m.peers = nil
		return
	}

	copied := *connection
	if connection.Peers != nil {
		copied.Peers = append([]string(nil), connection.Peers...)
	}
	m.conn = &copied
	m.peers = append([]string(nil), copied.Peers...)
}

// upsertSend keeps the cached outbound records: an event for a known id replaces it,
// otherwise it is appended, and the newest maxCachedSends are kept.
func (m *Model) upsertSend(view protocol.SendView) {
	for i := range m.sends {
		if m.sends[i].ID == view.ID {
			m.sends[i] = view
			return
		}
	}

	m.sends = append(m.sends, view)
	if len(m.sends) > maxCachedSends {
		m.sends = append([]protocol.SendView(nil), m.sends[len(m.sends)-maxCachedSends:]...)
	}
}

// trimSends copies a snapshot's send list into the cache.
func trimSends(sends []protocol.SendView) []protocol.SendView {
	if len(sends) <= maxCachedSends {
		return append([]protocol.SendView(nil), sends...)
	}
	return append([]protocol.SendView(nil), sends[len(sends)-maxCachedSends:]...)
}

// outboundReaderFor returns the connected read seam, when the router offers it.
func outboundReaderFor(router Router) (outboundReader, bool) {
	reader, ok := router.(outboundReader)
	return reader, ok
}

// outboundSendsMsg is the answer to one Sends read.
type outboundSendsMsg struct {
	sends      []protocol.SendView
	err        error
	generation uint64
}

// outboundDetailMsg is the answer to one SendDetail read.
type outboundDetailMsg struct {
	detail     protocol.SendDetail
	err        error
	generation uint64
	id         string
}

// fetchOutboundSends reads the outbound list from the store in a command.
func (m *Model) fetchOutboundSends() tea.Cmd {
	reader, ok := outboundReaderFor(m.router)
	if !ok {
		return nil
	}

	m.outbound.generation++
	generation := m.outbound.generation
	m.outbound.pending = true
	m.outbound.dirty = false
	m.outbound.last = m.now()
	router := reader

	return func() tea.Msg {
		sends, err := router.Sends(outboundSendsLimit)
		return outboundSendsMsg{sends: sends, err: err, generation: generation}
	}
}

// fetchOutboundDetail reads one send in full.
func (m *Model) fetchOutboundDetail(id string) tea.Cmd {
	reader, ok := outboundReaderFor(m.router)
	if !ok {
		return nil
	}

	m.outbound.generation++
	generation := m.outbound.generation
	m.outbound.detailPending = true
	m.outbound.pending = false
	if m.outbound.detailID != id {
		// A different send: drop the old body now, so a slow read cannot show one
		// send under another's identity.
		m.outbound.detail = protocol.SendDetail{}
		m.outbound.detailLoaded = false
		m.outbound.detailErr = nil
		m.outbound.detailScroll = 0
	}
	m.outbound.detailID = id
	router := reader

	return func() tea.Msg {
		detail, err := router.SendDetail(id)
		return outboundDetailMsg{detail: detail, err: err, generation: generation, id: id}
	}
}

// installOutboundSends applies a list answer, if it is still the current one.
func (m *Model) installOutboundSends(msg outboundSendsMsg) {
	if !m.logMode || !m.outbound.source || msg.generation != m.outbound.generation {
		return
	}

	m.outbound.pending = false
	if msg.err != nil {
		// Keep what is readable and say what failed.
		m.outbound.err = msg.err
		m.feedback = "outbound history unavailable: " + sanitize(msg.err.Error())
		return
	}

	m.outbound.loaded = true
	m.outbound.err = nil
	m.outbound.list = append([]protocol.SendView(nil), msg.sends...)
	m.selectAfterOutboundRefresh()
}

// installOutboundDetail applies a detail answer, if it is still the current one.
func (m *Model) installOutboundDetail(msg outboundDetailMsg) {
	if !m.logMode || !m.outbound.source || msg.generation != m.outbound.generation || msg.id != m.outbound.detailID {
		return
	}

	m.outbound.detailPending = false
	if msg.err != nil {
		// Same id only, so the previous body may stay: it belongs to this send.
		m.outbound.detailErr = msg.err
		m.feedback = "send detail unavailable: " + sanitize(msg.err.Error())
		return
	}

	m.outbound.detail = msg.detail
	m.outbound.detailLoaded = true
	m.outbound.detailErr = nil
	m.outbound.detailScroll = 0
}

// selectAfterOutboundRefresh keeps the selected send, or falls back to the newest.
func (m *Model) selectAfterOutboundRefresh() {
	if len(m.outbound.list) == 0 {
		m.outbound.selected = -1
		m.outbound.selectedID = ""
		return
	}

	if m.outbound.selectedID != "" {
		for i, view := range m.outbound.list {
			if view.ID == m.outbound.selectedID {
				m.outbound.selected = i
				return
			}
		}
	}

	// The store returns the newest last, oldest first.
	m.outbound.selected = len(m.outbound.list) - 1
	m.outbound.selectedID = m.outbound.list[m.outbound.selected].ID
}
