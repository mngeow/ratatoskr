package tui

// The wizard's model: the nine setup steps, one at a time, with the step list on the left and the current
// step on the right (contract 4.1).
//
// Rules this file keeps:
//   - Update and View do no terminal I/O and never call the engine directly. Plan, Apply, ParsePaste,
//     StagePair and Validate all run inside a tea.Cmd, with the run's context, so the UI stays responsive
//     and a slow readiness check cannot freeze the screen (the engine waits up to 20 s).
//   - Secrets live only in secretInput buffers (secret.go). They are never placed in a tea.Msg, a command
//     error, a log line or a rendered string, and they are wiped when handed to the engine, when a field
//     error arrives, and on every exit path.
//   - Consent starts exactly on Question.DefaultYes and is never inverted.
//   - While a command is in flight no second engine call is started, and a quit request waits for it.

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/setup"
)

// spinnerInterval is how often the frame advances while a command is in flight.
const spinnerInterval = 80 * time.Millisecond

// spinnerFrames is hand-rolled, so this package needs no charm.land/bubbles dependency.
var spinnerFrames = []string{"|", "/", "-", "\\"}

// engine is the part of *setup.Engine the wizard uses.
//
// The model depends on this interface, not on the concrete engine, so the front end can be driven by a
// fake in a test without exporting anything else from this package.
type engine interface {
	Plan(context.Context, setup.StepID) (setup.Plan, error)
	Apply(context.Context, setup.StepID, setup.Answers) setup.Result
	Validate(setup.StepID, string, string) string
	ParsePaste([]byte) (setup.PasteSummary, error)
	StagePair(string, string, []byte) (setup.PasteSummary, error)
	Summary() setup.Summary
	ExitCode() int
	Close()
}

// The real engine satisfies it, so Run can pass *setup.Engine unchanged.
var _ engine = (*setup.Engine)(nil)

// phase is what the right pane is doing.
type phase int

const (
	phasePlanning phase = iota // a Plan command is in flight
	phaseEditing               // the pane shows a plan and collects answers
	phaseApplying              // an Apply, ParsePaste or StagePair command is in flight
	phaseSummary               // the closing checklist is on screen
)

// focusKind is one selectable row of the right pane.
type focusKind int

const (
	focusPaste    focusKind = iota // the masked block area (step 3, paste mode)
	focusField                     // a text field
	focusQuestion                  // a consent question
)

// focusItem points at one selectable row.
type focusItem struct {
	kind  focusKind
	index int
}

// Messages. Every one carries the generation of the operation that produced it, so a late answer from a
// superseded operation can never change the screen.
type (
	planMsg struct {
		gen  int
		step setup.StepID
		plan setup.Plan
		err  error
	}
	applyMsg struct {
		gen  int
		step setup.StepID
		res  setup.Result
	}
	stageMsg struct {
		gen     int
		summary setup.PasteSummary
		err     error
	}
	validateMsg struct {
		gen     int // the field's own sequence number
		fieldID string
		value   string
		message string
	}
	spinnerMsg struct{ gen int }
)

// model is the wizard.
type model struct {
	ctx    context.Context
	engine engine
	steps  []setup.StepID

	index int   // the step being worked on
	phase phase // what the pane is doing
	plan  setup.Plan

	lines   []string // Result lines and other text the owner should see
	notices []setup.Notice

	fieldValues map[string]string
	fieldErrors map[string]string
	answers     map[string]bool
	statuses    map[setup.StepID]setup.Status

	focus  int
	cursor int // rune offset inside the focused field's value
	items  []focusItem

	secret    *secretInput
	fieldMode bool // step 3: type the fields instead of pasting the block
	staged    *setup.PasteSummary

	busy      bool
	busyLabel string
	frame     int

	gen                   int
	validating            map[string]int
	validationBusy        string
	validationPending     map[string]string
	validationOrder       []string
	submitAfterValidation bool
	scrollTop             int

	summary  setup.Summary
	exitCode int

	quitArmed    bool
	quitWhenIdle bool

	width  int
	height int
}

// newModel starts at the first step. The caller owns the context and the engine.
func newModel(eng engine, ctx context.Context) *model {
	return &model{
		ctx:               ctx,
		engine:            eng,
		steps:             setup.Steps,
		index:             0,
		phase:             phasePlanning,
		fieldValues:       make(map[string]string),
		fieldErrors:       make(map[string]string),
		answers:           make(map[string]bool),
		statuses:          make(map[setup.StepID]setup.Status),
		validating:        make(map[string]int),
		validationPending: make(map[string]string),
		exitCode:          setup.ExitFailed, // unless a step says otherwise, an unfinished run is not a success
	}
}

// Init asks for the first step's plan.
func (m *model) Init() tea.Cmd { return m.planCmd() }

// ---- commands ------------------------------------------------------------------------------------

// planCmd plans the current step. Planning is read-only but may stat files, so it runs off the UI loop.
func (m *model) planCmd() tea.Cmd {
	step := m.steps[m.index]
	m.phase = phasePlanning
	m.busy = true
	m.busyLabel = "planning " + step.Title()
	gen := m.nextGen()

	eng, ctx := m.engine, m.ctx
	return tea.Batch(m.spinCmd(gen), func() tea.Msg {
		plan, err := eng.Plan(ctx, step)
		return planMsg{gen: gen, step: step, plan: plan, err: err}
	})
}

