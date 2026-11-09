// UNTESTED (GR-3): these tests have never been run. Compile checks only.

package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/setup"
)

// fakeEngine is a driver-able engine: it records calls and answers from fields the test sets.
type fakeEngine struct {
	plan        setup.Plan
	planErr     error
	results     []setup.Result
	applyCalls  []setup.Answers
	validated   []string
	pasteErr    error
	pasteCalls  int
	parsedPaste []byte
	staged      setup.PasteSummary
	stageErr    error
	stageCode   []byte
	summary     setup.Summary
	exitCode    int
}

func (f *fakeEngine) Plan(context.Context, setup.StepID) (setup.Plan, error) {
	return f.plan, f.planErr
}

func (f *fakeEngine) Apply(_ context.Context, _ setup.StepID, a setup.Answers) setup.Result {
	f.applyCalls = append(f.applyCalls, a)
	if len(f.results) == 0 {
		return setup.Result{Status: setup.StatusDone}
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

func (f *fakeEngine) Validate(_ setup.StepID, id, value string) string {
	f.validated = append(f.validated, id+"="+value)
	if strings.HasPrefix(value, "bad") {
		return "not acceptable"
	}
	return ""
}

func (f *fakeEngine) ParsePaste(block []byte) (setup.PasteSummary, error) {
	f.pasteCalls++
	f.parsedPaste = append(f.parsedPaste[:0], block...)
	setup.Wipe(block)
	return f.staged, f.pasteErr
}
func (f *fakeEngine) StagePair(_, _ string, code []byte) (setup.PasteSummary, error) {
	f.stageCode = append([]byte(nil), code...)
	setup.Wipe(code)
	return f.staged, f.stageErr
}
func (f *fakeEngine) Summary() setup.Summary { return f.summary }
func (f *fakeEngine) ExitCode() int          { return f.exitCode }
func (f *fakeEngine) Close()                 {}

// newTestModel builds a model already showing a plan, without going through a command.
func newTestModel(fake *fakeEngine, plan setup.Plan) *model {
	m := newModel(fake, context.Background())
	m.width, m.height = 100, 30
	m.enterPlan(plan, false)
	return m
}

// runLastBatchCommand runs the final child of a batch, which is the operation result; it deliberately
// skips the earlier spinner timer command so these source-only tests do not wait for a tick.
func runLastBatchCommand(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return msg
	}
	if len(batch) == 0 {
		t.Fatal("command returned an empty batch")
	}
	return batch[len(batch)-1]()
}

// key is a convenience for building a key press.
func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// text is a convenience for building a printable key.
func text(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s, Code: rune(s[0])} }

// TestFocusOrder walks tab through a form and back again.
func TestFocusOrder(t *testing.T) {
	plan := setup.Plan{
		Step:      setup.StepLocalOpenRig,
		Fields:    []setup.Field{{ID: "rig_path", Label: "rig"}, {ID: "openrig_home", Label: "home"}},
		Questions: []setup.Question{{ID: "check", Prompt: "Check the seat?", DefaultYes: true}},
	}
	m := newTestModel(&fakeEngine{}, plan)

	if len(m.controls) != 4 { // two fields, one toggle, the button row
		t.Fatalf("%d controls, want 4", len(m.controls))
	}

	want := []controlKind{controlField, controlField, controlToggle, controlButtons}
	for index, kind := range want {
		if m.currentControl().kind != kind {
			t.Fatalf("focus %d is %v, want %v", index, m.currentControl().kind, kind)
		}
		if index < len(want)-1 {
			m.moveFocus(1)
		}
	}

	m.moveFocus(1) // wraps to the first control
	if m.currentControl().kind != controlField || m.focus != 0 {
		t.Fatalf("focus did not wrap: %v at %d", m.currentControl().kind, m.focus)
	}

	m.moveFocus(-1)
	if m.currentControl().kind != controlButtons {
		t.Fatalf("shift+tab from the first control = %v, want the button row", m.currentControl().kind)
	}
}

// TestEnterPerControl: Enter advances from a field, from a toggle, and presses Continue.
func TestEnterPerControl(t *testing.T) {
	plan := setup.Plan{
		Step:      setup.StepLocalOpenRig,
		Fields:    []setup.Field{{ID: "rig_path", Label: "rig", Default: "/tmp/rig"}},
		Questions: []setup.Question{{ID: "check", Prompt: "Check?", DefaultYes: true}},
	}
	fake := &fakeEngine{}
	m := newTestModel(fake, plan)

	if _, cmd := m.Update(key(tea.KeyEnter)); cmd != nil {
		t.Fatal("enter on a field scheduled a command")
	}
	if m.currentControl().kind != controlToggle {
		t.Fatalf("enter on a field left focus on %v", m.currentControl().kind)
	}

	m.Update(key(tea.KeyEnter))
	if m.currentControl().kind != controlButtons {
		t.Fatalf("enter on a toggle left focus on %v", m.currentControl().kind)
	}

	_, cmd := m.Update(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("enter on Continue scheduled no command")
	}
	if len(fake.applyCalls) != 0 {
		t.Fatal("apply ran inside Update instead of a command")
	}
	if _, ok := runLastBatchCommand(t, cmd).(applyMsg); !ok {
		t.Fatal("the Continue command did not report an apply result")
	}
}

// TestConsentStartsAtDefaultYes: the toggle is exactly the engine's ruling, both ways.
func TestConsentStartsAtDefaultYes(t *testing.T) {
	for _, defaultYes := range []bool{true, false} {
		plan := setup.Plan{
			Step:      setup.StepLocalOpenRig,
			Questions: []setup.Question{{ID: "check", Prompt: "Check?", DefaultYes: defaultYes}},
		}
		m := newTestModel(&fakeEngine{}, plan)
		if got := m.toggles["check"].Yes; got != defaultYes {
			t.Fatalf("toggle started at %v, want %v", got, defaultYes)
		}
	}
}

// TestEscBacksOutAndQuitDefaultsToStay: esc opens the dialog with Stay focused and esc closes it.
func TestEscBacksOutAndQuitDefaultsToStay(t *testing.T) {
	plan := setup.Plan{Step: setup.StepLocalOpenRig, Fields: []setup.Field{{ID: "rig_path", Label: "rig"}}}
	m := newTestModel(&fakeEngine{}, plan)

	m.Update(key(tea.KeyEscape))
	if !m.dialog.open {
		t.Fatal("esc at a step's top level did not open the quit dialog")
	}
	if m.dialog.Buttons.Selected != 0 {
		t.Fatalf("quit dialog focused button %d, want Stay (0)", m.dialog.Buttons.Selected)
	}

	if _, cmd := m.Update(key(tea.KeyEnter)); cmd != nil {
		t.Fatal("pressing Stay quit the program")
	}
	if m.dialog.open {
		t.Fatal("Stay did not close the dialog")
	}
}

