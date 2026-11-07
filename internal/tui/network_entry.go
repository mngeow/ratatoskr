package tui

// Enter on a Network row: the intent to open a session, and no more than that.
//
// The daemon answers a plan; this file records it and, for an ssh plan, keeps what the caller needs
// to run it after the window has detached. Nothing here builds a command, starts a process or runs
// ssh: the attach loop (a later stage) reads EntryIntent, closes the client and runs the exact Argv.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No Enter has been pressed and no plan built.

import (
	"context"
	"strings"

	"charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

const (
	// entryTimeout bounds one enter query. It is the same budget a network query gets, so the two
	// kinds of read cannot behave differently under a slow daemon.
	entryTimeout = networkRequestBudget

	// entryOtherHostBlocked is what an SSH-entered window says about another host's row: a second hop
	// is deliberately not supported.
	entryOtherHostBlocked = "press F10 to return to the machine you came from, then choose from there"

	// entryNotInWindow is what a local plan says when this window does not show the session.
	entryNotInWindow = "not in this window; reattach to see it"

	// entrySelectASession is what every row that is not a session answers.
	entrySelectASession = "select a session to enter"

	// entryUnknownKind is what an unreadable plan answers.
	entryUnknownKind = "the daemon sent an entry plan this build does not know"
)

// NetworkBookmark is one row's raw identity, for coming back to it later.
//
// It holds the raw values the show uses, never a cleaned copy: a bookmark is a key, and a key that
// has been cleaned no longer matches.
type NetworkBookmark struct {
	Kind string `json:"kind"`
	Host string `json:"host,omitempty"`
	ID   string `json:"id,omitempty"`
}

// EntryIntent is what Enter produced for an ssh row: the daemon's plan, the row to come back to, and
// the address to show while connecting.
type EntryIntent struct {
	Plan      wire.EnterPlan
	Selection NetworkBookmark
	Address   string
}

// entryMsg is one plan, or one failure, from the daemon.
type entryMsg struct {
	generation uint64
	viewEpoch  uint64
	selection  rowKey
	plan       wire.EnterPlan
	err        error
}

// SetEnteredViaSSH records that this window runs inside an SSH entry.
//
// The attach loop sets it from its own Options, which stays authoritative; a window with no daemon
// ignores it.
func (m *Model) SetEnteredViaSSH(entered bool) {
	if m.daemon == nil {
		return
	}
	m.daemon.enteredViaSSH = entered
}

// ResumeNetwork opens the Network view on one row without asking anything yet.
//
// It is the way back after an ssh session: the caller builds the model again, points it at the row it
// left, and the first tick starts a fresh query. A resumed model has no cache to keep, so nothing
// stale can be shown in the meantime.
//
// UNTESTED.
func (m *Model) ResumeNetwork(bookmark NetworkBookmark) {
	if m.daemon == nil {
		return
	}
	m.networkMode = true
	m.mouse = mouseState{owner: -1}
	m.network.selection = rowKey{kind: bookmark.Kind, host: bookmark.Host, id: bookmark.ID}
	m.network.haveSelection = true
	m.network.scrollTop = 0
}

// EntryIntent is the plan the last Enter produced for an ssh row, or nil.
//
// The copy is deep enough for the caller to run it: the argv slice is its own.
//
// UNTESTED.
func (m *Model) EntryIntent() *EntryIntent {
	if m.entryIntent == nil {
		return nil
	}
	copied := *m.entryIntent
	copied.Plan.Argv = append([]string(nil), m.entryIntent.Plan.Argv...)
	return &copied
}

// enterNetwork handles Enter in the Network view.
//
// A row that is not a session, or a row this window may not enter, is answered here without asking
// the daemon anything.
func (m *Model) enterNetwork() tea.Cmd {
	if m.daemon == nil || m.daemon.enter == nil {
		m.feedback = "entry is not available in this window"
		return nil
	}
	if m.entryPending {
		// One at a time: the answer to the first decides what the state is.
		return nil
	}

	rows := m.networkRows()
	index := m.selectedNetworkIndex()
	if index < 0 || index >= len(rows) {
		m.feedback = entrySelectASession
		return nil
	}
	row := rows[index]
	if !row.selectable {
		m.feedback = entrySelectASession
		return nil
	}

	request, ok := m.enterRequestFor(row)
	if !ok {
		m.feedback = entrySelectASession
		return nil
	}

	if m.daemon.enteredViaSSH && request.Host != "" && request.Host != m.network.view.Local.Host {
		m.feedback = entryOtherHostBlocked
		return nil
	}

	m.entryPending = true
	m.entryGeneration++
	generation := m.entryGeneration
	selection := row.key
	viewEpoch := m.entryViewEpoch

	enter := m.daemon.enter
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), entryTimeout)
		defer cancel()

		plan, err := enter(ctx, request)
		return entryMsg{generation: generation, viewEpoch: viewEpoch, selection: selection, plan: plan, err: err}
	}
}