// applyCmd runs the step with the answers collected so far.
func (m *model) applyCmd(a setup.Answers) tea.Cmd {
	step := m.steps[m.index]
	m.phase = phaseApplying
	m.busy = true
	m.busyLabel = "applying " + step.Title()
	gen := m.nextGen()

	eng, ctx := m.engine, m.ctx
	return tea.Batch(m.spinCmd(gen), func() tea.Msg {
		return applyMsg{gen: gen, step: step, res: eng.Apply(ctx, step, a)}
	})
}

// pasteCmd parses the pasted block.
//
// The buffer is taken (and the wizard's copy zeroed) before the command is returned, so the goroutine owns
// the only reference; the engine zeroes it when it is done.
func (m *model) pasteCmd(block []byte) tea.Cmd {
	m.phase = phaseApplying
	m.busy = true
	m.busyLabel = "reading the pasted block"
	gen := m.nextGen()

	eng := m.engine
	return tea.Batch(m.spinCmd(gen), func() tea.Msg {
		summary, err := eng.ParsePaste(block)
		return stageMsg{gen: gen, summary: summary, err: err}
	})
}

// fieldsCmd stages the values typed one at a time. code may be nil and is zeroed by the engine.
func (m *model) fieldsCmd(url, pin string, code []byte) tea.Cmd {
	m.phase = phaseApplying
	m.busy = true
	m.busyLabel = "checking the typed values"
	gen := m.nextGen()

	eng := m.engine
	return tea.Batch(m.spinCmd(gen), func() tea.Msg {
		summary, err := eng.StagePair(url, pin, code)
		return stageMsg{gen: gen, summary: summary, err: err}
	})
}

// validateCmd asks the engine about one field. It may stat the filesystem, so it never runs in Update.
func (m *model) validateCmd(fieldID, value string) tea.Cmd {
	seq := m.validating[fieldID] + 1
	m.validating[fieldID] = seq
	if _, queued := m.validationPending[fieldID]; !queued {
		m.validationOrder = append(m.validationOrder, fieldID)
	}
	m.validationPending[fieldID] = value
	return m.startValidation()
}

// startValidation serializes Validate calls with every other engine operation.
func (m *model) startValidation() tea.Cmd {
	if m.validationBusy != "" || len(m.validationOrder) == 0 || m.busy || m.quitWhenIdle {
		return nil
	}
	fieldID := m.validationOrder[0]
	m.validationOrder = m.validationOrder[1:]
	value := m.validationPending[fieldID]
	delete(m.validationPending, fieldID)
	m.validationBusy = fieldID
	seq, step, eng := m.validating[fieldID], m.plan.Step, m.engine
	return func() tea.Msg {
		return validateMsg{gen: seq, fieldID: fieldID, value: value, message: eng.Validate(step, fieldID, value)}
	}
}

// spinCmd schedules the next spinner frame.
func (m *model) spinCmd(gen int) tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerMsg{gen: gen} })
}

// nextGen starts a new operation and invalidates every answer to an older one.
func (m *model) nextGen() int {
	m.gen++
	return m.gen
}

// ---- update --------------------------------------------------------------------------------------

// Update handles one message.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.scrollTop = 0
		return m, nil

	case spinnerMsg:
		if !m.busy || msg.gen != m.gen {
			return m, nil // the command finished: the ticker stops here
		}
		m.frame++
		return m, m.spinCmd(m.gen)

	case planMsg:
		return m.onPlan(msg)

	case applyMsg:
		return m.onApply(msg)

	case stageMsg:
		return m.onStage(msg)

	case validateMsg:
		if m.validationBusy == msg.fieldID {
			m.validationBusy = ""
		}
		// A result for a value the owner has already changed is stale and is dropped.
		if m.validating[msg.fieldID] == msg.gen && m.fieldValues[msg.fieldID] == msg.value {
			if msg.message == "" {
				delete(m.fieldErrors, msg.fieldID)
			} else {
				m.fieldErrors[msg.fieldID] = msg.message
			}
		}
		if cmd := m.startValidation(); cmd != nil {
			return m, cmd
		}
		if m.quitWhenIdle {
			return m, tea.Quit
		}
		if m.submitAfterValidation {
			m.submitAfterValidation = false
			if len(m.fieldErrors) > 0 {
				m.focusOnFirstError(m.fieldErrors)
				return m, nil
			}
			return m.submitStep()
		}
		return m, nil

	case tea.PasteMsg:
		return m.onPaste(msg)

	case tea.KeyPressMsg:
		return m.onKey(msg)
	}

	return m, nil
}