// TestQuitWhileBusyWaits: a quit request during a command finishes only when the command reports.
func TestQuitWhileBusyWaits(t *testing.T) {
	plan := setup.Plan{Step: setup.StepLocalOpenRig, Auto: true}
	m := newTestModel(&fakeEngine{}, plan)
	m.busy = true
	m.busyLabel = "applying"

	if _, cmd := m.quitNow(); cmd != nil {
		t.Fatal("quit ran while a command was in flight")
	}
	if !m.quitWhenIdle {
		t.Fatal("the quit was not recorded as pending")
	}

	_, cmd := m.Update(applyMsg{gen: m.gen, res: setup.Result{Status: setup.StatusDone}})
	if cmd == nil {
		t.Fatal("the pending quit did not fire when the command answered")
	}
}

// TestPasteAutoParses: a bracketed paste ending with the block's last line parses at once, with no Ctrl
// key and no button press.
func TestPasteAutoParses(t *testing.T) {
	plan := setup.Plan{Step: setup.StepPairingMaterial, PasteBlock: true, NeedCode: true}
	fake := &fakeEngine{staged: setup.PasteSummary{URL: "https://10.0.0.5:7790", CodeReceived: true}}
	m := newTestModel(fake, plan)
	m.choices.Selected = 0
	m.beginMethod()
	if m.screen != screenPaste {
		t.Fatalf("screen = %v, want the paste view", m.screen)
	}

	block := "PAIR-CODE\nparticipant: openrig-main\nparticipant API: https://10.0.0.5:7790\n" +
		"certificate SHA-256 (the pin): sha256:abc\n" +
		setup.EndLinePrefix + " by the gateway at its start\n"
	_, cmd := m.Update(tea.PasteMsg{Content: block})
	if cmd == nil {
		t.Fatal("the end-marker line did not start the parse")
	}
	if m.secret == nil || m.secret.size() == 0 {
		t.Fatal("the masked paste was not kept available for a retry")
	}

	// The parse answers, and the review screen appears with the consent ready.
	m.Update(runLastBatchCommand(t, cmd))
	if m.screen != screenReview {
		t.Fatalf("screen = %v, want the review", m.screen)
	}
	if m.staged == nil || !m.staged.CodeReceived {
		t.Fatal("the staged summary did not reach the review screen")
	}
	if m.secret != nil && m.secret.size() != 0 {
		t.Fatal("the successful parse did not wipe the retained paste")
	}
}

func TestTypedCodeUsesMaskedBuffer(t *testing.T) {
	const code = "PAIR-CODE-SECRET"
	plan := setup.Plan{Step: setup.StepPairingMaterial, NeedCode: true, Fields: []setup.Field{
		{ID: "url", Label: "URL"}, {ID: "pin", Label: "certificate pin"}, {ID: "code", Label: "pairing code", Secret: true},
	}}
	fake := &fakeEngine{staged: setup.PasteSummary{CodeReceived: true}}
	m := newTestModel(fake, plan)
	m.choices.Selected = 1
	m.beginMethod()
	m.focus = 2
	m.syncFocus()
	m.onPaste(tea.PasteMsg{Content: code})
	if m.fields["code"].Value() != "" {
		t.Fatal("typed code was stored in a Go string field")
	}
	if m.secret == nil || !bytes.Equal(m.secret.bytes, []byte(code)) {
		t.Fatal("typed code was not held in the masked byte buffer")
	}
	if strings.Contains(m.View().Content, code) {
		t.Fatal("typed code appeared in the rendered view")
	}
	_, cmd := m.submitFields()
	if cmd == nil || m.secret != nil {
		t.Fatal("submission did not transfer and drop the buffered code")
	}
	if _, ok := runLastBatchCommand(t, cmd).(stageMsg); !ok {
		t.Fatal("typed fields did not return a staging result")
	}
	if !bytes.Equal(fake.stageCode, []byte(code)) {
		t.Fatal("the staged code differs from the typed code")
	}
	setup.Wipe(fake.stageCode)
}

func TestEndMarkerMustBeginALine(t *testing.T) {
	secret := &secretInput{}
	secret.appendText("ordinary text "+setup.EndLinePrefix+" embedded", setup.MaxPasteBytes)
	if secret.contains(setup.EndLinePrefix) {
		t.Fatal("an embedded marker was treated as a line prefix")
	}
	secret.appendText("\n"+setup.EndLinePrefix+" at gateway", setup.MaxPasteBytes)
	if !secret.contains(setup.EndLinePrefix) {
		t.Fatal("a marker at the start of a line was not detected")
	}
}

func TestQuitDialogIsRendered(t *testing.T) {
	m := newTestModel(&fakeEngine{}, setup.Plan{Step: setup.StepLocalOpenRig})
	m.openQuitDialog()
	view := m.View().Content
	if !strings.Contains(view, "Quit setup?") || !strings.Contains(view, "Stay") || !strings.Contains(view, "Quit") {
		t.Fatalf("quit dialog is absent from the rendered view: %q", view)
	}
}

// TestPasteMarkerLineTypedByHand: typing the end-marker line parses too, without any Ctrl key.
func TestPasteMarkerLineTypedByHand(t *testing.T) {
	plan := setup.Plan{Step: setup.StepPairingMaterial, PasteBlock: true, NeedCode: true}
	m := newTestModel(&fakeEngine{staged: setup.PasteSummary{URL: "https://10.0.0.5:7790"}}, plan)
	m.beginMethod()

	// A line arrives keystroke by keystroke, then Enter closes it. Drive Update, the real key path.
	for _, r := range setup.EndLinePrefix {
		m.Update(tea.KeyPressMsg{Text: string(r), Code: r})
	}
	_, cmd := m.Update(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("typing the marker line did not start the parse")
	}
}

// TestContinueBlockedByFieldErrors: a field error blocks Continue and names the count.
func TestContinueBlockedByFieldErrors(t *testing.T) {
	plan := setup.Plan{Step: setup.StepLocalOpenRig, Fields: []setup.Field{{ID: "rig_path", Label: "rig"}}}
	m := newTestModel(&fakeEngine{}, plan)

	m.fieldErrors["rig_path"] = "not acceptable"
	m.refreshButtons()
	if reason, blocked := m.continueBlocked(); !blocked || !strings.Contains(reason, "1 field") {
		t.Fatalf("continue = %q, %v; want it blocked with a reason", reason, blocked)
	}

	delete(m.fieldErrors, "rig_path")
	m.fields["rig_path"].SetValue("/tmp/rig")
	m.refreshButtons()
	if _, blocked := m.continueBlocked(); blocked {
		t.Fatal("continue stayed blocked with no errors")
	}
}

