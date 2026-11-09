package setup

// The setup engine: the nine steps of `ratatoskr-openrig setup` as functions that take values and return
// results, with NO terminal I/O (contract 4.1 rule 4, ruling W5). The Bubble Tea front (internal/setup/tui)
// collects answers, calls Plan and Apply, and renders what they return. Nothing in this package imports Charm.
//
// How a step runs:
//
//	plan, err := e.Plan(ctx, step)        // read-only: what was found, defaults, warnings, the questions
//	... the front shows plan and collects an Answers ...
//	res := e.Apply(ctx, step, answers)    // does the work, once
//	for res.Follow != nil { ... }         // a step with several consents returns the next Plan; answer it
//	                                      // with another Apply of the same step
//
// Plan and Apply may run external commands (rig, launchctl, plutil) and may wait (readiness waits up to 20 s):
// the front calls them from a tea.Cmd and passes a context it can cancel. Both are safe to call again after a
// step was done: each step detects that it is already done.
//
// Secrets: the pasted block and the pairing code travel as []byte in Answers or in ParsePaste/StagePair. The
// engine takes ownership and zeroes them after use; the front must not keep a copy, must not render them, and
// should zero its own buffer when it is done. Nothing the engine returns contains the code.
//
// Defaults: a Question's DefaultYes is the engine's ruling. Every question that stops, replaces or starts
// something defaults to No; the front must start its choice on DefaultYes and never invert it.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"context"
	"time"
)

// StepID names a step, in running order.
type StepID int

const (
	StepPreflight       StepID = iota + 1 // 1: config path, state dir, what exists
	StepLocalOpenRig                      // 2: rig, rig_env_path, openrig_home, operator_seat, openrig_url
	StepPairingMaterial                   // 3: VM-A block (or URL, pin, code) -> staged, then confirmed
	StepConfig                            // 4: validate, diff, atomic write
	StepPair                              // 5: redeem the code in memory
	StepService                           // 6: launchd (darwin); a foreground command elsewhere
	StepReadiness                         // 7: wait for the socket, show the status
	StepSkill                             // 8: write SKILL.md, offer rig walk
	StepFinish                            // 9: the checklist (Summary)
)

// Steps lists every step in order.
var Steps = []StepID{StepPreflight, StepLocalOpenRig, StepPairingMaterial, StepConfig, StepPair, StepService, StepReadiness, StepSkill, StepFinish}

// Title is the label for the step list.
func (s StepID) Title() string { return stepTitles[s] }

var stepTitles = map[StepID]string{
	StepPreflight: "Preflight", StepLocalOpenRig: "Local OpenRig", StepPairingMaterial: "VM-A pairing material",
	StepConfig: "Configuration file", StepPair: "Pair", StepService: "Service", StepReadiness: "Readiness",
	StepSkill: "Skill activation", StepFinish: "Finish",
}

// Status is how a step ended. Its String form is the label shown in the checklist.
type Status string

const (
	StatusDone     Status = "done"
	StatusAlready  Status = "already done"
	StatusSkipped  Status = "skipped"
	StatusNotDone  Status = "NOT DONE"
	StatusYourTurn Status = "YOUR TURN"
)

// Level marks a Notice.
type Level int

const (
	Info Level = iota
	Warn
)

// Notice is one line the front shows as text; Warn lines should stand out.
type Notice struct {
	Level Level
	Text  string
}

// Fact is one "label: value" line of what Plan found.
type Fact struct{ Label, Value string }

// Field is a text input with a default. Secret fields are masked and arrive in Answers.Code, never in Text.
type Field struct {
	ID       string
	Label    string
	Default  string
	Help     string
	Optional bool
	Secret   bool
}

// Question is a consent choice. DefaultYes is the engine's ruling (see the package comment).
type Question struct {
	ID         string
	Prompt     string
	DefaultYes bool
	Detail     []string // lines to show with the question (a diff, a command)
}

// Plan is what a step found and what it needs. Auto means there is nothing to ask: the front may call Apply
// at once with empty Answers.
type Plan struct {
	Step         StepID
	Facts        []Fact
	Notices      []Notice
	Instructions []string // text to show first (the VM-A command and the pin rule)
	Fields       []Field
	Questions    []Question
	Changes      []Change
	PasteBlock   bool // StepPairingMaterial: also offer a masked area for the whole pasted block
	NeedCode     bool // StepPairingMaterial: a pairing code is required (no credential yet)
	Auto         bool
}

// Answers is what the owner chose. Text is keyed by Field.ID, Yes by Question.ID (a missing key means the
// question's default). Block and Code are secrets; see the package comment.
type Answers struct {
	Text  map[string]string
	Yes   map[string]bool
	Block []byte
	Code  []byte
}

