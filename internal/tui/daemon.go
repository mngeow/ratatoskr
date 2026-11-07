package tui

// Daemon windows: a window attached to a daemon over its socket.
//
// The daemon owns the sessions and decides who may type; this file is everything the model does
// differently because of that. It is deliberately one file: the local model stays as it is, and
// every daemon behaviour hangs off a non-nil *daemonWindow so the local paths are untouched.
//
// The window never holds drafting authority, never adds a session after attach, and never claims
// that a lost socket ended an agent.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No window has attached and no key has been
// forwarded.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/windowclient"
)

const (
	// reattachHint is shown in the title of a daemon window, because a session started after
	// attach can never appear in this window.
	reattachHint = "New sessions need re-attach"

	// daemonCtrlCAgain is the first Ctrl+C hint in a daemon window: the second press detaches
	// rather than quitting, because the sessions keep running either way.
	daemonCtrlCAgain = "Ctrl+C again to detach"

	// daemonTakeInputTimeout bounds one F6 request. The client bounds its own requests too; this
	// is the outer limit so a wedged socket cannot hold the command open.
	daemonTakeInputTimeout = 10 * time.Second // UNTESTED guess
)

// daemonWindow is the model's daemon state. It exists only in a daemon window.
type daemonWindow struct {
	client daemonClient
	// network asks the daemon for the network answer (F5). It is a function rather than a method on
	// daemonClient so a test client needs no new method, and a window built without it simply has no
	// Network view to open.
	network func(ctx context.Context, request wire.NetworkRequest) (protocol.NetworkView, uint64, error)
	// enter asks the daemon what pressing Enter on one Network row should do. Nil in a test-built
	// window, which is what makes the Network view's Enter answer politely instead of panicking.
	enter func(ctx context.Context, request wire.EnterRequest) (wire.EnterPlan, error)
	// enteredViaSSH records that this window runs inside an SSH entry, so a second hop is refused.
	enteredViaSSH bool

	// lastRev is the ownership revision last reconciled per slot, so a draft is seeded when a
	// window becomes the holder and never on every frame.
	lastRev [2]uint64
	// lastHolder is whether this window held input at the last reconciliation, so a change in
	// either direction is noticed once.
	lastHolder   [2]bool
	lastDrafting [2]bool
	cancelledSeq [2]uint64

	// detached records an intentional detach, so a later client Close is not reported as a
	// window error.
	detached bool

	// disconnected records that the window lost its daemon, with the reason to show.
	disconnected bool
	disconnect   string

	// quitReason is what the model reports before quitting after a disconnect.
	quitReason string

	// f6Pending is one outstanding take-input request per slot; a second press while one is in
	// flight is consumed rather than queued.
	f6Pending [2]bool

	// startErrors are the sessions this attach could not start, sanitised.
	startErrors []string
}

// Narrow internal seams permit stub-only checks; the public constructor still
// accepts the concrete socket client agreed with backend.
type daemonClient interface {
	Err() error
	OperationError(protocol.AgentID) error
	TakeInput(context.Context, protocol.AgentID) error
	WindowEvents() <-chan windowclient.WindowEvent
}
type daemonSession interface {
	Pane
	State() wire.PaneState
	Ended() bool
}

// windowEventMsg is one WindowEvent, or the channel closing.
type windowEventMsg struct {
	event windowclient.WindowEvent
	ok    bool
}

// takeInputDone is the result of one F6 request. The authoritative ownership arrives through
// WindowEvents, so this carries the error only.
type takeInputDone struct {
	index int
	err   error
}

