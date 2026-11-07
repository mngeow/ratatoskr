package tui

// The Network view (F5): a read-only picture of this host's sessions and, when the daemon has one,
// the gateway's snapshot.
//
// It exists only in a daemon window. The first F5 makes the window a consumer of the daemon's fetch
// loop, which is what starts the daemon asking the gateway; a window that never opens it never
// fetches anything. There is at most one request in flight, so the UI can never overlap two daemon
// waits, and a reply that arrives while the view is closed still updates the cache without starting
// another request.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No window has opened this view.

import (
	"context"
	"errors"
	"time"

	"charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// networkFirstWait is the first request's budget: a caller with no snapshot yet waits this long
	// for the daemon's first fetch to land.
	networkFirstWait = wire.MaxNetworkFirstMS
	// networkWait is every later request's budget: it parks on the daemon's version and returns as
	// soon as something changes.
	networkWait = wire.MaxNetworkWaitMS
	// networkRefreshFloor is the shortest gap between two requests, so an immediate or failed answer
	// cannot turn the view into a busy loop.
	networkRefreshFloor = 250 * time.Millisecond // UNTESTED guess
)

// networkCache is the model's copy of the daemon's answer.
//
// have says a known-schema body is on screen; it can survive an error, and then it is old. version
// is the daemon's counter, which the next request echoes as Since. Age uses
// the fetched_at supplied by each successful answer, including no-change replies.
type networkCache struct {
	have    bool
	view    protocol.NetworkView
	version uint64
	// oldBody marks a retained body that no longer describes now: an error came after it, or the
	// gateway stopped being connected, or a newer-schema answer replaced nothing.
	oldBody bool
	// errText is the last failure, cleaned at draw time.
	errText string
	// generation changes with every request, so a late reply for an older request is ignored.
	generation uint64
	pending    bool
	// refreshWanted is an R press that arrived while a request was already in flight.
	refreshWanted bool
	// lastRequestAt is when the last request started, for the refresh floor.
	lastRequestAt time.Time
	// selection, scrollTop and haveSelection drive the list.
	selection     rowKey
	haveSelection bool
	scrollTop     int
	selectedIndex int
}

// networkMsg is one answer, or one failure, from the daemon.
type networkMsg struct {
	generation uint64
	view       protocol.NetworkView
	version    uint64
	err        error
}

// openNetwork switches to the Network view and, if nothing has been requested yet, asks once.
func (m *Model) openNetwork() tea.Cmd {
	if m.daemon == nil {
		return nil
	}
	if m.logMode {
		m.leaveHistory()
	}
	m.networkMode = true
	m.mouse = mouseState{owner: -1}

	if m.network.have || m.network.pending {
		// Reopening reuses the cache; a reply that is still in flight will land in it.
		m.network.refreshWanted = true
		return m.networkTick()
	}
	return m.requestNetwork()
}

// closeNetwork returns to the panes. A request already in flight is left to finish: cancelling it
// would only occupy the daemon's query slot for its own timer anyway, and its reply is useful.
func (m *Model) closeNetwork() {
	m.networkMode = false
	m.entryViewEpoch++
}

// requestNetwork starts one request, or marks a refresh if one is already running.
func (m *Model) requestNetwork() tea.Cmd {
	if m.daemon == nil || m.network.pending {
		if m.network.pending {
			m.network.refreshWanted = true
		}
		return nil
	}

	request := wire.NetworkRequest{}
	if m.network.refreshWanted {
		// R/reopen explicitly asks now, retaining the previous readable cache.
	} else if m.network.have {
		// Ask about the version we have: the daemon parks until something changes.
		request.Since = m.network.version
		request.WaitMS = networkWait
	} else {
		// Nothing yet: wait briefly for the first snapshot so the view is useful.
		request.FirstWaitMS = networkFirstWait
	}

	m.network.generation++
	generation := m.network.generation
	m.network.pending = true
	m.network.refreshWanted = false
	m.network.lastRequestAt = m.now()

	ask := m.daemon.network
	if ask == nil {
		m.network.pending = false
		m.network.errText = "network data unavailable: client has no network reader"
		return nil
	}
	return func() tea.Msg {
		// The daemon bounds itself well inside the client's query timeout, so a cancelled context
		// here only happens when the window closes.
		ctx, cancel := context.WithTimeout(context.Background(), networkRequestBudget)
		defer cancel()

		view, version, err := ask(ctx, request)
		return networkMsg{generation: generation, view: view, version: version, err: err}
	}
}

