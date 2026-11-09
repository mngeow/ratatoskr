package setup

// Steps 6 (service) and 7 (readiness). The launchd rules come from platform-devops:
//   - bootstrap gui/<uid> <plist>; bootout gui/<uid>/<label> only, never a domain-only bootout;
//   - `launchctl print` exit 0 means loaded; only the not-found exit counts as absent, and only after the GUI
//     domain answered; every other failure is unknown, and unknown never leads to a replace or a stop;
//   - the job state is established BEFORE any plist is written or replaced; a loaded job with no plist of ours
//     is treated as someone else's and left alone;
//   - replacing the plist, stopping the job and starting it are separate consents, all defaulting to No
//     (installing a plist where none exists defaults to Yes);
//   - the plist is rendered and linted in a private temp file before anything changes; logs are created 0600
//     and never truncated; no sudo, no `load`, no `kickstart`;
//   - a failed bootstrap after a bootout is reported as STOPPED, never as a rollback.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/ipc"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/sidecar"
)

type jobStatus int

const (
	jobUnknown jobStatus = iota
	jobNone
	jobLoaded
)

const (
	launchctlPath = "/bin/launchctl"
	plutilPath    = "/usr/bin/plutil"

	// launchctlNotFound is the exit code `launchctl print` gives for a service that does not exist. UNVERIFIED
	// on this machine: only this code counts as absent; any other failure is unknown.
	launchctlNotFound = 113
)

func (e *Engine) plistPath() string {
	return filepath.Join(e.env.Home, "Library", "LaunchAgents", Label+".plist")
}
func (e *Engine) logDir() string {
	return filepath.Join(e.env.Home, "Library", "Logs", "ratatoskr-openrig")
}
func (e *Engine) domain() string        { return fmt.Sprintf("gui/%d", e.env.UID) }
func (e *Engine) serviceTarget() string { return e.domain() + "/" + Label }

func (e *Engine) jobState(ctx context.Context) (jobStatus, string) {
	if e.env.GOOS != "darwin" {
		return jobUnknown, "not macOS"
	}
	if _, code, err := e.runTimed(ctx, 10*time.Second, launchctlPath, "print", e.domain()); err != nil || code != 0 {
		return jobUnknown, "the GUI launchd domain " + e.domain() + " is not available to this terminal"
	}
	_, code, err := e.runTimed(ctx, 10*time.Second, launchctlPath, "print", e.serviceTarget())
	switch {
	case err != nil:
		return jobUnknown, "launchctl print could not complete"
	case code == 0:
		return jobLoaded, ""
	case code == launchctlNotFound:
		return jobNone, ""
	}
	return jobUnknown, fmt.Sprintf("launchctl print %s exited %d, which is not the not-found answer", Label, code)
}

// inCheckout reports whether a go.mod sits in the directory of path or any parent.
func inCheckout(path string) bool {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, "go.mod")); err == nil {
			return true
		}
		if dir == filepath.Dir(dir) {
			return false
		}
	}
}

func (e *Engine) binaryPath() string {
	if e.env.Executable != nil {
		if p, err := e.env.Executable(); err == nil && filepath.IsAbs(p) {
			return p
		}
	}
	return "ratatoskr-openrig"
}

func (e *Engine) foregroundCommand() string {
	extra := ""
	if e.st.cand != nil && strings.HasPrefix(e.st.cand.ParticipantURL, "http://") {
		extra = " --insecure-http"
	}
	return fmt.Sprintf("%s serve --config %s%s", shellQuote(e.binaryPath()), shellQuote(e.env.ConfigPath), extra)
}

// skipReason is why the launchd step cannot run here; "" when it can.
func (e *Engine) skipReason() (reason string, status Status) {
	switch {
	case e.env.GOOS != "darwin":
		return "This is not macOS: the launchd service is not available. systemd is outside v1.", StatusSkipped
	case e.st.inSeat:
		return "OPENRIG_SESSION_NAME is set: the service is not installed or started from inside a seat.", StatusYourTurn
	case e.st.cand != nil && strings.HasPrefix(e.st.cand.ParticipantURL, "http://"):
		return "The participant API is plain http://. The launchd template has no --insecure-http argument, so the service step is skipped.", StatusSkipped
	case strings.Contains(e.binaryPath(), "/go-build"):
		return "This binary was started by `go run`: it is a temporary file. Build it and install it outside the checkout, then run setup again.", StatusNotDone
	}
	return "", ""
}

