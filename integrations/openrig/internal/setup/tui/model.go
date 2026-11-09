package tui

// The setup wizard's model: a conventional, discoverable form over internal/setup's nine steps
// (contract 4.1, ruling W5).
//
// One focus order, one visible form, a scrolling pane, and a key legend that always says what the focused
// control accepts. Update and View do no terminal I/O and never call the engine directly: Plan, Apply,
// ParsePaste, StagePair and Validate all run inside a tea.Cmd with the run's context, so a 20-second
// readiness wait cannot freeze the screen.
//
// Secrets live in secretInput only. They are wiped when handed to the engine, when the owner clears them,
// when the method changes, when a staged review is discarded and on every exit path, and no view ever
// renders a byte of them.
//
// UNTESTED (GR-3): nothing here has been run. Compile checks only.

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/setup"
)

const (
	// spinnerInterval is how often the busy frame advances.
	spinnerInterval = 100 * time.Millisecond

	// Layout thresholds, in cells.
	sidebarMinWidth  = 80 // at or above this the nine-step list has its own column
	singleColMaxWide = 60 // at or below this the layout is one column
	minWidth         = 40
	minHeight        = 12

	sidebarWidth = 26
	footerLines  = 2
)

// spinnerFrames are ASCII, so no glyph is ever missing and nothing needs a Bubbles dependency.
var spinnerFrames = []string{"-", "\\", "|", "/"}

// engine is the part of *setup.Engine the wizard uses. The model depends on the interface so the front
// end can be driven by a fake without widening the package API.
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

// The real engine satisfies it, so Run passes *setup.Engine unchanged.
var _ engine = (*setup.Engine)(nil)

// Status aliases, so the sidebar and the components can compare without importing setup in components.go.
const (
	statusDone     = setup.StatusDone
	statusAlready  = setup.StatusAlready
	statusSkipped  = setup.StatusSkipped
	statusNotDone  = setup.StatusNotDone
	statusYourTurn = setup.StatusYourTurn
)

// phase is what the engine side is doing.
type phase int

const (
	phasePlanning phase = iota
	phaseEditing
	phaseApplying
	phaseSummary
)

// screen is which form is on the right, inside the current step.
type screen int

const (
	screenForm    screen = iota // a step's ordinary form
	screenChoice                // step 3: choose how to supply the pairing material
	screenPaste                 // step 3: the masked block
	screenFields                // step 3: URL, pin, code typed separately
	screenReview                // step 3: what was parsed, plus the consent
	screenSummary               // the closing checklist
)

// controlKind is one focusable row.
type controlKind int

const (
	controlField   controlKind = iota // a text field
	controlToggle                     // a consent toggle
	controlChoice                     // the method list on step 3
	controlPaste                      // the masked block area
	controlButtons                    // the Back/Continue row
)

// control points at one focusable row.
type control struct {
	kind  controlKind
	index int
}

// setupStepStatus is one sidebar line.
type setupStepStatus struct {
	Title  string
	Status setup.Status
}

// theme renders text. Colour is decorative: every state also carries a word or a mark, so the screen is
// readable with colour off, which is what NO_COLOR, TERM=dumb and a non-colour profile produce.
type theme struct {
	colour bool
}

// accent is the emphatic style: bold, and coloured only when colour is on.
func (t theme) accent(text string) string {
	style := lipgloss.NewStyle().Bold(true)
	if t.colour {
		style = style.Foreground(lipgloss.Color("6"))
	}
	return style.Render(text)
}

// ok marks something that passed.
func (t theme) ok(text string) string {
	style := lipgloss.NewStyle().Bold(true)
	if t.colour {
		style = style.Foreground(lipgloss.Color("2"))
	}
	return style.Render(text)
}

// warn marks something to notice.
func (t theme) warn(text string) string {
	style := lipgloss.NewStyle().Bold(true)
	if t.colour {
		style = style.Foreground(lipgloss.Color("3"))
	}
	return style.Render(text)
}

// err marks something wrong.
func (t theme) err(text string) string {
	style := lipgloss.NewStyle().Bold(true)
	if t.colour {
		style = style.Foreground(lipgloss.Color("1"))
	}
	return style.Render(text)
}

// dim marks secondary text.
func (t theme) dim(text string) string {
	return lipgloss.NewStyle().Faint(true).Render(text)
}

// borderColour is the border colour used by boxes, or nil when colour is off.
func (t theme) borderColour() color.Color {
	if !t.colour {
		return nil
	}
	return lipgloss.Color("6")
}

// Messages. Every one carries the generation of the operation that produced it, so an answer to a
// superseded operation can never change the screen.
type (
	planMsg struct {
		gen  int
		step setup.StepID
		plan setup.Plan
		err  error
	}
	applyMsg struct {
		gen     int
		step    setup.StepID
		res     setup.Result
		discard bool // this Apply only discarded a staged block
	}
	stageMsg struct {
		gen     int
		summary setup.PasteSummary
		err     error
	}
	validateMsg struct {
		gen     int
		step    setup.StepID
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

	index  int
	phase  phase
	screen screen
	plan   setup.Plan
	follow bool

	lines   []string
	notices []setup.Notice
	theme   theme

	// editing
	fields   map[string]*textInput
	order    []string
	toggles  map[string]*toggle
	choices  choiceList
	controls []control
	focus    int
	buttons  buttonRow

	// step 3
	method int // 0 paste, 1 fields
	secret *secretInput
	staged *setup.PasteSummary
	from   screen // which subview a staged review came from

	// validation, serialised: one engine Validate call in flight at a time
	validating  bool
	queued      map[string]string
	fieldErrors map[string]string

	busy      bool
	busyLabel string
	frame     int

	gen int

	statusMap map[setup.StepID]setup.Status
	summary   setup.Summary
	exitCode  int

	dialog       dialog
	helpOpen     bool
	quitWhenIdle bool

	pane             pane
	width, height    int
	profileLabel     string
	changedSomething bool
}

// newModel starts at the first step. The caller owns the context and the engine.
func newModel(eng engine, ctx context.Context) *model {
	m := &model{
		ctx:         ctx,
		engine:      eng,
		steps:       setup.Steps,
		phase:       phasePlanning,
		fields:      make(map[string]*textInput),
		toggles:     make(map[string]*toggle),
		queued:      make(map[string]string),
		fieldErrors: make(map[string]string),
		exitCode:    setup.ExitFailed,
		theme:       theme{colour: colourEnabled()},
	}
	m.buttons = buttonRow{Labels: []string{"Back", "Continue"}}
	return m
}

// colourEnabled reports whether decoration may use colour.
//
// NO_COLOR is honoured when present and non-empty, TERM=dumb means no decoration, and a profile that
// arrives later as Ascii/NoTTY/Unknown switches colour off through applyProfile.
func colourEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	return true
}

// applyProfile takes the terminal's colour profile from Bubble Tea. The profile is reduced to its name so
// this package never imports the colorprofile module directly (GR-2).
func (m *model) applyProfile(name string) {
	m.profileLabel = name
	m.theme.colour = colourEnabled()
	switch name {
	case "Ascii", "NoTTY", "Unknown", "":
		m.theme.colour = false
	}
}

// Init asks for the first step's plan.
func (m *model) Init() tea.Cmd { return m.planCmd() }

// ---- commands ------------------------------------------------------------------------------------