// TestSmallLayout: below the minimum the wizard refuses to draw and keeps its state.
func TestSmallLayout(t *testing.T) {
	plan := setup.Plan{Step: setup.StepLocalOpenRig, Fields: []setup.Field{{ID: "rig_path", Label: "rig"}}}
	m := newTestModel(&fakeEngine{}, plan)

	m.width, m.height = 39, 11
	if view := m.View().Content; !strings.Contains(view, "terminal too small") {
		t.Fatalf("small view = %q, want the size message", view)
	}
	if m.currentControl().kind != controlField {
		t.Fatal("the model lost its focus while small")
	}

	m.width, m.height = 60, 20
	if view := m.View().Content; strings.Contains(view, "Preflight") && strings.Count(view, "●") > 4 {
		t.Fatal("a 60-column view drew the sidebar")
	}
}

// TestNoSecretBytesInView: neither the pasted block nor the code ever reaches a rendered screen.
func TestNoSecretBytesInView(t *testing.T) {
	const secret = "SUPER-SECRET-CODE-1234"
	plan := setup.Plan{Step: setup.StepPairingMaterial, PasteBlock: true, NeedCode: true}
	m := newTestModel(&fakeEngine{}, plan)
	m.beginMethod()

	m.secret.appendText(secret+"\nkey pin: sha256//AAAA\n", setup.MaxPasteBytes)
	m.staged = &setup.PasteSummary{URL: "https://10.0.0.5:7790", CodeReceived: true}
	m.screen = screenReview
	m.rebuildControls()

	for _, size := range [][2]int{{100, 30}, {60, 20}, {41, 13}, {39, 11}} {
		m.width, m.height = size[0], size[1]
		if view := m.View().Content; strings.Contains(view, secret) {
			t.Fatalf("the secret appeared in the %dx%d view", size[0], size[1])
		}
	}
}

// TestValidationIsSerialisedAndUsesTheLatestValue: one call at a time, newest value wins.
func TestValidationIsSerialisedAndUsesTheLatestValue(t *testing.T) {
	plan := setup.Plan{Step: setup.StepLocalOpenRig, Fields: []setup.Field{{ID: "rig_path", Label: "rig"}}}
	fake := &fakeEngine{}
	m := newTestModel(fake, plan)

	first := m.validateCmd("rig_path", "/old")
	if first == nil {
		t.Fatal("the first validation was not scheduled")
	}
	if queued := m.validateCmd("rig_path", "/new"); queued != nil {
		t.Fatal("validation overlapped the active call")
	}

	result := first().(validateMsg)
	if result.message != "" {
		t.Fatalf("validation message = %q, want none", result.message)
	}
	// The field's text is what the model holds, so set it before the answer is applied.
	m.fields["rig_path"].SetValue("/new")
	_, next := m.Update(result)
	if next == nil {
		t.Fatal("the queued validation was not scheduled")
	}
	nextResult := next().(validateMsg)
	m.Update(nextResult)

	if got, want := strings.Join(fake.validated, ","), "rig_path=/old,rig_path=/new"; got != want {
		t.Fatalf("validation order = %q, want %q", got, want)
	}
}

// TestFieldErrorsFocusFirstInvalid: Apply FieldErrors land beside the field and take focus.
func TestFieldErrorsFocusFirstInvalid(t *testing.T) {
	plan := setup.Plan{
		Step:   setup.StepLocalOpenRig,
		Fields: []setup.Field{{ID: "rig_path", Label: "rig"}, {ID: "openrig_home", Label: "home"}},
	}
	fake := &fakeEngine{results: []setup.Result{{
		Status:      setup.StatusNotDone,
		FieldErrors: map[string]string{"openrig_home": "not an existing absolute directory"},
	}}}
	m := newTestModel(fake, plan)

	result := fake.results[0]
	m.Update(applyMsg{gen: m.gen, res: result})

	if m.fieldErrors["openrig_home"] == "" {
		t.Fatal("the field error was not recorded")
	}
	focused := m.currentControl()
	if focused.kind != controlField || m.order[focused.index] != "openrig_home" {
		t.Fatalf("focus = %v/%d, want the invalid field", focused.kind, focused.index)
	}
}

// TestPlanErrorStopsTheRun: a planning failure shows the message and ends on the checklist.
func TestPlanErrorStopsTheRun(t *testing.T) {
	fake := &fakeEngine{planErr: errors.New("the configuration cannot be read")}
	m := newModel(fake, context.Background())
	m.width, m.height = 100, 30

	m.Update(planMsg{gen: m.gen + 1, err: fake.planErr})
	m.gen++
	m.Update(planMsg{gen: m.gen, err: fake.planErr})

	if m.phase != phaseSummary {
		t.Fatalf("phase = %v, want the summary", m.phase)
	}
	if view := m.View().Content; !strings.Contains(view, "cannot be read") {
		t.Fatalf("view = %q, want the planning error", view)
	}
}

// ---- plain-HTTP transport consent (allow_http) ---------------------------------------------------

// reviewModel builds a model on the pairing step's review screen with a staged summary.
func reviewModel(fake *fakeEngine, plain bool) *model {
	plan := setup.Plan{
		Step:       setup.StepPairingMaterial,
		PasteBlock: true,
		NeedCode:   true,
		Questions: []setup.Question{
			{ID: allowHTTPQuestion, Prompt: "Accept plain HTTP for this gateway? (not encrypted)", DefaultYes: false,
				Detail: []string{"detail one", "detail two", "detail three"}},
			{ID: "use", Prompt: "Use these values?", DefaultYes: true},
		},
	}
	m := newTestModel(fake, plan)
	summary := setup.PasteSummary{URL: "https://10.0.0.5:7790", Pin: "sha256:abc", CodeReceived: true}
	if plain {
		summary.URL, summary.Pin, summary.Plain = "http://10.0.0.5:7790", "", true
	}
	m.staged = &summary
	m.from = screenPaste
	m.screen = screenReview
	m.focus = 0
	m.rebuildControls()
	return m
}

// reviewControlIDs lists the toggles currently on the review screen, in focus order.
func reviewControlIDs(m *model) []string {
	var ids []string
	for _, item := range m.controls {
		if item.kind == controlToggle {
			ids = append(ids, m.plan.Questions[item.index].ID)
		}
	}
	return ids
}