// networkRequestBudget is the client-side bound on one network request: the daemon's own waits plus
// a margin, so a wedged daemon cannot leave the view waiting for ever.
const networkRequestBudget = 12 * time.Second // One wait kind per query, never their sum.

// applyNetwork installs one answer.
//
// Every successful reply supplies its own body and fetched_at, even at the
// same version. The version is a long-poll token, not a cache equality test.
func (m *Model) applyNetwork(msg networkMsg) tea.Cmd {
	if m.daemon == nil || msg.generation != m.network.generation {
		return nil
	}

	m.network.pending = false
	m.network.lastRequestAt = m.now()

	if msg.err != nil {
		if !errors.Is(msg.err, context.Canceled) {
			m.network.errText = msg.err.Error()
			if m.network.have {
				// The last readable data stays, labelled old.
				m.network.oldBody = true
			}
		}
		return m.afterNetwork()
	}

	if msg.view.Schema != protocol.NetworkSchema {
		// A newer schema is not readable at all: nothing from that body is drawn, and any body we
		// already had is marked old rather than replaced.
		m.network.errText = "network data unavailable: schema unsupported by this build"
		if m.network.have {
			m.network.oldBody = true
		}
		return m.afterNetwork()
	}

	// Every healthy answer installs its body, including one whose version is unchanged: the daemon
	// refreshes the snapshot and its fetched_at on each answer, so keeping the old body would keep an
	// old basis. The version only decides the next request's Since.
	m.network.view = copyNetworkView(msg.view)
	m.network.version = msg.version
	m.network.have = true
	m.network.errText = ""
	m.network.oldBody = false
	m.restoreNetworkSelection()
	return m.afterNetwork()
}

// afterNetwork schedules the next request, subject to the refresh floor and to the view being open.
func (m *Model) afterNetwork() tea.Cmd {
	// Tick alone schedules follow-ons, preserving the floor even after errors/R.
	return nil
}

// networkTick asks for the next request if the view is open and nothing is in flight, pacing it by
// the refresh floor so an immediate answer cannot spin.
func (m *Model) networkTick() tea.Cmd {
	if m.daemon == nil || !m.networkMode || m.network.pending {
		return nil
	}
	if m.now().Sub(m.network.lastRequestAt) < networkRefreshFloor {
		return nil
	}
	return m.requestNetwork()
}

// networkKey handles the Network view's keys. It reports whether the key was used.
func (m *Model) networkKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case isKey(k, tea.KeyF5), isKey(k, tea.KeyEsc):
		m.closeNetwork()
		return nil, true

	case isKey(k, tea.KeyF3):
		// The log is the other read-only view; switching to it leaves this one.
		m.closeNetwork()
		return m.enterHistory(), true

	case isKey(k, tea.KeyF1):
		m.closeNetwork()
		m.setFocus(0)
		return nil, true

	case isKey(k, tea.KeyF2):
		if m.single() {
			return nil, true
		}
		m.closeNetwork()
		m.setFocus(1)
		return nil, true

	case isKey(k, tea.KeyUp):
		m.selectNetwork(-1)
		return nil, true
	case isKey(k, tea.KeyDown):
		m.selectNetwork(1)
		return nil, true
	case isKey(k, tea.KeyPgUp):
		m.selectNetwork(-networkPageRows(m))
		return nil, true
	case isKey(k, tea.KeyPgDown):
		m.selectNetwork(networkPageRows(m))
		return nil, true
	case isKey(k, tea.KeyHome):
		m.selectNetworkTo(0)
		return nil, true
	case isKey(k, tea.KeyEnd):
		m.selectNetworkTo(len(m.networkRows()) - 1)
		return nil, true
	}

	if isNetworkRefreshKey(k) {
		if m.network.pending {
			// Never overlap a wait: remember the wish and ask once the reply lands.
			m.network.refreshWanted = true
			return nil, true
		}
		// An immediate refresh: ask from zero, which the daemon answers at once with a fresh body.
		m.network.refreshWanted = true
		return m.requestNetwork(), true
	}

	if isBareEnter(k) {
		// Enter records an intent; it never types into anything.
		return m.enterNetwork(), true
	}

	// Everything else is consumed: there is nothing here to type into.
	return nil, true
}

// isNetworkRefreshKey is R and Shift+R, with no other modifier.
func isNetworkRefreshKey(k tea.KeyPressMsg) bool {
	mod := k.Mod &^ (tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock | tea.ModShift)
	return mod == 0 && (k.Code == 'r' || k.Code == 'R')
}

