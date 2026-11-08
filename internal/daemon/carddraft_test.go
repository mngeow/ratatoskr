package daemon

// Tests for the draft manager, with a fake runner only: no model, no harness and no process is ever
// started. UNTESTED: written, not run (GR-3).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const goodAnswer = `{"description":"Build host with code review.","skills":[{"id":"code-review","name":"Code review","description":"Reviews a diff.","tags":["claude","detected"]}]}`

// fakeRunner stands in for the command. block, when set, holds the run until it is closed or the
// context ends.
type fakeRunner struct {
	mu       sync.Mutex
	output   string
	err      error
	block    chan struct{}
	called   int
	payloads [][]byte
}

func (f *fakeRunner) Name() string   { return "fake" }
func (f *fakeRunner) Binary() string { return "/fake/bin/claude" }
func (f *fakeRunner) Argv() []string { return []string{"claude", "-p"} }
func (f *fakeRunner) Calls() int     { f.mu.Lock(); defer f.mu.Unlock(); return f.called }
func (f *fakeRunner) Draft(ctx context.Context, payload []byte) ([]byte, error) {
	f.mu.Lock()
	f.called++
	f.payloads = append(f.payloads, append([]byte(nil), payload...))
	block, output, err := f.block, f.output, f.err
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, card.ErrDraftCancelled
		}
	}
	return []byte(output), err
}

func draftFixture(t *testing.T, runner *fakeRunner) (*cardService, string) {
	t.Helper()
	service, path := newTestService(t)
	writeCardFile(t, path, serviceFile)
	service.mu.Lock()
	service.refresh(true)
	service.mu.Unlock()
	service.drafts.newRunner = func(card.CommandSpec) (draftRunner, error) { return runner, nil }
	service.drafts.getenv = func(name string) string {
		if name == "PATH" {
			return "/usr/bin"
		}
		return ""
	}
	return service, path
}

func prepare(t *testing.T, service *cardService, owner any) protocol.CardDraft {
	t.Helper()
	result := service.Do(wire.CardOp{Op: wire.CardOpDraftPrepare}, owner)
	if result.Error != nil || result.Draft == nil {
		t.Fatalf("prepare: %+v", result.Error)
	}
	return *result.Draft
}