// onPlan shows a step's plan, or starts an automatic step straight away.
func (m *model) onPlan(msg planMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.busy = false
	if m.quitWhenIdle {
		return m, tea.Quit
	}

	if msg.err != nil {
		// The engine's own message is terminal-safe; a planning failure stops the run rather than
		// guessing at a step it could not read.
		m.lines = []string{msg.err.Error()}
		m.summary = m.engine.Summary()
		m.exitCode = setup.ExitFailed
		m.phase = phaseSummary
		return m, nil
	}

	m.enterPlan(msg.plan)

	// An automatic step has nothing to ask: apply it now, still inside a command.
	if msg.plan.Auto && len(msg.plan.Fields) == 0 && len(msg.plan.Questions) == 0 {
		return m, m.applyCmd(setup.Answers{Yes: map[string]bool{}})
	}
	return m, nil
}

// enterPlan replaces the pane's editing state with a plan's.
func (m *model) enterPlan(plan setup.Plan) {
	m.plan = plan
	m.phase = phaseEditing
	m.notices = plan.Notices
	m.lines = nil
	m.fieldErrors = make(map[string]string)
	m.answers = make(map[string]bool)
	m.staged = nil
	m.focus, m.cursor = 0, 0

	// Paste mode is the default whenever the step offers it; ctrl+f switches to the fields.
	m.fieldMode = plan.PasteBlock && len(plan.Fields) == 0

	m.fieldValues = make(map[string]string, len(plan.Fields))
	for _, field := range plan.Fields {
		m.fieldValues[field.ID] = field.Default
	}

	for _, question := range plan.Questions {
		// The engine's ruling is the starting selection, exactly as given.
		m.answers[question.ID] = question.DefaultYes
	}

	if plan.PasteBlock && !m.fieldMode {
		m.secret = &secretInput{}
	} else if field := secretFieldOf(plan); field != nil {
		m.secret = &secretInput{}
	} else {
		m.secret = nil
	}

	m.rebuildItems()
}

// rebuildItems lays the focusable rows out in display order.
func (m *model) rebuildItems() {
	m.items = m.items[:0]
	if m.plan.PasteBlock && !m.fieldMode {
		m.items = append(m.items, focusItem{kind: focusPaste})
	}
	if m.fieldMode || !m.plan.PasteBlock {
		for index := range m.plan.Fields {
			m.items = append(m.items, focusItem{kind: focusField, index: index})
		}
	}
	for index := range m.plan.Questions {
		m.items = append(m.items, focusItem{kind: focusQuestion, index: index})
	}
	if m.focus >= len(m.items) {
		m.focus = len(m.items) - 1
	}
	if m.focus < 0 {
		m.focus = 0
	}
}

// onApply takes a step's result.
func (m *model) onApply(msg applyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.busy = false
	if m.quitWhenIdle {
		return m, tea.Quit
	}
	res := msg.res

	if res.Status != "" && res.Follow == nil && len(res.FieldErrors) == 0 {
		m.statuses[res.Step] = res.Status
	}

	if res.Stop {
		m.lines = res.Lines
		m.summary = m.engine.Summary()
		m.exitCode = res.ExitCode
		m.phase = phaseSummary
		m.secret.wipe()
		m.secret = nil
		return m, nil
	}

	if len(res.FieldErrors) > 0 {
		// Nothing was applied. Show the messages beside the fields, drop any secret the owner typed
		// (it was not used, and keeping it would invite a second, confusing submit) and stay here.
		m.fieldErrors = res.FieldErrors
		m.lines = res.Lines
		m.secret.wipe()
		m.secret = nil
		m.phase = phaseEditing
		m.focusOnFirstError(res.FieldErrors)
		return m, nil
	}

	if res.Follow != nil {
		// Another consent round on the same step: show it and keep what the owner already typed.
		follow := *res.Follow
		follow.Step = res.Step
		m.enterFollow(follow, res.Lines)
		return m, nil
	}

	m.lines = res.Lines

	// The step is finished: move on.
	m.secret.wipe()
	m.secret = nil
	m.index++
	if m.index >= len(m.steps) {
		m.summary = m.engine.Summary()
		m.exitCode = m.engine.ExitCode()
		m.phase = phaseSummary
		return m, nil
	}
	return m, m.planCmd()
}

// enterFollow shows a follow-up round on the same step.
func (m *model) enterFollow(plan setup.Plan, lines []string) {
	m.plan = plan
	m.phase = phaseEditing
	m.lines = lines
	m.notices = plan.Notices
	m.staged = nil
	m.focus, m.cursor = 0, 0

	// Keep typed values that the follow plan still asks for; a follow round normally carries only
	// questions, and its answers start at the engine's ruling.
	values := make(map[string]string, len(plan.Fields))
	for _, field := range plan.Fields {
		if existing, ok := m.fieldValues[field.ID]; ok {
			values[field.ID] = existing
		} else {
			values[field.ID] = field.Default
		}
	}
	m.fieldValues = values

	m.answers = make(map[string]bool, len(plan.Questions))
	for _, question := range plan.Questions {
		m.answers[question.ID] = question.DefaultYes
	}

	m.fieldMode = plan.PasteBlock && len(plan.Fields) == 0
	if !plan.PasteBlock || m.fieldMode {
		m.secret.wipe()
		m.secret = nil
	} else if m.secret == nil {
		m.secret = &secretInput{}
	}

	m.rebuildItems()
}