// NewDaemon builds the model for a window attached to a daemon.
//
// It validates the attach (at most two sessions), wraps the client as the router and event source,
// marks the model as a daemon window, seeds the local draft map from the daemon's authoritative
// drafting state, and focuses the first slot that has not ended.
//
// Zero sessions is allowed: such a window opens on Network, keeps the tab strip and the detach keys,
// shows the daemon's Network answer, and says in the Terminals view that there are no sessions and how
// to start them. Nothing is fabricated to fill the space.
//
// UNTESTED.
func NewDaemon(client *windowclient.Client) (*Model, error) {
	if client == nil {
		return nil, fmt.Errorf("tui: NewDaemon: nil client")
	}

	attached := client.Attached()
	if len(attached.Slots) > 2 {
		return nil, fmt.Errorf("tui: NewDaemon: the daemon attached %d sessions, and a window shows at most two", len(attached.Slots))
	}

	slots := make([]Slot, 0, len(attached.Slots))
	for _, attachedSlot := range attached.Slots {
		if attachedSlot.Agent == "" {
			return nil, fmt.Errorf("tui: NewDaemon: a slot has no session ID")
		}
		pane := client.Pane(attachedSlot.Agent)
		if pane == nil {
			return nil, fmt.Errorf("tui: NewDaemon: the client has no pane for %q", attachedSlot.Agent)
		}
		slots = append(slots, Slot{ID: attachedSlot.Agent, Name: attachedSlot.Name, Pane: pane})
	}

	// Zero sessions is not an error: the window opens on Network and says how to start one. One or two
	// sessions go through the shared runtime constructor, which validates them.
	var model *Model
	if len(slots) == 0 {
		model = allocateModel(client, client.Events(), nil)
	} else {
		built, err := NewRuntime(client, client.Events(), slots)
		if err != nil {
			return nil, err
		}
		model = built
	}

	daemon := &daemonWindow{client: client, network: client.Network, enter: client.Enter}
	for i, slot := range slots {
		state := client.Pane(slot.ID).State()
		daemon.lastRev[i] = state.Rev
		daemon.lastHolder[i] = state.YouHold && !state.Ended
		daemon.lastDrafting[i] = state.Drafting
		if state.Drafting {
			// The daemon's drafting state is the truth; the window only mirrors it.
			model.drafts[slot.ID] = true
		}
	}

	for _, startError := range attached.StartErrors {
		message := sanitize(startError.Message)
		if message == "" {
			continue
		}
		daemon.startErrors = append(daemon.startErrors, sanitize(startError.Name)+": "+message)
	}
	model.daemon = daemon

	// The first slot that has not ended takes focus; with every slot ended the first is focused
	// anyway, because something has to be, and its header says it ended.
	model.focus = model.firstLiveSlot()

	if len(daemon.startErrors) > 0 {
		model.feedback = strings.Join(daemon.startErrors, "; ")
	}

	if model.emptyDaemon() {
		// A session-less window opens on Network: that is where the useful information is, and the tab
		// strip then shows which one is active.
		model.networkMode = true
	}
	return model, nil
}

// firstLiveSlot is the first slot that has not ended, or 0.
func (m *Model) firstLiveSlot() int {
	for _, i := range m.activeSlots() {
		slot := m.slots[i]
		if slot.Pane == nil {
			continue
		}
		if pane := daemonPane(slot.Pane); pane != nil && pane.Ended() {
			continue
		}
		return i
	}
	return 0
}

// daemonPane returns one slot's pane, or nil when the slot is not a daemon session.
func daemonPane(p Pane) daemonSession {
	pane, ok := p.(daemonSession)
	if !ok {
		return nil
	}
	return pane
}

// WindowError is why the window stopped, or nil.
//
// An intentional detach is not an error. A lost daemon is reported here even when the disconnect
// event could not be delivered, because the client's error is authoritative.
//
// UNTESTED.
func (m *Model) WindowError() error {
	if m.daemon == nil || m.daemon.detached {
		return nil
	}
	if m.daemon.disconnected {
		return fmt.Errorf("%s", m.daemon.quitReason)
	}
	if err := m.daemon.client.Err(); err != nil {
		return err
	}
	return nil
}

// waitWindowEvent waits for one daemon window event. Exactly one is outstanding at a time.
func (m *Model) waitWindowEvent() tea.Cmd {
	if m.daemon == nil {
		return nil
	}
	events := m.daemon.client.WindowEvents()
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-events
		return windowEventMsg{event: event, ok: ok}
	}
}

// applyWindowEvent reconciles one daemon event.
//
// The client has already merged the state, so this only reacts: an ownership change may seed the
// draft when this window became the holder, an end cancels queued input, and a disconnect is
// recorded and quits.
func (m *Model) applyWindowEvent(msg windowEventMsg) tea.Cmd {
	if m.daemon == nil {
		return nil
	}
	if !msg.ok {
		m.disconnectWindow("the daemon connection closed")
		return tea.Quit
	}

	switch msg.event.Kind {
	case windowclient.KindDisconnected:
		reason := "the daemon connection closed"
		if msg.event.Error != nil {
			reason = msg.event.Error.Error()
		}
		m.disconnectWindow(reason)
		return tea.Quit

	case windowclient.KindOperationError:
		if msg.event.Error != nil {
			m.feedback = "I/O WARNING: " + sanitize(msg.event.Error.Error())
		}
	case windowclient.KindOwnership, windowclient.KindSessionEnded:
		m.reconcileDaemon()
	}

	return m.waitWindowEvent()
}