func waitForDraft(t *testing.T, service *cardService, owner any, id, state string) protocol.CardDraft {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		result := service.Do(wire.CardOp{Op: wire.CardOpDraftStatus, DraftID: id}, owner)
		if result.Draft != nil && result.Draft.State == state {
			return *result.Draft
		}
		if time.Now().After(deadline) {
			t.Fatalf("draft never reached %s: %+v", state, result)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runDraft(t *testing.T, service *cardService, owner any, id string) {
	t.Helper()
	if result := service.Do(wire.CardOp{Op: wire.CardOpDraftRun, DraftID: id}, owner); result.Error != nil {
		t.Fatalf("run: %+v", result.Error)
	}
}

// TestPrepareExecutesNothingAndShowsTheExactPayload.
func TestPrepareExecutesNothingAndShowsTheExactPayload(t *testing.T) {
	runner := &fakeRunner{output: goodAnswer}
	service, _ := draftFixture(t, runner)
	draft := prepare(t, service, "w1")

	if runner.Calls() != 0 {
		t.Fatal("prepare ran the command")
	}
	if draft.State != protocol.CardDraftPrepared || draft.Binary != "/fake/bin/claude" || len(draft.Argv) == 0 || draft.Argv[0] != "claude" {
		t.Fatalf("draft = %+v", draft)
	}
	if draft.PayloadBytes != len(draft.Payload) || draft.PayloadBytes == 0 {
		t.Fatalf("payload bytes %d, text %d", draft.PayloadBytes, len(draft.Payload))
	}
	if draft.TimeoutSec != 120 || draft.OutputCapBytes != 64<<10 || len(draft.Caveats) == 0 {
		t.Fatalf("limits/caveats = %+v", draft)
	}
}

func TestPrepareNeedsAReadableFileAndAPreset(t *testing.T) {
	runner := &fakeRunner{}
	service, path := draftFixture(t, runner)

	writeCardFile(t, path, "description: d\nbogus: 1\n")
	service.Do(wire.CardOp{Op: wire.CardOpReload}, "w1")
	if result := service.Do(wire.CardOp{Op: wire.CardOpDraftPrepare}, "w1"); result.Error == nil {
		t.Fatal("prepared from an unreadable file")
	}

	writeCardFile(t, path, "description: d\n")
	service.Do(wire.CardOp{Op: wire.CardOpReload}, "w1")
	if result := service.Do(wire.CardOp{Op: wire.CardOpDraftPrepare}, "w1"); result.Error == nil {
		t.Fatal("prepared without a preset")
	}
}

// TestDraftThenAcceptRewritesTheFileAndBacksUpTheOld.
func TestDraftThenAcceptRewritesTheFileAndBacksUpTheOld(t *testing.T) {
	runner := &fakeRunner{output: "Here you go:\n" + goodAnswer + "\n"}
	service, path := draftFixture(t, runner)
	draft := prepare(t, service, "w1")
	runDraft(t, service, "w1", draft.ID)
	ready := waitForDraft(t, service, "w1", draft.ID, protocol.CardDraftReady)

	if len(ready.Diff) == 0 || !ready.Diff[0].Generated || ready.BackupNotice == "" || ready.RewriteNotice == "" || ready.CandidateBytes == 0 {
		t.Fatalf("ready = %+v", ready)
	}
	if got, _ := os.ReadFile(path); string(got) != serviceFile {
		t.Fatal("card.yaml changed before accept")
	}

	token := getView(t, service, wire.CardOpGet).Token
	result := service.Do(wire.CardOp{Op: wire.CardOpDraftAccept, DraftID: draft.ID, BaseToken: token}, "w1")
	if result.Error != nil || result.Card == nil {
		t.Fatalf("accept: %+v", result.Error)
	}
	backup, err := os.ReadFile(filepath.Join(filepath.Dir(path), cardBackupName))
	if err != nil || string(backup) != serviceFile {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	if result.Card.File.State != protocol.CardFileOK || result.Card.Served == nil ||
		!strings.Contains(result.Card.Served.Card.Description, "code review") {
		t.Fatalf("view after accept = %+v", result.Card)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if status := service.Do(wire.CardOp{Op: wire.CardOpDraftStatus, DraftID: draft.ID}, "w1"); status.Error == nil {
		t.Fatal("an accepted draft is still there")
	}
}

// TestAcceptWithAStaleTokenIsAConflict: the file changed after the draft was made; nothing is written,
// the draft stays for review, and it says it is stale.
func TestAcceptWithAStaleTokenIsAConflict(t *testing.T) {
	runner := &fakeRunner{output: goodAnswer}
	service, path := draftFixture(t, runner)
	draft := prepare(t, service, "w1")
	runDraft(t, service, "w1", draft.ID)
	waitForDraft(t, service, "w1", draft.ID, protocol.CardDraftReady)

	edited := serviceFile + "# edited by hand\n"
	writeCardFile(t, path, edited)
	token := getView(t, service, wire.CardOpReload).Token

	result := service.Do(wire.CardOp{Op: wire.CardOpDraftAccept, DraftID: draft.ID, BaseToken: token}, "w1")
	if result.Error == nil || result.Error.Code != wire.CodeCardConflict || result.Card == nil {
		t.Fatalf("accept: %+v", result)
	}
	if got, _ := os.ReadFile(path); string(got) != edited {
		t.Fatal("card.yaml was written despite the conflict")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), cardBackupName)); err == nil {
		t.Fatal("a backup was written despite the conflict")
	}
	still := service.Do(wire.CardOp{Op: wire.CardOpDraftStatus, DraftID: draft.ID}, "w1")
	if still.Draft == nil || still.Draft.State != protocol.CardDraftReady || !still.Draft.Stale {
		t.Fatalf("draft = %+v", still.Draft)
	}
}

// TestAFailedWriteLeavesTheFileUntouched: card.yaml.bak is a directory, so the backup cannot be made
// and card.yaml must not be touched.
func TestAFailedWriteLeavesTheFileUntouched(t *testing.T) {
	runner := &fakeRunner{output: goodAnswer}
	service, path := draftFixture(t, runner)
	draft := prepare(t, service, "w1")
	runDraft(t, service, "w1", draft.ID)
	waitForDraft(t, service, "w1", draft.ID, protocol.CardDraftReady)

	if err := os.Mkdir(filepath.Join(filepath.Dir(path), cardBackupName), 0o700); err != nil {
		t.Fatal(err)
	}
	token := getView(t, service, wire.CardOpGet).Token
	result := service.Do(wire.CardOp{Op: wire.CardOpDraftAccept, DraftID: draft.ID, BaseToken: token}, "w1")
	if result.Error == nil {
		t.Fatal("accept succeeded without a backup")
	}
	if got, _ := os.ReadFile(path); string(got) != serviceFile {
		t.Fatal("card.yaml changed although the backup failed")
	}
}

// TestBadAnswersFailWithoutEchoingThem.
func TestBadAnswersFailWithoutEchoingThem(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123"
	answers := map[string]string{
		"prose":      "I cannot help with that " + secret,
		"unknown":    `{"description":"d","extra":"` + secret + `"}`,
		"credential": `{"description":"` + secret + `","skills":[]}`,
		"rules":      `{"description":"d","skills":[{"id":"Bad Id","name":"x","description":"y","tags":["t"]}]}`,
		"trailing":   goodAnswer + ` {"again":1}`,
	}
	for name, answer := range answers {
		runner := &fakeRunner{output: answer}
		service, _ := draftFixture(t, runner)
		draft := prepare(t, service, "w1")
		runDraft(t, service, "w1", draft.ID)
		failed := waitForDraft(t, service, "w1", draft.ID, protocol.CardDraftFailed)
		if failed.Error == "" || strings.Contains(failed.Error, secret) {
			t.Fatalf("%s: error = %q", name, failed.Error)
		}
	}
}

func TestRunnerErrorsBecomeOurSentences(t *testing.T) {
	runner := &fakeRunner{err: card.ErrDraftTimeout}
	service, _ := draftFixture(t, runner)
	draft := prepare(t, service, "w1")
	runDraft(t, service, "w1", draft.ID)
	failed := waitForDraft(t, service, "w1", draft.ID, protocol.CardDraftFailed)
	if !strings.Contains(failed.Error, "120 seconds") {
		t.Fatalf("error = %q", failed.Error)
	}
}

// TestDraftsBelongToTheirWindow: another window cannot see, run, cancel or accept it.
func TestDraftsBelongToTheirWindow(t *testing.T) {
	runner := &fakeRunner{output: goodAnswer}
	service, _ := draftFixture(t, runner)
	draft := prepare(t, service, "w1")
	for _, op := range []string{wire.CardOpDraftRun, wire.CardOpDraftStatus, wire.CardOpDraftCancel, wire.CardOpDraftAccept} {
		if result := service.Do(wire.CardOp{Op: op, DraftID: draft.ID}, "w2"); result.Error == nil {
			t.Fatalf("%s: another window got through", op)
		}
	}
}

// TestOneDraftPerWindow: a second prepare replaces the first.
func TestOneDraftPerWindow(t *testing.T) {
	service, _ := draftFixture(t, &fakeRunner{output: goodAnswer})
	first := prepare(t, service, "w1")
	second := prepare(t, service, "w1")
	if first.ID == second.ID {
		t.Fatal("same id twice")
	}
	if result := service.Do(wire.CardOp{Op: wire.CardOpDraftStatus, DraftID: first.ID}, "w1"); result.Error == nil {
		t.Fatal("the replaced draft is still there")
	}
}

// TestClosingTheWindowCancelsARunningDraft.
func TestClosingTheWindowCancelsARunningDraft(t *testing.T) {
	runner := &fakeRunner{block: make(chan struct{})}
	service, _ := draftFixture(t, runner)
	draft := prepare(t, service, "w1")
	runDraft(t, service, "w1", draft.ID)

	service.drafts.release("w1")

	deadline := time.Now().Add(2 * time.Second)
	for {
		service.drafts.mu.Lock()
		running := service.drafts.running
		service.drafts.mu.Unlock()
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the running draft was not cancelled")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if result := service.Do(wire.CardOp{Op: wire.CardOpDraftStatus, DraftID: draft.ID}, "w1"); result.Error == nil {
		t.Fatal("the draft survived its window")
	}
}

// TestExplicitCancelStopsARunningDraft.
func TestExplicitCancelStopsARunningDraft(t *testing.T) {
	runner := &fakeRunner{block: make(chan struct{})}
	service, _ := draftFixture(t, runner)
	draft := prepare(t, service, "w1")
	runDraft(t, service, "w1", draft.ID)
	result := service.Do(wire.CardOp{Op: wire.CardOpDraftCancel, DraftID: draft.ID}, "w1")
	if result.Draft == nil || result.Draft.State != protocol.CardDraftCancelled || result.Draft.ID != draft.ID {
		t.Fatalf("cancel = %+v", result)
	}
}

// TestAtMostTwoDraftsRun: a third run is refused as busy.
func TestAtMostTwoDraftsRun(t *testing.T) {
	runner := &fakeRunner{block: make(chan struct{})}
	service, _ := draftFixture(t, runner)
	defer close(runner.block)
	var ids [3]string
	for index, owner := range []string{"w1", "w2", "w3"} {
		ids[index] = prepare(t, service, owner).ID
	}
	runDraft(t, service, "w1", ids[0])
	runDraft(t, service, "w2", ids[1])
	result := service.Do(wire.CardOp{Op: wire.CardOpDraftRun, DraftID: ids[2]}, "w3")
	if result.Error == nil || result.Error.Code != wire.CodeBusy {
		t.Fatalf("third run = %+v", result)
	}
}

// TestIdleDraftsExpire.
func TestIdleDraftsExpire(t *testing.T) {
	service, _ := draftFixture(t, &fakeRunner{output: goodAnswer})
	draft := prepare(t, service, "w1")
	now := time.Now()
	service.now = func() time.Time { return now.Add(draftIdle + time.Minute) }
	service.drafts.sweep()
	result := service.Do(wire.CardOp{Op: wire.CardOpDraftStatus, DraftID: draft.ID}, "w1")
	if result.Draft == nil || result.Draft.State != protocol.CardDraftCancelled || result.Draft.Error != "expired" {
		t.Fatalf("draft = %+v", result)
	}
	if run := service.Do(wire.CardOp{Op: wire.CardOpDraftRun, DraftID: draft.ID}, "w1"); run.Error == nil {
		t.Fatal("an expired draft ran")
	}
}

// TestThePayloadHoldsNamesAndDescriptionsOnly: no label (which carries a location), no path-like or
// credential-like text.
func TestThePayloadHoldsNamesAndDescriptionsOnly(t *testing.T) {
	inventory := protocol.SkillInventory{Found: []protocol.FoundSkill{
		{Harness: "claude", HarnessVersion: "2.1.0", Scope: "user", Label: "~/.claude/skills/review", DirName: "review", Name: "Review", Description: "Reviews a diff.", SHA256: strings.Repeat("a", 64)},
		{Harness: "codex", Scope: "user", Label: "~/.agents/skills/deploy", DirName: "deploy", Name: "Deploy", Description: "Runs /Users/me/bin/deploy with sk-abcdefghijklmnop", SHA256: strings.Repeat("b", 64)},
	}}
	payload, truncated, err := buildDraftPayload("vmb", inventory)
	if err != nil || truncated {
		t.Fatalf("payload: %v truncated=%v", err, truncated)
	}
	text := string(payload)
	for _, banned := range []string{"~/.claude", "~/.agents", "/Users/me", "sk-abcdefghijklmnop", strings.Repeat("a", 64), "label", "sha256"} {
		if strings.Contains(text, banned) {
			t.Fatalf("payload contains %q:\n%s", banned, text)
		}
	}
	for _, wanted := range []string{"vmb", "2.1.0", "Reviews a diff.", "dir_name", "deploy"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("payload lacks %q:\n%s", wanted, text)
		}
	}
}

func TestBuildDraftCommand(t *testing.T) {
	env := func(name string) string {
		return map[string]string{"PATH": "/usr/bin", "HOME": "/h", "MY_TOKEN_VAR": "x"}[name]
	}
	claude, err := buildDraftCommand(card.OwnerDraft{Preset: card.PresetClaude, PassEnv: []string{"MY_TOKEN_VAR", "UNSET_VAR"}}, env)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(claude.spec.Argv, " ")
	for _, want := range []string{"claude", "-p", "--safe-mode", "--no-session-persistence"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("claude argv lacks %q: %v", want, claude.spec.Argv)
		}
	}
	if strings.Join(claude.envNames, ",") != "HOME,MY_TOKEN_VAR,PATH" {
		t.Fatalf("env names = %v (unset names must not be listed)", claude.envNames)
	}
	for _, entry := range claude.spec.Env {
		if strings.HasPrefix(entry, "UNSET_VAR=") {
			t.Fatal("an unset name was passed")
		}
	}
	if claude.spec.Timeout != 120*time.Second || claude.spec.MaxOutput != 64<<10 {
		t.Fatalf("limits = %v %d", claude.spec.Timeout, claude.spec.MaxOutput)
	}

	codex, err := buildDraftCommand(card.OwnerDraft{Preset: card.PresetCodex}, env)
	if err != nil || !strings.Contains(strings.Join(codex.spec.Argv, " "), "exec") || !strings.Contains(strings.Join(codex.spec.Argv, " "), "--ignore-user-config") {
		t.Fatalf("codex: %v %v", codex.spec.Argv, err)
	}
	if !strings.Contains(strings.Join(codex.caveats, " "), "no per-run switch") && !strings.Contains(strings.Join(codex.caveats, " "), "offers no per-run switch") {
		t.Fatalf("codex caveats must say skills and hooks cannot be switched off: %v", codex.caveats)
	}

	custom, err := buildDraftCommand(card.OwnerDraft{Preset: card.PresetCustom, Argv: []string{"/opt/draft", "--json"}}, env)
	if err != nil || custom.spec.Argv[0] != "/opt/draft" || strings.Join(custom.envNames, ",") != "PATH" {
		t.Fatalf("custom: %+v %v", custom, err)
	}
	if _, err := buildDraftCommand(card.OwnerDraft{}, env); err == nil {
		t.Fatal("no preset accepted")
	}
}