// onStage takes the result of parsing or staging the pairing material.
func (m *model) onStage(msg stageMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.busy = false
	if m.quitWhenIdle {
		return m, tea.Quit
	}
	m.phase = phaseEditing

	if msg.err != nil {
		// The parser names the field and never echoes the secret; nothing is staged, so the owner can
		// try again.
		m.lines = []string{msg.err.Error()}
		m.staged = nil
		m.secret = &secretInput{}
		m.focusOn("use")
		return m, nil
	}

	summary := msg.summary
	m.staged = &summary
	m.lines = nil
	m.focusOn("use")
	return m, nil
}

// secretFieldOf returns a plan's secret field, if it has one.
func secretFieldOf(plan setup.Plan) *setup.Field {
	for index := range plan.Fields {
		if plan.Fields[index].Secret {
			return &plan.Fields[index]
		}
	}
	return nil
}

// focusOnFirstError puts the cursor on the first field the engine complained about.
func (m *model) focusOnFirstError(errors map[string]string) {
	for index, item := range m.items {
		if item.kind != focusField {
			continue
		}
		if _, failed := errors[m.plan.Fields[item.index].ID]; failed {
			m.focus, m.cursor = index, 0
			return
		}
	}
}

// focusOn focuses the question with this ID, if the round has one.
func (m *model) focusOn(questionID string) {
	for index, item := range m.items {
		if item.kind == focusQuestion && m.plan.Questions[item.index].ID == questionID {
			m.focus = index
			return
		}
	}
}

// onPaste appends bracketed-paste text to whatever is focused.
func (m *model) onPaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.phase != phaseEditing || m.busy {
		return m, nil
	}
	if msg.Content == "" {
		return m, nil
	}

	if m.secretActive() {
		m.secret.appendText(msg.Content, m.secretLimit())
		return m, nil
	}

	if field := m.focusedField(); field != nil {
		m.insertText(msg.Content)
		return m, m.validateCmd(field.ID, m.fieldValues[field.ID])
	}
	return m, nil
}

// secretActive reports whether typing goes into a masked buffer.
func (m *model) secretActive() bool {
	if m.plan.PasteBlock && !m.fieldMode {
		return true
	}
	field := m.focusedField()
	return field != nil && field.Secret
}

// focusedField returns the field the cursor is on, or nil.
func (m *model) focusedField() *setup.Field {
	if m.focus < 0 || m.focus >= len(m.items) {
		return nil
	}
	item := m.items[m.focus]
	if item.kind != focusField {
		return nil
	}
	return &m.plan.Fields[item.index]
}

// focusedQuestion returns the question the cursor is on, or nil.
func (m *model) focusedQuestion() *setup.Question {
	if m.focus < 0 || m.focus >= len(m.items) {
		return nil
	}
	item := m.items[m.focus]
	if item.kind != focusQuestion {
		return nil
	}
	return &m.plan.Questions[item.index]
}

// ---- keys ----------------------------------------------------------------------------------------

