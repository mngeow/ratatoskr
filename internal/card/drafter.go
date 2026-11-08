package card

// Running a draft command: one command, one payload, one answer.
//
// The owner's card can be drafted by an installed AI harness, and this is the only place that runs
// anything. It knows nothing about cards, harnesses, YAML or models: it is handed an exact argv, a
// complete environment and a payload, and it returns stdout or one of a few fixed sentences.
//
// Three properties are the point. There is no shell, so nothing in a payload or an environment can
// become a command. There is no inherited environment, so the command sees exactly what the caller
// listed. And nothing the command produced - stdout, stderr, argv, the environment or the payload - can
// appear in an error, so a failure cannot leak what it was given.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No command has been started.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The sentences a draft failure returns.
//
// They are sentinels and they are returned unwrapped: a caller matches them with errors.Is, and nothing
// here can attach a cause that might quote an output, an argv, an environment entry or the payload.
var (
	ErrDraftTimeout   = errors.New("the command ran past its time limit and was stopped")
	ErrDraftTooLarge  = errors.New("the command wrote more output than allowed and was stopped")
	ErrDraftCancelled = errors.New("the draft was cancelled")
	ErrDraftStart     = errors.New("the command could not be started")
	ErrDraftFailed    = errors.New("the command exited with an error")
	ErrDraftEmpty     = errors.New("the command wrote no output")
	// errDraftUnsupported is unexported: it is a platform fact, not a draft outcome a caller acts on.
	errDraftUnsupported = errors.New("the draft command is not supported on this platform")
)

// waitDelay bounds how long Wait is given to finish after the command is killed.
const waitDelay = 2 * time.Second

// Drafter turns a payload into a model's answer. The daemon holds one per run; tests use fakes.
type Drafter interface {
	// Name is the label a caller shows for this drafter.
	Name() string
	// Draft runs the command with the payload and returns what it wrote to stdout.
	Draft(ctx context.Context, payload []byte) ([]byte, error)
}

// CommandSpec is how to run one drafter command.
type CommandSpec struct {
	// Argv is the exact argv, argv[0] included. It is never passed to a shell.
	Argv []string
	// Env is the COMPLETE environment, as "NAME=value" entries. Nothing is inherited from the daemon.
	Env []string
	// Timeout is the hard limit for the whole run.
	Timeout time.Duration
	// MaxOutput is how many bytes of stdout are allowed.
	MaxOutput int
	// Label is what Name returns.
	Label string
}

// CommandDrafter runs one resolved command.
type CommandDrafter struct {
	spec   CommandSpec
	binary string
	env    []string
}

// NewCommandDrafter checks a spec and resolves its program.
//
// The resolution deliberately does not use the daemon's own PATH: the command must be found through the
// PATH the caller put in the spec, so a drafter runs the same program whatever the daemon's environment
// happens to be. An argv[0] with a slash is made absolute; a bare name is searched for in that PATH.
//
// Nothing is executed here.
//
// UNTESTED.
func NewCommandDrafter(spec CommandSpec) (*CommandDrafter, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("card: the draft command needs a program")
	}
	if spec.Argv[0] == "" || strings.ContainsRune(spec.Argv[0], 0) {
		return nil, errors.New("card: the draft command's program is empty or contains a NUL")
	}
	if spec.Timeout <= 0 {
		return nil, errors.New("card: the draft command needs a time limit")
	}
	if spec.MaxOutput <= 0 {
		return nil, errors.New("card: the draft command needs an output limit")
	}

	env := append([]string(nil), spec.Env...)

	binary, err := resolveProgram(spec.Argv[0], env)
	if err != nil {
		return nil, err
	}

	return &CommandDrafter{
		spec:   spec,
		binary: binary,
		env:    env,
	}, nil
}

// Name is the label the caller supplied, so a message can say which drafter failed.
//
// UNTESTED.
func (d *CommandDrafter) Name() string { return d.spec.Label }

// Binary is the resolved absolute path of the program.
//
// It is exposed so a caller can log which program it is about to run without guessing, and so a test
// can assert the resolution.
//
// UNTESTED.
func (d *CommandDrafter) Binary() string { return d.binary }

// Argv is a copy of the argv as given, with argv[0] unchanged.
//
// The resolved path is deliberately not substituted here: the argv is what the caller configured, and
// Draft uses it as it is.
//
// UNTESTED.
func (d *CommandDrafter) Argv() []string {
	return append([]string(nil), d.spec.Argv...)
}

