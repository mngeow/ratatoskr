package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
)

// staged is a parsed block or typed values waiting for the owner's "use".
type staged struct {
	URL, Pin string
	Code     []byte
}

func (s *staged) wipe() {
	if s != nil {
		Wipe(s.Code)
		s.Code = nil
	}
}

// serviceState is the memory of the multi-round service step.
type serviceState struct {
	phase     int // 0 first round, 1 plist consent, 2 job consent
	follow    *Plan
	rendered  string
	target    string
	binary    string
	existed   bool
	job       jobStatus
	jobDetail string
}

type state struct {
	detected bool
	inSeat   bool

	oldCfg   *config.Config
	oldErr   error
	cfgThere bool
	stateDir string
	credPath string
	credHave bool

	cand *config.Config // the configuration the owner asked for
	cfg  *config.Config // the configuration as loaded from disk after step 4

	staged  *staged
	chosen  *staged // confirmed with "use"
	summary PasteSummary

	configChanged  bool
	credNew        bool
	plistChanged   bool
	serviceStarted bool
	foreignJob     bool
	allowHTTP      bool // the owner accepted plain HTTP inside the wizard (allow_http, or kept an http config)

	svc serviceState

	done     map[StepID]Result
	stopped  bool
	exitCode int
}

func defaultEnv(configPath string, insecureHTTP bool, assets Assets) (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return Env{}, errors.New("cannot find the home directory")
	}
	if configPath == "" {
		if configPath, err = config.DefaultPath(); err != nil {
			return Env{}, err
		}
	}
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return Env{}, errors.New("--config must be a clean absolute path")
	}
	return Env{
		ConfigPath: configPath, InsecureHTTP: insecureHTTP, Home: home, UID: os.Geteuid(), GOOS: runtime.GOOS,
		Getenv: os.Getenv, LookPath: exec.LookPath,
		Executable: func() (string, error) {
			p, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(p)
		},
		Run: execRun, Now: time.Now, Assets: assets,
	}, nil
}

type limitedOutput struct{ buf bytes.Buffer }

func (l *limitedOutput) Write(p []byte) (int, error) {
	if room := 8192 - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

// execRun is the real Runner: no shell, the owner's environment, combined bounded output.
func execRun(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out limitedOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, -1, err
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return out.buf.Bytes(), -1, ctx.Err()
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.buf.Bytes(), 0, nil
	case errors.As(err, &exitErr):
		return out.buf.Bytes(), exitErr.ExitCode(), nil
	}
	return out.buf.Bytes(), -1, err
}

func (e *Engine) runTimed(ctx context.Context, limit time.Duration, name string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return e.env.Run(ctx, name, args...)
}

// ---- dispatch ----

func (e *Engine) plan(ctx context.Context, step StepID) (Plan, error) {
	if _, ok := stepTitles[step]; !ok {
		return Plan{}, fmt.Errorf("unknown step %d", step)
	}
	if err := e.detect(ctx); err != nil {
		return Plan{}, err
	}
	switch step {
	case StepPreflight:
		return e.planPreflight(ctx), nil
	case StepLocalOpenRig:
		return e.planLocal(), nil
	case StepPairingMaterial:
		return e.planPairingMaterial(), nil
	case StepConfig:
		return e.planConfig(), nil
	case StepPair:
		return e.planPair(), nil
	case StepService:
		return e.planService(ctx), nil
	case StepReadiness:
		return e.planReadiness(), nil
	case StepSkill:
		return e.planSkill(), nil
	}
	return Plan{Step: StepFinish, Auto: true}, nil
}

func (e *Engine) apply(ctx context.Context, step StepID, a Answers) Result {
	defer Wipe(a.Block)
	if err := e.detect(ctx); err != nil {
		Wipe(a.Code)
		return e.stop(step, StatusNotDone, ExitUsage, "%s", err.Error())
	}
	var r Result
	switch step {
	case StepPreflight:
		r = e.applyPreflight()
	case StepLocalOpenRig:
		r = e.applyLocal(ctx, a)
	case StepPairingMaterial:
		r = e.applyPairingMaterial(a)
	case StepConfig:
		r = e.applyConfig(a)
	case StepPair:
		r = e.applyPair()
	case StepService:
		r = e.applyService(ctx, a)
	case StepReadiness:
		r = e.applyReadiness(ctx)
	case StepSkill:
		r = e.applySkill(ctx, a)
	case StepFinish:
		r = Result{Status: StatusDone}
	default:
		r = e.stop(step, StatusNotDone, ExitFailed, "unknown step")
	}
	Wipe(a.Code)
	r.Step = step
	if r.Status != "" && r.Follow == nil && len(r.FieldErrors) == 0 {
		if e.st.done == nil {
			e.st.done = map[StepID]Result{}
		}
		e.st.done[step] = r
	}
	if r.Stop {
		e.st.stopped, e.st.exitCode = true, r.ExitCode
	}
	return r
}