// TestReviewShowsAllowHTTPOnlyWhenStagedIsPlain: the transport consent exists for http:// and not for https.
func TestReviewShowsAllowHTTPOnlyWhenStagedIsPlain(t *testing.T) {
	plain := reviewControlIDs(reviewModel(&fakeEngine{}, true))
	if len(plain) != 2 || plain[0] != allowHTTPQuestion || plain[1] != "use" {
		t.Fatalf("plain review consents = %v, want [allow_http use]", plain)
	}

	secure := reviewControlIDs(reviewModel(&fakeEngine{}, false))
	if len(secure) != 1 || secure[0] != "use" {
		t.Fatalf("https review consents = %v, want [use]", secure)
	}

	// The rendered review must agree with the controls, and keep the warning for http.
	m := reviewModel(&fakeEngine{}, true)
	m.width, m.height = 100, 30
	if view := m.View().Content; !strings.Contains(view, "Accept plain HTTP") {
		t.Fatal("the plain-HTTP consent is not rendered on review")
	}
	if view := m.View().Content; !strings.Contains(view, "detail one") || !strings.Contains(view, "detail three") {
		t.Fatal("the consent detail lines are not rendered on review")
	}
	if view := m.View().Content; !strings.Contains(view, "plain HTTP") {
		t.Fatal("the plain-HTTP warning is gone from review")
	}

	secureModel := reviewModel(&fakeEngine{}, false)
	secureModel.width, secureModel.height = 100, 30
	if view := secureModel.View().Content; strings.Contains(view, "Accept plain HTTP") {
		t.Fatal("the plain-HTTP consent appears for an https gateway")
	}
}

// TestAllowHTTPDefaultsToNo: the consent starts exactly at the engine's DefaultYes, which is No.
func TestAllowHTTPDefaultsToNo(t *testing.T) {
	m := reviewModel(&fakeEngine{}, true)
	item := m.toggles[allowHTTPQuestion]
	if item == nil {
		t.Fatal("the plain-HTTP consent is missing from the plan")
	}
	if item.Yes {
		t.Fatal("the plain-HTTP consent did not start at No")
	}
}

// TestMethodChooserExcludesAllowHTTP: only the keep question belongs on the chooser.
func TestMethodChooserExcludesAllowHTTP(t *testing.T) {
	plan := setup.Plan{
		Step:       setup.StepPairingMaterial,
		PasteBlock: true,
		Questions: []setup.Question{
			{ID: "keep", Prompt: "Keep this participant URL and pin?", DefaultYes: true},
			{ID: allowHTTPQuestion, Prompt: "Accept plain HTTP for this gateway? (not encrypted)"},
			{ID: "use", Prompt: "Use these values?", DefaultYes: true},
		},
	}
	m := newTestModel(&fakeEngine{}, plan)
	if m.screen != screenChoice {
		t.Fatalf("screen = %v, want the chooser", m.screen)
	}
	for _, id := range reviewControlIDs(m) {
		if id == allowHTTPQuestion || id == "use" {
			t.Fatalf("%s appears on the method chooser", id)
		}
	}
	m.width, m.height = 100, 30
	if view := m.View().Content; strings.Contains(view, "Accept plain HTTP") {
		t.Fatal("the plain-HTTP consent is rendered on the method chooser")
	}
}

// TestAcceptReviewSendsBothConsents: use travels with allow_http, and https sends use alone.
func TestAcceptReviewSendsBothConsents(t *testing.T) {
	fake := &fakeEngine{}
	m := reviewModel(fake, true)
	m.toggles[allowHTTPQuestion].Yes = true
	answers := m.reviewAnswers()
	if !answers.Yes["use"] || !answers.Yes[allowHTTPQuestion] {
		t.Fatalf("answers = %v, want use and allow_http both yes", answers.Yes)
	}

	secure := reviewModel(&fakeEngine{}, false)
	answers = secure.reviewAnswers()
	if _, sent := answers.Yes[allowHTTPQuestion]; sent {
		t.Fatal("allow_http was sent for an https gateway")
	}
	if !answers.Yes["use"] {
		t.Fatal("use was not sent for an https gateway")
	}
}

// TestNotAppliedResultKeepsTheReview: a result with no status keeps the screen, the staged values and
// the step, and shows the engine's line.
func TestNotAppliedResultKeepsTheReview(t *testing.T) {
	fake := &fakeEngine{}
	m := reviewModel(fake, true)
	before := m.index

	const line = "This gateway is plain http://: set 'Accept plain HTTP' to Yes to use it, or ask for an https listener."
	m.Update(applyMsg{gen: m.gen, step: setup.StepPairingMaterial, res: setup.Result{Step: setup.StepPairingMaterial, Lines: []string{line}}})

	if m.screen != screenReview {
		t.Fatalf("screen = %v, want the review", m.screen)
	}
	if m.phase != phaseEditing {
		t.Fatalf("phase = %v, want editing after the not-applied answer", m.phase)
	}
	if m.staged == nil {
		t.Fatal("the staged values were lost")
	}
	if m.index != before {
		t.Fatalf("step advanced from %d to %d", before, m.index)
	}
	if status, marked := m.statuses()[setup.StepPairingMaterial]; marked {
		t.Fatalf("the step was marked %q", status)
	}
	if len(m.lines) != 1 || m.lines[0] != line {
		t.Fatalf("lines = %v, want the engine's message", m.lines)
	}
	m.width, m.height = 100, 30
	if view := m.View().Content; !strings.Contains(view, line) {
		t.Fatal("the engine's not-applied message is not rendered")
	}
}

// TestFieldsBlockedNeedsAPinOnlyForHTTPS: an http address passes with an empty pin; https does not.
func TestFieldsBlockedNeedsAPinOnlyForHTTPS(t *testing.T) {
	plan := setup.Plan{
		Step:   setup.StepPairingMaterial,
		Fields: []setup.Field{{ID: "url", Label: "url"}, {ID: "pin", Label: "pin"}, {ID: "code", Label: "code", Secret: true}},
	}
	m := newTestModel(&fakeEngine{}, plan)
	m.fields["url"].SetValue("http://10.0.0.5:7790")
	m.fields["pin"].SetValue("")
	m.refreshButtons()
	if reason := m.fieldsBlocked(); reason != "" {
		t.Fatalf("plain http with an empty pin was blocked: %q", reason)
	}

	m.fields["url"].SetValue("HTTPS://10.0.0.5:7790")
	m.refreshButtons()
	if reason := m.fieldsBlocked(); reason == "" {
		t.Fatal("https with an empty pin was not blocked")
	}

	m.fields["url"].SetValue("https://10.0.0.5:7790")
	m.fields["pin"].SetValue("sha256:abc")
	m.refreshButtons()
	if reason := m.fieldsBlocked(); reason != "" {
		t.Fatalf("https with a pin was blocked: %q", reason)
	}

	// A pin typed for a plain http address is left in place for the engine to refuse; the field is not
	// rewritten behind the owner's back.
	m.fields["url"].SetValue("http://10.0.0.5:7790")
	m.fields["pin"].SetValue("sha256:abc")
	if got := m.fieldValue("pin"); got != "sha256:abc" {
		t.Fatalf("pin = %q, want it left alone", got)
	}
}

