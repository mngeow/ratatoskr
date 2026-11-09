// Package rigcli is the only place the bridge talks to OpenRig, and it does so through three `rig` commands
// and nothing else (docs/implementation/openrig-bridge-contract.md section 10 and hard rule 1):
//
//	rig --version
//	rig ps --nodes --rig RIG --json --fields canonicalSessionName,sessionStatus,startupStatus,intentionallyStopped
//	rig send SEAT TEXT
//
// Every child gets an environment built from scratch (PATH, HOME, OPENRIG_HOME, optionally OPENRIG_URL, LANG)
// and never anything from the sidecar's own environment, so it cannot carry a seat identity. Standard input is
// /dev/null, the working directory is /, output is bounded, and there is no shell.
//
// UNTESTED. Under GR-3 nothing here has been run, and the shapes of `rig`'s output are unverified.
package rigcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

const (
	// SendTimeout is the hard limit on one `rig send`.
	SendTimeout = 10 * time.Second
	// VersionTimeout is the limit on `rig --version`.
	VersionTimeout = 5 * time.Second

	maxOutput     = 64 * 1024
	maxStderrEcho = 200
)

// Result is what a finished child left behind.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int  // -1 when the child was killed or ended by a signal
	TimedOut bool // the context ended before the child did
}

// StartError means the child could not be started at all: nothing ran.
type StartError struct{ Err error }

func (e *StartError) Error() string { return "cannot start rig: " + e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// Runner runs one child process. The real one is ExecRunner; tests supply a fake.
type Runner interface {
	Run(ctx context.Context, path string, args []string, env []string) (Result, error)
}

// ExecRunner runs a real process.
type ExecRunner struct{}

type limitedBuffer struct {
	buf bytes.Buffer
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil // drop the rest rather than block the child
}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, path string, args []string, env []string) (Result, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Dir = "/"
	cmd.Stdin = nil // /dev/null
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second

	if err := cmd.Start(); err != nil {
		return Result{}, &StartError{Err: err}
	}
	err := cmd.Wait()
	result := Result{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	if ctx.Err() != nil {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		result.ExitCode = 0
	case errors.As(err, &exitErr):
		result.ExitCode = exitErr.ExitCode() // -1 for a signal
	default:
		result.ExitCode = -1
	}
	return result, nil
}

// Rig is the configured `rig` command and the one seat the bridge may type into.
type Rig struct {
	Path         string // absolute path of rig
	EnvPath      string // PATH given to rig
	Home         string // HOME
	OpenRigHome  string // OPENRIG_HOME
	OpenRigURL   string // OPENRIG_URL, optional
	Seat         string // canonical session name, e.g. operator-agent@kernel
	ReadyTimeout time.Duration
	Runner       Runner
}

// Env builds the child environment from scratch.
func (r *Rig) Env() []string {
	env := []string{
		"PATH=" + r.EnvPath,
		"HOME=" + r.Home,
		"OPENRIG_HOME=" + r.OpenRigHome,
		"LANG=en_US.UTF-8",
	}
	if r.OpenRigURL != "" {
		env = append(env, "OPENRIG_URL="+r.OpenRigURL)
	}
	return env
}

// Version runs `rig --version` and returns its first line, sanitised.
func (r *Rig) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, VersionTimeout)
	defer cancel()
	res, err := r.Runner.Run(ctx, r.Path, []string{"--version"}, r.Env())
	switch {
	case err != nil:
		return "", err
	case res.TimedOut:
		return "", errors.New("rig --version timed out")
	case res.ExitCode != 0:
		return "", fmt.Errorf("rig --version exited %d", res.ExitCode)
	}
	return firstLine(res.Stdout), nil
}

// Ready words, used in the refusal reason "operator seat not ready: <word>".
const (
	WordNotFound    = "not found"
	WordNotRunning  = "not running"
	WordNotStarted  = "not started"
	WordStopped     = "stopped"
	WordTimedOut    = "check timed out"
	WordCheckFailed = "check failed"
)

