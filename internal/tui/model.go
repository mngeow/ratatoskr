package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Pane is the part of a pane the UI uses. Defined here, satisfied by
// *pane.Pane. The UI never starts or stops anything: App owns the processes.
type Pane interface {
	Resize(width, height int) error
	RequestResize(width, height int) error
	Snapshot() pane.Frame
	SendKey(key uv.KeyPressEvent) error
	Paste(text string) error
	SendMouse(event uv.MouseEvent) error
	MouseEnabled(event uv.MouseEvent) bool
	InputReady() bool
	Err() error
}

// Router is the part of the delivery router the UI talks to.
type Router interface {
	UserDraft(agent protocol.AgentID, drafting bool)
	UserSubmitted(agent protocol.AgentID)
	DeliverHeld(agent protocol.AgentID)
	Snapshot() protocol.Snapshot
	History(q protocol.HistoryQuery) ([]protocol.MessageView, error)
	Runs(limit int) ([]protocol.RunView, error)
	MessageDetail(id string) (protocol.MessageDetail, error)
	PendingOnQuit() int
}

// Slot is one agent's place on screen. A nil Pane with a StartErr shows the
// failure; a nil Pane without one shows the agent as offline. Both are valid.
type Slot struct {
	ID       protocol.AgentID
	Name     string
	Pane     Pane
	StartErr error
}

// Layout constants. chromeRows is everything that is not a child screen:
// title, pane headers, separator, two message rows, feedback and hints.
const (
	chromeRows          = 7
	recentMessageRows   = 2
	messageHistoryLimit = 200
	historyQueryLimit   = 200
	minUsableWidth      = 40
	minUsableHeight     = 10
	refreshInterval     = time.Second / 30
	inputQueueLimit     = 128
	separatorColumn     = "│"
)

// Model is the Bubble Tea model: two panes, a status line, the message log and
// conservative draft tracking.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. Every behavioural claim in
// this file is unverified, and no harness has ever been rendered by it.
type Model struct {
	router Router
	events <-chan protocol.Event
	slots  [2]Slot

	// active is how many panes exist: 1 for a single-agent runtime run, 2
	// otherwise. It comes from the constructor and never from the layout or from
	// Snapshot.Runtime.
	active int

	focus     int
	logMode   bool
	logLoaded bool
	logErr    error
	log       []protocol.MessageView

	quitArmed bool
	feedback  string
	banner    string

	width, height int

	snap     protocol.Snapshot
	messages []protocol.MessageView
	drafts   map[protocol.AgentID]bool

	// Connected mode only, and all inert in local mode: the connection the router
	// last reported, its peers, and the outbound records for the recent rows.
	conn  *protocol.ConnectionView
	peers []string
	sends []protocol.SendView

	// outbound is the F3 outbound source, which lives inside the log view.
	outbound outboundState

	// daemon is nil in every mode but a window attached to a daemon. Every daemon behaviour
	// hangs off it, so the local paths are untouched.
	daemon *daemonWindow

	// networkMode is the daemon-only Network view (F5). It replaces the pane body and never
	// changes the pane geometry, so it never resizes a PTY.
	networkMode bool
	// theme is the window's palette state: whether colour is drawn, and how far it must be
	// downsampled. It starts inert and is only enabled by the terminal's own profile message.
	theme uiTheme
	// tabGesture is a strip press waiting for its release; it is its own state so a focus-consumed
	// press and a strip press can never be confused.
	tabGesture tabGesture
	// network is the cached answer and the query discipline.
	network networkCache
	// entryPending and entryGeneration guard the one Enter query, and entryIntent is what an ssh plan
	// left for the attach loop to run after this window has detached.
	entryPending    bool
	entryGeneration uint64
	entryViewEpoch  uint64
	entryIntent     *EntryIntent

	eventsClosed      bool
	eventWaiting      bool
	input             [2][]inputAction
	inputRunning      [2]bool
	nextInput         uint64
	frames            [2]pane.Frame
	historyGeneration uint64
	historyPending    bool
	historyDirty      bool
	historyLast       time.Time
	draftEpoch        [2]uint64
	inputStarted      [2]time.Time
	history           historyState
	interrupt         interruptState
	mouse             mouseState
	now               func() time.Time
}

type inputAction struct {
	control int // 1 clears router draft; 2 requests held delivery

	key        uv.KeyPressEvent
	text       string
	paste      bool
	mouse      bool
	mouseEvent uv.MouseEvent
	submit     bool
	// drafting marks an action that counts as the user typing. A wheel turn does
	// not, so it must not keep a draft alive after Enter.
	drafting bool
	// priorDraft records whether a draft already existed before this action
	// marked one, so a refused mouse event can be rolled back without clearing
	// somebody else's draft.
	priorDraft bool
	seq        uint64
	epoch      uint64
	// holderRev is the daemon holder revision this action was created for. A daemon window
	// refuses an action whose revision no longer matches, so input queued before a takeover is
	// never delivered to the window that lost input.
	holderRev uint64
}
type inputDone struct {
	index int
	// clearedDraft marks a daemon draft(false) whose acknowledgement arrived, which is what
	// makes clearing the local draft honest.
	clearedDraft bool
	seq          uint64
	err          error
}

// Messages the model sends itself.
type (
	eventMsg        protocol.Event
	tickMsg         time.Time
	eventsClosedMsg struct{}
	historyMsg      struct {
		messages   []protocol.MessageView
		err        error
		generation uint64
		runID      string
	}
)

var (
	_ tea.Model = (*Model)(nil)
	_ Pane      = (*pane.Pane)(nil)
)

// New builds the model. It validates the parts it cannot work without - a
// router and exactly two slots with unique, non-empty agent IDs - and keeps the
// slots in the order it was given, because F1 and F2 mean first and second.
//
// New does not start panes and does not read the router: Init seeds the first
// snapshot.
//
// UNTESTED.
func New(router Router, events <-chan protocol.Event, slots []Slot) (*Model, error) {
	if len(slots) != 2 {
		return nil, fmt.Errorf("tui: New: need exactly two slots, got %d", len(slots))
	}
	return buildModel("New", router, events, slots, false)
}