// disconnectWindow records a lost daemon and the reason to report.
func (m *Model) disconnectWindow(reason string) {
	if m.daemon == nil || m.daemon.detached {
		return
	}
	m.daemon.disconnected = true
	m.daemon.disconnect = "window disconnected: " + sanitize(reason)
	m.daemon.quitReason = m.daemon.disconnect + "; sessions may still be running; run rtk attach"
	m.feedback = m.daemon.quitReason
	// Queued input can never be delivered, and pretending otherwise would report success.
	m.cancelQueuedInput()
}

// detachWindow records an intentional detach: nothing is an error afterwards.
func (m *Model) detachWindow() {
	if m.daemon == nil {
		return
	}
	m.daemon.detached = true
}

// reconcileDaemon applies the cached daemon state to the model: focus-independent labels,
// drafting seeds and cancellations.
//
// It runs on a window event and on the refresh tick, because a coalesced frame can carry an
// ownership or ended change that no event announced.
func (m *Model) reconcileDaemon() {
	if m.daemon == nil {
		return
	}

	for _, i := range m.activeSlots() {
		pane := daemonPane(m.slots[i].Pane)
		if pane == nil {
			continue
		}
		state := pane.State()
		// A complete pane update can bring fresh drafting information at the
		// same holder revision as its earlier ownership acknowledgement.
		if state.Drafting != m.daemon.lastDrafting[i] {
			m.daemon.lastDrafting[i] = state.Drafting
			if state.YouHold && !state.Ended && len(m.input[i]) == 0 && !m.inputRunning[i] {
				m.drafts[m.slots[i].ID] = state.Drafting
			}
		}

		if state.Ended && len(m.input[i]) > 0 {
			// An ended session will never take the queue.
			m.cancelQueuedSlot(i)
		}

		if state.Rev == m.daemon.lastRev[i] {
			continue
		}
		wasHolder := m.daemon.lastHolder[i]
		m.daemon.lastRev[i] = state.Rev
		m.daemon.lastHolder[i] = state.YouHold && !state.Ended

		switch {
		case state.Ended:
			m.cancelQueuedSlot(i)
		case state.YouHold:
			m.cancelQueuedSlot(i)
			// A new holder takes over the daemon's drafting state, which is the only truth.
			m.drafts[m.slots[i].ID] = state.Drafting
			m.draftEpoch[i]++
			// The new holder sends the size it wants; the client coalesces it.
			m.requestBoxSize(i)
		case !state.YouHold && wasHolder:
			// Input lost: cancel what is queued, keep the draft, and let the daemon decide.
			m.cancelQueuedSlot(i)
			m.draftEpoch[i]++
		}
	}

	// A transport failure is worth showing even when no event arrived for it.
	if err := m.daemon.client.Err(); err != nil && !m.daemon.detached && !m.daemon.disconnected {
		m.disconnectWindow(err.Error())
	}
}

// cancelQueuedInput drops every queued action, because none of them can be honoured.
func (m *Model) cancelQueuedInput() {
	for _, i := range m.activeSlots() {
		m.cancelQueuedSlot(i)
	}
}

// cancelQueuedSlot drops one slot's queue and its running marker. A result that arrives for a
// dropped action is discarded, because its sequence no longer matches the head.
func (m *Model) cancelQueuedSlot(i int) {
	if m.daemon != nil && m.inputRunning[i] {
		if m.daemon.cancelledSeq[i] == 0 && len(m.input[i]) > 0 {
			m.daemon.cancelledSeq[i] = m.input[i][0].seq
		}
		// Keep the command slot occupied until its old completion arrives; a
		// rapid F6 regain must not launch a second command for the same pane.
		m.input[i] = nil
		return
	}
	m.input[i] = nil
	m.inputRunning[i] = false
	m.inputStarted[i] = time.Time{}
}

// requestBoxSize asks for the size this window wants for one slot. The client coalesces it and
// sends only while this window is the holder.
func (m *Model) requestBoxSize(i int) {
	box, ok := m.boxFor(i)
	if !ok || box.width < 1 || box.rows < 1 {
		return
	}
	if pane := daemonPane(m.slots[i].Pane); pane != nil {
		_ = pane.RequestResize(box.width, box.rows)
	}
}