// onKey handles one key press.
func (m *model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ctrl := msg.Mod&tea.ModCtrl != 0

	// Quit is available everywhere, but a secret command that is already running is allowed to finish so
	// nothing races with the buffer it owns.
	if ctrl && msg.Code == 'c' {
		return m.requestQuit()
	}

	if m.phase == phaseSummary {
		// The checklist stays up until the owner dismisses it.
		switch msg.Code {
		case tea.KeyPgUp:
			m.scrollTop = maxInt(0, m.scrollTop-maxInt(1, m.height-4))
			return m, nil
		case tea.KeyPgDown:
			m.scrollTop += maxInt(1, m.height-4)
			return m, nil
		case tea.KeyEnter, tea.KeyEscape, 'q', ' ':
			return m.requestQuit()
		}
		return m, nil
	}

	if m.quitArmed {
		switch msg.Code {
		case tea.KeyEscape, tea.KeyEnter:
			return m.requestQuit()
		default:
			m.quitArmed = false
			return m, nil
		}
	}

	switch msg.Code {
	case tea.KeyEscape:
		m.quitArmed = true
		return m, nil
	case tea.KeyTab:
		if msg.Mod&tea.ModShift != 0 {
			m.moveFocus(-1)
		} else {
			m.moveFocus(1)
		}
		return m, nil
	case tea.KeyUp:
		if !m.secretActive() {
			m.moveFocus(-1)
		}
		return m, nil
	case tea.KeyDown:
		if !m.secretActive() {
			m.moveFocus(1)
		}
		return m, nil
	case tea.KeyPgUp:
		m.scrollTop = maxInt(0, m.scrollTop-maxInt(1, m.height-4))
		return m, nil
	case tea.KeyPgDown:
		m.scrollTop += maxInt(1, m.height-4)
		return m, nil
	}

	if ctrl && msg.Code == 'f' {
		// Switch the pairing step between the pasted block and the typed fields. Offered whenever the
		// plan allows a block, with or without a code.
		if m.plan.PasteBlock && m.phase == phaseEditing && !m.busy {
			m.toggleFieldMode()
		}
		return m, nil
	}

	if ctrl && msg.Code == 'd' {
		// Submit the masked block, or the typed fields, while a secret area is focused.
		if m.phase == phaseEditing && !m.busy && m.secretActive() {
			return m.submitPairing()
		}
		return m, nil
	}
	if ctrl && msg.Code == 'u' && m.phase == phaseEditing && !m.busy && m.secretActive() {
		m.secret.wipe()
		m.secret = &secretInput{}
		m.lines = nil
		return m, nil
	}

	if msg.Code == tea.KeyEnter || msg.Code == tea.KeyKpEnter {
		if m.busy || m.phase != phaseEditing {
			return m, nil
		}
		// When bracketed paste is unavailable, a multi-line paste arrives as key presses with
		// Enter between lines. Preserve those line boundaries in the masked block; ctrl+d is its
		// explicit submit key. Single-line secret fields and ordinary fields still submit on Enter.
		if m.plan.PasteBlock && !m.fieldMode && m.secretActive() {
			m.secret.appendText("\n", setup.MaxPasteBytes)
			return m, nil
		}
		return m.submitStep()
	}

	if m.busy || m.phase != phaseEditing {
		return m, nil
	}

	// Editing a text field: cursor movement and deletion.
	if field := m.focusedField(); field != nil && !field.Secret {
		switch msg.Code {
		case tea.KeyLeft:
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case tea.KeyRight:
			if m.cursor < len([]rune(m.fieldValues[field.ID])) {
				m.cursor++
			}
			return m, nil
		case tea.KeyHome:
			m.cursor = 0
			return m, nil
		case tea.KeyEnd:
			m.cursor = len([]rune(m.fieldValues[field.ID]))
			return m, nil
		case tea.KeyBackspace:
			m.deleteBack()
			return m, m.validateCmd(field.ID, m.fieldValues[field.ID])
		case tea.KeyDelete:
			m.deleteForward()
			return m, m.validateCmd(field.ID, m.fieldValues[field.ID])
		}
	}

	if m.secretActive() {
		if msg.Code == tea.KeyBackspace {
			m.secret.backspace()
		}
		if msg.Text != "" && msg.Mod&(tea.ModAlt|tea.ModCtrl) == 0 {
			m.secret.appendText(msg.Text, m.secretLimit())
		}
		return m, nil
	}

	// A consent question: y, n and space set the answer.
	if question := m.focusedQuestion(); question != nil {
		switch {
		case msg.Code == 'y' || msg.Code == 'Y':
			m.answers[question.ID] = true
			return m, nil
		case msg.Code == 'n' || msg.Code == 'N':
			m.answers[question.ID] = false
			return m, nil
		case msg.Code == tea.KeySpace || msg.Text == " ":
			m.answers[question.ID] = !m.answers[question.ID]
			return m, nil
		}
	}

	if field := m.focusedField(); field != nil && msg.Text != "" && msg.Mod&(tea.ModAlt|tea.ModCtrl) == 0 {
		m.insertText(msg.Text)
		return m, m.validateCmd(field.ID, m.fieldValues[field.ID])
	}

	return m, nil
}

func (m *model) secretLimit() int {
	if m.plan.PasteBlock && !m.fieldMode {
		return setup.MaxPasteBytes
	}
	return setup.MaxCodeBytes
}

// toggleFieldMode switches the pairing step between the pasted block and the typed fields.
func (m *model) toggleFieldMode() {
	m.fieldMode = !m.fieldMode
	m.secret.wipe()
	m.secret = nil
	if !m.fieldMode || m.secretField() != nil {
		// The masked area, or the secret field, always needs a live buffer.
		m.secret = &secretInput{}
	}
	m.staged = nil
	m.lines = nil
	m.focus, m.cursor = 0, 0
	m.rebuildItems()
}

// submitStep applies the current step with what the owner chose.
func (m *model) submitStep() (tea.Model, tea.Cmd) {
	if m.validationBusy != "" || len(m.validationOrder) > 0 {
		m.submitAfterValidation = true
		return m, nil
	}
	// On the pairing step nothing can be applied until a block or the fields are staged; the use question
	// then sends it on.
	if m.plan.Step == setup.StepPairingMaterial && m.staged == nil {
		return m.submitPairing()
	}

	answers := setup.Answers{Text: map[string]string{}, Yes: map[string]bool{}}
	for _, field := range m.plan.Fields {
		if field.Secret {
			continue // a secret never goes into Text
		}
		answers.Text[field.ID] = m.fieldValues[field.ID]
	}
	for id, value := range m.answers {
		answers.Yes[id] = value
	}

	// The block was already handed over when it was parsed; nothing secret is left to pass.
	return m, m.applyCmd(answers)
}

// submitPairing parses the pasted block or stages the typed fields.
func (m *model) submitPairing() (tea.Model, tea.Cmd) {
	if m.fieldMode || !m.plan.PasteBlock {
		url := m.fieldValues["url"]
		pin := m.fieldValues["pin"]

		var code []byte
		if field := m.secretField(); field != nil {
			code = m.secret.take() // the wizard's copy is zeroed here
		}
		return m, m.fieldsCmd(url, pin, code)
	}

	if m.secret == nil || m.secret.empty() {
		m.lines = []string{"nothing was pasted: paste the block that `rtk gateway participant pair` printed, then press ctrl+d"}
		return m, nil
	}
	if m.secret.truncated {
		m.lines = []string{"the pasted block is larger than this wizard accepts: nothing was submitted"}
		return m, nil
	}

	block := m.secret.take() // the wizard's copy is zeroed before the command starts
	return m, m.pasteCmd(block)
}