// enterRequestFor turns one row into the request its kind means.
//
// A local row sends the local id, because a local plan needs no snapshot; a remote row sends the
// gateway's host and session id. Identity comes from the row's raw key, never from its text.
func (m *Model) enterRequestFor(row networkRow) (wire.EnterRequest, bool) {
	switch row.key.kind {
	case networkRowLocal:
		if row.key.id == "" {
			return wire.EnterRequest{}, false
		}
		for _, session := range m.network.view.Local.Sessions {
			id := session.SessionID
			if id == "" {
				id = session.LocalID
			}
			if id == row.key.id && session.LocalID != "" {
				return wire.EnterRequest{LocalID: session.LocalID}, true
			}
		}
		return wire.EnterRequest{}, false
	case networkRowRemote:
		if row.key.id == "" || row.key.host == "" {
			return wire.EnterRequest{}, false
		}
		return wire.EnterRequest{Host: row.key.host, SessionID: row.key.id}, true
	}
	// The gateway summary, a host row, an empty-state row and a bare directory peer are not sessions.
	return wire.EnterRequest{}, false
}

// applyEntry installs one plan, or reports why there is none.
func (m *Model) applyEntry(msg entryMsg) tea.Cmd {
	if msg.generation != m.entryGeneration {
		return nil
	}
	// The reply always clears the pending flag, even when the selection or the view has moved on:
	// otherwise closing and reopening the view could leave Enter wedged for ever.
	m.entryPending = false

	if msg.viewEpoch != m.entryViewEpoch || msg.selection != m.network.selection || !m.networkMode {
		// The user has moved on; the plan is no longer about what they are looking at.
		return nil
	}
	if m.daemon == nil || m.daemon.detached {
		return nil
	}
	if msg.err != nil {
		m.feedback = "entry failed: " + cleanNetworkText(msg.err.Error())
		return nil
	}

	switch msg.plan.Kind {
	case wire.EnterLocal:
		return m.applyLocalPlan(msg.plan)
	case wire.EnterManual:
		m.feedback = entryManualText(msg.plan)
		return nil
	case wire.EnterRefused:
		m.feedback = "not opened: " + cleanNetworkText(msg.plan.Reason)
		return nil
	case wire.EnterSSH:
		return m.applySSHPlan(msg.plan, msg.selection)
	}

	m.feedback = entryUnknownKind
	return nil
}

// applyLocalPlan focuses a session this window already shows, or says why it cannot.
func (m *Model) applyLocalPlan(plan wire.EnterPlan) tea.Cmd {
	if !plan.InWindow {
		// The wording is exact, whatever note the plan carries.
		m.feedback = entryNotInWindow
		return nil
	}

	for _, index := range m.activeSlots() {
		if string(m.slots[index].ID) != plan.LocalID {
			continue
		}
		m.setFocus(index)
		m.closeNetwork()
		m.feedback = ""
		return nil
	}

	// The plan says this window has it, but the slot is gone: say the same thing a missing slot says.
	m.feedback = entryNotInWindow
	return nil
}

// applySSHPlan records the intent and ends the window.
//
// The intent is what the attach loop runs; nothing here executes it, and the detach is intentional, so
// WindowError stays nil.
func (m *Model) applySSHPlan(plan wire.EnterPlan, selection rowKey) tea.Cmd {
	m.entryIntent = &EntryIntent{
		Plan: plan,
		Selection: NetworkBookmark{
			Kind: selection.kind,
			Host: selection.host,
			ID:   selection.id,
		},
		Address: m.addressFor(selection),
	}
	m.entryIntent.Plan.Argv = append([]string(nil), plan.Argv...)
	m.detachWindow()
	return tea.Quit
}

// SetWindowFeedback supplies a cleaned outcome to a newly created daemon window.
// The attach loop uses it after SSH, so failure guidance survives alternate-screen entry.
func (m *Model) SetWindowFeedback(message string) {
	if m.daemon != nil {
		m.feedback = cleanNetworkText(message)
	}
}

// addressFor is the selected row's address, cleaned for display.
//
// It comes from the matched session, not from the drawn text: a name that was truncated or altered on
// screen must never become the thing we reconnect to.
func (m *Model) addressFor(selection rowKey) string {
	switch selection.kind {
	case networkRowLocal:
		for _, session := range m.network.view.Local.Sessions {
			if session.SessionID == selection.id || session.LocalID == selection.id {
				return cleanNetworkText(session.Name)
			}
		}
	case networkRowRemote:
		if m.network.view.Snapshot != nil {
			for _, session := range m.network.view.Snapshot.Sessions {
				if session.Host == selection.host && session.SessionID == selection.id {
					return cleanNetworkText(session.Address)
				}
			}
		}
	}
	return ""
}

// entryManualText is the one line a manual plan shows: why it cannot run here, and what to type there.
func entryManualText(plan wire.EnterPlan) string {
	parts := make([]string, 0, 3)
	if reason := cleanNetworkText(plan.Reason); reason != "" {
		parts = append(parts, reason)
	}
	if note := cleanNetworkText(plan.Note); note != "" {
		parts = append(parts, note)
	}
	if manual := cleanNetworkText(plan.Manual); manual != "" {
		parts = append(parts, manual)
	}
	if len(parts) == 0 {
		return "not opened here"
	}
	return "not opened: " + strings.Join(parts, " ")
}