// Init takes the first snapshot and starts the two ongoing loops: waiting for
// one router event and the refresh tick.
//
// Snapshot is called directly rather than through a Cmd because the router
// serves it from an atomic pointer and never touches the actor, so it cannot
// block on a slow disk.
//
// UNTESTED.
func (m *Model) Init() tea.Cmd {
	m.applySnapshot(m.router.Snapshot())
	if m.daemon != nil {
		// Exactly one window-event wait, beside the one event wait, the tick, and one Network query so
		// the tab has data before anyone opens it.
		return tea.Batch(m.waitEvent(), m.tick(), m.waitWindowEvent(), m.seedNetwork())
	}
	return tea.Batch(m.waitEvent(), m.tick())
}

// Update applies one message. Exactly one event wait is outstanding at any
// time, and a closed or nil event channel stops consumption for good.
//
// UNTESTED.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, m.resize(msg.Width, msg.Height)

	case eventMsg:
		m.eventWaiting = false
		event := protocol.Event(msg)
		m.applyEvent(event)
		var cmds []tea.Cmd
		if event.Resync {
			// Events were dropped, so the snapshot, not the event, is the truth.
			m.applySnapshot(m.router.Snapshot())
		}
		if cmd := m.waitEvent(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)

	case eventsClosedMsg:
		m.eventWaiting = false
		m.eventsClosed = true
		return m, nil

	case windowEventMsg:
		return m, m.applyWindowEvent(msg)

	case takeInputDone:
		return m, m.applyTakeInputDone(msg)

	case networkMsg:
		return m, m.applyNetwork(msg)

	case entryMsg:
		return m, m.applyEntry(msg)

	case tea.ColorProfileMsg:
		// The terminal told us what it can show; nothing here probes for it.
		if m.daemon != nil {
			m.theme.applyProfile(msg)
		}
		return m, nil

	case tickMsg:
		if m.daemon != nil {
			m.expireInterrupt()
			// A coalesced frame can carry an ownership or ended change that no event announced,
			// so the tick reconciles too. This is cached reads only: no IO in Update.
			m.reconcileDaemon()
			if m.daemon.disconnected {
				return m, tea.Quit
			}
			if cmd := m.networkTick(); cmd != nil {
				return m, tea.Batch(m.tick(), cmd)
			}
		}
		// The outbound source refreshes on its own coalesced clock, and only while
		// it is the source on show with its list, not a detail, in front.
		if m.logMode && m.outbound.source && m.outbound.dirty && !m.outbound.pending && m.outbound.detailID == "" &&
			m.now().Sub(m.outbound.last) >= 250*time.Millisecond {
			return m, tea.Batch(m.tick(), m.fetchOutboundSends())
		}
		// A live refresh must never replace an older run or a message detail, so
		// it only runs while the current run is on screen.
		if m.logMode && !m.outbound.source && m.historyDirty && !m.historyPending && !m.history.runsPending &&
			m.history.detailID == "" && m.viewingCurrentRun() && time.Since(m.historyLast) >= 250*time.Millisecond {
			return m, tea.Batch(m.tick(), m.fetchHistoryFor(m.currentRunID()))
		}
		return m, m.tick()

	case outboundSendsMsg:
		m.installOutboundSends(msg)
		return m, nil

	case outboundDetailMsg:
		m.installOutboundDetail(msg)
		return m, nil

	case historyMsg:
		if !m.logMode || msg.generation != m.historyGeneration || msg.runID != m.currentRunID() {
			return m, nil
		}
		m.installHistory(msg)
		return m, nil

	case runsMsg:
		if !m.logMode || msg.generation != m.historyGeneration {
			return m, nil
		}
		return m, m.installRuns(msg)

	case detailMsg:
		if !m.logMode || msg.generation != m.historyGeneration {
			return m, nil
		}
		if msg.runID != m.currentRunID() || msg.id != m.history.detailID {
			return m, nil
		}
		m.installDetail(msg)
		return m, nil
	case inputDone:
		return m, m.inputCompleted(msg)

	case tea.MouseClickMsg:
		if cmd, handled := m.tabMouse(msg); handled {
			return m, cmd
		}
		if m.networkMode {
			return m, nil
		}
		return m, m.handleMouse(msg)
	case tea.MouseReleaseMsg:
		if cmd, handled := m.tabMouse(msg); handled {
			return m, cmd
		}
		if m.networkMode {
			return m, nil
		}
		return m, m.handleMouse(msg)
	case tea.MouseWheelMsg:
		if cmd, handled := m.tabMouse(msg); handled {
			return m, cmd
		}
		if m.networkMode {
			return m, nil
		}
		return m, m.handleMouse(msg)
	case tea.MouseMotionMsg:
		if cmd, handled := m.tabMouse(msg); handled {
			return m, cmd
		}
		if m.networkMode {
			return m, nil
		}
		return m, m.handleMouse(msg)

	case tea.PasteStartMsg:
		// Marking the draft at the start of the paste is the conservative side:
		// the content has not reached the child yet, but the user is typing.
		if !m.quitArmed {
			m.markDraft()
		}
		return m, nil

	case tea.PasteMsg:
		return m, m.paste(msg.Content)

	case tea.PasteEndMsg:
		// A finished paste does not mean the draft is gone; only Enter does.
		return m, nil

	case tea.KeyPressMsg:
		return m, m.key(msg)
	}

	return m, nil
}

// View renders exactly width by height cells. Child screens keep their styling;
// every label, error and preview is stripped of control characters first.
//
// UNTESTED.
func (m *Model) View() tea.View {
	if m.width < 1 || m.height < 1 {
		view := tea.NewView("")
		view.AltScreen = true
		view.MouseMode = tea.MouseModeCellMotion
		return view
	}
	for _, i := range m.activeSlots() {
		if slot := m.slots[i]; slot.Pane != nil {
			m.frames[i] = slot.Pane.Snapshot()
		}
	}

	lines := m.render()
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	// Cell motion, so clicks and drags arrive; the child decides whether it wants
	// them, and everything else about the terminal stays as it was.
	view.MouseMode = tea.MouseModeCellMotion
	if cursor := m.cursor(); cursor != nil {
		view.Cursor = cursor
	}
	return view
}