// secretField returns the plan's secret field, if it has one.
func (m *model) secretField() *setup.Field {
	for index := range m.plan.Fields {
		if m.plan.Fields[index].Secret {
			return &m.plan.Fields[index]
		}
	}
	return nil
}

// requestQuit ends the program, waiting for any command that already owns a secret.
func (m *model) requestQuit() (tea.Model, tea.Cmd) {
	m.secret.wipe()
	if m.busy || m.validationBusy != "" {
		// A command is in flight: quit as soon as it reports back, so nothing is written underneath it.
		m.quitWhenIdle = true
		return m, nil
	}
	return m, tea.Quit
}

// moveFocus moves the cursor through the pane's rows.
func (m *model) moveFocus(delta int) {
	if len(m.items) == 0 {
		return
	}
	m.focus += delta
	if m.focus < 0 {
		m.focus = len(m.items) - 1
	}
	if m.focus >= len(m.items) {
		m.focus = 0
	}
	m.cursor = 0
}

// insertText inserts printable text at the cursor of the focused field.
func (m *model) insertText(text string) {
	field := m.focusedField()
	if field == nil {
		return
	}
	runes := []rune(m.fieldValues[field.ID])
	cursor := m.cursor
	if cursor > len(runes) {
		cursor = len(runes)
	}
	insert := []rune(text)
	merged := make([]rune, 0, len(runes)+len(insert))
	merged = append(merged, runes[:cursor]...)
	merged = append(merged, insert...)
	merged = append(merged, runes[cursor:]...)
	m.fieldValues[field.ID] = string(merged)
	m.cursor = cursor + len(insert)
}

// deleteBack removes the rune before the cursor.
func (m *model) deleteBack() {
	field := m.focusedField()
	if field == nil || m.cursor == 0 {
		return
	}
	runes := []rune(m.fieldValues[field.ID])
	if m.cursor > len(runes) {
		m.cursor = len(runes)
	}
	runes = append(runes[:m.cursor-1], runes[m.cursor:]...)
	m.fieldValues[field.ID] = string(runes)
	m.cursor--
}

// deleteForward removes the rune at the cursor.
func (m *model) deleteForward() {
	field := m.focusedField()
	if field == nil {
		return
	}
	runes := []rune(m.fieldValues[field.ID])
	if m.cursor >= len(runes) {
		return
	}
	runes = append(runes[:m.cursor], runes[m.cursor+1:]...)
	m.fieldValues[field.ID] = string(runes)
}

// wipeSecrets forgets anything secret the model still holds.
func (m *model) wipeSecrets() {
	if m.secret != nil {
		m.secret.wipe()
		m.secret = nil
	}
}

// ---- view ----------------------------------------------------------------------------------------

// View renders the whole screen. It never touches the terminal and never calls the engine.
func (m *model) View() tea.View { return tea.NewView(m.render()) }

// render builds the screen text.
func (m *model) render() string {
	width := m.width
	if width <= 0 {
		width = 80 // before the first WindowSizeMsg, assume a normal terminal
	}

	var out strings.Builder

	title := "Ratatoskr OpenRig bridge setup"
	if m.phase != phaseSummary && m.index < len(m.steps) {
		title += fmt.Sprintf(" - step %d of %d", m.index+1, len(m.steps))
	}
	out.WriteString(title)
	out.WriteString("\n")
	out.WriteString(strings.Repeat("=", minInt(len(title), width)))
	out.WriteString("\n\n")

	if m.phase == phaseSummary {
		out.WriteString(pageSummary(m.renderSummary(width), maxInt(1, m.height-3), m.scrollTop))
		return out.String()
	}

	leftWidth := minInt(27, maxInt(8, width/3))
	if width-leftWidth < 12 {
		leftWidth = maxInt(1, width-13)
	}
	rightWidth := maxInt(1, width-leftWidth-3)
	left := wrapColumn(m.renderSteps(), leftWidth)
	right := m.renderPane(rightWidth)
	if m.height > 0 {
		pageSize := maxInt(1, m.height-4)
		if len(right) > pageSize {
			footer := right[len(right)-1]
			body := right[:len(right)-1]
			maxTop := maxInt(0, len(body)-maxInt(1, pageSize-1))
			if m.scrollTop > maxTop {
				m.scrollTop = maxTop
			}
			start := m.scrollTop
			end := minInt(len(body), start+maxInt(1, pageSize-1))
			right = append(append([]string(nil), body[start:end]...), footer)
			if start < len(left) {
				leftEnd := minInt(len(left), start+len(right))
				left = left[start:leftEnd]
			} else {
				left = nil
			}
		} else {
			m.scrollTop = 0
		}
	}
	rows := maxInt(len(left), len(right))
	for row := 0; row < rows; row++ {
		leftLine, rightLine := "", ""
		if row < len(left) {
			leftLine = left[row]
		}
		if row < len(right) {
			rightLine = right[row]
		}
		out.WriteString(leftLine)
		out.WriteString(strings.Repeat(" ", maxInt(0, leftWidth-runeWidth(leftLine))))
		out.WriteString(" | ")
		out.WriteString(rightLine)
		out.WriteString("\n")
	}
	return out.String()
}