// TestURLPasteStripsOneTrailingNewline: one final LF or CRLF is accepted and dropped before validating.
func TestURLPasteStripsOneTrailingNewline(t *testing.T) {
	tests := []struct {
		name  string
		paste string
		want  string
	}{
		{name: "plain", paste: "https://10.0.0.5:7790", want: "https://10.0.0.5:7790"},
		{name: "lf", paste: "https://10.0.0.5:7790\n", want: "https://10.0.0.5:7790"},
		{name: "crlf", paste: "https://10.0.0.5:7790\r\n", want: "https://10.0.0.5:7790"},
		{name: "printed label", paste: "participant API: https://10.0.0.5:7790",
			want: "https://10.0.0.5:7790"},
		{name: "printed label with lf", paste: "participant API: https://10.0.0.5:7790\n",
			want: "https://10.0.0.5:7790"},
		{name: "printed label, padded, crlf",
			paste: "  participant API:   https://10.0.0.5:7790  \r\n",
			want:  "https://10.0.0.5:7790"},
		{name: "label in another case", paste: "Participant API: https://10.0.0.5:7790",
			want: "https://10.0.0.5:7790"},
		{name: "http label", paste: "participant API: http://10.0.0.5:7790\r\n",
			want: "http://10.0.0.5:7790"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEngine{}
			plan := setup.Plan{
				Step:   setup.StepPairingMaterial,
				Fields: []setup.Field{{ID: "url", Label: "url"}, {ID: "pin", Label: "pin"}},
			}
			m := newTestModel(fake, plan)
			m.screen = screenFields
			m.rebuildControls()

			_, cmd := m.Update(tea.PasteMsg{Content: tc.paste})
			if got := m.fieldValue("url"); got != tc.want {
				t.Fatalf("url = %q, want %q", got, tc.want)
			}
			if cmd == nil {
				t.Fatal("no validation was scheduled")
			}
			cmd()
			if len(fake.validated) != 1 {
				t.Fatalf("%d validation calls, want exactly 1", len(fake.validated))
			}
			if want := "url=" + tc.want; fake.validated[0] != want {
				t.Fatalf("validated %q, want %q", fake.validated[0], want)
			}
			if m.fieldErrors["url"] != "" {
				t.Fatalf("field error %q on a one-line paste", m.fieldErrors["url"])
			}
		})
	}
}

// TestURLPasteRejectsInteriorNewlines: a real line break changes nothing and shows the usual message.
func TestURLPasteRejectsInteriorNewlines(t *testing.T) {
	fake := &fakeEngine{}
	plan := setup.Plan{
		Step:   setup.StepPairingMaterial,
		Fields: []setup.Field{{ID: "url", Label: "url"}, {ID: "pin", Label: "pin"}},
	}
	m := newTestModel(fake, plan)
	m.screen = screenFields
	m.rebuildControls()
	m.fields["url"].SetValue("https://kept:7790")

	_, cmd := m.Update(tea.PasteMsg{Content: "https://10.0.0.5:7790\nhttps://10.0.0.6:7790"})
	if cmd != nil {
		t.Fatal("a multi-line paste scheduled a validation")
	}
	if got := m.fieldValue("url"); got != "https://kept:7790" {
		t.Fatalf("url = %q, want the field left unchanged", got)
	}
	if len(fake.validated) != 0 {
		t.Fatalf("Validate ran on a multi-line paste: %v", fake.validated)
	}
	if got := m.fieldErrors["url"]; got != "paste one line into this field" {
		t.Fatalf("field error = %q, want the existing message", got)
	}

	// A label in front does not rescue a paste that still has a real line break inside it.
	m.fields["url"].SetValue("https://kept:7790")
	_, cmd = m.Update(tea.PasteMsg{Content: "participant API: https://10.0.0.5:7790\nparticipant API: https://10.0.0.6:7790"})
	if cmd != nil {
		t.Fatal("a labelled multi-line paste scheduled a validation")
	}
	if got := m.fieldValue("url"); got != "https://kept:7790" {
		t.Fatalf("url = %q, want the field left unchanged", got)
	}
	if got := m.fieldErrors["url"]; got != "paste one line into this field" {
		t.Fatalf("field error = %q, want the existing message", got)
	}

	// The URL-specific trim must not rewrite another ordinary field's pasted text.
	m.focus = 1 // the pin field
	_, _ = m.Update(tea.PasteMsg{Content: "  participant API: sha256:abc  "})
	if got := m.fieldValue("pin"); got != "  participant API: sha256:abc  " {
		t.Fatalf("pin = %q, want pasted text unchanged outside the URL field", got)
	}
}

// ---- paste line endings and Enter in the paste box ------------------------------------------------

// pasteModel opens the pairing step's masked paste view.
func pasteModel(fake *fakeEngine) *model {
	plan := setup.Plan{Step: setup.StepPairingMaterial, PasteBlock: true, NeedCode: true}
	m := newTestModel(fake, plan)
	m.choices.Selected = 0
	m.beginMethod()
	return m
}

// fullBlock builds a printed block whose lines are joined by the given separator.
func fullBlock(sep string) string {
	return strings.Join([]string{
		"PAIR-CODE",
		"participant: openrig-main",
		"expires in 10 minutes; single use",
		"participant API: https://10.0.0.5:7790",
		"certificate SHA-256 (the pin): sha256:abc",
		setup.EndLinePrefix + " by the gateway at its start",
	}, sep)
}

// TestBracketedPasteNormalisesLineEndings: a CR- or CRLF-separated block is stored with LF and read at once.
func TestBracketedPasteNormalisesLineEndings(t *testing.T) {
	for _, tc := range []struct {
		name string
		sep  string
	}{
		{name: "lf", sep: "\n"},
		{name: "crlf", sep: "\r\n"},
		{name: "cr", sep: "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEngine{staged: setup.PasteSummary{URL: "https://10.0.0.5:7790", CodeReceived: true}}
			m := pasteModel(fake)

			block := fullBlock(tc.sep)
			_, cmd := m.Update(tea.PasteMsg{Content: block})
			if cmd == nil {
				t.Fatalf("a %s block did not start reading itself", tc.name)
			}
			if bytes.ContainsRune(m.secret.bytes, '\r') {
				t.Fatal("a CR survived in the buffered block")
			}
			if !m.pasteComplete() {
				t.Fatal("the marker was not recognised after normalisation")
			}
			if got, want := m.secret.lineCount(), 6; got != want {
				t.Fatalf("%d lines buffered, want %d", got, want)
			}
			m.Update(runLastBatchCommand(t, cmd))
			if fake.pasteCalls != 1 {
				t.Fatalf("ParsePaste called %d times, want 1", fake.pasteCalls)
			}
			if !bytes.Equal(fake.parsedPaste, []byte(fullBlock("\n"))) {
				t.Fatal("ParsePaste did not receive the normalized six-line block")
			}
			if m.screen != screenReview {
				t.Fatalf("screen = %v, want review after auto-read", m.screen)
			}
		})
	}
}