func (e *Engine) planService(ctx context.Context) Plan {
	if e.st.svc.follow != nil {
		return *e.st.svc.follow
	}
	p := Plan{Step: StepService}
	if reason, _ := e.skipReason(); reason != "" {
		p.Auto = true
		p.Notices = []Notice{{Warn, reason}}
		p.Instructions = []string{"Foreground command (keep it running in a terminal of your own):", "    " + e.foregroundCommand()}
		return p
	}
	binary := e.binaryPath()
	p.Facts = []Fact{
		{"service", Label + " (a launchd user agent that runs `serve`; logout stops it)"},
		{"binary", Printable(binary)}, {"config", Printable(e.env.ConfigPath)},
		{"logs", Printable(e.logDir())}, {"plist", Printable(e.plistPath())},
	}
	if inCheckout(binary) {
		p.Notices = append(p.Notices, Notice{Warn, "The binary is inside a source tree (a go.mod is above it). A job should point at an installed copy outside the checkout."})
		p.Questions = append(p.Questions, Question{ID: "checkout", Prompt: "Use this binary anyway?", DefaultYes: false})
	}
	p.Questions = append(p.Questions, Question{ID: "prepare", Prompt: "Prepare the log directory and render the plist? (nothing is loaded yet)", DefaultYes: true})
	return p
}

func (e *Engine) applyService(ctx context.Context, a Answers) Result {
	if reason, status := e.skipReason(); reason != "" {
		r := Result{Status: status, Detail: reason, Lines: []string{reason, "Foreground command: " + e.foregroundCommand()}}
		if status == StatusSkipped {
			r.Detail = "not available here: run serve in the foreground"
		}
		return r
	}
	svc := &e.st.svc
	switch svc.phase {
	case 1:
		return e.serviceInstall(a)
	case 2:
		return e.serviceStart(ctx, a)
	}
	return e.serviceRender(ctx, a)
}

func (e *Engine) follow(plan Plan) *Plan {
	e.st.svc.follow = &plan
	return &plan
}

// serviceRender is round one: prepare directories, render and lint, establish the job state, and either finish
// or ask the next question.
func (e *Engine) serviceRender(ctx context.Context, a Answers) Result {
	st := &e.st
	svc := &st.svc
	binary := e.binaryPath()
	if inCheckout(binary) && !yes(a, "checkout", false) {
		return Result{Status: StatusSkipped, Detail: "binary is inside a checkout"}
	}
	if !yes(a, "prepare", true) {
		return Result{Status: StatusSkipped, Detail: "declined"}
	}
	if st.cfg == nil {
		return e.stop(StepService, StatusNotDone, ExitFailed, "there is no configuration yet")
	}
	var lines []string
	if err := e.prepareDirs(&lines); err != nil {
		return e.stop(StepService, StatusNotDone, ExitFailed, "%v", err)
	}
	rendered, err := RenderPlist(e.env.Assets.PlistTemplate, PlistValues{
		Binary: binary, Config: e.env.ConfigPath, StateDir: st.cfg.StateDir, LogDir: e.logDir(), Home: e.env.Home,
	})
	if err != nil {
		return e.stop(StepService, StatusNotDone, ExitFailed, "%v", err)
	}
	if err := e.lint(ctx, rendered); err != nil {
		return e.stop(StepService, StatusNotDone, ExitFailed, "%v (nothing was installed or stopped)", err)
	}
	lines = append(lines, "plist rendered and checked with plutil -lint.")

	// The job state comes BEFORE any plist is written or replaced.
	target := e.plistPath()
	state, detail := e.jobState(ctx)
	_, lstatErr := os.Lstat(target)
	existed := lstatErr == nil
	switch {
	case state == jobUnknown:
		lines = append(lines, "Cannot tell whether the job is loaded: "+Printable(detail)+".",
			"Nothing was installed, replaced, started or stopped. Once launchd answers, inspect with:  launchctl print "+e.serviceTarget(),
			"and run setup again.")
		return Result{Status: StatusYourTurn, Detail: "launchd state unknown; nothing changed", Lines: lines}
	case state == jobLoaded && !existed:
		st.foreignJob = true
		lines = append(lines, "A job labelled "+Label+" is loaded, but there is no plist of ours at "+Printable(target)+". It may be someone else's service: nothing was written or stopped.",
			"Inspect it with:  launchctl print "+e.serviceTarget())
		return Result{Status: StatusYourTurn, Detail: "a job with this label is loaded and was not installed by setup", Lines: lines}
	}
	svc.rendered, svc.target, svc.binary, svc.existed, svc.job, svc.jobDetail = rendered, target, binary, existed, state, detail

	if !existed {
		svc.phase = 1
		return Result{Lines: lines, Follow: e.follow(Plan{Step: StepService,
			Questions: []Question{{ID: "install", Prompt: "Install the plist at " + Printable(target) + "?", DefaultYes: true}}})}
	}
	if _, err := OwnedRegular(target); err != nil {
		return e.stop(StepService, StatusNotDone, ExitFailed, "%v", err)
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return e.stop(StepService, StatusNotDone, ExitFailed, "cannot read the installed plist")
	}
	if string(current) == rendered {
		lines = append(lines, "The installed plist is already this one.")
		return e.serviceJobRound(lines)
	}
	svc.phase = 1
	return Result{Lines: lines, Follow: e.follow(Plan{Step: StepService,
		Questions: []Question{{ID: "replace", Prompt: "The installed plist differs. Replace it? (the old file is kept as a .bak-<time> copy)",
			DefaultYes: false, Detail: LineDiff(string(current), rendered, 24)}}})}
}

