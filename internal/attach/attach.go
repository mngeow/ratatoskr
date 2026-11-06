// Package attach opens a window on a running daemon's sessions.
//
// `rtk attach` is a window, not an owner: it never starts a daemon, never stops a session, and a
// signal or a lost terminal closes its own socket and nothing else. The daemon keeps the
// sessions running, which is what lets a window come back to them.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No window has attached, no daemon has been
// contacted, and the UI has never started.

package attach

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"

	"github.com/mngeow/ratatoskr/internal/daemon"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/store"
	"github.com/mngeow/ratatoskr/internal/tui"
	"github.com/mngeow/ratatoskr/internal/windowclient"
)

const (
	// pingTimeout bounds the "is a daemon there" check, so a wedged socket does not delay the
	// obvious answer.
	pingTimeout = 2 * time.Second // UNTESTED guess

	// fallbackCols and fallbackRows are used when the terminal size cannot be read.
	fallbackCols = 80
	fallbackRows = 24

	// chromeRows is what the UI keeps for its own lines: the daemon window's title, headers,
	// separator, message rows and hints. It is a copy of the tui layout constant, which is not
	// exported.
	chromeRows = 7

	// uiStopGrace is how long a cancelled window gets before the terminal is restored by force.
	uiStopGrace = 2 * time.Second // UNTESTED guess

	// shutdownBudget bounds one shutdown step.
	shutdownBudget = 2 * time.Second // UNTESTED guess
)

// Ending kinds, for detachMessage.
const (
	// endingSignal: a signal ended this window.
	endingSignal = "signal"
	// endingLost: the daemon connection failed.
	endingLost = "lost"
	// endingDetach: the user detached on purpose.
	endingDetach = "detach"
)

// Run opens a window on the daemon's sessions.
//
// An empty agentsFlag starts nothing: `rtk attach` joins what is already running. Otherwise the
// listed sessions start if they are not running already.
//
// UNTESTED.
func Run(version, agentsFlag string) error {
	names, err := startNames(agentsFlag)
	if err != nil {
		return err
	}

	stateDir, err := store.DefaultDir()
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}

	// A window is only for a daemon that is already running: attach never starts one.
	pingCtx, cancelPing := context.WithTimeout(context.Background(), pingTimeout)
	_, err = daemon.Ping(pingCtx, stateDir)
	cancelPing()
	if err != nil {
		return err
	}

	width, height := attachSize(names)

	// The window's lifetime, and only the window's: a signal closes this socket, and the daemon
	// and its sessions carry on.
	windowCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	// windowCtx is passed to Dial as it is, and it must stay alive: Dial keeps the context it is
	// given as the client's lifetime (the client fails when it ends), and it bounds its own
	// bootstrap with its own attach deadline. A shorter context here, cancelled after Dial
	// returns, would close the window as it opens.
	client, err := windowclient.Dial(windowCtx, daemon.SocketPath(stateDir), version, wire.Attach{
		Dir:    dir,
		Start:  names,
		Width:  width,
		Height: height,
	})
	if err != nil {
		return err
	}

	attached := client.Attached()
	if len(attached.Slots) == 0 {
		// Nothing to show. Print what could not start, then the guidance, and leave.
		for _, startErr := range attached.StartErrors {
			fmt.Fprintln(os.Stderr, startErrorLine(startErr.Name, startErr.Message))
		}
		_ = client.Close()
		return fmt.Errorf("%s", noSessionsGuidance)
	}

	model, err := tui.NewDaemon(client)
	if err != nil {
		_ = client.Close()
		return err
	}

	program := tea.NewProgram(model, tea.WithContext(windowCtx), tea.WithoutSignalHandler())
	runErr := runUI(windowCtx, program)

	// The cached disconnect reason must be read before the client is closed, because closing is
	// what clears the way for an intentional detach to look like nothing happened.
	windowErr := model.WindowError()
	_ = client.Close()

	return finish(windowCtx.Err() != nil, windowErr, runErr)
}

// finish turns the ending into what the user sees.
//
// A signal wins over a window error: the user's action is why the window went away, and a
// disconnect that happened at the same moment is not something they can act on.
func finish(signalled bool, windowErr, runErr error) error {
	switch {
	case signalled:
		text, _ := detachMessage(endingSignal, "")
		fmt.Fprintln(os.Stdout, text)
		return nil
	case windowErr != nil:
		text, isError := detachMessage(endingLost, windowErr.Error())
		if isError {
			return errors.New(text)
		}
		fmt.Fprintln(os.Stdout, text)
		return nil
	case runErr != nil:
		return runErr
	}

	text, _ := detachMessage(endingDetach, "")
	fmt.Fprintln(os.Stdout, text)
	return nil
}