// startDaemonInput runs one queued action with the daemon's acknowledgements.
//
// The checks are the contract: admission and any latched failure before the action, again after
// draft(true) and before the bytes, and again after each control call. A failure keeps the draft
// and reports through inputDone rather than pretending the control worked.
func (m *Model) startDaemonInput(i int, a inputAction, id protocol.AgentID) tea.Cmd {
	router := m.router
	client := m.daemon.client
	pane := daemonPane(m.slots[i].Pane)

	return func() tea.Msg {
		fail := func(err error) tea.Msg {
			return inputDone{index: i, seq: a.seq, err: err}
		}

		if err := m.daemonAdmission(i, a); err != nil {
			return fail(err)
		}
		if err := client.OperationError(id); err != nil {
			return fail(err)
		}

		switch a.control {
		case 1:
			// draft(false): the daemon's router is the drafting authority, so its answer decides.
			router.UserDraft(id, false)
			if err := client.OperationError(id); err != nil {
				return fail(err)
			}
			if err := m.daemonAdmission(i, a); err != nil {
				return fail(err)
			}
			return inputDone{index: i, seq: a.seq, clearedDraft: true}

		case 2:
			router.DeliverHeld(id)
			if err := client.OperationError(id); err != nil {
				return fail(err)
			}
			if err := m.daemonAdmission(i, a); err != nil {
				return fail(err)
			}
			return inputDone{index: i, seq: a.seq}
		}

		if a.drafting {
			// The daemon's draft flag, then its answer, then the bytes: in that order, always.
			router.UserDraft(id, true)
			if err := client.OperationError(id); err != nil {
				return fail(err)
			}
		}
		if err := m.daemonAdmission(i, a); err != nil {
			return fail(err)
		}

		if pane == nil {
			return fail(fmt.Errorf("that session is not available"))
		}

		var err error
		switch {
		case a.mouse:
			err = pane.SendMouse(a.mouseEvent)
		case a.paste:
			err = pane.Paste(a.text)
		default:
			err = pane.SendKey(a.key)
		}
		if err != nil {
			return fail(err)
		}

		if a.submit {
			if err := m.daemonAdmission(i, a); err != nil {
				return fail(err)
			}
			router.UserSubmitted(id)
			if err := client.OperationError(id); err != nil {
				return fail(err)
			}
		}
		if err := m.daemonAdmission(i, a); err != nil {
			return fail(err)
		}
		return inputDone{index: i, seq: a.seq}
	}
}

// daemonAdmission is the local gate for one queued action: the session has not ended, this window
// holds it, and the queued action was created for the same holder revision.
func (m *Model) daemonAdmission(i int, a inputAction) error {
	pane := daemonPane(m.slots[i].Pane)
	if pane == nil {
		return fmt.Errorf("that session is not available")
	}
	state := pane.State()
	if state.Ended {
		return fmt.Errorf("that session has ended")
	}
	if !state.YouHold {
		return fmt.Errorf("another window holds input for that session")
	}
	if state.Rev != a.holderRev {
		return fmt.Errorf("input was dropped: the holder changed")
	}
	return nil
}

// daemonHolderRev is the holder revision an action must still see when it runs.
func (m *Model) daemonHolderRev(i int) uint64 {
	if m.daemon == nil {
		return 0
	}
	pane := daemonPane(m.slots[i].Pane)
	if pane == nil {
		return 0
	}
	return pane.State().Rev
}

// draftBefore reports whether a draft was already open for the focused agent, which is what a
// refused action restores rather than clearing somebody else's draft.
func (m *Model) draftBefore(i int) bool {
	if i < 0 || i >= m.activeCount() {
		return false
	}
	return m.drafts[m.slots[i].ID]
}

// requestTakeInput runs F6 for the focused session.
//
// One request per slot at a time: a second press while one is in flight is consumed, because the
// answer to the first decides what the state is.
func (m *Model) requestTakeInput() tea.Cmd {
	if m.emptyDaemon() {
		m.feedback = emptyWindowHint
		return nil
	}
	if m.daemon == nil {
		return nil
	}
	i := m.focus
	if i < 0 || i >= m.activeCount() {
		return nil
	}
	pane := daemonPane(m.slots[i].Pane)
	if pane == nil {
		return nil
	}
	if pane.Ended() {
		m.feedback = "F6 unavailable: " + m.slotName(i) + " has ended"
		return nil
	}
	if m.daemon.f6Pending[i] {
		return nil
	}

	m.daemon.f6Pending[i] = true
	agent := m.slots[i].ID
	client := m.daemon.client
	// The index is captured with the request, so the answer is applied to the slot that asked
	// even if focus moved meanwhile.
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), daemonTakeInputTimeout)
		defer cancel()
		return takeInputDone{index: i, err: client.TakeInput(ctx, agent)}
	}
}