// key routes one keystroke. Control keys are the application's; everything else
// belongs to the focused child. Ctrl+C has the interrupt/quit policy in interrupt.go.
//
// UNTESTED.
func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	if !isCtrlC(k) {
		m.clearInterrupt()
	}
	if m.quitArmed {
		if isKey(k, tea.KeyF10) || isCtrlC(k) {
			return tea.Quit
		}
		// Esc cancels the warning, anything else dismisses it, and neither is
		// forwarded to a child.
		m.quitArmed = false
		m.feedback = ""
		return nil
	}

	if isKey(k, tea.KeyF10) {
		if m.daemon != nil {
			// A window detaching stops nothing: the sessions keep running.
			m.detachWindow()
			return tea.Quit
		}
		return m.requestQuit()
	}

	if m.daemon != nil && isKey(k, tea.KeyF5) {
		// F5 toggles the Network view. It is daemon-only: in a plain window the key still reaches
		// the child, exactly as it always has.
		if m.networkMode {
			m.closeNetwork()
			return nil
		}
		return m.openNetwork()
	}

	if m.daemon != nil && isKey(k, tea.KeyF6) {
		// F6 works before the log and the size warning, because taking input is how a viewer
		// becomes useful, and it is not input for a child.
		return m.requestTakeInput()
	}

	// Ctrl+C is decided before the log, the size warning and key forwarding: a
	// pane that cannot receive it must not be able to swallow the quit path.
	if isCtrlC(k) {
		return m.ctrlC(k)
	}
	if m.networkMode {
		cmd, _ := m.networkKey(k)
		return cmd
	}
	// Any other key means the user has moved on.
	m.clearInterrupt()

	if m.logMode {
		return m.historyKey(k)
	}

	switch {
	case isKey(k, tea.KeyF1):
		if m.emptyDaemon() {
			return nil
		}
		m.setFocus(0)
		return nil
	case isKey(k, tea.KeyF2):
		// With one pane there is nowhere to go; the key is spent, not forwarded,
		// so a child never sees an F2 that was meant for the layout.
		if m.single() {
			return nil
		}
		m.setFocus(1)
		return nil
	case isKey(k, tea.KeyF3):
		return m.enterHistory()
	case isKey(k, tea.KeyF4):
		if m.emptyDaemon() {
			m.feedback = emptyWindowHint
			return nil
		}
		if m.tooSmall() {
			m.feedback = "pane too small: enlarge the terminal before delivering"
			return nil
		}
		if !m.paneInputReady(m.focus) {
			m.feedback = "F4 delivery unavailable: pane cannot receive input"
			return nil
		}
		if len(m.input[m.focus]) > 0 {
			m.feedback = "wait for queued input before F4 delivery"
			return nil
		}
		agent, ok := m.agent(m.slots[m.focus].ID)
		if !ok || (agent.State != protocol.AgentIdle && agent.State != protocol.AgentUnknown) {
			m.feedback = "F4 delivery waits: agent must be idle or unknown"
			return nil
		}
		command := m.enqueueControl(m.focus, 2)
		if m.daemon != nil {
			// The daemon's router owns the override. The window seeds from the daemon's cached
			// drafting state once the acknowledgement arrives, so a refused override cannot look
			// like a cleared draft.
			return command
		}
		// Router owns the atomic override. Do not separately clear its gate.
		m.drafts[m.slots[m.focus].ID] = false
		m.feedback = "F4 requested: pastes after any text in the box and submits both"
		return command
	}

	return m.forward(k)
}

// forward sends one key to the focused child.
//
// The draft is reported before the keystroke is written, never after, so the
// router cannot deliver in the gap. Enter is the only key that clears it, and
// only when the write succeeded: a failed write proves nothing about what the
// child saw.
func (m *Model) forward(k tea.KeyPressMsg) tea.Cmd {
	if m.emptyDaemon() {
		return nil
	}
	if m.networkMode {
		return nil
	}
	if m.tooSmall() {
		// Child input is paused while the warning is up.
		m.feedback = "terminal too small: child input is paused"
		return nil
	}

	slot := m.slots[m.focus]
	if slot.Pane == nil {
		return nil
	}
	if !m.paneInputReady(m.focus) {
		if m.daemon != nil {
			// A viewer is a healthy view, not a failure: say what the keys do instead.
			if pane := daemonPane(slot.Pane); pane != nil && !pane.Ended() && !pane.State().YouHold {
				m.feedback = "VIEWER: F6 takes input; Ctrl+C detaches"
			}
		}
		// No draft, no queued action: the header and feedback row already say the
		// pane cannot receive input.
		return nil
	}

	return m.enqueueInput(inputAction{key: toKeyPress(k), submit: isBareEnter(k)})
}

// paste forwards pasted text through the pane's own paste path, which follows
// the child's bracketed-paste mode. The text itself is not sanitized here: a
// paste is user input, not a message.
func (m *Model) paste(content string) tea.Cmd {
	if m.emptyDaemon() {
		return nil
	}
	if m.networkMode {
		return nil
	}
	if m.quitArmed {
		return nil
	}
	if m.tooSmall() || m.logMode {
		m.feedback = "paste not forwarded: pane input is paused"
		return nil
	}
	slot := m.slots[m.focus]
	if slot.Pane == nil {
		m.feedback = "paste not forwarded: pane is offline"
		return nil
	}
	if !m.paneInputReady(m.focus) {
		// The priority line already explains it; a paste must not create a draft
		// or a queued action for a pane that cannot receive one.
		return nil
	}
	return m.enqueueInput(inputAction{text: content, paste: true})
}

// enqueueInput marks draft before scheduling any bytes. Exactly one command
// per pane runs at a time; a stalled PTY cannot spawn a goroutine for each key.
// UNTESTED: on overflow this input is refused visibly and draft remains held.
func (m *Model) enqueueInput(action inputAction) tea.Cmd {
	if m.emptyDaemon() {
		return nil
	}
	action.drafting = true
	action.priorDraft = m.drafts[m.slots[m.focus].ID]
	return m.enqueueInputFor(m.focus, action, false)
}

// enqueueInputFor is the general form: which pane, whether the action counts as
// typing, and whether a full queue should complain. A wheel turn is neither
// typing nor worth complaining about, so it must not bump the draft epoch.
func (m *Model) enqueueInputFor(i int, action inputAction, silentOverflow bool) tea.Cmd {
	if i < 0 || i >= m.activeCount() || m.emptyDaemon() {
		return nil
	}

	if m.slots[i].Pane == nil || !m.paneInputReady(i) {
		return nil
	}
	if action.drafting {
		m.markDraftAgent(m.slots[i].ID)
		m.draftEpoch[i]++
	}

	if len(m.input[i]) >= inputQueueLimit {
		if !silentOverflow {
			m.feedback = "input queue full: key/paste not forwarded; wait and retype"
		}
		return nil
	}

	m.nextInput++
	action.seq = m.nextInput
	action.epoch = m.draftEpoch[i]
	action.holderRev = m.daemonHolderRev(i)
	m.input[i] = append(m.input[i], action)
	return m.startInput(i)
}