// renderSteps is the left column: every step, with the ones already finished marked.
func (m *model) renderSteps() string {
	var out strings.Builder
	for index, step := range m.steps {
		marker := "  "
		if index == m.index {
			marker = "> "
		}
		suffix := ""
		if status, done := m.statuses[step]; done {
			suffix = "  (" + string(status) + ")"
		}
		fmt.Fprintf(&out, "%s%d. %s%s\n", marker, index+1, step.Title(), suffix)
	}
	return out.String()
}

// renderPane is the right column: what the current step needs and what it found.
func (m *model) renderPane(width int) []string {
	var lines []string
	wrap := func(text string) {
		for _, piece := range wrapText(text, width) {
			lines = append(lines, piece)
		}
	}

	wrap(m.steps[m.index].Title())
	lines = append(lines, "")

	for _, instruction := range m.plan.Instructions {
		wrap(instruction)
	}
	if len(m.plan.Instructions) > 0 {
		lines = append(lines, "")
	}

	for _, fact := range m.plan.Facts {
		wrap("  " + fact.Label + ": " + fact.Value)
	}

	for _, notice := range m.notices {
		label := "note: "
		if notice.Level == setup.Warn {
			label = "WARNING: "
		}
		wrap(label + notice.Text)
	}

	for _, change := range m.plan.Changes {
		wrap("  ~ " + change.Field + ": " + change.Old + " -> " + change.New)
	}

	if len(m.plan.Facts) > 0 || len(m.notices) > 0 || len(m.plan.Changes) > 0 {
		lines = append(lines, "")
	}

	lines = append(lines, m.renderInputs(width)...)

	if m.staged != nil {
		lines = append(lines, "")
		for _, line := range m.renderStaged(width) {
			lines = append(lines, line)
		}
	}

	for _, line := range m.lines {
		for _, piece := range wrapText(line, width) {
			lines = append(lines, "  "+piece)
		}
	}

	if m.busy {
		lines = append(lines, "")
		lines = append(lines, "  "+spinnerFrames[m.frame%len(spinnerFrames)]+" "+m.busyLabel)
	}

	lines = append(lines, "")
	if m.quitArmed {
		lines = append(lines, "  press esc or enter again to quit; anything not done stays undone")
	} else {
		lines = append(lines, "  "+m.hint())
	}
	var safeLines []string
	for _, line := range lines {
		safeLines = append(safeLines, wrapText(line, width)...)
	}
	return safeLines
}

// renderInputs draws the fields, the question(s) and the masked area.
func (m *model) renderInputs(width int) []string {
	var lines []string

	for index, item := range m.items {
		focused := index == m.focus
		switch item.kind {
		case focusPaste:
			lines = append(lines, "  "+m.secret.describe("pasted block"))
			lines = append(lines, "  paste the block that `rtk gateway participant pair` printed, then press ctrl+d")
		case focusField:
			field := m.plan.Fields[item.index]
			marker := "    "
			if focused {
				marker = "  > "
			}
			switch {
			case field.Secret:
				lines = append(lines, marker+field.Label+": "+m.secret.describe("code"))
			case focused:
				lines = append(lines, marker+field.Label+": "+m.renderEditingValue(field.ID))
			default:
				lines = append(lines, marker+field.Label+": "+m.fieldValues[field.ID])
			}
			if field.Help != "" {
				for _, piece := range wrapText(field.Help, width-4) {
					lines = append(lines, "      "+piece)
				}
			}
			if message, failed := m.fieldErrors[field.ID]; failed {
				for _, piece := range wrapText(message, width-6) {
					lines = append(lines, "      ! "+piece)
				}
			}
		case focusQuestion:
			question := m.plan.Questions[item.index]
			marker := "    "
			if focused {
				marker = "  > "
			}
			answer := "[no]"
			if m.answers[question.ID] {
				answer = "[yes]"
			}
			lines = append(lines, marker+answer+" "+question.Prompt)
			for _, detail := range question.Detail {
				for _, piece := range wrapText(detail, width-6) {
					lines = append(lines, "      "+piece)
				}
			}
		}
	}

	if m.plan.PasteBlock {
		lines = append(lines, "")
		lines = append(lines, "  ctrl+f switches between the pasted block and the typed fields")
	}
	return lines
}

// renderEditingValue draws the focused field with a visible cursor.
func (m *model) renderEditingValue(fieldID string) string {
	runes := []rune(m.fieldValues[fieldID])
	cursor := m.cursor
	if cursor > len(runes) {
		cursor = len(runes)
	}
	return string(runes[:cursor]) + "|" + string(runes[cursor:])
}

