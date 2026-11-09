package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
)

// fakeRun answers launchctl and plutil from a table keyed by the joined command line; it records every call.
type fakeRun struct {
	answers map[string]int
	calls   []string
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, int, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	for prefix, code := range f.answers {
		if strings.HasPrefix(key, prefix) {
			return nil, code, nil
		}
	}
	return nil, 0, nil
}

func testEngine(t *testing.T, goos string, run *fakeRun) *Engine {
	t.Helper()
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	env := Env{
		ConfigPath: filepath.Join(state, "config.json"), Home: home, UID: 501, GOOS: goos,
		Getenv:     func(string) string { return "" },
		LookPath:   func(string) (string, error) { return "", fmt.Errorf("not found") },
		Executable: func() (string, error) { return filepath.Join(home, "bin", "ratatoskr-openrig"), nil },
		Run:        run.run,
		Now:        func() time.Time { return time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC) },
		Assets: Assets{
			PlistTemplate: "<a>__BINARY__</a><b>__CONFIG__</b><c>__STATE_DIR__</c><d>__LOG_DIR__</d><e>__HOME__</e>",
			Skill:         "skill text",
		},
	}
	e := New(env)
	if err := e.detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.st.cfg = goodConfig(state)
	e.st.cfg.Home = home
	return e
}

func TestJobStateOnlyNotFoundIsAbsent(t *testing.T) {
	cases := []struct {
		serviceExit int
		want        jobStatus
	}{{0, jobLoaded}, {launchctlNotFound, jobNone}, {1, jobUnknown}, {5, jobUnknown}}
	for _, c := range cases {
		run := &fakeRun{answers: map[string]int{"/bin/launchctl print gui/501/": c.serviceExit}}
		e := testEngine(t, "darwin", run)
		if got, _ := e.jobState(context.Background()); got != c.want {
			t.Errorf("service exit %d: got %v want %v", c.serviceExit, got, c.want)
		}
	}
	// A GUI domain that does not answer is unknown, whatever the service says.
	run := &fakeRun{answers: map[string]int{"/bin/launchctl print gui/501": 1}}
	e := testEngine(t, "darwin", run)
	if got, _ := e.jobState(context.Background()); got != jobUnknown {
		t.Errorf("an unavailable domain must be unknown, got %v", got)
	}
}

func TestForeignLoadedJobWritesNothing(t *testing.T) {
	run := &fakeRun{answers: map[string]int{}} // every launchctl print exits 0: loaded
	e := testEngine(t, "darwin", run)
	res := e.Apply(context.Background(), StepService, Answers{})
	if res.Status != StatusYourTurn || res.Follow != nil {
		t.Fatalf("expected YOUR TURN and no follow-up, got %+v", res)
	}
	if _, err := os.Lstat(e.plistPath()); err == nil {
		t.Fatal("a plist was written although a foreign job is loaded")
	}
	for _, call := range run.calls {
		if strings.Contains(call, "bootout") || strings.Contains(call, "bootstrap") {
			t.Fatalf("launchd was changed: %s", call)
		}
	}
}

func TestUnknownJobStateWritesNothing(t *testing.T) {
	run := &fakeRun{answers: map[string]int{"/bin/launchctl print gui/501/": 7}}
	e := testEngine(t, "darwin", run)
	res := e.Apply(context.Background(), StepService, Answers{})
	if res.Status != StatusYourTurn {
		t.Fatalf("expected YOUR TURN, got %+v", res)
	}
	if _, err := os.Lstat(e.plistPath()); err == nil {
		t.Fatal("a plist was written although the job state is unknown")
	}
}

