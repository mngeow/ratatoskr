package tui

// UNTESTED under GR-3. Graph state never authorizes terminal input or remote writes.
import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"sort"
	"strings"
	"time"
)

const graphLocalKey = "\x00local-card"

type graphState struct {
	view                                     protocol.GraphView
	have, pending, refresh                   bool
	version, generation                      uint64
	last                                     time.Time
	err                                      string
	order                                    []string
	hosts                                    map[string]protocol.GraphHost
	selected                                 string
	focus, section, scroll, listTop, control int
	filter                                   int
	polls, messages                          bool
	fullDetail, edgeView, urlPanel           bool
	url                                      string
	card                                     cardState
}
type graphMsg struct {
	generation uint64
	view       protocol.GraphView
	version    uint64
	err        error
}

func (m *Model) openGraph() tea.Cmd {
	if m.daemon == nil {
		return nil
	}
	if m.logMode {
		m.leaveHistory()
	}
	if m.networkMode {
		m.closeNetwork()
	}
	m.graphMode = true
	m.mouse = mouseState{owner: -1}
	if m.graph.selected == "" {
		m.graph.selected = graphLocalKey
	}
	if !m.graph.have && !m.graph.pending {
		return tea.Batch(m.graphQuery(), m.loadCard())
	}
	return m.loadCard()
}
func (m *Model) closeGraph() { m.graphMode = false; m.graph.urlPanel = false }
func (m *Model) graphQuery() tea.Cmd {
	g := &m.graph
	if m.daemon == nil || g.pending || m.daemon.graph == nil {
		return nil
	}
	r := wire.GraphRequest{}
	if g.have && !g.refresh {
		r.Since = g.version
		r.WaitMS = wire.MaxGraphWaitMS
	} else if !g.have && !g.refresh {
		r.FirstWaitMS = wire.MaxGraphFirstMS
	}
	g.pending = true
	g.refresh = false
	g.generation++
	g.last = m.now()
	generation := g.generation
	ask := m.daemon.graph
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), networkRequestBudget)
		defer cancel()
		v, version, err := ask(ctx, r)
		return graphMsg{generation, v, version, err}
	}
}
func (m *Model) graphTick() tea.Cmd {
	if m.daemon == nil || m.graph.pending || m.now().Sub(m.graph.last) < networkRefreshFloor {
		return nil
	}
	return m.graphQuery()
}
func (m *Model) applyGraph(msg graphMsg) {
	g := &m.graph
	if msg.generation != g.generation {
		return
	}
	g.pending = false
	g.last = m.now()
	if msg.err != nil {
		g.err = msg.err.Error()
		return
	}
	if msg.view.Schema != protocol.GraphSchema || msg.view.Snapshot != nil && msg.view.Snapshot.Schema != protocol.GraphSchema {
		g.err = "graph schema unavailable in this build"
		return
	}
	// Own a deep copy, including cards and evidence maps, never a mutable caller slice.
	data, err := json.Marshal(msg.view)
	if err != nil {
		g.err = err.Error()
		return
	}
	var owned protocol.GraphView
	if err = json.Unmarshal(data, &owned); err != nil {
		g.err = err.Error()
		return
	}
	g.view = owned
	g.hosts = make(map[string]protocol.GraphHost)
	g.version = msg.version
	g.have = true
	g.err = ""
	present := map[string]bool{}
	names := []string{}
	if owned.Snapshot != nil {
		for _, h := range owned.Snapshot.Hosts {
			g.hosts[h.Name] = h
			present[h.Name] = true
			names = append(names, h.Name)
		}
	}
	// Stable slots: existing names never move when other hosts appear/disappear.
	for i, name := range g.order {
		if !present[name] {
			g.order[i] = ""
		}
	}
	sort.Strings(names)
	for _, name := range names {
		found := false
		for _, old := range g.order {
			if old == name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		placed := false
		for i, old := range g.order {
			if old == "" {
				g.order[i] = name
				placed = true
				break
			}
		}
		if !placed {
			g.order = append(g.order, name)
		}
	}
	if g.selected != "" && g.selected != graphLocalKey && !present[g.selected] {
		g.selected = graphLocalKey
		g.scroll = 0
	}
}
func (m *Model) graphHost(name string) (protocol.GraphHost, bool) {
	if m.graph.hosts != nil {
		h, ok := m.graph.hosts[name]
		return h, ok
	}
	if m.graph.view.Snapshot != nil {
		for _, h := range m.graph.view.Snapshot.Hosts {
			if h.Name == name {
				return h, true
			}
		}
	}
	return protocol.GraphHost{}, false
}
func (m *Model) graphNames() []string {
	names := []string{graphLocalKey}
	for _, name := range m.graph.order {
		h, ok := m.graphHost(name)
		if !ok {
			continue
		}
		if m.graph.filter > 0 && h.Card.State != []string{"", protocol.CardFresh, protocol.CardStale, protocol.CardOffline, protocol.CardNone, protocol.CardRejected}[m.graph.filter] {
			continue
		}
		names = append(names, name)
	}
	return names
}
func (m *Model) selectGraph(delta int) {
	names := m.graphNames()
	index := 0
	for i, n := range names {
		if n == m.graph.selected {
			index = i
		}
	}
	index = max(0, min(len(names)-1, index+delta))
	m.graph.selected = names[index]
	m.graph.scroll = 0
}
func (m *Model) localGraphSelected() bool {
	return m.graph.selected == graphLocalKey || m.graph.card.have && m.graph.card.view.Host.Name != "" && m.graph.selected == m.graph.card.view.Host.Name
}
func (m *Model) graphKey(k tea.KeyPressMsg) tea.Cmd {
	g := &m.graph
	if g.urlPanel {
		if isKey(k, tea.KeyEsc) {
			g.urlPanel = false
			return nil
		}
		if isKey(k, tea.KeyEnter) && g.url != "" {
			m.feedback = "clipboard request sent; support unconfirmed; URL remains selectable"
			return tea.SetClipboard(g.url)
		}
		return nil
	}
	if g.card.open {
		return m.cardGenerationKey(k)
	}
	if isKey(k, tea.KeyEsc) {
		if g.fullDetail || g.edgeView {
			g.fullDetail = false
			g.edgeView = false
			return nil
		}
		m.closeGraph()
		return nil
	}
	if isKey(k, tea.KeyTab) {
		g.focus = (g.focus + 1) % 3
		return nil
	}
	if isKey(k, tea.KeyF3) {
		m.closeGraph()
		return m.enterHistory()
	}
	if isKey(k, tea.KeyEnter) {
		if g.focus == 2 {
			m.graphControl()
			return nil
		}
		g.fullDetail = true
		g.focus = 1
		return nil
	}
	if isKey(k, tea.KeyUp) || isKey(k, tea.KeyDown) {
		delta := 1
		if isKey(k, tea.KeyUp) {
			delta = -1
		}
		if g.focus == 0 {
			m.selectGraph(delta)
		} else if g.focus == 1 {
			g.scroll = max(0, g.scroll+delta)
		} else {
			g.control = (g.control + delta + 4) % 4
		}
		return nil
	}
	if isKey(k, tea.KeyLeft) || isKey(k, tea.KeyRight) {
		delta := 1
		if isKey(k, tea.KeyLeft) {
			delta = -1
		}
		if g.focus == 1 {
			g.section = (g.section + delta + 5) % 5
			g.scroll = 0
		} else if g.focus == 0 {
			m.selectGraph(delta)
		} else {
			m.graphControl()
		}
		return nil
	}
	if isKey(k, tea.KeyPgDown) {
		g.scroll += max(1, m.screenRows()-3)
		return nil
	}
	if isKey(k, tea.KeyPgUp) {
		g.scroll = max(0, g.scroll-max(1, m.screenRows()-3))
		return nil
	}
	if k.Mod&^(tea.ModCapsLock|tea.ModNumLock|tea.ModScrollLock|tea.ModShift) == 0 {
		switch strings.ToLower(string(k.Code)) {
		case "r":
			g.refresh = true
			return tea.Batch(m.graphQuery(), m.cardOperation(wire.CardOpReload))
		case "g":
			if !m.localGraphSelected() {
				m.feedback = "read-only here; edit card.yaml on that host"
				return nil
			}
			g.card.open = true
			if g.card.draft != nil {
				return nil
			}
			return m.cardOperation(wire.CardOpDraftPrepare)
		case "e":
			m.feedback = "edit card.yaml in your own editor; local status shows its path"
			return nil
		case "c":
			m.openCardURL()
			return nil
		}
	}
	return nil
}
func (m *Model) graphControl() {
	switch m.graph.control {
	case 0:
		m.graph.polls = !m.graph.polls
	case 1:
		m.graph.messages = !m.graph.messages
	case 2:
		m.graph.filter = (m.graph.filter + 1) % 6
		found := false
		for _, name := range m.graphNames() {
			if name == m.graph.selected {
				found = true
			}
		}
		if !found {
			m.graph.selected = graphLocalKey
			m.graph.scroll = 0
		}
	case 3:
		m.graph.edgeView = true
		m.graph.scroll = 0
	}
}
func (m *Model) graphAge(age *int64) string {
	if age == nil || *age < 0 {
		return "age unavailable"
	}
	fetched, ok := protocol.ParseTime(m.graph.view.FetchedAt)
	if !ok {
		return "age unavailable"
	}
	elapsed := m.now().Sub(fetched)
	if elapsed < 0 {
		elapsed = 0
	}
	if *age > int64((time.Duration(1<<63-1)-elapsed)/time.Millisecond) {
		return "age unavailable"
	}
	return shortAge(time.Duration(*age)*time.Millisecond + elapsed)
}
func (m *Model) graphStatus() string {
	g := &m.graph
	if !g.have {
		return "Graph - waiting for the first answer " + cleanUISpan(g.err)
	}
	state := g.view.Gateway.State
	if state == protocol.GatewayTooOld {
		state = "gateway too old"
	}
	status := "Graph - " + state
	if g.view.FetchedAt != "" {
		status += "; fetched " + cleanUISpan(g.view.FetchedAt)
		if at, ok := protocol.ParseTime(g.view.FetchedAt); ok {
			age := m.now().Sub(at)
			if age < 0 {
				age = 0
			}
			status += " (" + shortAge(age) + " ago)"
		}
	}
	if state != protocol.GatewayConnected || g.err != "" {
		status += "; last data, not current"
	}
	if g.view.Gateway.Error != "" {
		status += "; " + cleanUISpan(g.view.Gateway.Error)
	}
	if g.err != "" {
		status += "; " + cleanUISpan(g.err)
	}
	if g.view.Snapshot != nil {
		if g.view.Snapshot.Truncated {
			status += "; CARD BODIES OMITTED"
		}
		if g.view.Snapshot.EdgesPartial {
			status += "; EDGE COUNTS PARTIAL"
		}
	}
	return status
}
func (m *Model) selectedCard() (*protocol.HostCard, *protocol.GraphCard, *protocol.CardFileView) {
	if m.graph.selected == graphLocalKey {
		e := &m.graph.card
		if !e.have {
			return nil, nil, nil
		}
		if e.view.Served == nil {
			return nil, nil, &e.view
		}
		return &e.view.Served.Card, nil, &e.view
	}
	h, ok := m.graphHost(m.graph.selected)
	if !ok {
		return nil, nil, nil
	}
	var local *protocol.CardFileView
	if m.localGraphSelected() && m.graph.card.have {
		local = &m.graph.card.view
	}
	return h.Card.Card, &h.Card, local
}
func (m *Model) openCardURL() {
	if m.localGraphSelected() {
		e := &m.graph.card
		if !e.have {
			m.feedback = "local card listener state unavailable"
			return
		}
		if e.view.Listener.State != protocol.CardListenerListening {
			m.feedback = "listener " + cleanUISpan(e.view.Listener.State)
			if e.view.Listener.State == protocol.CardListenerNotBound {
				m.feedback = "not bound: " + cleanUISpan(e.view.Listener.Reason)
			}
			return
		}
		if e.view.Listener.URL == "" {
			m.feedback = "listener URL unavailable"
			return
		}
		m.graph.url = cleanUISpan(e.view.Listener.URL)
		m.graph.urlPanel = true
		return
	}
	card, _, _ := m.selectedCard()
	if card == nil || len(card.SupportedInterfaces) == 0 || card.SupportedInterfaces[0].URL == "" {
		m.feedback = "no parsed card URL to copy"
		return
	}
	m.graph.url = cleanUISpan(card.SupportedInterfaces[0].URL)
	m.graph.urlPanel = true
}

func graphKeyName(name string) string {
	if name == graphLocalKey {
		return "Local card / status"
	}
	return cleanUISpan(name)
}
func graphField(label, value string) string { return fmt.Sprintf("%s: %s", label, cleanUISpan(value)) }