// Result is what Apply did. When FieldErrors is not empty nothing was applied: show the messages beside the
// fields and call Apply again. While Follow is not nil the step is not finished: show Follow, collect its
// answers and call Apply again for the same step.
type Result struct {
	Step        StepID
	Status      Status
	Detail      string   // one line for the checklist
	Lines       []string // what to show now: findings, commands to copy, error output (already terminal-safe)
	FieldErrors map[string]string
	Follow      *Plan
	Stop        bool // the run cannot continue; ExitCode says how it ends
	ExitCode    int
}

// PasteSummary is what a parsed block (or typed fields) contained. It never holds the code.
type PasteSummary struct {
	Participant  string
	URL          string
	Pin          string // sha256:HEX, empty for plain HTTP
	Plain        bool
	KeyPinSeen   bool
	Rotation     bool
	StaleNote    string
	CodeReceived bool
}

// Row is one line of the final checklist.
type Row struct {
	Step   StepID
	Status Status
	Detail string
}

// Summary is the final screen: the rows, then what the owner must still do. The last Todo line is always
// "scenario 8 round trip: PENDING USER EXECUTION".
type Summary struct {
	Rows []Row
	Todo []string
}

// Exit codes of the command (the bridge's own).
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitUsage     = 2 // not a terminal, no echo control, bad configuration
	ExitUncertain = 3 // the pairing code may or may not have been redeemed
)

// Assets are the two embedded files.
type Assets struct{ PlistTemplate, Skill string }

// Runner runs a command and returns its combined output (bounded), its exit code, and an error only when it
// could not run or the context ended (then the code is -1). Tests supply a fake.
type Runner func(ctx context.Context, name string, args ...string) (out []byte, code int, err error)

// Env is everything the engine reads from the machine, so a test can replace it.
type Env struct {
	ConfigPath   string // absolute, clean
	InsecureHTTP bool
	Home         string
	UID          int
	GOOS         string
	Getenv       func(string) string
	LookPath     func(string) (string, error)
	Executable   func() (string, error)
	Run          Runner
	Now          func() time.Time
	Assets       Assets
}

// Engine holds the state shared by the steps of one run.
type Engine struct {
	env Env
	st  state
}

// DefaultEnv is the real machine: os.UserHomeDir, os.Getenv, exec.LookPath, os.Executable, a bounded exec
// Runner, time.Now. configPath may be empty for the default path.
func DefaultEnv(configPath string, insecureHTTP bool, assets Assets) (Env, error) {
	return defaultEnv(configPath, insecureHTTP, assets)
}

// New makes an engine. Close it when done.
func New(env Env) *Engine { return &Engine{env: env} }

// Plan is the read-only half of a step. It may run read-only commands (rig --version is not among them; those
// run in Apply when the owner agrees). It never changes a file.
func (e *Engine) Plan(ctx context.Context, step StepID) (Plan, error) { return e.plan(ctx, step) }

// Apply is the half that acts. The first call for a step takes the answers to its Plan; a Result with Follow
// asks for another round.
func (e *Engine) Apply(ctx context.Context, step StepID, a Answers) Result {
	return e.apply(ctx, step, a)
}

// Validate checks one text field of StepLocalOpenRig or StepPairingMaterial; "" means acceptable. Use it for
// inline messages while typing; Apply checks again.
func (e *Engine) Validate(step StepID, fieldID, value string) string {
	return e.validate(step, fieldID, value)
}

// ParsePaste parses the pasted block (section 4.2), checks it against the configured participant and the
// plain-HTTP flag, and stages it. The block is zeroed. A nil error means the summary can be shown and
// confirmed with Apply(StepPairingMaterial, Answers{Yes: {"use": true}}).
func (e *Engine) ParsePaste(block []byte) (PasteSummary, error) { return e.parsePaste(block) }

// StagePair validates and stages values typed one at a time. code is zeroed (and may be nil when no code is
// needed). Confirm like ParsePaste.
func (e *Engine) StagePair(url, pin string, code []byte) (PasteSummary, error) {
	return e.stagePair(url, pin, code)
}

// Results are the finished steps so far.
func (e *Engine) Results() []Result { return e.results() }

// Summary is the final checklist.
func (e *Engine) Summary() Summary { return e.summary() }

// ExitCode is how the command should end: ExitOK unless a step stopped the run.
func (e *Engine) ExitCode() int { return e.exitCode() }

// Close zeroes every secret the engine still holds. Safe to call twice.
func (e *Engine) Close() { e.close() }