func TestServiceConsentsDefaultToNo(t *testing.T) {
	run := &fakeRun{answers: map[string]int{"/bin/launchctl print gui/501/": launchctlNotFound}}
	e := testEngine(t, "darwin", run)
	res := e.Apply(context.Background(), StepService, Answers{})
	if res.Follow == nil || res.Follow.Questions[0].ID != "install" || !res.Follow.Questions[0].DefaultYes {
		t.Fatalf("expected the install question (default Yes for a new file), got %+v", res)
	}
	res = e.Apply(context.Background(), StepService, Answers{})
	if res.Follow == nil || res.Follow.Questions[0].ID != "start" || res.Follow.Questions[0].DefaultYes {
		t.Fatalf("expected the start question defaulting to No, got %+v", res)
	}
	res = e.Apply(context.Background(), StepService, Answers{})
	if res.Status != StatusYourTurn {
		t.Fatalf("an unanswered start question must not start the job, got %+v", res)
	}
	for _, call := range run.calls {
		if strings.Contains(call, "bootstrap") || strings.Contains(call, "bootout") {
			t.Fatalf("launchd was changed without consent: %s", call)
		}
	}
}

func TestLinuxServiceStepOnlyPrintsTheForegroundCommand(t *testing.T) {
	e := testEngine(t, "linux", &fakeRun{})
	plan, _ := e.Plan(context.Background(), StepService)
	if !plan.Auto || len(plan.Instructions) == 0 {
		t.Fatalf("unexpected plan %+v", plan)
	}
	if res := e.Apply(context.Background(), StepService, Answers{}); res.Status != StatusSkipped {
		t.Fatalf("got %+v", res)
	}
}

func TestSkillRunDefaultsToNo(t *testing.T) {
	e := testEngine(t, "linux", &fakeRun{})
	plan, _ := e.Plan(context.Background(), StepSkill)
	for _, q := range plan.Questions {
		if q.DefaultYes {
			t.Fatalf("question %q defaults to Yes", q.ID)
		}
	}
	res := e.Apply(context.Background(), StepSkill, Answers{})
	if res.Status != StatusYourTurn {
		t.Fatalf("got %+v", res)
	}
	if _, err := os.Stat(e.skillPath()); err != nil {
		t.Fatalf("skill file not written: %v", err)
	}
}

func TestParsePasteStagesWithoutReturningTheCode(t *testing.T) {
	e := testEngine(t, "linux", &fakeRun{})
	sum, err := e.ParsePaste(block(false, "https://10.0.0.5:7790", true, true))
	if err != nil {
		t.Fatal(err)
	}
	if !sum.CodeReceived || strings.Contains(fmt.Sprintf("%+v", sum), "PAIR-CODE-123") {
		t.Fatalf("summary must say a code was received and never hold it: %+v", sum)
	}
	res := e.Apply(context.Background(), StepPairingMaterial, Answers{Yes: map[string]bool{"use": true}})
	if res.Status != StatusDone || strings.Contains(fmt.Sprintf("%+v", res), "PAIR-CODE-123") {
		t.Fatalf("got %+v", res)
	}
	if e.st.cand.ParticipantURL != "https://10.0.0.5:7790" || e.st.cand.CertificateSHA256 != "sha256:"+hex64 {
		t.Fatalf("candidate not updated: %+v", e.st.cand)
	}
	e.Close()
	if e.st.chosen != nil && len(e.st.chosen.Code) > 0 && e.st.chosen.Code[0] != 0 {
		t.Fatal("Close must zero the code")
	}
}

func TestStagePairRefusesTheKeyPinAndShortCodes(t *testing.T) {
	e := testEngine(t, "linux", &fakeRun{})
	if _, err := e.StagePair("https://10.0.0.5:7790", "sha256//QUJD", []byte("code")); err == nil {
		t.Fatal("the key pin must be refused")
	}
	if _, err := e.StagePair("http://10.0.0.5:7790", "", []byte("code")); err == nil {
		t.Fatal("plain http must be refused without --insecure-http")
	}
	if _, err := e.StagePair("https://10.0.0.5:7790", "sha256:"+hex64, []byte("two words")); err == nil {
		t.Fatal("a code with a space must be refused")
	}
}

func TestSummaryEndsWithTheScenarioLine(t *testing.T) {
	e := testEngine(t, "linux", &fakeRun{})
	s := e.Summary()
	if len(s.Todo) == 0 || s.Todo[len(s.Todo)-1] != "scenario 8 round trip: PENDING USER EXECUTION" {
		t.Fatalf("got %v", s.Todo)
	}
}

var _ = config.Version