// hasLaterDrafting reports whether anything still queued counts as typing. A
// wheel turn in the queue does not, so it cannot keep a draft open after Enter.
func (m *Model) hasLaterDrafting(i int) bool {
	for _, action := range m.input[i] {
		if action.drafting {
			return true
		}
	}
	return false
}

func (m *Model) startInput(i int) tea.Cmd {
	if m.inputRunning[i] || len(m.input[i]) == 0 {
		return nil
	}
	m.inputRunning[i] = true
	m.inputStarted[i] = m.now()
	a, p := m.input[i][0], m.slots[i].Pane
	router, id := m.router, m.slots[i].ID
	if m.daemon != nil {
		// The daemon branch checks admission and every acknowledgement; the local branch below
		// is unchanged.
		return m.startDaemonInput(i, a, id)
	}
	return func() tea.Msg {
		var err error
		switch a.control {
		case 1:
			router.UserDraft(id, false)
		case 2:
			router.DeliverHeld(id)
		default:
			if a.drafting {
				router.UserDraft(id, true)
			} // ordered before any bytes; never on Update
			switch {
			case a.mouse:
				err = p.SendMouse(a.mouseEvent)
			case a.paste:
				err = p.Paste(a.text)
			default:
				err = p.SendKey(a.key)
			}
			if err == nil && a.submit {
				router.UserSubmitted(id)
			}
		}
		return inputDone{index: i, seq: a.seq, err: err}
	}
}

func (m *Model) inputCompleted(done inputDone) tea.Cmd {
	i := done.index
	if m.daemon != nil && i >= 0 && i < m.activeCount() && m.daemon.cancelledSeq[i] != 0 && done.seq == m.daemon.cancelledSeq[i] {
		m.daemon.cancelledSeq[i] = 0
		m.inputRunning[i] = false
		m.inputStarted[i] = time.Time{}
		return m.startInput(i)
	}
	if i < 0 || i >= m.activeCount() || len(m.input[i]) == 0 || m.input[i][0].seq != done.seq {
		return nil
	}
	a := m.input[i][0]
	m.input[i] = m.input[i][1:]
	m.inputRunning[i] = false
	m.inputStarted[i] = time.Time{}

	if m.daemon != nil {
		return m.completeDaemonInput(i, a, done)
	}
	switch {
	case errors.Is(done.err, pane.ErrMouseDisabled):
		// The child stopped wanting this event between the decision and the
		// write. That is not the user's fault and needs no message; if this mouse
		// event was the only thing that created the draft, undo it.
		if a.drafting && !a.priorDraft && a.epoch == m.draftEpoch[i] && !m.hasLaterDrafting(i) {
			m.prependControl(i, 1)
			m.drafts[m.slots[i].ID] = false
		}
	case done.err != nil:
		m.feedback = "input error (may be partial): " + sanitize(done.err.Error())
	case a.submit:
		// Router submission resets budget only. The draft is cleared after real
		// PTY completion, when nothing queued after it still counts as typing and
		// no other action has bumped the epoch since.
		if a.epoch == m.draftEpoch[i] && !m.hasLaterDrafting(i) {
			m.prependControl(i, 1)
			m.drafts[m.slots[i].ID] = false
		}
	}
	return m.startInput(i)
}

// markDraft reports a draft for the focused agent when the input could actually
// reach it. Where input is intercepted - the log, a too-small terminal, a pane
// that never started - nothing is reported, because the user is not typing into
// a child.
func (m *Model) markDraft() {
	if m.emptyDaemon() {
		return
	}
	if m.networkMode {
		return
	}
	if m.tooSmall() || m.logMode {
		return
	}
	if m.slots[m.focus].Pane == nil || !m.paneInputReady(m.focus) {
		return
	}
	m.markDraftAgent(m.slots[m.focus].ID)
}

func (m *Model) enqueueControl(i, kind int) tea.Cmd {
	m.nextInput++
	m.input[i] = append(m.input[i], inputAction{
		seq: m.nextInput, control: kind,
		epoch: m.draftEpoch[i], holderRev: m.daemonHolderRev(i),
	})
	return m.startInput(i)
}
func (m *Model) prependControl(i, kind int) {
	m.nextInput++
	m.input[i] = append([]inputAction{{
		seq: m.nextInput, control: kind,
		epoch: m.draftEpoch[i], holderRev: m.daemonHolderRev(i),
	}}, m.input[i]...)
}

func (m *Model) markDraftAgent(id protocol.AgentID) {
	m.drafts[id] = true
}

// requestQuit quits, unless a delivery is pending or a draft is open. In that
// case the first F10 arms a warning and the second quits.
func (m *Model) requestQuit() tea.Cmd {
	return m.requestQuitWithDraft(m.anyDraft())
}

// requestQuitWithDraft is the shared quit path: an explicit draft answer lets the
// Ctrl+C policy use the state from before its own first press.
func (m *Model) requestQuitWithDraft(hasDraft bool) tea.Cmd {
	pending := m.router.PendingOnQuit()
	if pending > 0 || hasDraft {
		m.quitArmed = true
		m.mouse = mouseState{owner: -1}
		parts := make([]string, 0, 2)
		if pending > 0 {
			parts = append(parts, fmt.Sprintf("%d message(s) still queued", pending))
		}
		if hasDraft {
			parts = append(parts, "a draft is open in a pane")
		}
		m.feedback = strings.Join(parts, "; ") + ". Press F10 or Ctrl+C again to quit, Esc to stay."
		return nil
	}
	return tea.Quit
}

func (m *Model) anyDraft() bool {
	for _, drafting := range m.drafts {
		if drafting {
			return true
		}
	}
	return false
}

// enterLog and leaveLog are the old current-run helpers: they delegate to the
// history view so there is one owner of log mode.
func (m *Model) enterLog() tea.Cmd { return m.enterHistory() }

func (m *Model) leaveLog() { m.leaveHistory() }

// fetchHistory reads the current run, delegating to the history view.
func (m *Model) fetchHistory() tea.Cmd {
	return m.fetchHistoryFor("")
}