// serviceInstall is round two: write or replace the plist.
func (e *Engine) serviceInstall(a Answers) Result {
	svc := &e.st.svc
	var lines []string
	if svc.existed {
		if !yes(a, "replace", false) {
			svc.phase, svc.follow = 0, nil
			return Result{Status: StatusSkipped, Detail: "installed plist differs; replacement declined",
				Lines: []string{"Leaving the installed plist as it is. A running job is untouched."}}
		}
		backup, err := CopyAside(svc.target, e.env.Now())
		if err != nil {
			return e.stop(StepService, StatusNotDone, ExitFailed, "%v", err)
		}
		if err := WriteFileAtomic(svc.target, []byte(svc.rendered), 0o600); err != nil {
			return e.stop(StepService, StatusNotDone, ExitFailed, "%v (the old plist is untouched)", err)
		}
		lines = append(lines, "replaced; the old plist is kept as "+Printable(backup))
	} else {
		if !yes(a, "install", true) {
			svc.phase, svc.follow = 0, nil
			return Result{Status: StatusSkipped, Detail: "plist not installed"}
		}
		if err := WriteFileAtomic(svc.target, []byte(svc.rendered), 0o600); err != nil {
			return e.stop(StepService, StatusNotDone, ExitFailed, "%v", err)
		}
		lines = append(lines, "installed (mode 0600).")
	}
	e.st.plistChanged = true
	return e.serviceJobRound(lines)
}

// serviceJobRound finishes the step or asks the start/restart question.
func (e *Engine) serviceJobRound(lines []string) Result {
	st := &e.st
	svc := &st.svc
	target := svc.target
	if svc.job == jobNone {
		svc.phase = 2
		return Result{Lines: lines, Follow: e.follow(Plan{Step: StepService,
			Questions: []Question{{ID: "start", Prompt: "Start the service now? (launchctl bootstrap " + e.domain() + " ...)", DefaultYes: false,
				Detail: []string{"launchctl bootstrap " + e.domain() + " " + shellQuote(target)}}}})}
	}
	if !st.plistChanged && !st.configChanged && !st.credNew {
		svc.phase, svc.follow = 0, nil
		return Result{Status: StatusAlready, Detail: "loaded, up to date", Lines: append(lines, "The job is loaded and nothing it reads has changed.")}
	}
	svc.phase = 2
	return Result{Lines: append(lines, "The job is loaded, and the plist, the configuration or the credential changed. A running bridge keeps its old settings until it is stopped and started again."),
		Follow: e.follow(Plan{Step: StepService,
			Questions: []Question{{ID: "restart", Prompt: "Stop the running job and start it again? (launchctl bootout, then bootstrap)", DefaultYes: false,
				Detail: []string{"launchctl bootout " + e.serviceTarget(), "launchctl bootstrap " + e.domain() + " " + shellQuote(target)}}}})}
}

