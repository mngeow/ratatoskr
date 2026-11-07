package attach

// Handing the terminal to ssh, and taking it back.
//
// Bubble Tea has restored the terminal by the time this runs, but its reader may still be finishing,
// and a stray byte arriving after the program ends would be typed into the ssh session before the
// user has seen it. The mitigation is deliberately small: stop echoing and echoing newlines, wait a
// moment for the reader to drain, throw away anything that arrived on the input path, put the terminal
// back exactly as it was, and only then exec.
//
// It is a mitigation, not a proof. It loses typeahead typed during the handover, it cannot catch a
// reply that arrives arbitrarily late, and nothing here claims the terminal is quiescent.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No terminal was changed and no ssh was started.
//
// Source evidence for the mitigation, read not run: Bubble Tea v2.1.1 cancels its input reader before
// cleanup and caps waitForReadLoop at 500 ms, and executeQuery documents that a late reply can leak
// past a completed query. That is why the wait below is 600 ms and why the input flush happens before
// the exec rather than after it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

var errTerminalRestore = errors.New("the terminal could not be restored: ssh was not started")

type handoverTerminal struct {
	get   func(int) (*unix.Termios, error)
	set   func(int, *unix.Termios) error
	flush func(int) error
}

const (
	// handoverWait is how long the reader is given to finish before the terminal is handed over.
	handoverWait = 600 * time.Millisecond // UNTESTED guess

	// terminalReset is what is written after an ssh attempt that did not end cleanly, so the local
	// terminal does not inherit the remote's alternate screen, mouse tracking, bracketed paste,
	// kitty keyboard flags, hidden cursor or colours.
	//
	// A clean exit (0) gets no reset: the remote window restored the terminal itself.
	terminalReset = "\x1b[?1049l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l" +
		"\x1b[<u\x1b[?25h\x1b[0m"
)

// writeReset writes the reset sequence and reports a short write as an error.
//
// It is the one place the sequence is written, so a test can call it with a buffer.
//
// UNTESTED.
func writeReset(w io.Writer) error {
	written, err := io.WriteString(w, terminalReset)
	if err != nil {
		return fmt.Errorf("the terminal could not be reset: %w", err)
	}
	if written != len(terminalReset) {
		return errors.New("the terminal could not be reset: the sequence was cut short")
	}
	return nil
}

// prepareHandover stops the terminal echoing, waits for the program's reader to finish, discards
// anything that arrived on the input path, and restores the terminal exactly.
//
// It always restores what it changed, including when it gives up, and it gives up rather than waiting
// when either context ends: attemptCtx is cancelled by a SIGINT during the handover, and rootCtx by a
// SIGHUP or SIGTERM at any time.
//
// UNTESTED.
func prepareHandover(rootCtx, attemptCtx context.Context) error {
	return prepareTerminal(rootCtx, attemptCtx, int(os.Stdin.Fd()),
		handoverTerminal{get: terminalGet, set: terminalSet, flush: flushInput}, handoverWait)
}

// prepareTerminal keeps terminal operations injectable for unrun, fake-only checks.
func prepareTerminal(rootCtx, attemptCtx context.Context, fd int, tty handoverTerminal, wait time.Duration) error {
	if rootCtx.Err() != nil || attemptCtx.Err() != nil {
		return errors.New("interrupted: ssh was not started")
	}

	original, err := tty.get(fd)
	if err != nil {
		return errors.New("the terminal could not be read: ssh was not started")
	}

	quiet := *original
	quiet.Lflag &^= unix.ECHO | unix.ECHONL
	if err := tty.set(fd, &quiet); err != nil {
		return errors.New("the terminal could not be prepared: ssh was not started")
	}

	// Whatever happens below, the exact original state goes back before anything else runs.
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		restored = true
		if err := tty.set(fd, original); err != nil {
			return fmt.Errorf("%w: %v", errTerminalRestore, err)
		}
		return nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-attemptCtx.Done():
		return errors.Join(errors.New("interrupted: ssh was not started"), restore())
	case <-rootCtx.Done():
		return errors.Join(errors.New("interrupted: ssh was not started"), restore())
	}

	// A signal and timer can become ready together: cancellation wins before flush/exec.
	if rootCtx.Err() != nil || attemptCtx.Err() != nil {
		return errors.Join(errors.New("interrupted: ssh was not started"), restore())
	}
	if err := tty.flush(fd); err != nil {
		return errors.Join(errors.New("the terminal's pending input could not be discarded: ssh was not started"), restore())
	}

	if err := restore(); err != nil {
		return err
	}
	if rootCtx.Err() != nil || attemptCtx.Err() != nil {
		return errors.New("interrupted: ssh was not started")
	}
	return nil
}
