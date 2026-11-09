package tui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/setup"
)

type validationFake struct{ calls []string }

func (f *validationFake) Plan(context.Context, setup.StepID) (setup.Plan, error) {
	return setup.Plan{}, nil
}
func (f *validationFake) Apply(context.Context, setup.StepID, setup.Answers) setup.Result {
	return setup.Result{}
}
func (f *validationFake) Validate(_ setup.StepID, id, value string) string {
	f.calls = append(f.calls, id+"="+value)
	return ""
}
func (f *validationFake) ParsePaste([]byte) (setup.PasteSummary, error) {
	return setup.PasteSummary{}, nil
}
func (f *validationFake) StagePair(string, string, []byte) (setup.PasteSummary, error) {
	return setup.PasteSummary{}, nil
}
func (f *validationFake) Summary() setup.Summary { return setup.Summary{} }
func (f *validationFake) ExitCode() int          { return setup.ExitOK }
func (f *validationFake) Close()                 {}

func TestValidationCallsAreSerializedAndUseLatestValue(t *testing.T) {
	fake := &validationFake{}
	m := newModel(fake, context.Background())
	m.plan = setup.Plan{Step: setup.StepLocalOpenRig}

	first := m.validateCmd("rig_path", "/old")
	if first == nil {
		t.Fatal("first validation was not scheduled")
	}
	if queued := m.validateCmd("rig_path", "/new"); queued != nil {
		t.Fatal("validation overlapped the active call")
	}
	firstResult := first().(validateMsg)
	_, next := m.Update(firstResult)
	if next == nil {
		t.Fatal("latest queued validation was not scheduled")
	}
	nextResult := next().(validateMsg)
	m.Update(nextResult)

	if got, want := strings.Join(fake.calls, ","), "rig_path=/old,rig_path=/new"; got != want {
		t.Fatalf("validation order = %q, want %q", got, want)
	}
}

func TestSubmitWaitsForValidation(t *testing.T) {
	m := newModel(&validationFake{}, context.Background())
	m.phase = phaseEditing
	m.plan = setup.Plan{Step: setup.StepLocalOpenRig}
	m.validationBusy = "rig_path"

	_, cmd := m.submitStep()
	if cmd != nil || !m.submitAfterValidation {
		t.Fatalf("submit while validation runs = cmd %v, deferred %v", cmd != nil, m.submitAfterValidation)
	}
}

func TestTypedCodeUsesCodeLimit(t *testing.T) {
	m := &model{plan: setup.Plan{Step: setup.StepPairingMaterial}}
	if got := m.secretLimit(); got != setup.MaxCodeBytes {
		t.Fatalf("typed code limit = %d, want %d", got, setup.MaxCodeBytes)
	}
	m.plan.PasteBlock = true
	if got := m.secretLimit(); got != setup.MaxPasteBytes {
		t.Fatalf("pasted block limit = %d, want %d", got, setup.MaxPasteBytes)
	}
}

func TestEnterInPasteBlockPreservesKeystrokePasteLines(t *testing.T) {
	m := newModel(&validationFake{}, context.Background())
	m.enterPlan(setup.Plan{Step: setup.StepPairingMaterial, PasteBlock: true})

	for _, ch := range []string{"<CODE>", "participant: openrig-main"} {
		for _, r := range ch {
			m.onKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		_, cmd := m.onKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil {
			t.Fatal("Enter submitted a keystroke-pasted block")
		}
	}

	if got, want := string(m.secret.bytes), "<CODE>\nparticipant: openrig-main\n"; got != want {
		t.Fatalf("masked block = %q, want %q", got, want)
	}
	if m.phase != phaseEditing || m.busy {
		t.Fatal("Enter left paste mode or started a command")
	}
}

func TestSummaryEndsWithPendingExecutionLine(t *testing.T) {
	m := newModel(&validationFake{}, context.Background())
	m.summary.Todo = []string{"finish setup", roundTripLine}
	got := strings.TrimSuffix(m.renderSummary(80), "\n")
	if !strings.HasSuffix(got, roundTripLine) {
		t.Fatalf("summary does not end with required line: %q", got)
	}
	page := strings.TrimSuffix(pageSummary("a\nb\n"+roundTripLine+"\n", 2, 0), "\n")
	if !strings.HasSuffix(page, roundTripLine) {
		t.Fatalf("paged summary does not keep required line visible: %q", page)
	}
}

func TestNoQuestionDefaultGetsInverted(t *testing.T) {
	m := newModel(&validationFake{}, context.Background())
	m.enterPlan(setup.Plan{
		Step:      setup.StepService,
		Questions: []setup.Question{{ID: "start", DefaultYes: false}},
	})
	if m.answers["start"] {
		t.Fatal("DefaultYes=false was inverted")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestWrapTextDoesNotSplitUTF8(t *testing.T) {
	for _, line := range wrapText("一二三四五", 2) {
		if !utf8.ValidString(line) {
			t.Fatalf("wrapped line is not valid UTF-8: %q", line)
		}
	}
}