// planCmd plans the current step.
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
func (m *model) applyCmd(a setup.Answers, discard bool) tea.Cmd {
	step := m.steps[m.index]
	m.phase = phaseApplying
	m.busy = true
	if discard {
		m.busyLabel = "discarding the staged values"
	} else {
		m.busyLabel = "applying " + step.Title()
	}
	gen := m.nextGen()

	eng, ctx := m.engine, m.ctx
	return tea.Batch(m.spinCmd(gen), func() tea.Msg {
		return applyMsg{gen: gen, step: step, res: eng.Apply(ctx, step, a), discard: discard}
	})
}

// pasteCmd parses a copy of the masked block. Keeping the original lets the owner clear or retry after
// a parse error; ParsePaste zeroes the command's copy.
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

// fieldsCmd stages the values typed one at a time. code may be nil; the engine zeroes it.
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

// validateCmd asks the engine about one field. Only one call is in flight; the newest value is queued and
// scheduled when the active call answers, which keeps the engine's Validate serialised.
func (m *model) validateCmd(fieldID, value string) tea.Cmd {
	if m.validating {
		m.queued[fieldID] = value
		return nil
	}
	m.validating = true
	step := m.steps[m.index]
	gen := m.nextGen()

	eng := m.engine
	return func() tea.Msg {
		return validateMsg{gen: gen, step: step, fieldID: fieldID, value: value, message: eng.Validate(step, fieldID, value)}
	}
}

// spinCmd schedules the next busy frame.
func (m *model) spinCmd(gen int) tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerMsg{gen: gen} })
}

// nextGen invalidates every answer to an older operation.
func (m *model) nextGen() int { m.gen++; return m.gen }

// ---- update --------------------------------------------------------------------------------------