// resize records the terminal size and, when the size is usable, tells both
// panes their new screen size. A too-small terminal is not propagated: resizing
// a child to nothing would destroy the frame the user needs to see once the
// terminal grows again.
func (m *Model) resize(width, height int) tea.Cmd {
	m.width, m.height = width, height
	m.applySnapshot(m.router.Snapshot())
	if m.logMode {
		m.historyDirty = true
	}
	if width < 1 || height < 1 || m.tooSmall() {
		m.mouse = mouseState{owner: -1}
		return nil
	}
	for _, box := range m.boxes() {
		if box.width < 1 || box.rows < 1 {
			continue
		}
		p := m.slots[box.index].Pane
		if p == nil {
			continue
		}
		// Non-blocking: the pane coalesces the request and applies the newest size
		// from its own lifecycle worker, so a dragged window edge cannot stall the
		// UI behind a PTY write.
		if m.daemon != nil {
			// A viewer keeps the size it wants and sends nothing until the daemon says it holds
			// input; the client coalesces the request, so a dragged edge cannot pile up.
			m.daemonDesiredSize(box.index, box.width, box.rows)
			continue
		}
		if err := p.RequestResize(box.width, box.rows); err != nil {
			m.feedback = fmt.Sprintf("resize %s failed: %s", m.slotName(box.index), sanitize(err.Error()))
		}
	}
	return nil
}

func (m *Model) tooSmall() bool {
	return m.width < minUsableWidth || m.height < minUsableHeight
}

func (m *Model) screenRows() int {
	rows := m.height - chromeRows
	if rows < 0 {
		return 0
	}
	return rows
}

// paneWidths splits the width into two pane widths plus the one-column
// separator. The left pane takes the smaller half when the width is odd.
func paneWidths(width int) (int, int) {
	usable := width - 1
	if usable < 2 {
		return 0, 0
	}
	left := usable / 2
	return left, usable - left
}

// render builds exactly height lines of width cells.
func (m *Model) render() []string {
	width, height := m.width, m.height
	if m.tooSmall() || height < chromeRows {
		return m.renderMinimal(width, height)
	}

	lines := make([]string, 0, height)
	if m.daemon != nil {
		lines = append(lines, m.daemonTitleRow(width))
	} else {
		lines = append(lines, fitPlain(m.titleLine(), width))
	}
	lines = append(lines, m.headerLine())

	rows := m.screenRows()
	switch {
	case m.networkMode:
		lines = append(lines, m.networkLines(width, rows)...)
	case m.logMode:
		lines = append(lines, m.styleLogLines(m.historyLines(width, rows), width)...)
	case m.emptyDaemon():
		lines = append(lines, m.emptyTerminalsLines(width, rows)...)
	default:
		lines = append(lines, m.screenLines(width, rows)...)
	}

	separator := strings.Repeat("─", width)
	if m.daemon != nil && !m.theme.glyphs() {
		separator = strings.Repeat("-", width)
	}
	lines = append(lines, m.uiLine(separator, width, colLine, colTermBG, false))
	if m.logMode {
		for _, line := range m.historyHelpLines()[:recentMessageRows] {
			lines = append(lines, m.uiLine(line, width, colDim, colTermBG, false))
		}
	} else {
		messages := m.messageLines(width, recentMessageRows)
		if m.daemon != nil {
			for i := range messages {
				messages[i] = m.uiLine(messages[i], width, colDim, colTermBG, false)
			}
		}
		lines = append(lines, messages...)
	}
	lines = append(lines, m.feedbackRow(width))
	lines = append(lines, m.hintRow(width))

	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines[:height]
}

// renderMinimal is the view for a terminal too small for the layout. It still
// says why the big layout is missing, still renders the log when F3 opened it,
// and still offers the quit key.
func (m *Model) renderMinimal(width, height int) []string {
	lines := m.renderMinimalRows(width, height)
	if m.daemon != nil {
		for i := 1; i < len(lines); i++ {
			lines[i] = m.uiLine(lines[i], width, colDim, colTermBG, false)
		}
	}
	return lines
}