// TestContainsAcceptsCRWithoutNormalisation: the defensive path still finds the marker.
func TestContainsAcceptsCRWithoutNormalisation(t *testing.T) {
	state := &secretInput{bytes: []byte("code\rparticipant: x\r" + setup.EndLinePrefix + " by the gateway")}
	if !state.contains(setup.EndLinePrefix) {
		t.Fatal("a CR-separated block was not recognised")
	}

	crlf := &secretInput{bytes: []byte("code\r\n" + setup.EndLinePrefix + " by the gateway\r\n")}
	if !crlf.contains(setup.EndLinePrefix) {
		t.Fatal("a CRLF block was not recognised")
	}

	// The marker has to be a line prefix, not an arbitrary substring.
	mid := &secretInput{bytes: []byte("code\nsee " + setup.EndLinePrefix + " below\n")}
	if mid.contains(setup.EndLinePrefix) {
		t.Fatal("a marker in the middle of a line was accepted")
	}
}

// TestTypedEnterAddsALineAndExplainsItself: typing a line gives a visible line count and the way forward.
func TestTypedEnterAddsALineAndExplainsItself(t *testing.T) {
	m := pasteModel(&fakeEngine{})

	m.secret.appendText("PAIR-CODE", setup.MaxPasteBytes)
	_, cmd := m.pasteKey(key(tea.KeyEnter))
	if cmd != nil {
		t.Fatal("Enter on a one-line typed buffer started a parse")
	}
	if got := m.secret.lineCount(); got != 2 {
		t.Fatalf("%d lines after Enter, want 2", got)
	}
	if len(m.lines) != 1 {
		t.Fatalf("lines = %v, want one feedback line", m.lines)
	}
	if !strings.Contains(m.lines[0], "line added (2 lines received)") {
		t.Fatalf("feedback = %q, want the line count", m.lines[0])
	}
	if !strings.Contains(m.lines[0], "Read pasted text") {
		t.Fatalf("feedback = %q, want the way to the button", m.lines[0])
	}

	// The normalisation covers typed line breaks too: a CR typed by a terminal becomes one LF.
	before := m.secret.lineCount()
	m.secret.appendText("\r", setup.MaxPasteBytes)
	if got := m.secret.lineCount(); got != before+1 {
		t.Fatalf("a typed CR counted %d lines, want %d", got, before+1)
	}
}

// TestPasteEnterOnAnIncompleteBracketedBlockReads: the parser's recoverable error is shown, not silence.
func TestPasteEnterOnAnIncompleteBracketedBlockReads(t *testing.T) {
	fake := &fakeEngine{pasteErr: errors.New("the first line is not a pairing code")}
	m := pasteModel(fake)

	// A bracketed paste that stops before the block's last line.
	_, cmd := m.Update(tea.PasteMsg{Content: "PAIR-CODE\nparticipant: openrig-main"})
	if cmd != nil {
		t.Fatal("a block without its last line read itself")
	}
	if !m.secret.pasted {
		t.Fatal("the bracketed paste did not set the arrival latch")
	}

	_, cmd = m.pasteKey(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Enter on a pasted block did not start the parse")
	}
	m.Update(runLastBatchCommand(t, cmd))
	if fake.pasteCalls != 1 {
		t.Fatalf("ParsePaste called %d times, want 1", fake.pasteCalls)
	}
	if len(m.lines) != 1 || !strings.Contains(m.lines[0], "pairing code") {
		t.Fatalf("lines = %v, want the parser's message", m.lines)
	}
	if m.secret == nil || m.secret.empty() {
		t.Fatal("the masked block was dropped after a parser error")
	}
	if m.screen != screenPaste {
		t.Fatalf("screen = %v, want to stay on the paste view", m.screen)
	}
}

// TestPasteEnterOnEmptyAndReadOnEmptyShowReasons: nothing in the paste box is ever silent.
func TestPasteEnterOnEmptyAndReadOnEmptyShowReasons(t *testing.T) {
	m := pasteModel(&fakeEngine{})

	_, cmd := m.pasteKey(key(tea.KeyEnter))
	if cmd != nil {
		t.Fatal("Enter on an empty buffer started a parse")
	}
	if len(m.lines) != 1 || !strings.Contains(m.lines[0], "nothing pasted yet") {
		t.Fatalf("lines = %v, want the empty-buffer instruction", m.lines)
	}

	before := m.lines[0]
	_, cmd = m.startPaste()
	if cmd != nil {
		t.Fatal("Read pasted text on an empty buffer started a parse")
	}
	if len(m.lines) != 1 || m.lines[0] == before {
		t.Fatalf("lines = %v, want the read button's own reason", m.lines)
	}

	fake := &fakeEngine{pasteErr: errors.New("the first line is not a pairing code")}
	m = pasteModel(fake)
	m.secret.appendText("not a pairing block", setup.MaxPasteBytes)
	_, cmd = m.pressButton(0)
	if cmd == nil {
		t.Fatal("Read pasted text did not start parsing a non-empty buffer")
	}
	m.Update(runLastBatchCommand(t, cmd))
	if len(m.lines) != 1 || !strings.Contains(m.lines[0], "pairing code") {
		t.Fatalf("lines = %v, want the parser's error", m.lines)
	}
}

// TestClearResetsTheArrivalLatch: after Clear, Enter behaves like typing again.
func TestClearResetsTheArrivalLatch(t *testing.T) {
	m := pasteModel(&fakeEngine{})
	_, _ = m.Update(tea.PasteMsg{Content: "PAIR-CODE\nparticipant: openrig-main"})
	if !m.secret.pasted {
		t.Fatal("the paste did not set the latch")
	}

	// The Clear button is the second button on the paste view.
	m.buttons.Selected = 1
	if _, cmd := m.pressButton(1); cmd != nil {
		t.Fatal("Clear scheduled a command")
	}
	if m.secret == nil || !m.secret.empty() {
		t.Fatal("Clear left content in the buffer")
	}
	if m.secret.pasted {
		t.Fatal("Clear left the arrival latch set")
	}

	m.secret.appendText("typed line", setup.MaxPasteBytes)
	_, cmd := m.pasteKey(key(tea.KeyEnter))
	if cmd != nil {
		t.Fatal("after Clear and typing, Enter started a parse")
	}
	if len(m.lines) != 1 || !strings.Contains(m.lines[0], "line added") {
		t.Fatalf("lines = %v, want the typed feedback", m.lines)
	}
}