// Update handles one message.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.ColorProfileMsg:
		m.applyProfile(msg.Profile.String())
		return m, nil

	case spinnerMsg:
		if !m.busy || msg.gen != m.gen {
			return m, nil
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
		return m.onValidated(msg)

	case tea.PasteMsg:
		return m.onPaste(msg)

	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// onPlan shows a step's plan, or applies an automatic step at once.
func (m *model) onPlan(msg planMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.busy = false
	if m.quitWhenIdle {
		return m, tea.Quit
	}
	if msg.err != nil {
		m.lines = []string{msg.err.Error()}
		m.summary = m.engine.Summary()
		m.exitCode = setup.ExitFailed
		m.screen = screenSummary
		m.phase = phaseSummary
		return m, nil
	}

	m.enterPlan(msg.plan, false)
	if msg.plan.Auto && len(msg.plan.Fields) == 0 && len(msg.plan.Questions) == 0 {
		return m, m.applyCmd(setup.Answers{Yes: map[string]bool{}}, false)
	}
	return m, nil
}

// enterPlan sets up the form for one plan. follow marks a consent round inside the same step.
func (m *model) enterPlan(plan setup.Plan, follow bool) {
	m.plan, m.follow = plan, follow
	m.phase = phaseEditing
	m.notices = plan.Notices
	m.lines = nil
	m.fieldErrors = make(map[string]string)
	m.staged = nil
	m.focus = 0

	m.order = m.order[:0]
	for _, field := range plan.Fields {
		input := m.fields[field.ID]
		if input == nil {
			input = &textInput{ID: field.ID}
			m.fields[field.ID] = input
		}
		input.Label, input.Help, input.Optional, input.Secret = field.Label, field.Help, field.Optional, field.Secret
		if field.Secret {
			input.SetValue("")
		} else {
			input.SetValue(field.Default)
		}
		input.Blur()
		m.order = append(m.order, field.ID)
	}

	for _, question := range plan.Questions {
		m.toggles[question.ID] = &toggle{ID: question.ID, Prompt: question.Prompt, Detail: question.Detail, Yes: question.DefaultYes}
	}

	// Step 3 opens on the method chooser; every other step opens on its form.
	if plan.Step == setup.StepPairingMaterial {
		m.method = 0
		m.secret = nil
		m.choices = choiceList{
			Options: []string{
				"Paste the whole output of `rtk gateway participant pair openrig-main` (recommended)",
				"Enter the URL, the certificate pin and the code separately",
			},
			Notes: []string{
				"one paste; the wizard reads the code, the URL and the certificate pin from it",
				"for an output you cannot paste, or when the code is already in a file",
			},
		}
		m.screen = screenChoice
	} else {
		m.screen = screenForm
	}
	m.rebuildControls()
}

// rebuildControls lays out the focusable rows for the current screen.
func (m *model) rebuildControls() {
	m.controls = m.controls[:0]

	switch m.screen {
	case screenForm:
		for index := range m.order {
			m.controls = append(m.controls, control{kind: controlField, index: index})
		}
		for index, question := range m.plan.Questions {
			m.controls = append(m.controls, control{kind: controlToggle, index: index})
			_ = question
		}
		m.controls = append(m.controls, control{kind: controlButtons})
	case screenChoice:
		for index, question := range m.plan.Questions {
			if question.ID == "use" || question.ID == allowHTTPQuestion {
				continue // those consents belong on the review screen
			}
			m.controls = append(m.controls, control{kind: controlToggle, index: index})
		}
		m.controls = append(m.controls, control{kind: controlChoice})
		m.controls = append(m.controls, control{kind: controlButtons})
	case screenPaste:
		m.controls = append(m.controls, control{kind: controlPaste})
		m.controls = append(m.controls, control{kind: controlButtons})
	case screenFields:
		for index := range m.order {
			m.controls = append(m.controls, control{kind: controlField, index: index})
		}
		m.controls = append(m.controls, control{kind: controlButtons})
	case screenReview:
		for index, question := range m.plan.Questions {
			switch question.ID {
			case allowHTTPQuestion:
				if !m.allowsPlainHTTPToggle() {
					continue
				}
			case "use":
			default:
				continue
			}
			m.controls = append(m.controls, control{kind: controlToggle, index: index})
		}
		m.controls = append(m.controls, control{kind: controlButtons})
	}

	if m.focus >= len(m.controls) {
		m.focus = len(m.controls) - 1
	}
	if m.focus < 0 {
		m.focus = 0
	}
	m.refreshButtons()
	m.syncFocus()
}

// syncFocus applies Focus/Blur to the components and refreshes the button row.
func (m *model) syncFocus() {
	for _, input := range m.fields {
		input.Blur()
	}
	for _, item := range m.toggles {
		item.Blur()
	}
	m.choices.Blur()
	m.buttons.Blur()

	current := m.currentControl()
	switch current.kind {
	case controlField:
		if index, ok := m.fieldIndex(current.index); ok {
			m.fields[index].Focus()
		}
	case controlToggle:
		if question, ok := m.toggleAt(current.index); ok {
			m.toggles[question].Focus()
		}
	case controlChoice:
		m.choices.Focus()
	case controlButtons:
		m.buttons.Focus()
	}
}

// currentControl is the focused control, or the button row when there is nothing else.
func (m *model) currentControl() control {
	if m.focus >= 0 && m.focus < len(m.controls) {
		return m.controls[m.focus]
	}
	return control{kind: controlButtons}
}

// fieldIndex turns a form index into a field ID.
func (m *model) fieldIndex(index int) (string, bool) {
	if index < 0 || index >= len(m.order) {
		return "", false
	}
	return m.order[index], true
}

// toggleAt turns a question index into an ID.
func (m *model) toggleAt(index int) (string, bool) {
	if index < 0 || index >= len(m.plan.Questions) {
		return "", false
	}
	return m.plan.Questions[index].ID, true
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
	m.phase = phaseEditing
	res := msg.res

	if msg.discard {
		// The staged block is gone; stay on the method chooser.
		m.staged = nil
		m.screen = screenChoice
		m.rebuildControls()
		if len(res.Lines) > 0 {
			m.lines = res.Lines
		}
		return m, nil
	}

	if res.Status != "" && res.Follow == nil && len(res.FieldErrors) == 0 {
		m.markStep(res.Step, res.Status)
	}

	if res.Stop {
		m.lines = res.Lines
		m.summary = m.engine.Summary()
		m.exitCode = res.ExitCode
		m.phase = phaseSummary
		m.screen = screenSummary
		m.wipeSecrets()
		return m, nil
	}

	if len(res.FieldErrors) > 0 {
		m.fieldErrors = res.FieldErrors
		m.lines = res.Lines
		m.wipeSecrets()
		m.focusFirstError(res.FieldErrors)
		return m, nil
	}

	if res.Follow != nil {
		follow := *res.Follow
		follow.Step = res.Step
		m.enterPlan(follow, true)
		m.lines = res.Lines
		return m, nil
	}

	if msg.step == setup.StepPairingMaterial && m.screen == screenReview && res.Status == "" && len(res.Lines) > 0 {
		// Nothing was applied and nothing failed: the engine is waiting for another consent (a plain
		// http:// gateway needs allow_http). Keep the staged values, stay where the owner is, show why.
		m.lines = res.Lines
		m.refreshButtons()
		return m, nil
	}

	m.lines = res.Lines
	m.changedSomething = true
	m.wipeSecrets()
	m.index++
	if m.index >= len(m.steps) {
		m.summary = m.engine.Summary()
		m.exitCode = m.engine.ExitCode()
		m.phase = phaseSummary
		m.screen = screenSummary
		return m, nil
	}
	return m, m.planCmd()
}

// markStep records a step's status for the sidebar.
func (m *model) markStep(step setup.StepID, status setup.Status) {
	m.statuses()[step] = status
}

// statuses is the map behind the sidebar.
func (m *model) statuses() map[setup.StepID]setup.Status {
	if m.statusMap == nil {
		m.statusMap = make(map[setup.StepID]setup.Status)
	}
	return m.statusMap
}

// onValidated records one validation answer and schedules the newest queued value.
func (m *model) onValidated(msg validateMsg) (tea.Model, tea.Cmd) {
	m.validating = false

	input := m.fields[msg.fieldID]
	if input != nil && m.index < len(m.steps) && m.steps[m.index] == msg.step && input.Value() == msg.value {
		input.SetMessage(msg.message)
		if msg.message == "" {
			delete(m.fieldErrors, msg.fieldID)
		}
	}
	if m.quitWhenIdle {
		m.wipeSecrets()
		return m, tea.Quit
	}

	// The newest value wins: if the owner typed again while this call was in flight, send that now.
	next := ""
	found := false
	for fieldID, value := range m.queued {
		if input := m.fields[fieldID]; input != nil && input.Value() == value {
			next, found = fieldID, true
			break
		}
		delete(m.queued, fieldID)
	}
	if found {
		delete(m.queued, next)
		return m, m.validateCmd(next, m.fields[next].Value())
	}

	m.refreshButtons()
	if m.screen == screenPaste && m.pasteComplete() {
		return m.startPaste()
	}
	if m.quitWhenIdle {
		m.wipeSecrets()
		return m, tea.Quit
	}
	return m, nil
}

// onStage takes a parsed or staged pairing block.
func (m *model) onStage(msg stageMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.busy = false
	if m.quitWhenIdle {
		return m, tea.Quit
	}

	if msg.err != nil {
		// Nothing was staged; the engine's message names the field and never echoes the secret.
		m.lines = []string{msg.err.Error()}
		m.staged = nil
		m.refreshButtons()
		return m, nil
	}

	summary := msg.summary
	m.wipeSecrets()
	m.staged = &summary
	m.changedSomething = true
	m.lines = nil
	m.from = m.screen
	m.screen = screenReview
	m.focus = 0
	m.rebuildControls()
	return m, nil
}

// onPaste appends a bracketed paste to whatever accepts text.
func (m *model) onPaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.phase != phaseEditing || m.busy || msg.Content == "" || m.dialog.open || m.helpOpen {
		return m, nil
	}

	switch m.screen {
	case screenPaste:
		m.secret.appendText(msg.Content, setup.MaxPasteBytes)
		// The arrival source is what distinguishes a block from typing, and it is not content.
		m.secret.pasted = true
		m.changedSomething = true
		if m.pasteComplete() {
			return m.startPaste()
		}
		m.refreshButtons()
		return m, nil
	}

	if current := m.currentControl(); current.kind == controlField {
		if fieldID, ok := m.fieldIndex(current.index); ok {
			input := m.fields[fieldID]
			if input.Secret {
				if m.secret == nil {
					m.secret = &secretInput{}
				}
				m.secret.appendText(msg.Content, setup.MaxCodeBytes)
				m.changedSomething = true
				m.refreshButtons()
				return m, nil
			}
			content := msg.Content
			if fieldID == "url" {
				// A copied URL often carries a line ending. Drop exactly one final LF or CRLF, then
				// trim the gateway's label and surrounding whitespace. A real line break inside the paste
				// is refused below and leaves the field untouched.
				if strings.HasSuffix(content, "\r\n") {
					content = content[:len(content)-2]
				} else if strings.HasSuffix(content, "\n") {
					content = content[:len(content)-1]
				}
				content = strings.TrimSpace(content)
				content = trimParticipantAPILabel(content)
			}
			if strings.ContainsAny(content, "\r\n") {
				input.SetMessage("paste one line into this field")
				m.fieldErrors[fieldID] = input.message
				m.refreshButtons()
				return m, nil
			}
			updated, _ := input.Update(tea.KeyPressMsg{Text: content, Code: tea.KeyExtended})
			*input = updated
			m.changedSomething = true
			m.refreshButtons()
			return m, m.validateCmd(fieldID, input.Value())
		}
	}
	return m, nil
}

// pasteComplete reports whether the buffer already carries the end marker of the printed block.
func (m *model) pasteComplete() bool {
	return m.secret.contains(setup.EndLinePrefix)
}

// startPaste parses the masked block.
func (m *model) startPaste() (tea.Model, tea.Cmd) {
	if m.validating || len(m.queued) > 0 {
		m.lines = []string{"checking a field value; the pasted block will be ready to read when that finishes"}
		return m, nil
	}
	if m.secret == nil || m.secret.empty() {
		m.lines = []string{"nothing was pasted yet"}
		return m, nil
	}
	if m.secret.truncated {
		m.lines = []string{"the pasted block is larger than this wizard accepts; use Clear and paste again"}
		return m, nil
	}
	return m, m.pasteCmd(m.secret.copyBytes())
}

// ---- keys ----------------------------------------------------------------------------------------