// applyTakeInputDone finishes one F6 request.
//
// The authoritative ownership arrives through WindowEvents; this only records the end of the
// request and reports a failure, which the daemon's answer is the truth about.
func (m *Model) applyTakeInputDone(done takeInputDone) tea.Cmd {
	if m.daemon == nil || done.index < 0 || done.index >= m.activeCount() {
		return nil
	}
	m.daemon.f6Pending[done.index] = false
	if done.err != nil {
		m.feedback = "F6 failed: " + sanitize(done.err.Error())
		return nil
	}
	m.reconcileDaemon()
	return nil
}

// completeDaemonInput finishes one queued daemon action.
//
// A failure keeps the draft: the daemon may or may not have applied part of it, and this window
// never pretends otherwise. A draft(false) clears the local draft only once its own
// acknowledgement arrived, and a submit schedules one without clearing it optimistically.
func (m *Model) completeDaemonInput(i int, a inputAction, done inputDone) tea.Cmd {
	if done.err == nil {
		if err := m.daemonAdmission(i, a); err != nil {
			done.err = err
		}
	}
	switch {
	case done.err != nil:
		m.feedback = "I/O WARNING: " + sanitize(done.err.Error())
		if a.drafting || a.submit || a.control != 0 {
			m.drafts[m.slots[i].ID] = true
		}
		// The draft stays as it was: nothing here knows the daemon's drafting state after a
		// failure, and guessing would be worse than a stale flag.
	case done.clearedDraft:
		if a.epoch == m.draftEpoch[i] && !m.hasLaterDrafting(i) {
			m.drafts[m.slots[i].ID] = false
		}
	case a.control == 2:
		// F4: the daemon's router owns the override, so the window takes its drafting state
		// rather than assuming the draft is gone.
		m.seedDraftFromDaemon(i)
	case a.submit:
		// Enter schedules draft(false) after real completion; the clear happens when that
		// action's own acknowledgement arrives, not here.
		if a.epoch == m.draftEpoch[i] && !m.hasLaterDrafting(i) {
			m.prependControl(i, 1)
		}
	}
	return m.startInput(i)
}

// daemonHeaderLabel is the leading word for one slot's header: who holds input, or whether the
// session ended.
//
// It leads so a narrow header truncates the name rather than the state a user must see. An
// operation error keeps its own warning line, so it does not displace this.
func (m *Model) daemonHeaderLabel(i int) string {
	pane := daemonPane(m.slots[i].Pane)
	if pane == nil {
		return ""
	}
	state := pane.State()
	switch {
	case state.Ended:
		if state.ExitCode != nil {
			return fmt.Sprintf("SESSION ENDED exit=%d", *state.ExitCode)
		}
		return "SESSION ENDED"
	case state.YouHold:
		return "INPUT HOLDER"
	default:
		return "VIEWER"
	}
}

// daemonDesiredSize records the size this window wants for one slot, and asks for it only when
// the daemon says this window holds input. A viewer's request would be refused anyway, and
// sending it would make the daemon's gate the place a resize is rejected rather than the window.
func (m *Model) daemonDesiredSize(i, width, height int) {
	pane := daemonPane(m.slots[i].Pane)
	if pane == nil {
		return
	}
	if state := pane.State(); !state.YouHold || state.Ended {
		return
	}
	if err := pane.RequestResize(width, height); err != nil {
		m.feedback = fmt.Sprintf("resize %s failed: %s", m.slotName(i), sanitize(err.Error()))
	}
}

// seedDraftFromDaemon copies the daemon's drafting state for one slot into the local map.
//
// The daemon is the only drafting authority, so this is how the window learns what actually
// happened, including after a takeover.
func (m *Model) seedDraftFromDaemon(i int) {
	if m.daemon == nil || i < 0 || i >= m.activeCount() {
		return
	}
	pane := daemonPane(m.slots[i].Pane)
	if pane == nil {
		return
	}
	m.drafts[m.slots[i].ID] = pane.State().Drafting
}