// serviceStart is round three: bootstrap, or bootout then bootstrap.
func (e *Engine) serviceStart(ctx context.Context, a Answers) Result {
	svc := &e.st.svc
	target := svc.target
	restart := svc.job == jobLoaded
	id := "start"
	if restart {
		id = "restart"
	}
	svc.phase, svc.follow = 0, nil
	if !yes(a, id, false) {
		if restart {
			return Result{Status: StatusYourTurn, Detail: "restart declined: the running bridge has old settings",
				Lines: []string{"Left running with its old settings. Restart later with:",
					"launchctl bootout " + e.serviceTarget() + " && launchctl bootstrap " + e.domain() + " " + shellQuote(target)}}
		}
		return Result{Status: StatusYourTurn, Detail: "plist installed; job not started",
			Lines: []string{"Not started. Start it later with:  launchctl bootstrap " + e.domain() + " " + shellQuote(target)}}
	}
	if restart {
		out, code, err := e.runTimed(ctx, 30*time.Second, launchctlPath, "bootout", e.serviceTarget())
		if err != nil || code != 0 {
			return Result{Status: StatusNotDone, Detail: "bootout failed",
				Lines: append([]string{fmt.Sprintf("launchctl bootout failed (exit %d). The job may still be running; nothing was started.", code)}, outputLines(out, 6)...)}
		}
	}
	out, code, err := e.runTimed(ctx, 30*time.Second, launchctlPath, "bootstrap", e.domain(), target)
	if err != nil || code != 0 {
		head := fmt.Sprintf("launchctl bootstrap failed (exit %d); the job is not loaded.", code)
		if restart {
			head = fmt.Sprintf("The service is now STOPPED: bootstrap failed (exit %d) after the old job was stopped. Nothing was rolled back.", code)
		}
		lines := append([]string{head}, outputLines(out, 8)...)
		lines = append(lines, "Read the log:     tail -n 50 "+shellQuote(filepath.Join(e.logDir(), "serve.err.log")),
			"After fixing it:  launchctl bootstrap "+e.domain()+" "+shellQuote(target))
		return Result{Status: StatusNotDone, Detail: "bootstrap failed", Lines: lines}
	}
	e.st.serviceStarted = true
	if restart {
		return Result{Status: StatusDone, Detail: "restarted", Lines: []string{"bootstrapped."}}
	}
	return Result{Status: StatusDone, Detail: "loaded", Lines: []string{"bootstrapped."}}
}

func outputLines(b []byte, n int) []string {
	text := strings.TrimSpace(string(b))
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	for i := range lines {
		lines[i] = "    " + Printable(lines[i])
	}
	return lines
}

// prepareDirs makes the state, log and LaunchAgents directories and the two log files. Logs are never truncated.
func (e *Engine) prepareDirs(lines *[]string) error {
	if err := CheckPrivateDir(e.st.cfg.StateDir, true); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.plistPath()), 0o755); err != nil {
		return errors.New("cannot create ~/Library/LaunchAgents")
	}
	if err := os.MkdirAll(filepath.Dir(e.logDir()), 0o755); err != nil {
		return errors.New("cannot create ~/Library/Logs")
	}
	if err := CheckPrivateDir(e.logDir(), true); err != nil {
		return err
	}
	for _, name := range []string{"serve.out.log", "serve.err.log"} {
		note, err := EnsureLogFile(filepath.Join(e.logDir(), name))
		if err != nil {
			return err
		}
		if note != "" {
			*lines = append(*lines, "WARNING: "+note)
		}
	}
	return nil
}