// onKey routes one key press.
func (m *model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	ctrl := msg.Mod&tea.ModCtrl != 0

	if ctrl && msg.Code == 'c' {
		return m.requestQuit()
	}

	if m.dialog.open {
		return m.onDialogKey(msg)
	}

	if m.helpOpen {
		switch msg.Code {
		case '?', tea.KeyEscape:
			m.helpOpen = false
		}
		return m, nil
	}

	if m.phase == phaseSummary || m.screen == screenSummary {
		switch msg.Code {
		case tea.KeyEnter, 'q', 'Q':
			return m.quitNow()
		case tea.KeyEscape:
			m.openQuitDialog()
		case tea.KeyPgUp:
			m.pane.ScrollBy(-5)
		case tea.KeyPgDown:
			m.pane.ScrollBy(5)
		case tea.KeyHome:
			m.pane.Top()
		case tea.KeyEnd:
			m.pane.Bottom()
		case '?':
			m.helpOpen = true
		}
		return m, nil
	}

	if m.busy || m.phase != phaseEditing {
		return m, nil
	}

	// In a text field or the paste box, ? is a character to type, not the help key.
	if kind := m.currentControl().kind; msg.Code == '?' && kind != controlField && kind != controlPaste {
		m.helpOpen = true
		return m, nil
	}

	switch msg.Code {
	case tea.KeyEscape:
		return m.back()
	case tea.KeyTab:
		if msg.Mod&tea.ModShift != 0 {
			m.moveFocus(-1)
		} else {
			m.moveFocus(1)
		}
		return m, nil
	case tea.KeyUp:
		if m.currentControl().kind == controlChoice {
			updated, _ := m.choices.Update(msg)
			m.choices = updated
			m.changedSomething = true
			return m, nil
		}
		m.moveFocus(-1)
		return m, nil
	case tea.KeyDown:
		if m.currentControl().kind == controlChoice {
			updated, _ := m.choices.Update(msg)
			m.choices = updated
			m.changedSomething = true
			return m, nil
		}
		m.moveFocus(1)
		return m, nil
	case tea.KeyPgUp:
		m.pane.ScrollBy(-5)
		return m, nil
	case tea.KeyPgDown:
		m.pane.ScrollBy(5)
		return m, nil
	case tea.KeyEnter, tea.KeyKpEnter:
		return m.pressEnter()
	}

	current := m.currentControl()
	switch current.kind {
	case controlField:
		return m.fieldKey(current, msg)
	case controlToggle:
		return m.toggleKey(current, msg)
	case controlChoice:
		updated, _ := m.choices.Update(msg)
		m.choices = updated
		return m, nil
	case controlPaste:
		return m.pasteKey(msg)
	case controlButtons:
		updated, _ := m.buttons.Update(msg)
		m.buttons = updated
		return m, nil
	}
	return m, nil
}

// fieldKey sends a key to the focused field and revalidates when the text changed.
func (m *model) fieldKey(current control, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	fieldID, ok := m.fieldIndex(current.index)
	if !ok {
		return m, nil
	}
	input := m.fields[fieldID]
	if input.Secret {
		if m.secret == nil {
			m.secret = &secretInput{}
		}
		switch msg.Code {
		case tea.KeyBackspace, tea.KeyDelete:
			m.secret.backspace()
		default:
			if msg.Text != "" && msg.Mod&(tea.ModAlt|tea.ModCtrl) == 0 {
				m.secret.appendText(msg.Text, setup.MaxCodeBytes)
			}
		}
		m.changedSomething = true
		m.refreshButtons()
		return m, nil
	}

	// Home and End edit the field; the pane has PageUp/PageDown for scrolling.
	before := input.Value()
	updated, _ := input.Update(msg)
	*input = updated
	if input.Value() != before {
		m.changedSomething = true
		m.refreshButtons()
		return m, m.validateCmd(fieldID, input.Value())
	}
	return m, nil
}

// toggleKey sends a key to the focused consent.
func (m *model) toggleKey(current control, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	question, ok := m.toggleAt(current.index)
	if !ok {
		return m, nil
	}
	item := m.toggles[question]
	updated, _ := item.Update(msg)
	*item = updated
	m.changedSomething = true
	m.refreshButtons()
	return m, nil
}

// pasteKey edits the masked block. Enter adds a line; keys never reach the screen.
func (m *model) pasteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEnter, tea.KeyKpEnter:
		return m.pasteEnter()
	case tea.KeyBackspace:
		m.secret.backspace()
		m.changedSomething = true
		m.refreshButtons()
		return m, nil
	}
	if msg.Text != "" && msg.Mod&(tea.ModAlt|tea.ModCtrl) == 0 {
		m.secret.appendText(msg.Text, setup.MaxPasteBytes)
		m.changedSomething = true
		m.refreshButtons()
	}
	return m, nil
}

// pasteEnter decides what Enter means in the paste box, and always leaves something on screen.
//
// The rules, in order: an empty buffer explains itself; a buffer that already carries the block's last
// line is read at once; a bracketed paste that arrived without that line is read at once too, so the
// parser's recoverable error is visible instead of the screen looking frozen; typed text gets a line
// break and a line of feedback that says how to finish or how to reach the button.
func (m *model) pasteEnter() (tea.Model, tea.Cmd) {
	if m.secret == nil || m.secret.empty() {
		m.lines = []string{"nothing pasted yet: paste the block, or type it, and the wizard reads it when its last line " +
			"begins `the address above was recorded`; Tab reaches Read pasted text."}
		m.refreshButtons()
		return m, nil
	}
	if m.pasteComplete() {
		return m.startPaste()
	}
	if m.secret.pasted {
		return m.startPaste()
	}

	m.secret.appendText("\n", setup.MaxPasteBytes)
	m.changedSomething = true
	if m.pasteComplete() {
		return m.startPaste()
	}
	m.lines = []string{"line added (" + itoa(m.secret.lineCount()) + " lines received). The block is read automatically " +
		"when its last line, beginning `the address above was recorded`, arrives, or press Tab to reach Read pasted text."}
	m.refreshButtons()
	return m, nil
}

// pressEnter implements Enter for the focused control.
func (m *model) pressEnter() (tea.Model, tea.Cmd) {
	current := m.currentControl()
	switch current.kind {
	case controlButtons:
		if reason, blocked := m.buttons.Disabled(); blocked {
			m.lines = []string{reason}
			m.refreshButtons()
			return m, nil
		}
		return m.pressButton(m.buttons.Selected)
	case controlChoice:
		return m.beginMethod()
	case controlField, controlToggle:
		m.moveFocus(1)
		return m, nil
	case controlPaste:
		// Enter closes a typed line; pasteKey also starts the read when that line is the end marker.
		return m.pasteKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	return m, nil
}

// pressButton runs the highlighted button.
func (m *model) pressButton(index int) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenPaste:
		switch index {
		case 0: // Read pasted text
			return m.startPaste()
		case 1: // Clear
			m.secret.wipe()
			m.changedSomething = true
			m.lines = nil
			m.refreshButtons()
			return m, nil
		case 2: // Back
			return m.back()
		}
		return m, nil
	case screenChoice:
		if index == 0 {
			return m.back()
		}
		return m.beginMethod()
	case screenFields:
		if index == 0 {
			return m.back()
		}
		return m.submitFields()
	case screenReview:
		if index == 0 {
			return m.back()
		}
		return m.acceptReview()
	default: // step forms and follow rounds
		if index == 0 {
			return m.back()
		}
		return m.continueForm()
	}
}

// continueForm applies the step with the answers on screen.
func (m *model) continueForm() (tea.Model, tea.Cmd) {
	if reason, blocked := m.continueBlocked(); blocked {
		m.lines = []string{reason}
		return m, nil
	}
	answers := setup.Answers{Text: map[string]string{}, Yes: map[string]bool{}}
	for _, fieldID := range m.order {
		if input := m.fields[fieldID]; input != nil && !input.Secret {
			answers.Text[fieldID] = input.Value()
		}
	}
	for id := range m.toggles {
		answers.Yes[id] = m.toggles[id].Yes
	}
	return m, m.applyCmd(answers, false)
}