// ---- idle restoration and recoverable notices ----------------------------------------------------

// stageOnce drives the paste box to a bracketed block, presses Enter and returns the resulting command's
// message, which is what a real program would hand back to Update.
func stageOnce(t *testing.T, m *model, block string) stageMsg {
	t.Helper()
	if _, cmd := m.Update(tea.PasteMsg{Content: block}); cmd != nil {
		raw := runLastBatchCommand(t, cmd)
		msg, ok := raw.(stageMsg)
		if !ok {
			t.Fatalf("paste command returned %T, want stageMsg", raw)
		}
		return msg
	}
	_, cmd := m.pasteKey(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("the paste was neither read automatically nor on Enter")
	}
	raw := runLastBatchCommand(t, cmd)
	msg, ok := raw.(stageMsg)
	if !ok {
		t.Fatalf("paste command returned %T, want stageMsg", raw)
	}
	return msg
}

// TestStageReplyRestoresEditing: a successful stage leaves the review editable, which is the freeze regression.
func TestStageReplyRestoresEditing(t *testing.T) {
	fake := &fakeEngine{staged: setup.PasteSummary{URL: "https://10.0.0.5:7790", CodeReceived: true}}
	m := pasteModel(fake)

	msg := stageOnce(t, m, "PAIR-CODE\nparticipant: openrig-main")
	m.Update(msg)

	if m.screen != screenReview {
		t.Fatalf("screen = %v, want the review", m.screen)
	}
	if m.busy {
		t.Fatal("the model is still busy after the reply")
	}
	if m.phase != phaseEditing {
		t.Fatalf("phase = %v, want editing: the review would ignore every key", m.phase)
	}

	before := m.focus
	m.Update(key(tea.KeyTab))
	if m.focus == before {
		t.Fatal("Tab did not move focus on the review screen")
	}
}

// TestFailedStageOpensANoticeThatDismisses: the error is visible, Enter closes it, and keys work again.
func TestFailedStageOpensANoticeThatDismisses(t *testing.T) {
	fake := &fakeEngine{pasteErr: errors.New("the first line is not a pairing code")}
	m := pasteModel(fake)

	msg := stageOnce(t, m, "not-a-code\nstill-not")
	m.Update(msg)

	if !m.notice.open {
		t.Fatal("a failed parse opened no notice")
	}
	if m.phase != phaseEditing {
		t.Fatalf("phase = %v, want editing after the failure", m.phase)
	}
	if len(m.notice.Body) != 1 || !strings.Contains(m.notice.Body[0], "pairing code") {
		t.Fatalf("notice body = %v, want the parser's message", m.notice.Body)
	}

	m.Update(key(tea.KeyEnter))
	if m.notice.open {
		t.Fatal("Enter on OK did not dismiss the notice")
	}
	before := m.focus
	m.Update(key(tea.KeyTab))
	if m.focus == before {
		t.Fatal("focus did not move after dismissing the notice")
	}
}

// TestNoticeRendersNoSecretBytes: the dialog shows engine text only.
func TestNoticeRendersNoSecretBytes(t *testing.T) {
	const code = "SECRET-PAIRING-CODE-9876"
	fake := &fakeEngine{pasteErr: errors.New("the first line is not a pairing code")}
	m := pasteModel(fake)
	msg := stageOnce(t, m, code+"\nparticipant: openrig-main")
	m.Update(msg)
	m.width, m.height = 100, 30

	view := m.View().Content
	if !strings.Contains(view, "pairing code") {
		t.Fatal("the notice is not rendered")
	}
	if strings.Contains(view, code) {
		t.Fatal("the notice leaked the pairing code")
	}
}

// TestRetryReadsTheRetainedBlock: Retry calls ParsePaste again with the masked copy.
func TestRetryReadsTheRetainedBlock(t *testing.T) {
	fake := &fakeEngine{pasteErr: errors.New("incomplete block")}
	m := pasteModel(fake)
	msg := stageOnce(t, m, "code\nparticipant: openrig-main")
	m.Update(msg)
	if fake.pasteCalls != 1 {
		t.Fatalf("%d parse calls, want 1", fake.pasteCalls)
	}

	// Retry is the second action.
	m.notice.Buttons.Selected = 1
	_, cmd := m.activateNotice()
	if fake.pasteCalls != 1 {
		t.Fatal("Retry parsed before the command ran")
	}
	if cmd == nil {
		t.Fatal("Retry scheduled no parse")
	}
	result, ok := runLastBatchCommand(t, cmd).(stageMsg)
	if !ok {
		t.Fatalf("Retry returned %T, want stageMsg", result)
	}
	m.Update(result)
	if fake.pasteCalls != 2 {
		t.Fatalf("%d parse calls after Retry, want 2", fake.pasteCalls)
	}
	if m.secret == nil || m.secret.empty() {
		t.Fatal("Retry dropped the masked block")
	}
}

