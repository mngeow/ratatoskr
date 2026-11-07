package attach

// The SSH entry loop: run the daemon's exact command, come back, and do it again.
//
// The window builds no command and this file builds none either: the argv comes from the daemon's plan
// and is run as it is, after the fixed shape it must have is checked. Nothing is retried, nothing is
// reconstructed from display text, and a failure before the exec means no ssh at all.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No ssh was executed and no plan was received.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mngeow/ratatoskr/internal/entry"
	"github.com/mngeow/ratatoskr/internal/tui"
)

// entryLoop is the whole life of `rtk attach`: one window, then zero or more ssh entries, each
// returning to a fresh window.
//
// A root context watches SIGHUP and SIGTERM for the entire loop. Either one stops only the ssh this
// process owns, restores the terminal, prints the same detach line a signal has always printed, and
// ends the program without reopening anything.
//
// UNTESTED.
func entryLoop(options Options) error {
	rootCtx, stopRoot := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM)
	defer stopRoot()
	return entryWindows(rootCtx, options, entryOperations{open: window, ssh: runSSH, report: printOutcome})
}

type entryOperations struct {
	open   func(context.Context, Options, *tui.NetworkBookmark, string) (*tui.EntryIntent, error)
	ssh    func(context.Context, tui.EntryIntent) (entry.Outcome, error)
	report func(entry.Outcome, tui.EntryIntent) error
}

// entryWindows separates loop decisions from terminal and subprocess operations.
func entryWindows(rootCtx context.Context, options Options, operations entryOperations) error {

	current := options
	var resume *tui.NetworkBookmark
	feedback := ""

	for {
		if rootCtx.Err() != nil {
			return nil
		}
		intent, err := operations.open(rootCtx, current, resume, feedback)
		if err != nil {
			return err
		}
		if rootCtx.Err() != nil {
			// A signal ended the window: the same line as before, and no reopen.
			return nil
		}
		if intent == nil {
			// An ordinary ending: window() has already said what happened.
			return nil
		}

		printOpening(*intent)

		outcome, err := operations.ssh(rootCtx, *intent)
		if err != nil {
			return err
		}
		if err := operations.report(outcome, *intent); err != nil {
			return err
		}
		feedback = outcome.Message
		if outcome.Kind == entry.OutcomeSSHFailed && intent.Plan.Manual != "" {
			feedback += "  " + intent.Plan.Manual
		}

		if rootCtx.Err() != nil {
			// SIGHUP or SIGTERM during ssh: it was killed, the terminal is reset, and the program ends.
			text, _ := detachMessage(endingSignal, "")
			fmt.Fprintln(os.Stdout, text)
			return nil
		}

		// Back on the Network tab: a full attach, on the row we came from, with nothing started.
		bookmark := intent.Selection
		resume = &bookmark
		current = Options{
			Version:       options.Version,
			EnteredViaSSH: options.EnteredViaSSH,
			// Agents and ExpectSession are deliberately empty: returning is not a restart.
		}
	}
}