// beginMethod opens the chosen way of supplying the pairing material.
func (m *model) beginMethod() (tea.Model, tea.Cmd) {
	// "keep" with an existing credential needs nothing else: apply it straight away.
	if item, ok := m.toggles["keep"]; ok && item.Yes {
		return m, m.applyCmd(setup.Answers{Yes: map[string]bool{"keep": true}}, false)
	}

	// Switching method discards anything staged.
	var discard tea.Cmd
	if m.staged != nil {
		m.staged = nil
		discard = m.applyCmd(setup.Answers{Yes: map[string]bool{"use": false}}, true)
	}

	m.secret.wipe()
	m.secret = &secretInput{}
	m.method = m.choices.Selected
	if m.choices.Selected == 0 {
		m.screen = screenPaste
	} else {
		m.screen = screenFields
	}
	m.focus = 0
	m.lines = nil
	m.rebuildControls()
	return m, discard
}

// submitFields stages the typed URL, pin and code.
func (m *model) submitFields() (tea.Model, tea.Cmd) {
	url := m.fieldValue("url")
	pin := m.fieldValue("pin")
	var code []byte
	if m.secret != nil {
		code = m.secret.take()
	}
	m.wipeSecrets()
	return m, m.fieldsCmd(url, pin, code)
}

// acceptReview applies the pairing material with the engine's own consent answer.
func (m *model) acceptReview() (tea.Model, tea.Cmd) {
	return m, m.applyCmd(m.reviewAnswers(), false)
}

// reviewAnswers returns only the consents visible for the staged pairing material.
func (m *model) reviewAnswers() setup.Answers {
	answers := setup.Answers{Yes: map[string]bool{}}
	if item, ok := m.toggles["use"]; ok {
		answers.Yes["use"] = item.Yes
	}
	// The transport consent travels with the answer, and only when it was offered and shown. Nothing
	// here turns it on: it is exactly what the owner chose, which starts at No.
	if m.allowsPlainHTTPToggle() {
		if item, ok := m.toggles[allowHTTPQuestion]; ok {
			answers.Yes[allowHTTPQuestion] = item.Yes
		}
	}
	return answers
}

// fieldValue is a field's current text.
func (m *model) fieldValue(fieldID string) string {
	if input, ok := m.fields[fieldID]; ok {
		return input.Value()
	}
	return ""
}

// back goes up one level: out of a subview, or into the quit dialog at a step's top level.
func (m *model) back() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenReview:
		m.screen = m.from
		m.focus = 0
		m.rebuildControls()
		return m, nil
	case screenPaste, screenFields:
		m.wipeSecrets()
		if m.screen == screenFields {
			for _, fieldID := range []string{"url", "pin"} {
				if input := m.fields[fieldID]; input != nil {
					input.SetValue("")
				}
			}
		}
		m.lines = nil
		if m.staged != nil {
			// A staged block must not survive leaving review for the chooser: discard it through the
			// engine's own use=false path so its secret is wiped there too.
			staged := m.staged
			m.staged = nil
			_ = staged
			m.screen = screenChoice
			m.rebuildControls()
			return m, m.applyCmd(setup.Answers{Yes: map[string]bool{"use": false}}, true)
		}
		m.screen = screenChoice
		m.focus = 0
		m.rebuildControls()
		return m, nil
	case screenChoice:
		m.openQuitDialog()
		return m, nil
	}
	m.openQuitDialog()
	return m, nil
}

// openQuitDialog shows the confirmation, defaulting to Stay.
func (m *model) openQuitDialog() {
	title := "Quit setup? Nothing more will be changed."
	body := []string{"Steps that are already done stay done.", "Steps that are not done stay undone.", ""}
	if m.busy || m.validating {
		title = "Quit setup? The current step will finish first."
		body = []string{"The current operation finishes before the program exits.", "No later step will start.", ""}
	}
	m.dialog = dialog{
		Title:   title,
		Body:    body,
		Details: m.doneLines(),
		Buttons: buttonRow{Labels: []string{"Stay", "Quit"}},
	}
	m.dialog.Open()
}

// doneLines lists the steps already completed, for the quit dialog.
func (m *model) doneLines() []string {
	var lines []string
	for _, step := range m.steps {
		if status, ok := m.statuses()[step]; ok && status != setup.StatusNotDone && status != setup.StatusYourTurn {
			lines = append(lines, step.Title()+" ("+string(status)+")")
		}
	}
	return lines
}

// requestQuit opens the dialog, or quits at once when nothing has happened yet.
func (m *model) requestQuit() (tea.Model, tea.Cmd) {
	if !m.changedSomething && len(m.statuses()) == 0 && !m.busy && !m.validating {
		return m.quitNow()
	}
	m.openQuitDialog()
	return m, nil
}

// quitNow ends the program.
func (m *model) quitNow() (tea.Model, tea.Cmd) {
	m.wipeSecrets()
	if m.busy || m.validating {
		m.quitWhenIdle = true
		return m, nil
	}
	return m, tea.Quit
}

// onDialogKey drives the quit dialog.
func (m *model) onDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		m.dialog.Close()
		return m, nil
	case tea.KeyEnter, tea.KeyKpEnter:
		if m.dialog.QuitSelected() {
			return m.quitNow()
		}
		m.dialog.Close()
		return m, nil
	}
	if msg.Code == tea.KeyLeft || msg.Code == tea.KeyRight || msg.Code == tea.KeyTab {
		updated, _ := m.dialog.Update(msg)
		m.dialog = updated
	}
	return m, nil
}

// moveFocus steps through the focusable rows.
func (m *model) moveFocus(delta int) {
	if len(m.controls) == 0 {
		return
	}
	m.focus += delta
	if m.focus < 0 {
		m.focus = len(m.controls) - 1
	}
	if m.focus >= len(m.controls) {
		m.focus = 0
	}
	m.syncFocus()
	m.refreshButtons()
}

// focusFirstError puts the cursor on the first field the engine rejected.
func (m *model) focusFirstError(errors map[string]string) {
	for index, fieldID := range m.order {
		if _, failed := errors[fieldID]; failed {
			for position, item := range m.controls {
				if item.kind == controlField && item.index == index {
					m.focus = position
					m.syncFocus()
					return
				}
			}
		}
	}
}

// refreshButtons recomputes what the button row offers and why Continue may be blocked.
func (m *model) refreshButtons() {
	switch m.screen {
	case screenPaste:
		m.buttons = buttonRow{Labels: []string{"Read pasted text", "Clear", "Back"}}
		blocked := ""
		if m.secret == nil || m.secret.empty() {
			blocked = "nothing pasted yet: paste the block, or go Back"
		} else if m.secret.truncated {
			blocked = "the pasted block is too large: use Clear"
		}
		m.buttons.blocked = []string{blocked, "", ""}
		if m.buttons.Selected >= len(m.buttons.Labels) {
			m.buttons.Selected = 0
		}
	case screenChoice:
		m.buttons = buttonRow{Labels: []string{"Back", "Continue"}, Selected: 1}
		m.buttons.blocked = []string{m.backBlockedReason(), m.choiceBlocked()}
	case screenFields:
		m.buttons = buttonRow{Labels: []string{"Back", "Continue"}, Selected: 1}
		m.buttons.blocked = []string{"", m.fieldsBlocked()}
	case screenReview:
		m.buttons = buttonRow{Labels: []string{"Back", "Continue"}, Selected: 1}
		m.buttons.blocked = []string{"", ""}
	default:
		m.buttons = buttonRow{Labels: []string{"Back", "Continue"}, Selected: 1}
		m.buttons.blocked = []string{m.backBlockedReason(), m.continueBlockedReason()}
	}
	if m.focus < len(m.controls) && m.controls[m.focus].kind == controlButtons {
		m.buttons.Focus()
	}
}