// selectNetwork moves the selection by delta rows, clamped.
func (m *Model) selectNetwork(delta int) {
	rows := m.networkRows()
	if len(rows) == 0 {
		return
	}
	index := m.selectedNetworkIndex()
	next := index + delta
	if next < 0 {
		next = 0
	}
	if next >= len(rows) {
		next = len(rows) - 1
	}
	if delta < 0 {
		for next > 0 && !rows[next].selectable {
			next--
		}
	}
	m.selectNetworkTo(next)
}

// selectNetworkTo selects one row index.
func (m *Model) selectNetworkTo(index int) {
	rows := m.networkRows()
	if len(rows) == 0 || index < 0 || index >= len(rows) {
		return
	}
	if !rows[index].selectable {
		found := -1
		for i := index; i < len(rows); i++ {
			if rows[i].selectable {
				found = i
				break
			}
		}
		if found < 0 {
			for i := index - 1; i >= 0; i-- {
				if rows[i].selectable {
					found = i
					break
				}
			}
		}
		if found < 0 {
			return
		}
		index = found
	}
	m.network.selection = rows[index].key
	m.network.selectedIndex = index
	m.network.haveSelection = true
	m.ensureNetworkVisible(rows)
}

// selectedNetworkIndex is the selected row's position, or 0.
func (m *Model) selectedNetworkIndex() int {
	rows := m.networkRows()
	for i, row := range rows {
		if row.key == m.network.selection {
			return i
		}
	}
	return 0
}

// ensureNetworkVisible scrolls so the selected row is inside the visible window.
func (m *Model) ensureNetworkVisible(rows []networkRow) {
	index := m.selectedNetworkIndex()
	visible := m.networkBodyRows()
	if visible < 1 {
		visible = 1
	}
	if index < m.network.scrollTop {
		m.network.scrollTop = index
	}
	if index >= m.network.scrollTop+visible {
		m.network.scrollTop = index - visible + 1
	}
	if m.network.scrollTop < 0 {
		m.network.scrollTop = 0
	}
}

// networkBodyRows is how many rows the body can show.
func (m *Model) networkBodyRows() int {
	rows := m.screenRows()
	// One row for the selected detail, so freshness is never hidden by a narrow column.
	if rows > 1 {
		return rows - 1
	}
	return rows
}

// networkPageRows is one page of selection movement.
func networkPageRows(m *Model) int {
	page := m.networkBodyRows()
	if page < 2 {
		return 1
	}
	return page
}

// restoreNetworkSelection keeps the selection by raw identity across an answer.
//
// A row that is gone falls back to the same index (or the first row), which is the "nearest" rule:
// the user is looking at one part of a list, not at a particular session id.
func (m *Model) restoreNetworkSelection() {
	rows := m.networkRows()
	if len(rows) == 0 {
		m.network.haveSelection = false
		return
	}

	index := -1
	for i, row := range rows {
		if row.key == m.network.selection {
			index = i
			break
		}
	}
	if index < 0 {
		index = m.network.selectedIndex
		if index >= len(rows) {
			index = len(rows) - 1
		}
		if index < 0 {
			index = 0
		}
	}
	m.network.haveSelection = false
	m.selectNetworkTo(index)
}

// copyNetworkView deep-copies every slice a draw reads, so a reply cannot be changed later.
func copyNetworkView(view protocol.NetworkView) protocol.NetworkView {
	copied := view
	copied.Local.Sessions = append([]protocol.ReportSession(nil), view.Local.Sessions...)

	if view.Snapshot != nil {
		snapshot := *view.Snapshot
		snapshot.Hosts = append([]protocol.NetworkHost(nil), view.Snapshot.Hosts...)
		for i := range snapshot.Hosts {
			if snapshot.Hosts[i].Report != nil {
				report := *snapshot.Hosts[i].Report
				snapshot.Hosts[i].Report = &report
			}
			if snapshot.Hosts[i].Connection.HeartbeatAgeMS != nil {
				age := *snapshot.Hosts[i].Connection.HeartbeatAgeMS
				snapshot.Hosts[i].Connection.HeartbeatAgeMS = &age
			}
		}
		snapshot.Sessions = append([]protocol.NetworkSession(nil), view.Snapshot.Sessions...)
		for i := range snapshot.Sessions {
			if snapshot.Sessions[i].Directory.Live != nil {
				live := *snapshot.Sessions[i].Directory.Live
				snapshot.Sessions[i].Directory.Live = &live
			}
			if snapshot.Sessions[i].Reported != nil {
				reported := *snapshot.Sessions[i].Reported
				snapshot.Sessions[i].Reported = &reported
			}
		}
		copied.Snapshot = &snapshot
	}
	return copied
}