func (e *Engine) lint(ctx context.Context, rendered string) error {
	tmp, err := os.CreateTemp(e.st.cfg.StateDir, ".plist-lint-*")
	if err != nil {
		return errors.New("cannot create a private temporary file to check the plist")
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(rendered); err != nil {
		_ = tmp.Close()
		return errors.New("cannot write the temporary plist")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("cannot write the temporary plist")
	}
	out, code, runErr := e.runTimed(ctx, 15*time.Second, plutilPath, "-lint", tmp.Name())
	if runErr != nil || code != 0 {
		return fmt.Errorf("plutil -lint did not accept the rendered plist: %s", strings.Join(outputLines(out, 6), " "))
	}
	return nil
}

// ---- step 7: readiness ----

func (e *Engine) planReadiness() Plan {
	return Plan{Step: StepReadiness, Auto: true,
		Notices: []Notice{{Info, "Waits up to 20 s for the bridge to answer on its socket (only if the service was started or is already running)."}}}
}

func (e *Engine) applyReadiness(ctx context.Context) Result {
	st := &e.st
	if st.cfg == nil {
		return e.stop(StepReadiness, StatusNotDone, ExitFailed, "there is no configuration yet")
	}
	expect := st.serviceStarted
	if !expect && e.env.GOOS == "darwin" && !st.inSeat && !st.foreignJob {
		if state, _ := e.jobState(ctx); state == jobLoaded {
			expect = true
		}
	}
	deadline := time.Now()
	if expect {
		deadline = time.Now().Add(20 * time.Second)
	}
	var rep sidecar.StatusReport
	got := false
	for {
		cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		resp, err := ipc.Call(cctx, st.cfg.StateDir, st.cfg.SocketPath(), ipc.Request{Op: ipc.OpStatus})
		cancel()
		if err == nil && resp.Outcome == ipc.OutcomeOK && json.Unmarshal(resp.Status, &rep) == nil {
			got = true
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	if !got {
		if expect {
			return Result{Status: StatusNotDone, Detail: "the bridge did not answer within 20 s", Lines: []string{
				"Look at:  tail -n 50 " + shellQuote(filepath.Join(e.logDir(), "serve.err.log")),
				"and:      launchctl print " + e.serviceTarget()}}
		}
		return Result{Status: StatusSkipped, Detail: "bridge not running", Lines: []string{"The bridge is not running, so there is nothing to check yet."}}
	}
	lines := StatusLines(st.cfg.ParticipantURL, pinSummary(st.cfg), rep)
	problems := 0
	if rep.Gateway != "reachable" {
		problems++
		lines = append(lines, line("gateway %q: check that the gateway runs on VM-A and that participant_url and the pin match its pair output.", rep.Gateway))
	}
	if !rep.Registered {
		problems++
		lines = append(lines, "session not registered yet: it registers after the first successful contact with the gateway.")
	}
	if !rep.SeatReady {
		problems++
		lines = append(lines, line("operator seat not ready (%s): start or check the seat in OpenRig; setup never starts one.", rep.SeatWord))
	}
	if rep.Stopped != "" {
		problems++
		lines = append(lines, line("the bridge was stopped (%s): pair again, or run `serve --resume` if it was only replaced.", rep.Stopped))
	}
	if problems == 0 {
		return Result{Status: StatusDone, Detail: "gateway reachable, seat ready", Lines: lines}
	}
	return Result{Status: StatusYourTurn, Detail: fmt.Sprintf("%d field(s) not ready, see above", problems), Lines: lines}
}

// StatusLines formats the sidecar's status report as the `status` command does.
func StatusLines(url, pin string, st sidecar.StatusReport) []string {
	seat := fmt.Sprintf("%s  ready", st.OperatorSeat)
	if !st.SeatReady {
		seat = fmt.Sprintf("%s  not ready (%s)", st.OperatorSeat, st.SeatWord)
	}
	registered := "registered"
	if !st.Registered {
		registered = "not registered"
	}
	out := []string{
		line("bridge:          running (pid %d, version %s, up %s)", st.PID, st.Version, time.Since(st.StartedAt).Round(time.Second)),
		line("gateway:         %s", st.Gateway),
		line("participant API: %s  contract 1.0  pin %s", url, pin),
		line("session:         %s  %s", st.Session, registered),
		line("poll:            %s", st.Poll),
		line("operator seat:   %s", seat),
		line("rig:             %s", st.RigPath),
	}
	if st.Stopped != "" {
		out = append(out, line("stopped:         %s", st.Stopped))
	}
	return out
}