// renderStaged shows the non-secret part of what was parsed.
func (m *model) renderStaged(width int) []string {
	summary := m.staged
	lines := []string{
		"  participant URL: " + summary.URL,
	}
	if summary.Pin != "" {
		lines = append(lines, "  certificate pin: "+summary.Pin)
	}
	if summary.Plain {
		lines = append(lines, "  WARNING: the participant API is plain HTTP")
	}
	if summary.Rotation {
		lines = append(lines, "  note: the gateway said this is a rotation; the old credential stops working when the new code is redeemed")
	}
	if summary.StaleNote != "" {
		for _, piece := range wrapText("  "+summary.StaleNote, width) {
			lines = append(lines, piece)
		}
	}
	if summary.CodeReceived {
		lines = append(lines, "  pairing code: received")
	} else {
		lines = append(lines, "  pairing code: not used by this step")
	}
	return lines
}

// renderSummary is the closing checklist.
func (m *model) renderSummary(width int) string {
	var out strings.Builder

	rows := m.summary.Rows
	if len(rows) == 0 {
		for _, step := range m.steps {
			status, done := m.statuses[step]
			if !done {
				status = setup.StatusNotDone
			}
			rows = append(rows, setup.Row{Step: step, Status: status})
		}
	}

	out.WriteString("Checklist\n")
	for _, row := range rows {
		line := fmt.Sprintf("  %-16s %s", row.Step.Title(), row.Status)
		if row.Detail != "" {
			line += " - " + row.Detail
		}
		for _, piece := range wrapText(line, width) {
			out.WriteString(piece)
			out.WriteString("\n")
		}
	}

	todo := append([]string(nil), m.summary.Todo...)
	if !endsWithRoundTrip(todo) {
		// The contract fixes this line; if the engine ever omits it, the screen must not lose it.
		todo = append(todo, roundTripLine)
	}
	for _, line := range m.lines {
		out.WriteString(terminalText(line))
		out.WriteString("\n")
	}
	out.WriteString("press enter to close\n")
	out.WriteString("\nStill yours to do\n")
	for _, item := range todo {
		if item == roundTripLine {
			continue
		}
		for _, piece := range wrapText("  - "+item, width) {
			out.WriteString(piece)
			out.WriteString("\n")
		}
	}
	out.WriteString(roundTripLine)
	out.WriteString("\n")
	return out.String()
}

// pageSummary keeps the contract's final pending-execution line visible while paging long checklists.
func pageSummary(text string, height, top int) string {
	if height <= 0 {
		return text
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) <= height {
		return text
	}
	last := lines[len(lines)-1]
	body := lines[:len(lines)-1]
	pageSize := maxInt(1, height-1)
	maxTop := maxInt(0, len(body)-pageSize)
	if top > maxTop {
		top = maxTop
	}
	end := minInt(len(body), top+pageSize)
	return strings.Join(append(append([]string(nil), body[top:end]...), last), "\n") + "\n"
}

// roundTripLine is the last line of every checklist (contract 4.1 step 9).
const roundTripLine = "scenario 8 round trip: PENDING USER EXECUTION"

// endsWithRoundTrip reports whether the checklist already carries that line.
func endsWithRoundTrip(todo []string) bool {
	return len(todo) > 0 && todo[len(todo)-1] == roundTripLine
}

// hint is the one-line key reminder for the current pane.
func (m *model) hint() string {
	if m.busy {
		return "working; keys are ignored until it finishes"
	}
	if m.plan.Step == setup.StepPairingMaterial && m.staged == nil {
		if m.fieldMode || !m.plan.PasteBlock {
			return "tab moves, enter checks fields, ctrl+f returns to paste, pgup/pgdn scroll, esc quits"
		}
		return "paste block, ctrl+d reads it, ctrl+f types fields, pgup/pgdn scroll, esc quits"
	}
	return "tab moves, y/n answer, enter continues, pgup/pgdn scroll, esc quits"
}

// ---- text helpers --------------------------------------------------------------------------------

// wrapText breaks a line at word boundaries for the given width and never returns an empty slice.
func wrapText(text string, width int) []string {
	text = terminalText(text)
	if width < 1 {
		width = 1
	}
	if text == "" {
		return []string{""}
	}
	var lines []string
	remaining := []rune(text)
	for lipgloss.Width(string(remaining)) > width {
		fit := 0
		for fit < len(remaining) && lipgloss.Width(string(remaining[:fit+1])) <= width {
			fit++
		}
		if fit == 0 {
			fit = 1 // A wide glyph cannot fit in a one-cell terminal, but keep the rune intact.
		}
		cut := fit
		for index := fit - 1; index > 0; index-- {
			if unicode.IsSpace(remaining[index]) {
				cut = index
				break
			}
		}
		lines = append(lines, strings.TrimRight(string(remaining[:cut]), " "))
		remaining = remaining[cut:]
		for len(remaining) > 0 && unicode.IsSpace(remaining[0]) {
			remaining = remaining[1:]
		}
	}
	return append(lines, string(remaining))
}

// terminalText makes control and directional format characters visible so source-provided text cannot
// inject terminal controls or visually reorder the wizard's instructions.
func terminalText(text string) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&out, "\\u{%04X}", r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func wrapColumn(text string, width int) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		lines = append(lines, wrapText(line, width)...)
	}
	return lines
}

func runeWidth(text string) int { return lipgloss.Width(text) }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// minInt is the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