type psAnswer struct {
	Entries   []psEntry `json:"entries"`
	Truncated bool      `json:"truncated"`
}

type psEntry struct {
	CanonicalSessionName string `json:"canonicalSessionName"`
	SessionStatus        string `json:"sessionStatus"`
	StartupStatus        string `json:"startupStatus"`
	IntentionallyStopped bool   `json:"intentionallyStopped"`
}

// RigName is the part of the seat's session name after the last '@'.
func (r *Rig) RigName() string {
	if i := strings.LastIndexByte(r.Seat, '@'); i >= 0 {
		return r.Seat[i+1:]
	}
	return ""
}

// Ready reports whether the configured seat exists, is running and has started. Busy is ready. On anything
// else it returns one of the Word constants, never raw rig output. A check that cannot be completed is
// "not ready", never "ready".
func (r *Rig) Ready(ctx context.Context) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, r.ReadyTimeout)
	defer cancel()
	args := []string{"ps", "--nodes", "--rig", r.RigName(), "--json",
		"--fields", "canonicalSessionName,sessionStatus,startupStatus,intentionallyStopped"}
	res, err := r.Runner.Run(ctx, r.Path, args, r.Env())
	switch {
	case err != nil:
		return false, WordCheckFailed
	case res.TimedOut:
		return false, WordTimedOut
	case res.ExitCode != 0:
		return false, WordCheckFailed
	}
	var answer psAnswer
	if json.Unmarshal(res.Stdout, &answer) != nil {
		return false, WordCheckFailed
	}
	// A truncated listing may have left the seat out: not ready, unknown.
	if answer.Truncated {
		return false, WordCheckFailed
	}
	for _, entry := range answer.Entries {
		if entry.CanonicalSessionName != r.Seat {
			continue
		}
		switch {
		case entry.IntentionallyStopped:
			return false, WordStopped
		case entry.SessionStatus != "running":
			return false, WordNotRunning
		case entry.StartupStatus != "ready":
			return false, WordNotStarted
		}
		return true, ""
	}
	return false, WordNotFound
}

// SendOutcome is what the bridge may say about one `rig send`.
type SendOutcome int

const (
	// Pasted: rig send exited 0.
	Pasted SendOutcome = iota
	// Failed: rig could not be started, so nothing was typed.
	Failed
	// Unconfirmed: any other end. Never retried.
	Unconfirmed
)

// SendResult carries the outcome and a short, sanitised note for the log (never the text).
type SendResult struct {
	Outcome  SendOutcome
	ExitCode int
	Note     string
}

// Send runs `rig send SEAT TEXT` with no other flag. text must not start with '-' (the renderer guarantees
// it); this is checked again here.
func (r *Rig) Send(ctx context.Context, text string) SendResult {
	if text == "" || strings.HasPrefix(text, "-") {
		return SendResult{Outcome: Failed, ExitCode: -1, Note: "refused to run rig send with this text"}
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	res, err := r.Runner.Run(ctx, r.Path, []string{"send", r.Seat, text}, r.Env())
	var start *StartError
	switch {
	case errors.As(err, &start):
		return SendResult{Outcome: Failed, ExitCode: -1, Note: "rig could not be started"}
	case err != nil:
		return SendResult{Outcome: Unconfirmed, ExitCode: -1, Note: "rig send ended in an unknown way"}
	case res.TimedOut:
		return SendResult{Outcome: Unconfirmed, ExitCode: -1, Note: "rig send timed out"}
	case res.ExitCode == 0:
		return SendResult{Outcome: Pasted, ExitCode: 0}
	}
	return SendResult{Outcome: Unconfirmed, ExitCode: res.ExitCode, Note: firstLine(res.Stderr)}
}

// firstLine returns the first line of b, control characters turned into spaces, at most maxStderrEcho runes.
func firstLine(b []byte) string {
	line, _, _ := strings.Cut(string(b), "\n")
	var out []rune
	for _, r := range line {
		if len(out) == maxStderrEcho {
			break
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