func (m *model) backBlockedReason() string {
	if m.index <= 0 {
		return "there is no earlier step"
	}
	return "earlier steps are already applied and cannot be reopened"
}

// continueBlocked reports whether Continue cannot act, and why.
func (m *model) continueBlocked() (string, bool) {
	if m.buttons.Selected >= len(m.buttons.blocked) {
		return "", false
	}
	if reason := m.buttons.blocked[m.buttons.Selected]; reason != "" {
		return reason, true
	}
	return "", false
}

// continueBlockedReason is the form's own rule.
func (m *model) continueBlockedReason() string {
	if m.validating || len(m.queued) > 0 {
		return "checking the latest field value"
	}
	failing := 0
	for _, fieldID := range m.order {
		if m.fieldErrors[fieldID] != "" {
			failing++
		}
	}
	if failing == 1 {
		return "1 field needs attention"
	}
	if failing > 1 {
		return itoa(failing) + " fields need attention"
	}
	for _, fieldID := range m.order {
		if input := m.fields[fieldID]; input != nil && !input.Optional && strings.TrimSpace(input.Value()) == "" {
			return "a required field is still empty"
		}
	}
	return ""
}

// choiceBlocked is why the method chooser cannot continue.
func (m *model) choiceBlocked() string {
	if m.busy {
		return "a step is already running"
	}
	if m.validating || len(m.queued) > 0 {
		return "a field check is still running"
	}
	return ""
}

// fieldsBlocked is why the typed values cannot be staged yet.
func (m *model) fieldsBlocked() string {
	if m.validating || len(m.queued) > 0 {
		return "checking the latest field value"
	}
	failing := 0
	for _, fieldID := range []string{"url", "pin"} {
		if m.fieldErrors[fieldID] != "" {
			failing++
		}
	}
	if failing == 1 {
		return "1 field needs attention"
	}
	if failing > 1 {
		return itoa(failing) + " fields need attention"
	}
	if strings.TrimSpace(m.fieldValue("url")) == "" {
		return "the participant API URL is still empty"
	}
	if needsPinForURL(m.fieldValue("url")) && strings.TrimSpace(m.fieldValue("pin")) == "" {
		return "the certificate pin is required for an https:// gateway"
	}
	if m.plan.NeedCode && (m.secret == nil || m.secret.empty() || m.secret.truncated) {
		return "the pairing code is empty or too long"
	}
	return ""
}

// participantAPILabel is the label the gateway prints in front of the participant API address.
const participantAPILabel = "participant API:"

// trimParticipantAPILabel drops one leading "participant API:" label, in any case, and the whitespace
// around it. Anything else is returned unchanged, including a value that merely contains the words.
func trimParticipantAPILabel(text string) string {
	if len(text) < len(participantAPILabel) {
		return text
	}
	if !strings.EqualFold(text[:len(participantAPILabel)], participantAPILabel) {
		return text
	}
	return strings.TrimSpace(text[len(participantAPILabel):])
}

// needsPinForURL reports whether this address needs a certificate pin: only https does. A plain http
// address has no certificate to pin, and a pin typed by mistake is left for the engine to refuse.
func needsPinForURL(url string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "https://")
}

// isPlainHTTPURL reports whether a typed address is plain http.
func isPlainHTTPURL(url string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "http://")
}

// wipeSecrets zeroes every masked buffer the model holds.
func (m *model) wipeSecrets() {
	if m.secret != nil {
		m.secret.wipe()
		m.secret = nil
	}
	if input, ok := m.fields["code"]; ok && input.Secret {
		input.SetValue("")
	}
}

// ---- view ----------------------------------------------------------------------------------------

// View renders the whole screen.
func (m *model) View() tea.View { return tea.NewView(m.render()) }

// render builds the screen text.
func (m *model) render() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	if width < minWidth || height < minHeight {
		return "terminal too small: make it at least " + itoa(minWidth) + "x" + itoa(minHeight) + "\n" +
			"(the wizard keeps its place; resize and carry on)\n"
	}

	if m.dialog.open {
		contentWidth := minInt(width-6, 68)
		if contentWidth < 24 {
			contentWidth = width - 2
		}
		content := lipgloss.NewStyle().Width(contentWidth).Border(lipgloss.RoundedBorder()).Padding(1, 2).
			Render(m.dialog.View(m.theme, contentWidth-4))
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content) + "\n"
	}
	if m.screen == screenSummary || m.phase == phaseSummary {
		return m.renderSummary(width, height)
	}

	header := m.renderHeader(width)
	bodyWidth := width
	sidebar := []string(nil)
	if width >= sidebarMinWidth {
		sidebar = m.renderSidebar()
		bodyWidth = width - sidebarWidth
	}

	body := m.renderBody(bodyWidth, height-len(header)-footerLines-1)
	footer := m.renderFooter(width)

	if len(sidebar) == 0 {
		return strings.Join(append(append(header, body...), footer...), "\n") + "\n"
	}

	left := lipgloss.NewStyle().Width(sidebarWidth).Render(strings.Join(sidebar, "\n"))
	right := strings.Join(body, "\n")
	main := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	return strings.Join(append(append(header, strings.Split(main, "\n")...), footer...), "\n") + "\n"
}

// renderHeader is the title, the step and the progress bar.
func (m *model) renderHeader(width int) []string {
	title := m.theme.accent("Ratatoskr ⇄ OpenRig setup")
	lines := []string{title}

	if m.index < len(m.steps) {
		step := m.steps[m.index]
		lines = append(lines, terminalText(fmt.Sprintf("Step %d of %d · %s", m.index+1, len(m.steps), step.Title())))
	} else {
		lines = append(lines, "Setup complete")
	}

	done := 0
	for _, step := range m.steps {
		if status, ok := m.statuses()[step]; ok && status != setup.StatusNotDone && status != setup.StatusYourTurn {
			done++
		}
	}
	countLabel := itoa(done) + "/" + itoa(len(m.steps)) + " steps done"
	barWidth := width - lipgloss.Width(countLabel) - 2
	if barWidth > 40 {
		barWidth = 40
	}
	if barWidth < 1 {
		barWidth = 1
	}
	lines = append(lines, renderProgress(done, len(m.steps), barWidth, m.theme)+"  "+countLabel)

	if width < sidebarMinWidth {
		lines = append(lines, m.renderStepsInline(width))
	}
	return lines
}

// renderStepsInline folds the step list into one line for narrow terminals.
func (m *model) renderStepsInline(width int) string {
	parts := make([]string, 0, len(m.steps))
	for index, step := range m.steps {
		mark := "○"
		if index == m.index {
			mark = "●"
		} else if status, ok := m.statuses()[step]; ok {
			switch status {
			case setup.StatusDone, setup.StatusAlready:
				mark = "✓"
			case setup.StatusSkipped:
				mark = "–"
			case setup.StatusNotDone, setup.StatusYourTurn:
				mark = "!"
			}
		}
		parts = append(parts, mark)
	}
	return truncate(strings.Join(parts, " ")+"  ("+itoa(m.index+1)+"/"+itoa(len(m.steps))+")", width)
}