// runSSH runs one plan and reports what happened.
//
// A plan whose argv is not the fixed shape is refused rather than run. The caller
// still writes its targeted reset for a non-OK outcome.
//
// UNTESTED.
func runSSH(rootCtx context.Context, intent tui.EntryIntent) (entry.Outcome, error) {
	argv := intent.Plan.Argv
	if !validSSHArgv(argv) {
		return entry.Outcome{
			Kind:    entry.OutcomeCannotStart,
			Message: "the daemon sent a command this build will not run",
		}, nil
	}

	// A SIGINT during the handover means "do not go": it aborts the attempt, and it must not reach the
	// ssh that is not running. The channel is drained for the whole attempt so rtk itself survives a
	// press while it holds the terminal.
	attemptCtx, cancelAttempt := context.WithCancel(rootCtx)
	defer cancelAttempt()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	stopDrain := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-sigCh:
				// Once ssh is running it owns the terminal's SIGINT; before that, a press cancels the
				// handover.
				cancelAttempt()
			case <-stopDrain:
				return
			}
		}
	}()
	stopDraining := func() {
		close(stopDrain)
		<-drained
	}
	defer stopDraining()

	if err := prepareHandover(rootCtx, attemptCtx); err != nil {
		cancelAttempt()
		if errors.Is(err, errTerminalRestore) {
			return entry.Outcome{}, err
		}
		return entry.Outcome{Kind: entry.OutcomeCannotStart, Message: err.Error()}, nil
	}

	// Keep catching and draining the parent's SIGINT while SSH runs. The child has
	// the normal signal disposition after exec and remains in the terminal's process
	// group. Cancelling attemptCtx now has no effect: only rootCtx controls SSH.

	command := exec.CommandContext(rootCtx, argv[0], argv[1:]...)
	configureSSHStop(command)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	// Save the cooked state immediately before exec: SSH may make this tty raw.
	fd := int(os.Stdin.Fd())
	original, err := terminalGet(fd)
	if err != nil {
		return entry.Outcome{Kind: entry.OutcomeCannotStart, Message: "the terminal could not be read: ssh was not started"}, nil
	}
	if err := command.Start(); err != nil {
		return entry.ClassifyStart(err, intent.Plan.Host), nil
	}
	runErr, restoreErr := waitSSH(command.Wait, func() error { return terminalSet(fd, original) })
	if restoreErr != nil {
		return entry.Outcome{}, restoreErr
	}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return entry.ClassifySSHExit(0, intent.Plan.Host), nil
	case errors.As(runErr, &exitErr):
		code := exitErr.ExitCode()
		if code < 0 {
			// Killed by a signal: the root context's HUP/TERM, or an outside kill.
			return entry.Outcome{Kind: entry.OutcomeSSHFailed, Message: "ssh ended unexpectedly"}, nil
		}
		return entry.ClassifySSHExit(code, intent.Plan.Host), nil
	default:
		return entry.ClassifyStart(runErr, intent.Plan.Host), nil
	}
}

// sshStopGrace allows SSH to restore its tty before a forced kill. UNTESTED guess.
const sshStopGrace = 2 * time.Second

func configureSSHStop(command *exec.Cmd) {
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = sshStopGrace
}

// waitSSH restores termios for every unsuccessful started SSH, including signals
// and the forced-kill fallback, before the caller resets display modes or reopens.
func waitSSH(wait func() error, restore func() error) (error, error) {
	err := wait()
	if err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return err, fmt.Errorf("%w: %v", errTerminalRestore, restoreErr)
		}
	}
	return err, nil
}

// validSSHArgv checks the fixed shape the daemon's planner produces.
//
// The list is exact: the program, -t, the one -o pair, --, the destination and one remote argument. A
// plan that disagrees is not run at all, because running it would be trusting a shape nobody promised.
//
// UNTESTED.
func validSSHArgv(argv []string) bool {
	if len(argv) != 7 {
		return false
	}
	if argv[0] != "ssh" || argv[1] != "-t" || argv[2] != "-o" || argv[3] != "ConnectTimeout=5" || argv[4] != "--" {
		return false
	}
	return argv[5] != "" && argv[6] != ""
}

// printOpening is the one line shown before the terminal is handed over: where this is going, and how
// to come back.
//
// UNTESTED.
func printOpening(intent tui.EntryIntent) {
	address := strings.TrimSpace(intent.Address)
	if address == "" {
		address = strings.TrimSpace(intent.Plan.Host)
	}
	if address == "" {
		address = "that session"
	}
	fmt.Fprintf(os.Stdout, "rtk · opening %s over ssh. F10 inside returns here.\n", sanitize(address))

	if note := strings.TrimSpace(intent.Plan.Note); note != "" {
		// What weakens the preview is worth saying before ssh, not after.
		fmt.Fprintln(os.Stdout, sanitize(note))
	}
}

// printOutcome tells the user what happened, and resets the terminal unless ssh ended cleanly.
//
// A clean exit needs no reset: the remote window put the terminal back itself. Everything else gets the
// targeted reset, so the local terminal cannot inherit the remote's modes.
//
// UNTESTED.
func printOutcome(outcome entry.Outcome, intent tui.EntryIntent) error {
	if outcome.Kind != entry.OutcomeOK {
		if err := writeReset(os.Stdout); err != nil {
			return err
		}
	}

	if outcome.Message != "" {
		fmt.Fprintln(os.Stdout, sanitize(outcome.Message))
	}
	if outcome.Kind == entry.OutcomeSSHFailed && intent.Plan.Manual != "" {
		// ssh could not connect or the connection dropped: the by-hand line is the fallback.
		fmt.Fprintln(os.Stdout, sanitize(intent.Plan.Manual))
	}
	return nil
}