// TestPlainReviewContinueSendsBothConsents: the HTTP flow still works through the notice machinery.
func TestPlainReviewContinueSendsBothConsents(t *testing.T) {
	fake := &fakeEngine{}
	m := reviewModel(fake, true)

	// The allow_http toggle is the first control on the review.
	m.Update(key(tea.KeySpace))
	if !m.toggles[allowHTTPQuestion].Yes {
		t.Fatal("Space did not flip the plain-HTTP consent")
	}

	// Continue is the last control.
	for m.currentControl().kind != controlButtons {
		m.Update(key(tea.KeyTab))
	}
	_, cmd := m.Update(key(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Continue scheduled no command")
	}
	result := runLastBatchCommand(t, cmd)
	if _, ok := result.(applyMsg); !ok {
		t.Fatalf("Continue returned %T, want applyMsg", result)
	}
	if len(fake.applyCalls) != 1 {
		t.Fatalf("%d apply calls, want 1", len(fake.applyCalls))
	}
	if !fake.applyCalls[0].Yes[allowHTTPQuestion] || !fake.applyCalls[0].Yes["use"] {
		t.Fatalf("answers = %v, want use and allow_http", fake.applyCalls[0].Yes)
	}
}

// TestNotAppliedOpensANoticeAndKeepsTheReview: the review, the staged values and the step survive.
func TestNotAppliedOpensANoticeAndKeepsTheReview(t *testing.T) {
	fake := &fakeEngine{}
	m := reviewModel(fake, true)
	before := m.index

	const line = "This gateway is plain http://: set 'Accept plain HTTP' to Yes to use it, or ask for an https listener."
	m.Update(applyMsg{gen: m.gen, res: setup.Result{Step: setup.StepPairingMaterial, Lines: []string{line}}})

	if !m.notice.open {
		t.Fatal("the not-applied result opened no notice")
	}
	if m.notice.Title != "Not applied" {
		t.Fatalf("notice title = %q", m.notice.Title)
	}
	if got := len(m.notice.Buttons.Labels); got != 2 {
		t.Fatalf("%d notice actions, want OK and Back", got)
	}
	if m.screen != screenReview || m.staged == nil || m.index != before {
		t.Fatal("the review, the staged values or the step were lost")
	}
	if m.phase != phaseEditing {
		t.Fatalf("phase = %v, want editing", m.phase)
	}

	// Back returns to the subview the review came from.
	m.notice.Buttons.Selected = 1
	m.activateNotice()
	if m.screen != screenPaste {
		t.Fatalf("screen after Back = %v, want the paste view", m.screen)
	}
}

// TestFieldErrorsKeepInlineAndOpenASummary: both the field message and the notice are present.
func TestFieldErrorsKeepInlineAndOpenASummary(t *testing.T) {
	plan := setup.Plan{
		Step:   setup.StepLocalOpenRig,
		Fields: []setup.Field{{ID: "rig_path", Label: "Path of rig"}, {ID: "openrig_home", Label: "OpenRig home"}},
	}
	m := newTestModel(&fakeEngine{}, plan)

	m.Update(applyMsg{gen: m.gen, res: setup.Result{FieldErrors: map[string]string{
		"rig_path":     "not an absolute path of an executable file",
		"openrig_home": "not an existing absolute directory",
	}}})

	if m.fieldErrors["rig_path"] == "" || m.fieldErrors["openrig_home"] == "" {
		t.Fatal("the inline field errors were dropped")
	}
	if !m.notice.open {
		t.Fatal("no summary notice was opened")
	}
	body := strings.Join(m.notice.Body, " ")
	if !strings.Contains(body, "2 fields need attention") {
		t.Fatalf("notice body = %q, want the count", body)
	}
	if !strings.Contains(body, "OpenRig home") {
		t.Fatalf("notice body = %q, want the field labels", body)
	}
	if len(m.notice.Buttons.Labels) != 1 {
		t.Fatalf("notice actions = %v, want OK only", m.notice.Buttons.Labels)
	}
	if m.currentControl().kind != controlField {
		t.Fatal("focus did not land on an invalid field")
	}
}

// TestValidationNoticeOnlyOnBlockedContinue: typing shows the inline message, Continue raises the notice.
func TestValidationNoticeOnlyOnBlockedContinue(t *testing.T) {
	plan := setup.Plan{Step: setup.StepLocalOpenRig, Fields: []setup.Field{{ID: "rig_path", Label: "Path of rig"}}}
	fake := &fakeEngine{}
	m := newTestModel(fake, plan)

	// The first failure is stale because the owner changed the field while it was in flight; the
	// serialized queue schedules validation for the new value and only that answer may block Continue.
	m.fields["rig_path"].SetValue("older-value")
	first := m.validateCmd("rig_path", "older-value")
	m.fields["rig_path"].SetValue("bad-path")
	if queued := m.validateCmd("rig_path", "bad-path"); queued != nil {
		t.Fatal("validation overlapped the first value")
	}
	_, next := m.Update(first().(validateMsg))
	if m.notice.open {
		t.Fatal("a stale validation answer raised a notice")
	}
	if next == nil {
		t.Fatal("the latest value was not validated after the stale answer")
	}

	// The current value fails: the inline message appears, and still no notice while typing.
	m.Update(next().(validateMsg))
	if m.fieldErrors["rig_path"] != "not acceptable" && m.fields["rig_path"].message != "not acceptable" {
		t.Fatal("the inline validation message was not recorded")
	}
	if m.notice.open {
		t.Fatal("a validation answer raised a notice while typing")
	}

	// Continue is blocked, so pressing it explains itself in a notice.
	for m.currentControl().kind != controlButtons {
		m.Update(key(tea.KeyTab))
	}
	m.Update(key(tea.KeyEnter))
	if !m.notice.open {
		t.Fatal("Continue on an invalid field raised no notice")
	}
	if m.notice.Title != "Needs attention" {
		t.Fatalf("notice title = %q", m.notice.Title)
	}
	if len(m.notice.Body) != 1 || !strings.Contains(m.notice.Body[0], "Path of rig: not acceptable") {
		t.Fatalf("notice body = %v, want the current field error", m.notice.Body)
	}
}

// TestCtrlCOverANoticeKeepsIt: quit confirmation stacks above, and Stay returns to the notice.
func TestCtrlCOverANoticeKeepsIt(t *testing.T) {
	fake := &fakeEngine{pasteErr: errors.New("incomplete block")}
	m := pasteModel(fake)
	msg := stageOnce(t, m, "code\nparticipant: x")
	m.Update(msg)
	if !m.notice.open {
		t.Fatal("no notice to stack over")
	}
	m.changedSomething = false // the notice itself must still make Ctrl+C ask before quitting.

	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !m.dialog.open {
		t.Fatal("Ctrl+C did not open the quit confirmation over the notice")
	}
	if !m.notice.open {
		t.Fatal("the notice was lost when the quit dialog opened")
	}

	// Stay is the default button.
	m.Update(key(tea.KeyEnter))
	if m.dialog.open {
		t.Fatal("Stay did not close the quit dialog")
	}
	if !m.notice.open {
		t.Fatal("Stay closed the notice as well")
	}
}

// TestTypedStageBackReturnsToFields: the code is gone, but non-secret values remain editable.
func TestTypedStageBackReturnsToFields(t *testing.T) {
	plan := setup.Plan{Step: setup.StepPairingMaterial, NeedCode: true, Fields: []setup.Field{
		{ID: "url", Label: "Participant URL"}, {ID: "pin", Label: "Certificate pin"},
	}}
	m := newTestModel(&fakeEngine{}, plan)
	m.screen = screenFields
	m.rebuildControls()
	m.fields["url"].SetValue("https://example.test:7790")
	m.fields["pin"].SetValue("sha256:abcd")
	m.stageFrom = screenFields
	m.Update(stageMsg{gen: m.gen, err: errors.New("the pairing material was refused")})

	if !m.notice.open || len(m.notice.Buttons.Labels) != 2 {
		t.Fatal("typed staging error should offer OK and Back, without Retry")
	}
	m.notice.Buttons.Selected = 1
	m.activateNotice()
	if m.screen != screenFields {
		t.Fatalf("screen after Back = %v, want typed fields", m.screen)
	}
	if got := m.fieldValue("url"); got != "https://example.test:7790" {
		t.Fatalf("URL after Back = %q, want it retained", got)
	}
}