// renderSidebar is the nine-step list.
func (m *model) renderSidebar() []string {
	steps := make([]setupStepStatus, 0, len(m.steps))
	for _, step := range m.steps {
		steps = append(steps, setupStepStatus{Title: step.Title(), Status: m.statuses()[step]})
	}
	return renderSidebar(steps, m.index, sidebarWidth-1, m.theme)
}

// renderBody is the scrolling pane plus the footer-independent content.
func (m *model) renderBody(width, height int) []string {
	lines := m.buildBody(width)
	if height < 4 {
		height = 4
	}
	m.pane.height = height
	m.pane.SetLines(lines)
	if m.helpOpen {
		m.pane.Bottom()
	} else {
		m.pane.EnsureRow(m.focusRowInBody(lines))
	}
	visible := m.pane.View()

	out := make([]string, 0, len(visible)+2)
	above, below := m.pane.More()
	if above {
		out = append(out, m.theme.dim("  ^ more above (PageUp)"))
	}
	out = append(out, visible...)
	if below {
		out = append(out, m.theme.dim("  v more below (PageDown)"))
	}
	return out
}

// focusRowInBody finds the rendered line of the focused control, for auto-scrolling.
func (m *model) focusRowInBody(lines []string) int {
	if len(m.controls) == 0 {
		return 0
	}
	current := m.currentControl()
	switch current.kind {
	case controlField:
		if fieldID, ok := m.fieldIndex(current.index); ok {
			if input := m.fields[fieldID]; input != nil {
				for index, line := range lines {
					if strings.Contains(line, input.Label+": ") {
						return index
					}
				}
			}
		}
	case controlToggle:
		if question, ok := m.toggleAt(current.index); ok {
			if item := m.toggles[question]; item != nil {
				for index, line := range lines {
					if strings.Contains(line, item.Prompt) {
						return index
					}
				}
			}
		}
	case controlChoice:
		for index, line := range lines {
			if strings.Contains(line, "How do you want to supply") {
				return index
			}
		}
	case controlPaste:
		for index, line := range lines {
			if strings.Contains(line, "Pasted block") {
				return index
			}
		}
	case controlButtons:
		for index, line := range lines {
			if strings.Contains(line, "[ Back ]") {
				return index
			}
		}
	}
	return 0
}

// buildBody renders the pane content for the current screen.
func (m *model) buildBody(width int) []string {
	var lines []string
	wrap := func(text string) {
		lines = append(lines, wrapText(terminalText(text), width)...)
	}
	for _, titleLine := range wrapText(terminalText(m.steps[m.index].Title()), width) {
		lines = append(lines, m.theme.accent(titleLine))
	}
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
		for _, piece := range wrapText(terminalText(label+notice.Text), width) {
			if notice.Level == setup.Warn && strings.HasPrefix(piece, "WARNING:") {
				piece = m.theme.warn("WARNING:") + strings.TrimPrefix(piece, "WARNING:")
			}
			lines = append(lines, piece)
		}
	}
	for _, change := range m.plan.Changes {
		wrap("  ~ " + change.Field + ": " + change.Old + " -> " + change.New)
	}

	if m.busy {
		lines = append(lines, "")
		for _, piece := range wrapText("  "+spinnerFrames[m.frame%len(spinnerFrames)]+" "+terminalText(m.busyLabel), width) {
			lines = append(lines, m.theme.accent(piece))
		}
	}

	if !m.busy {
		switch m.screen {
		case screenChoice:
			lines = append(lines, "")
			for _, piece := range wrapText("How do you want to supply the pairing material?", width) {
				lines = append(lines, m.theme.accent(piece))
			}
			lines = append(lines, "")
			// The consents come first, matching the focus order rebuildControls gives them.
			for _, question := range m.plan.Questions {
				if question.ID == "use" || question.ID == allowHTTPQuestion {
					continue
				}
				if item := m.toggles[question.ID]; item != nil {
					lines = append(lines, strings.Split(item.View(m.theme, width), "\n")...)
				}
			}
			lines = append(lines, strings.Split(m.choices.View(m.theme, width), "\n")...)
		case screenPaste:
			lines = append(lines, "")
			lines = append(lines, m.renderPasteBox(width)...)
		case screenFields:
			lines = append(lines, "")
			for _, fieldID := range m.order {
				if input := m.fields[fieldID]; input != nil {
					lines = append(lines, strings.Split(m.renderField(fieldID), "\n")...)
				}
				if fieldID == "pin" && isPlainHTTPURL(m.fieldValue("url")) {
					// A plain http:// address has no certificate to pin; say so beside the field
					// without changing the engine's own Help text.
					lines = append(lines, "    "+m.theme.dim("not used for http:// — leave this empty"))
				}
			}
		case screenReview:
			lines = append(lines, "")
			lines = append(lines, m.renderReview(width)...)
		default:
			if len(m.plan.Fields) > 0 || len(m.plan.Questions) > 0 {
				lines = append(lines, "")
			}
			for _, fieldID := range m.order {
				if input := m.fields[fieldID]; input != nil {
					lines = append(lines, strings.Split(m.renderField(fieldID), "\n")...)
				}
			}
			for _, question := range m.plan.Questions {
				if item := m.toggles[question.ID]; item != nil {
					lines = append(lines, strings.Split(item.View(m.theme, width), "\n")...)
				}
			}
		}
	}

	if !m.busy && m.showsButtons() {
		lines = append(lines, "")
		lines = append(lines, strings.Split(m.buttons.View(m.theme, width), "\n")...)
	}

	for _, line := range m.lines {
		wrap("  " + line)
	}

	if m.helpOpen {
		lines = append(lines, "")
		lines = append(lines, m.renderHelp(width)...)
	}
	return lines
}

func (m *model) renderField(fieldID string) string {
	input := m.fields[fieldID]
	if input == nil {
		return ""
	}
	if !input.Secret {
		return input.View(m.theme)
	}
	marker := "  "
	if input.focus {
		marker = "> "
	}
	value := m.theme.dim(terminalText(input.Placeholder))
	if m.secret != nil && !m.secret.empty() {
		value = m.theme.accent(maskPresence(m.secret.size()))
	}
	line := marker + terminalText(input.Label) + ": " + value
	if input.Help != "" {
		line += "\n    " + terminalText(input.Help)
	}
	if input.message != "" {
		line += "\n    " + m.theme.err("! "+terminalText(input.message))
	}
	return line + "\n"
}

// showsButtons reports whether the current screen has a button row.
func (m *model) showsButtons() bool {
	return m.screen != screenSummary
}

// renderPasteBox draws the masked area and its buttons.
func (m *model) renderPasteBox(width int) []string {
	label := "Pasted block"
	content := m.theme.dim("waiting for paste...") + "\n" +
		m.theme.dim("paste with your terminal's paste shortcut, or type and press enter for each line")
	if m.secret != nil && m.secret.size() > 0 {
		content = m.theme.ok("● "+maskPresence(m.secret.size())) + "\n" +
			m.theme.dim("the content is hidden; it is cleared after a successful read or when you leave this screen")
	}
	if m.secret != nil && m.secret.truncated {
		content += "\n" + m.theme.err("! too long: use Clear, then paste again")
	}

	lines := strings.Split(box(m.theme, width, label+"\n"+content), "\n")
	lines = append(lines, "")
	lines = append(lines, m.theme.dim("  the block ends with a line beginning `the address above was recorded`; the wizard reads it automatically"))
	return lines
}