// detachMessage is what a window says when it ends.
//
// It is a pure function of the ending and its reason, so the three sentences can be pinned
// without a daemon.
//
// UNTESTED.
func detachMessage(kind, reason string) (text string, isError bool) {
	switch kind {
	case endingSignal:
		return "detached; the agents keep running (rtk attach returns to them)", false
	case endingLost:
		// The window's own error already says what happened and ends with what to do. It is
		// printed as it is: a second sentence about the agents would be a claim nobody can make,
		// because a lost connection is also what a daemon crash or stop looks like.
		if reason == "" {
			reason = "window disconnected: the daemon connection closed"
		}
		text = sanitize(reason)
		// An error that came from somewhere other than the window's own text has no guidance yet.
		if !strings.Contains(text, "rtk attach") {
			text += "; " + lostGuidance
		}
		return text, true
	case endingDetach:
		return "detached; the agents keep running (rtk attach returns to them, rtk daemon stop ends them)", false
	}
	return "", false
}

// lostGuidance is what a lost connection tells the user to do next.
const lostGuidance = "sessions may still be running; run rtk attach"

// startErrorLine is how one session that could not start is reported.
//
// UNTESTED.
func startErrorLine(name, message string) string {
	return "rtk: could not start " + sanitize(name) + ": " + sanitize(message)
}

// noSessionsGuidance is the error a zero-slot attach returns. It carries no "rtk: " prefix, so
// the caller prints it once.
const noSessionsGuidance = "no sessions are running: start some with: rtk attach --agents claude,codex"

// startNames is the list of sessions to start.
//
// An empty flag starts nothing: that is what joining a running team means. Only a non-empty flag
// is parsed, and a bad one is refused before anything else happens.
//
// UNTESTED.
func startNames(agentsFlag string) ([]string, error) {
	if strings.TrimSpace(agentsFlag) == "" {
		return nil, nil
	}
	return tui.ParseAgents(agentsFlag)
}

// attachSize is the initial pane size for the sessions this window starts.
//
// The dimensions are a starting point only: the UI corrects the holder's size once it knows the
// real terminal, and a viewer's size is never applied.
//
// UNTESTED.
func attachSize(names []string) (width, height int) {
	cols, rows := terminalSize()
	panes := len(names)
	if panes == 0 {
		// Joining: the daemon may have one session or two, and two is the wider layout.
		panes = 2
	}
	return estimatePaneSize(cols, rows, panes)
}

// estimatePaneSize splits the terminal into one or two panes and keeps the UI's own rows.
//
// UNTESTED.
func estimatePaneSize(cols, rows, panes int) (width, height int) {
	if panes >= 2 {
		width = cols / 2
	} else {
		width = cols
	}
	if width < 1 {
		width = 1
	}

	height = rows - chromeRows
	if height < 1 {
		height = 1
	}
	return width, height
}

// terminalSize reads the terminal's size, or the fallback when it cannot.
func terminalSize() (cols, rows int) {
	size, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || size == nil || size.Col < 1 || size.Row < 1 {
		return fallbackCols, fallbackRows
	}
	return int(size.Col), int(size.Row)
}

// sanitize strips control characters from anything a daemon said, so a message cannot rewrite the
// terminal.
func sanitize(text string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	return strings.TrimSpace(cleaned)
}

// runUI runs one program with the same stop discipline app uses: a cancelled context gets a grace
// period, then the terminal is restored by force.
//
// UNTESTED.
func runUI(ctx context.Context, program *tea.Program) error {
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}

	timer := time.NewTimer(uiStopGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
	}
	return noteOverrun("restoring the terminal", bounded(shutdownBudget, func() error {
		program.Kill()
		return nil
	}))
}

// bounded runs one step with a deadline, so a wedged step cannot hang the exit.
func bounded(limit time.Duration, step func() error) error {
	done := make(chan error, 1)
	go func() { done <- step() }()

	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

// noteOverrun turns a deadline into a line rather than a failure.
func noteOverrun(step string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintf(os.Stderr, "rtk: %s did not finish in time\n", step)
		return nil
	}
	return err
}