func (e *Engine) stop(step StepID, status Status, code int, format string, args ...any) Result {
	return Result{Step: step, Status: status, Detail: fmt.Sprintf(format, args...), Stop: true, ExitCode: code,
		Lines: []string{fmt.Sprintf(format, args...)}}
}

// insecureHTTP is the one effective plain-HTTP switch: the launch flag or the owner's consent in step 3.
func (e *Engine) insecureHTTP() bool { return e.env.InsecureHTTP || e.st.allowHTTP }

func yes(a Answers, id string, def bool) bool {
	if v, ok := a.Yes[id]; ok {
		return v
	}
	return def
}

func (e *Engine) results() []Result {
	var out []Result
	for _, step := range Steps {
		if r, ok := e.st.done[step]; ok {
			out = append(out, r)
		}
	}
	return out
}

func (e *Engine) exitCode() int {
	if e.st.stopped {
		return e.st.exitCode
	}
	return ExitOK
}

func (e *Engine) close() {
	e.st.staged.wipe()
	e.st.chosen.wipe()
	e.st.staged, e.st.chosen = nil, nil
}

func (e *Engine) summary() Summary {
	var s Summary
	for _, step := range Steps {
		if step == StepFinish {
			continue
		}
		r, ok := e.st.done[step]
		if !ok {
			s.Rows = append(s.Rows, Row{Step: step, Status: StatusNotDone, Detail: "not reached"})
			continue
		}
		s.Rows = append(s.Rows, Row{Step: step, Status: r.Status, Detail: r.Detail})
		if r.Status == StatusYourTurn || r.Status == StatusNotDone {
			s.Todo = append(s.Todo, fmt.Sprintf("%s: %s", step.Title(), r.Detail))
		}
	}
	sort.SliceStable(s.Rows, func(a, b int) bool { return s.Rows[a].Step < s.Rows[b].Step })
	s.Todo = append(s.Todo,
		"check the bridge with: ratatoskr-openrig status",
		"known limitation: OpenRig shows \"From: <unknown sender>\" on messages the bridge types",
		"scenario 8 round trip: PENDING USER EXECUTION")
	return s
}

// ---- detection ----

func (e *Engine) detect(ctx context.Context) error {
	if e.st.detected {
		return nil
	}
	env := e.env
	if env.ConfigPath == "" || !filepath.IsAbs(env.ConfigPath) {
		return errors.New("the configuration path must be absolute")
	}
	e.st.inSeat = env.Getenv("OPENRIG_SESSION_NAME") != ""
	switch _, err := os.Lstat(env.ConfigPath); {
	case err == nil:
		e.st.cfgThere = true
		e.st.oldCfg, e.st.oldErr = config.Load(env.ConfigPath, true)
	case errors.Is(err, os.ErrNotExist):
	default:
		return errors.New("cannot inspect the configuration file")
	}
	e.st.stateDir = config.DefaultStateDir(env.Home)
	e.st.credPath = filepath.Join(e.st.stateDir, "credential")
	if e.st.oldCfg != nil {
		e.st.stateDir, e.st.credPath = e.st.oldCfg.StateDir, e.st.oldCfg.CredentialPath
	}
	if info, err := os.Lstat(e.st.credPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		e.st.credHave = true
	}
	// The candidate starts from the existing configuration, else the fixed names.
	if e.st.oldCfg != nil {
		kept := *e.st.oldCfg
		e.st.cand = &kept
	} else {
		e.st.cand = &config.Config{ConfigVersion: config.Version, Participant: "openrig-main", Session: "operator"}
	}
	e.st.cand.Home = env.Home
	e.st.cand.StateDir, e.st.cand.CredentialPath = e.st.stateDir, e.st.credPath
	e.st.detected = true
	_ = ctx
	return nil
}

func line(format string, args ...any) string { return Printable(fmt.Sprintf(format, args...)) }