// renderReview shows the parsed values and the consent.
func (m *model) renderReview(width int) []string {
	if m.staged == nil {
		return []string{m.theme.dim("  nothing parsed yet")}
	}
	summary := m.staged
	lines := []string{
		m.theme.accent("Review"),
		"",
		"  participant URL: " + terminalText(summary.URL),
	}
	if summary.Pin != "" {
		lines = append(lines, "  certificate pin: "+terminalText(summary.Pin))
	}
	if summary.Plain {
		lines = append(lines, "  "+m.theme.warn("WARNING: the participant API is plain HTTP"))
	}
	if summary.Rotation {
		lines = append(lines, "  note: this is a rotation; the old credential stops working when the new code is redeemed")
	}
	if summary.StaleNote != "" {
		for _, piece := range wrapText(terminalText("  "+summary.StaleNote), width) {
			lines = append(lines, piece)
		}
	}
	if summary.CodeReceived {
		lines = append(lines, "  pairing code: received")
	} else {
		lines = append(lines, "  pairing code: not used by this step")
	}
	lines = append(lines, "")
	for _, question := range m.plan.Questions {
		switch question.ID {
		case allowHTTPQuestion:
			if !m.allowsPlainHTTPToggle() {
				continue
			}
		case "use":
		default:
			continue
		}
		if item := m.toggles[question.ID]; item != nil {
			lines = append(lines, strings.Split(item.View(m.theme, width), "\n")...)
		}
	}
	return lines
}

// allowHTTPQuestion is the engine's transport-consent question ID.
const allowHTTPQuestion = "allow_http"

// allowsPlainHTTPToggle reports whether the transport consent belongs on screen now: the engine offered
// it, and the staged address is plain HTTP.
func (m *model) allowsPlainHTTPToggle() bool {
	if m.staged == nil || !m.staged.Plain {
		return false
	}
	_, offered := m.toggles[allowHTTPQuestion]
	return offered
}

// renderFooter is the persistent key legend plus the context line.
func (m *model) renderFooter(width int) []string {
	if m.helpOpen {
		return []string{m.theme.dim("? or esc closes the help")}
	}
	legend := "tab/↑/↓ move · enter next · esc back · ? help · ctrl+c quit"
	switch m.currentControl().kind {
	case controlField:
		legend = "type to edit · enter next · ↑/↓ move · esc back · ctrl+c quit"
	case controlToggle:
		legend = "space/←/→ or y/n toggle · enter next · esc back · ? help · ctrl+c quit"
	case controlChoice:
		legend = "↑/↓ choose · enter select · esc back · ? help · ctrl+c quit"
	case controlPaste:
		legend = "paste, or type a line and press enter · tab for the buttons · esc back · ctrl+c quit"
	case controlButtons:
		legend = "←/→ choose · enter press · tab move · esc back · ? help · ctrl+c quit"
	}
	if m.busy {
		legend = "ctrl+c: quit after this step finishes"
	}
	context := ""
	if m.width >= sidebarMinWidth {
		context = m.theme.dim("step " + itoa(m.index+1) + " of " + itoa(len(m.steps)))
	}
	return []string{truncate(legend, width), context}
}

// renderHelp is the non-modal full help.
func (m *model) renderHelp(width int) []string {
	lines := []string{m.theme.accent("Keys")}
	lines = append(lines, wrapText("tab, shift+tab, up, down: move between the controls", width)...)
	lines = append(lines, wrapText("enter: confirm the field and move on, press the highlighted button, or accept a choice", width)...)
	lines = append(lines, wrapText("space, left, right, y, n: switch a Yes/No consent", width)...)
	lines = append(lines, wrapText("page up, page down: scroll this pane", width)...)
	lines = append(lines, wrapText("esc: back one level, or open the quit dialog at a step's top level", width)...)
	lines = append(lines, wrapText("?: close this help; ctrl+c: quit setup", width)...)
	return lines
}

// renderSummary is the closing checklist.
func (m *model) renderSummary(width, height int) string {
	var lines []string
	lines = append(lines, m.theme.accent("Ratatoskr ⇄ OpenRig setup"), "")

	rows := m.summary.Rows
	if len(rows) == 0 {
		for _, step := range m.steps {
			status := setup.StatusNotDone
			if recorded, ok := m.statuses()[step]; ok {
				status = recorded
			}
			rows = append(rows, setup.Row{Step: step, Status: status})
		}
	}

	lines = append(lines, m.theme.accent("Checklist"))
	for _, row := range rows {
		mark := statusMark(row.Status)
		line := "  " + mark + " " + terminalText(row.Step.Title()) + ": " + terminalText(string(row.Status))
		if row.Detail != "" {
			line += " - " + terminalText(row.Detail)
		}
		lines = append(lines, wrapText(line, width)...)
	}

	todo := m.summary.Todo
	if !endsWithRoundTrip(todo) {
		todo = append(todo, roundTripLine)
	}
	if len(todo) > 0 {
		lines = append(lines, "")
		lines = append(lines, m.theme.accent("YOUR TURN"))
		for _, item := range todo {
			lines = append(lines, wrapText(terminalText("  - "+item), width)...)
		}
	}

	if len(m.lines) > 0 {
		lines = append(lines, "")
		for _, line := range m.lines {
			lines = append(lines, wrapText(terminalText(line), width)...)
		}
	}

	// The checklist scrolls like every other screen, so a long list of YOUR TURN rows cannot be cut off.
	m.pane.height = height - 1
	m.pane.SetLines(lines)
	visible := m.pane.View()
	above, below := m.pane.More()

	out := make([]string, 0, len(visible)+3)
	if above {
		out = append(out, m.theme.dim("^ more above (PageUp)"))
	}
	out = append(out, visible...)
	if below {
		out = append(out, m.theme.dim("v more below (PageDown)"))
	}
	out = append(out, m.theme.dim("enter or q: exit"))
	return strings.Join(out, "\n") + "\n"
}

// statusWord is the plain word for a status, so the sidebar never relies on a mark or a colour alone.
func statusWord(status setup.Status) string {
	switch status {
	case setup.StatusDone:
		return "done"
	case setup.StatusAlready:
		return "already done"
	case setup.StatusSkipped:
		return "skipped"
	case setup.StatusNotDone:
		return "not done"
	case setup.StatusYourTurn:
		return "your turn"
	}
	return ""
}

// statusMark is the icon for a status; the status word is always shown beside it.
func statusMark(status setup.Status) string {
	switch status {
	case setup.StatusDone, setup.StatusAlready:
		return "✓"
	case setup.StatusSkipped:
		return "–"
	case setup.StatusYourTurn:
		return "!"
	case setup.StatusNotDone:
		return "!"
	}
	return "○"
}

// roundTripLine is the fixed last line of every checklist (contract 4.1 step 9).
const roundTripLine = "scenario 8 round trip: PENDING USER EXECUTION"

// endsWithRoundTrip reports whether the checklist already carries it.
func endsWithRoundTrip(todo []string) bool {
	return len(todo) > 0 && todo[len(todo)-1] == roundTripLine
}