// resolveProgram turns argv[0] into an absolute path.
//
// A name with a slash is taken as a path and made absolute. A bare name is looked up in the PATH entry of
// the given environment - empty entries are skipped rather than treated as the current directory, which is
// deliberately stricter than exec.LookPath: the daemon's own working directory must never be searched.
//
// UNTESTED.
func resolveProgram(program string, env []string) (string, error) {
	if strings.ContainsRune(program, os.PathSeparator) {
		absolute, err := filepath.Abs(program)
		if err != nil {
			return "", errors.New("card: the draft command's program could not be located")
		}
		if !executableFile(absolute) {
			return "", fmt.Errorf("card: the draft command's program %q is not an executable file", program)
		}
		return absolute, nil
	}

	path, ok := envValue(env, "PATH")
	if !ok || path == "" {
		// Nothing to search: a bare name cannot be resolved without a PATH in the spec's environment.
		return "", errors.New("card: the draft command's program could not be found")
	}

	for _, dir := range strings.Split(path, ":") {
		if dir == "" {
			// An empty entry means the current directory to a shell; this deliberately refuses it.
			continue
		}
		candidate := filepath.Join(dir, program)
		if !filepath.IsAbs(candidate) {
			absolute, err := filepath.Abs(candidate)
			if err != nil {
				continue
			}
			candidate = absolute
		}
		if executableFile(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("card: the draft command's program could not be found")
}

// envValue finds one entry in an environment slice.
func envValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return entry[len(prefix):], true
		}
	}
	return "", false
}

// executableFile reports whether a path is a regular file this user may execute.
//
// Stat follows symlinks, which is what a PATH entry normally is.
func executableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// Draft runs the command and returns its stdout.
//
// The command runs in an empty temporary directory, with stdin the payload, stderr discarded and stdout
// capped. A timeout, a cancellation or an over-long output kills the whole process group, so a child that
// spawned its own children cannot be left behind.
//
// UNTESTED.
func (d *CommandDrafter) Draft(ctx context.Context, payload []byte) ([]byte, error) {
	if !draftSupported() {
		// Without process groups a kill cannot reach what the command started, so the run is refused
		// rather than left unsafe.
		return nil, errDraftUnsupported
	}

	tempDir, err := os.MkdirTemp("", "rtk-draft-*")
	if err != nil {
		return nil, ErrDraftStart
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	ctx2, cancel := context.WithTimeout(ctx, d.spec.Timeout)
	defer cancel()

	output := &limitedWriter{limit: d.spec.MaxOutput}

	// Env is set to a non-nil slice even when it is empty: that is what stops os/exec from inheriting
	// the daemon's own environment.
	env := d.env
	if env == nil {
		env = []string{}
	}

	// CommandContext is what makes Cancel and WaitDelay apply: on a Cmd built any other way the context
	// is never watched and a timeout would not stop anything. The path is already absolute, so no
	// lookup happens; Args is then set to the exact argv, argv[0] as given.
	cmd := exec.CommandContext(ctx2, d.binary)
	cmd.Args = append([]string(nil), d.spec.Argv...)
	cmd.Env = env
	cmd.Dir = tempDir
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = output
	// stderr goes nowhere: it is never stored, returned or logged.
	cmd.Stderr = io.Discard

	setProcessGroup(cmd)
	// Cancel kills the whole group when the context ends, and WaitDelay bounds how long Wait is then
	// given to reap it.
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = waitDelay

	output.kill = func() { _ = killProcessGroup(cmd) }

	startErr := cmd.Start()
	var waitErr error
	if startErr == nil {
		waitErr = cmd.Wait()
	}

	switch {
	case output.overflowed():
		return nil, ErrDraftTooLarge
	case ctx.Err() != nil:
		return nil, ErrDraftCancelled
	case errors.Is(ctx2.Err(), context.DeadlineExceeded):
		return nil, ErrDraftTimeout
	case startErr != nil:
		return nil, ErrDraftStart
	case waitErr != nil:
		return nil, ErrDraftFailed
	}

	result := output.result()
	if strings.TrimSpace(string(result)) == "" {
		return nil, ErrDraftEmpty
	}
	return result, nil
}

// limitedWriter keeps the first limit bytes, then discards the rest and stops the command once.
//
// It never returns a write error: a copy error would end os/exec's copying early and give a misleading
// answer, while stopping the command is what the limit actually means.
type limitedWriter struct {
	limit int

	mu    sync.Mutex
	taken []byte
	over  atomic.Bool
	once  sync.Once
	kill  func()
}

// Write accepts bytes until the limit and discards the rest.
func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	room := w.limit - len(w.taken)
	if room > 0 {
		if room > len(p) {
			room = len(p)
		}
		w.taken = append(w.taken, p[:room]...)
	}
	w.mu.Unlock()

	if len(p) > room {
		// Beyond the limit: remember it, and stop the command once. Process is set by now - the copy
		// happens only after Start - and the guard keeps a race with a finished process harmless.
		if w.over.CompareAndSwap(false, true) {
			w.once.Do(func() {
				if w.kill != nil {
					w.kill()
				}
			})
		}
	}
	return len(p), nil
}

// overflowed reports whether the limit was passed.
func (w *limitedWriter) overflowed() bool { return w.over.Load() }

// result is a copy of what was kept.
func (w *limitedWriter) result() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.taken...)
}