func (m *Model) renderMinimalRows(width, height int) []string {
	lines := make([]string, 0, height)

	// In a daemon window the strip keeps row 0 whatever else is happening. A warning is a line below
	// it, never a replacement for the tab a user clicks, so no interactive row is ever hidden or moved.
	daemonStrip := m.daemon != nil
	if daemonStrip {
		lines = append(lines, m.daemonTitleRow(width))
	}

	if m.quitArmed {
		lines = append(lines, m.feedbackRow(width))
		if height > len(lines) {
			lines = append(lines, fitPlain(m.hintsLine(), width))
		}
		for len(lines) < height {
			lines = append(lines, strings.Repeat(" ", width))
		}
		return lines[:height]
	}
	if line := m.inputUnavailableLine(); line != "" {
		lines = append(lines, fitPlain(line, width))
	}
	if !daemonStrip {
		lines = append(lines, fitPlain(m.titleLine(), width))
	}
	if m.daemon != nil && len(m.daemon.startErrors) > 0 {
		// In a small terminal the start errors are the only place a partially failed attach is
		// visible, so they get a line of their own.
		lines = append(lines, fitPlain("start errors: "+strings.Join(m.daemon.startErrors, "; "), width))
	}

	if m.networkMode {
		// A small Network view still says what it knows: the gateway line and the local or waiting
		// state, as many of them as fit. It never claims a pane needs 40x10, because nothing there is a
		// pane.
		for _, line := range m.networkStateLines(width) {
			if len(lines) >= height-1 {
				break
			}
			lines = append(lines, fitPlain(line, width))
		}
		for len(lines) < height {
			lines = append(lines, strings.Repeat(" ", width))
		}
		if height > 1 {
			hint := "F10 detach  Esc terminals  F6 input"
			if m.emptyDaemon() {
				hint = "F10 detach  Esc terminals"
			}
			lines[height-1] = fitPlain(hint, width)
		}
		return lines[:height]
	}
	if m.logMode {
		lines = append(lines, fitPlain("log in a small terminal (F3/Esc back)", width))
		rows := height - 3
		if rows > 0 {
			lines = append(lines, m.historyLines(width, rows)...)
		}
	} else if m.emptyDaemon() {
		// Nothing needs a size, so the empty window shows its guidance instead of a size warning.
		for _, line := range emptyMessages {
			if len(lines) >= height {
				break
			}
			lines = append(lines, fitPlain(line, width))
		}
	} else {
		lines = append(lines, fitPlain(fmt.Sprintf("terminal too small: need %dx%d, have %dx%d", minUsableWidth, minUsableHeight, width, height), width))
		lines = append(lines, fitPlain("child input is paused", width))
		if m.feedback != "" {
			lines = append(lines, m.feedbackRow(width))
		}
	}

	for len(lines) < height-1 {
		lines = append(lines, strings.Repeat(" ", width))
	}
	if len(lines) < height {
		lines = append(lines, m.theme.style(fitPlain(m.hintsLine(), width), colDim, colTermBG, false))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

func (m *Model) titleLine() string {
	budget := m.snap.Budget
	mode := "panes"
	if m.logMode {
		mode = "log"
	}
	if m.networkMode {
		mode = "network"
	}
	title := fmt.Sprintf("ratatoskr [%s]  messages %d  budget %d/%d", mode, len(m.messages), budget.Used, budget.Limit)
	if m.daemon != nil {
		// A session started after this window attached can never appear here, and the hint is
		// part of the title so a crop cannot hide the fact that a re-attach is needed.
		if m.runtimeMode() {
			title = connectionLabel(m.conn) + "  " + title
		}
		return title + "  " + reattachHint
	}
	if m.runtimeMode() {
		// The label leads so truncation cannot hide the gateway state.
		return connectionLabel(m.conn) + "  " + title
	}
	return title
}

// headerLine puts one header over each pane, matching the pane widths.
//
// In the Network view the panes are still laid out exactly as they were - the body is replaced,
// not resized - so the header is the one network line instead.
func (m *Model) headerLine() string {
	if m.networkMode {
		return m.uiLine(m.networkHeaderLine(), m.width, colAccent, colTermBG, true)
	}
	boxes := m.boxes()
	if len(boxes) == 0 {
		return strings.Repeat(" ", m.width)
	}

	var builder strings.Builder
	for i, box := range boxes {
		if i > 0 {
			builder.WriteString(separatorColumn)
		}
		builder.WriteString(m.styledHeader(box.index, box.width))
	}
	return builder.String()
}

func (m *Model) styledHeader(i, width int) string {
	if m.daemon == nil {
		style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
		if i == m.focus {
			style = style.Foreground(lipgloss.Color("231")).Background(lipgloss.Color("24"))
		}
		if agent, ok := m.agent(m.slots[i].ID); ok && agent.State == protocol.AgentNeedsYou {
			style = style.Foreground(lipgloss.Color("220"))
		}
		return style.Render(fitPlain(m.headerText(i), width))
	}
	text := fitPlain(cleanUISpan(m.headerText(i)), width)
	if !m.theme.colours {
		return text
	}

	// Every header cell pairs foreground with a KNOWN background - the focused surface or the
	// terminal one - so a light terminal cannot leave dark text on its own dark bar.
	bg := colTermBG
	fg := colDim
	bold := false
	if i == m.focus {
		bg, fg, bold = colBarBG, colTabSel, true
	}

	label := m.daemonHeaderLabel(i)
	labelColour := fg
	switch label {
	case "INPUT HOLDER":
		labelColour = colOK
	case "SESSION ENDED":
		labelColour = colBad
	case "VIEWER":
		labelColour = colFocus
	}
	// Split only the UI's fixed prefix, never a reported name or state word.
	prefix := fmt.Sprintf(" %d ", i+1)
	if i == m.focus {
		prefix = fmt.Sprintf(">%d ", i+1)
	}
	if label != "" && strings.HasPrefix(text, prefix+label) {
		return m.renderSpans([]uiSpan{{text: prefix, fg: colFocus, bold: bold}, {text: label, fg: labelColour, bold: true}, {text: strings.TrimPrefix(text, prefix+label), fg: fg}}, width, bg)
	}
	return m.theme.style(text, fg, bg, bold)
}

func (m *Model) headerText(index int) string {
	slot := m.slots[index]
	marker := " "
	if m.focus == index {
		marker = "▶"
		if m.daemon != nil {
			marker = ">"
		}
	}

	daemonLabel := ""
	if m.daemon != nil {
		daemonLabel = m.daemonHeaderLabel(index)
	}

	name := sanitize(slot.Name)
	if name == "" {
		name = string(slot.ID)
	}

	state := "unknown"
	held := 0
	if agent, ok := m.agent(slot.ID); ok {
		// In a runtime run the agent's network name is what its peers see, so the
		// header shows that when the router supplied one.
		if m.runtimeMode() && agent.Address != "" {
			name = sanitize(agent.Address)
		}
		state = string(agent.State)
		if agent.State == protocol.AgentNeedsYou {
			state = "needs you"
		}
		held = agent.Held
	}

	draft := ""
	if m.drafts[slot.ID] {
		draft = "*"
	}

	label := ""
	if daemonLabel != "" {
		label = daemonLabel + " "
	}
	header := fmt.Sprintf("%s%d %s%s [%s]%s", marker, index+1, label, name, state, draft)
	if held > 0 {
		header += fmt.Sprintf(" held=%d", held)
	}
	if slot.StartErr != nil {
		header += " FAILED"
	}
	if m.daemon != nil {
		// The holder label already says VIEWER or SESSION ENDED, and the line above the panes
		// reports a real input failure; only a transport or operation error belongs here.
		if slot.Pane != nil && slot.Pane.Err() != nil {
			header += " I/O WARNING"
		}
	} else if slot.Pane != nil && !m.paneInputReady(index) {
		header += " INPUT UNAVAILABLE"
	} else if slot.Pane != nil && slot.Pane.Err() != nil {
		header += " I/O WARNING"
	}
	return header
}

func (m *Model) screenLines(width, rows int) []string {
	boxes := m.boxes()
	if rows < 1 || len(boxes) == 0 {
		return nil
	}

	rendered := make([][]string, 0, len(boxes))
	for _, box := range boxes {
		if box.width < 1 {
			return nil
		}
		rendered = append(rendered, m.paneLines(box.index, box.width, rows))
	}

	lines := make([]string, 0, rows)
	for row := 0; row < rows; row++ {
		var builder strings.Builder
		for i := range rendered {
			if i > 0 {
				builder.WriteString(separatorColumn)
			}
			builder.WriteString(rendered[i][row])
		}
		lines = append(lines, builder.String())
	}
	return lines
}

// paneLines renders one pane's screen area: its frame when it has a pane, its
// failure or offline state when it does not. Child content keeps its styling.
func (m *Model) paneLines(index, width, rows int) []string {
	lines := make([]string, 0, rows)
	slot := m.slots[index]

	switch {
	case slot.Pane == nil && slot.StartErr != nil:
		lines = append(lines, fitPlain("start failed: "+slot.StartErr.Error(), width))
	case slot.Pane == nil:
		lines = append(lines, fitPlain("offline", width))
	default:
		frame := m.frames[index]
		body := strings.Split(frame.Content, "\n")
		for i := 0; i < rows && i < len(body); i++ {
			lines = append(lines, fitStyled(body[i], width))
		}
	}

	for len(lines) < rows {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines[:rows]
}

func logLine(msg protocol.MessageView) string {
	line := fmt.Sprintf("%s %s→%s %s", msg.At.Format("15:04:05"), sanitize(msg.From), sanitize(msg.To), sanitize(string(msg.State)))
	if msg.Reason != "" {
		line += " " + holdReason(msg.Reason)
	}
	if msg.ID != "" {
		line += " [" + sanitize(msg.ID) + "]"
	}
	if msg.Preview != "" {
		line += " " + sanitize(msg.Preview)
	}
	return line
}

// messageLines shows the most recent messages, newest first.
func (m *Model) messageLines(width, rows int) []string {
	if m.runtimeMode() {
		rows2 := append([]recentRow{}, m.recentRows()...)
		lines := make([]string, 0, rows)
		for i := 0; i < rows; i++ {
			if i >= len(rows2) {
				lines = append(lines, strings.Repeat(" ", width))
				continue
			}
			lines = append(lines, fitPlain(rows2[i].text, width))
		}
		return lines
	}

	lines := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		index := len(m.messages) - 1 - i
		if index < 0 {
			lines = append(lines, strings.Repeat(" ", width))
			continue
		}
		lines = append(lines, fitPlain(messageLine(m.messages[index]), width))
	}
	return lines
}

func messageLine(msg protocol.MessageView) string {
	from := sanitize(msg.From)
	to := sanitize(msg.To)
	if from == "" {
		from = "?"
	}
	if to == "" {
		to = "?"
	}
	line := fmt.Sprintf("→ %s→%s %s", from, to, sanitize(string(msg.State)))
	if msg.Reason != "" {
		line += " " + holdReason(msg.Reason)
	}
	if msg.Preview != "" {
		line += ": " + sanitize(msg.Preview)
	}
	return line
}

// feedbackLine shows, in order: the quit warning, the last feedback, a pane
// transport error, then the router's banner. Only one line is available, so the
// most urgent wins.
// styledLine styles one of our own plain lines: muted by default, bad when it is an error.
func (m *Model) styledLine(line string) string {
	if m.daemon == nil || !m.theme.colours || line == "" {
		return line
	}
	if strings.HasPrefix(line, "I/O WARNING") || strings.HasPrefix(line, "not opened") || strings.HasPrefix(line, "entry failed") ||
		strings.HasPrefix(line, "F4") || strings.Contains(line, "failed") {
		return m.theme.style(line, colBad, colTermBG, false)
	}
	return m.theme.style(line, colDim, colTermBG, false)
}

// feedbackRow is the feedback line with its styling: an error reads as bad, anything else as muted.
func (m *Model) feedbackRow(width int) string {
	return m.styledLine(fitPlain(m.feedbackLine(), width))
}

func (m *Model) feedbackLine() string {
	// A pane that cannot receive input is the reason keystrokes appear to do
	// nothing, so it outranks feedback, banners and history notices.
	if m.quitArmed {
		return m.feedback
	}
	if line := m.inputUnavailableLine(); line != "" {
		return line
	}
	if m.feedback != "" {
		if !m.quitArmed && m.banner != "" {
			return m.feedback + " | " + m.banner
		}
		return m.feedback
	}
	if line := m.connectionErrLine(); line != "" {
		return line
	}
	if m.banner != "" {
		return m.banner
	}
	if m.logMode && len(m.log) >= historyQueryLimit {
		return "Showing newest 200 messages of this run; earlier messages are not listed"
	}
	for _, msg := range m.messages {
		if msg.State == protocol.MessageHeld && msg.Reason == "drafting" {
			return holdReason(msg.Reason)
		}
	}
	for _, i := range m.activeSlots() {
		if m.slots[i].Pane == nil {
			continue
		}
		if err := m.slots[i].Pane.Err(); err != nil {
			return fmt.Sprintf("%s: %s", m.slotName(i), err.Error())
		}
	}
	return m.banner
}

func holdReason(reason string) string {
	if reason == "drafting" {
		return "drafting: F4 pastes after any text in the box and submits both"
	}
	return strings.ReplaceAll(sanitize(reason), "_", " ")
}

func (m *Model) hintsLine() string {
	if m.daemon != nil {
		switch {
		case m.networkMode:
			if m.emptyDaemon() {
				return "F5/Esc terminals   ↑/↓ select   PgUp/PgDn page   R refresh   Enter open session   F10 detach"
			}
			return "F5/Esc terminals   ↑/↓ select   PgUp/PgDn page   R refresh   Enter open session   F6 focused terminal input   F10 detach"
		case m.emptyDaemon():
			return "F10 detach   Ctrl+C detach   F3 log   F5 network"
		case m.tooSmall():
			return "F10 detach   Ctrl+C detach   F6 take input (child input paused)"
		case m.logMode:
			return m.historyHelpLines()[recentMessageRows]
		default:
			return "F10 detach   Ctrl+C interrupt; again detach   " + m.focusHints() + "   F3 log   F5 network   F4 deliver held   F6 take input"
		}
	}
	switch {
	case m.quitArmed:
		return "F10 or Ctrl+C quits anyway   Esc cancels"
	case m.tooSmall():
		return m.focusHints() + "   F3 log   F10 or Ctrl+C quits (child input paused)"
	case m.logMode:
		return m.historyHelpLines()[recentMessageRows]
	default:
		return "F10 quit   Ctrl+C interrupt; again quit   " + m.focusHints() + "   F3 log   F4 deliver held"
	}
}

// focusHints names only the focus keys that exist: a single-pane run has no F2
// and should not advertise one.
func (m *Model) focusHints() string {
	if m.single() {
		return "F1 focus"
	}
	return "F1/F2 focus"
}

// cursor places the terminal cursor on the focused child's cursor, offset by the
// pane's position. It is nil when there is no live cursor to show.
func (m *Model) cursor() *tea.Cursor {
	if m.networkMode {
		return nil
	}
	if m.tooSmall() || m.logMode {
		return nil
	}
	slot := m.slots[m.focus]
	if slot.Pane == nil {
		return nil
	}
	frame := m.frames[m.focus]
	if frame.Hidden {
		return nil
	}

	box, ok := m.boxFor(m.focus)
	if !ok || box.rows < 1 {
		return nil
	}
	x, y := frame.X, frame.Y
	if x < 0 || y < 0 || x >= box.width || y >= box.rows {
		return nil
	}
	return &tea.Cursor{Position: tea.Position{X: box.x + x, Y: box.top + y}}
}

// screenTop is the first screen row: the title and the pane headers sit above it.
func screenTop() int { return 2 }

func (m *Model) agent(id protocol.AgentID) (protocol.AgentView, bool) {
	for _, agent := range m.snap.Agents {
		if agent.ID == id {
			return agent, true
		}
	}
	return protocol.AgentView{}, false
}

func (m *Model) slotName(index int) string {
	if name := sanitize(m.slots[index].Name); name != "" {
		return name
	}
	return string(m.slots[index].ID)
}

func (m *Model) applySnapshot(snapshot protocol.Snapshot) {
	m.snap = snapshot
	// Router snapshots can be shared immutable values; never mutate their slices.
	m.snap.Agents = append([]protocol.AgentView(nil), snapshot.Agents...)
	m.banner = snapshot.Banner
	m.messages = trimMessages(snapshot.Messages)
	// A snapshot replaces the connected cache outright; Resync therefore heals it.
	m.applyConnection(snapshot.Connection)
	m.sends = trimSends(snapshot.Sends)
	if m.logMode && m.outbound.source {
		m.outbound.dirty = true
	}
}

func (m *Model) applyEvent(event protocol.Event) {
	if m.logMode && (event.Message != nil || event.Resync) {
		m.historyDirty = true
	}
	if event.Agent != nil {
		m.upsertAgent(*event.Agent)
	}
	if event.Message != nil {
		m.upsertMessage(*event.Message)
	}
	if event.Connection != nil {
		m.applyConnection(event.Connection)
	}
	if event.Send != nil {
		// The recent rows follow every send; only the outbound screen, when it is
		// the source on show, needs a coalesced refresh.
		m.upsertSend(*event.Send)
		if m.logMode && m.outbound.source {
			m.outbound.dirty = true
		}
	}
	if event.Budget != nil {
		m.snap.Budget = *event.Budget
	}
	if event.Banner != "" {
		// An empty banner does not clear the current one: only a snapshot does,
		// which is why a cleared banner arrives with Resync.
		m.banner = event.Banner
	}
}

func (m *Model) upsertAgent(view protocol.AgentView) {
	for i := range m.snap.Agents {
		if m.snap.Agents[i].ID == view.ID {
			m.snap.Agents[i] = view
			return
		}
	}
	m.snap.Agents = append(m.snap.Agents, view)
}

// upsertMessage keeps the newest messageHistoryLimit messages, newest last. An
// event for a message already known replaces it, so a state change does not
// appear as a second message.
func (m *Model) upsertMessage(view protocol.MessageView) {
	for i := range m.messages {
		if m.messages[i].ID == view.ID {
			m.messages[i] = view
			return
		}
	}
	m.messages = append(m.messages, view)
	if len(m.messages) > messageHistoryLimit {
		m.messages = append([]protocol.MessageView(nil), m.messages[len(m.messages)-messageHistoryLimit:]...)
	}
}

func trimMessages(messages []protocol.MessageView) []protocol.MessageView {
	if len(messages) <= messageHistoryLimit {
		return append([]protocol.MessageView(nil), messages...)
	}
	return append([]protocol.MessageView(nil), messages[len(messages)-messageHistoryLimit:]...)
}

// waitEvent starts one event wait. Nothing is started while a wait is already
// outstanding, and nothing is started again after the channel closed: one event
// per wait keeps the queue bounded.
func (m *Model) waitEvent() tea.Cmd {
	if m.events == nil || m.eventsClosed || m.eventWaiting {
		return nil
	}
	m.eventWaiting = true
	events := m.events
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return eventsClosedMsg{}
		}
		return eventMsg(event)
	}
}

// tick schedules the next refresh. The tick only asks for a repaint; it never
// reads the router, so a stuck disk cannot freeze the screen and the router is
// not polled.
func (m *Model) tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(at time.Time) tea.Msg { return tickMsg(at) })
}

// toKeyPress copies a Bubble Tea key into the emulator's key type field by
// field. The two structs are different types that happen to share a layout, so
// the conversion is explicit and will keep compiling if either grows a field
// that matters.
func toKeyPress(k tea.KeyPressMsg) uv.KeyPressEvent {
	return uv.KeyPressEvent{
		Text:        k.Text,
		Mod:         uv.KeyMod(k.Mod),
		Code:        k.Code,
		ShiftedCode: k.ShiftedCode,
		BaseCode:    k.BaseCode,
		IsRepeat:    k.IsRepeat,
	}
}

// isKey matches a control key. Modifiers must be empty: Ctrl+F3 or Shift+F10 are
// the child's business, not the application's.
func isKey(k tea.KeyPressMsg, code rune) bool {
	return k.Code == code && k.Mod&^(tea.ModCapsLock|tea.ModNumLock|tea.ModScrollLock) == 0
}

// isBareEnter is Enter with no modifier. Shift+Enter and Ctrl+J are newline
// input, not submission, and leave the draft in place.
func isBareEnter(k tea.KeyPressMsg) bool {
	return isKey(k, tea.KeyEnter)
}

// sanitize removes everything that must not reach the screen: ANSI sequences
// first, then any remaining control character. This deliberately duplicates part
// of the router's sanitizer: the UI must not depend on the router package, and a
// label is never allowed to move the cursor.
func sanitize(s string) string {
	if s == "" {
		return ""
	}
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// Dropped.
		case r >= 0x80 && r <= 0x9f:
			// Dropped.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fitPlain sanitizes a label and pads or truncates it to exactly width cells.
func fitPlain(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = sanitize(s)
	if ansi.StringWidth(s) > width {
		s = ansi.Truncate(s, width, "")
	}
	if pad := width - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// fitStyled pads or truncates child output to exactly width cells without
// disturbing its styling. Child content is the only place styling is allowed.
func fitStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\r", "")
	if ansi.StringWidth(s) > width {
		s = ansi.Truncate(s, width, "")
	}
	if pad := width - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	// A child's final style/link must not colour or hyperlink its neighbour.
	return s + "\x1b[0m\x1b]8;;\x1b\\"
}
