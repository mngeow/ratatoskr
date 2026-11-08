package daemon

// `rtk daemon`: starting the daemon in the background.
//
// The parent does not open the store and does not run the daemon body. It opens the log,
// re-executes this binary with `daemon --foreground` in a new session, and waits until the
// socket answers with the child's own PID.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run: no child
// process has been started and no socket has been dialled.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/link"
)

const (
	// detachPollEvery is how often the parent asks the socket whether the child is ready.
	detachPollEvery = 100 * time.Millisecond // UNTESTED guess

	// detachTimeout bounds the whole wait for the child to become ready.
	detachTimeout = 15 * time.Second // UNTESTED guess

	// detachDialTimeout bounds one attempt: a dial and one hello. It is short so the
	// overall budget is spent retrying rather than wedged on one attempt. UNTESTED guess.
	detachDialTimeout = 1 * time.Second // UNTESTED guess

	// detachLogTailLines is how many log lines a failed start shows.
	detachLogTailLines = 5
)

// Start runs `rtk daemon`: it detaches a child and waits until that child answers.
//
// A daemon that is already running is reported and is not replaced. The parent never opens
// the store, so it cannot hold or disturb the child's lock.
//
// UNTESTED.
func Start(cfg Config, out io.Writer) error {
	// A socket path the kernel would refuse is reported here, before a child is started
	// only to fail with the same message in a log.
	if err := checkSocketPath(socketPath(cfg.StateDir)); err != nil {
		return err
	}

	// The card listener's flags are validated here, before a child exists, so a mistake shows in this
	// terminal and not only in a log.
	if _, _, err := planCardListen(cfg); err != nil {
		return err
	}

	// Ask first: a daemon that already answers must not be duplicated.
	if ack, err := probeDaemon(cfg.StateDir, detachDialTimeout); err == nil {
		if cfg.CardListen != "" {
			return fmt.Errorf("a daemon is already running (pid %d); the card listener can only be set when the daemon starts, and stopping it ends every session (rtk daemon stop)", ack.PID)
		}
		return fmt.Errorf("a daemon is already running (pid %d)", ack.PID)
	}

	// A machine that was never paired says so, with the existing text, before a log is
	// opened or a child is started: the child would only fail with the same error.
	if _, err := link.LoadPairing(cfg.StateDir); err != nil {
		return err
	}

	dir := daemonDir(cfg.StateDir)
	if err := prepareDir(dir); err != nil {
		return err
	}
	logFile, err := openLogFile(dir)
	if err != nil {
		return err
	}

	rtkPath, err := executablePath()
	if err != nil {
		_ = logFile.Close()
		return err
	}

	// The child's stderr is the log, so its logf lines land in the file without the child
	// opening it again.
	childArgs := []string{"daemon", "--foreground"}
	if cfg.CardListen != "" {
		childArgs = append(childArgs, "--card-listen", cfg.CardListen)
		if cfg.CardAllowLAN {
			childArgs = append(childArgs, "--allow-lan")
		}
		if cfg.CardInsecureHTTP {
			childArgs = append(childArgs, "--insecure-http")
		}
	}
	child := exec.Command(rtkPath, childArgs...)
	child.Stdin = nil // the child gets /dev/null, not this terminal
	child.Stdout = logFile
	child.Stderr = logFile
	child.Env = os.Environ()
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start the daemon: %w", err)
	}
	pid := child.Process.Pid
	// The child holds the descriptor now; the parent's copy is closed so the child is the
	// only writer.
	_ = logFile.Close()

	// Wait is called exactly once, in its own goroutine, so the process cannot become a
	// zombie while the parent polls.
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()

	deadline := time.Now().Add(detachTimeout)
	for {
		// The child may have exited already, which is the clearest diagnostic there is.
		select {
		case waitErr := <-exited:
			return startFailed(dir, waitErr)
		default:
		}

		if time.Now().After(deadline) {
			// The child is left alone: it may still be starting, and killing it here would
			// throw away a daemon that is about to be useful.
			return fmt.Errorf(
				"the daemon has not answered yet (pid %d); it may still be starting. Log: %s",
				pid, logPath(dir),
			)
		}

		ack, err := probeDaemon(cfg.StateDir, detachDialTimeout)
		switch {
		case err == nil && ack.PID == pid:
			fmt.Fprintf(out, "daemon started (pid %d)\n", pid)
			fmt.Fprintf(out, "log: %s\n", logPath(dir))
			// One line, and it says only what is true: harnesses get this terminal's
			// environment. Nothing here promises the daemon survives logout, sleep or a
			// cleanup by some other tool.
			fmt.Fprintln(out, "harnesses will inherit the environment of this terminal")
			return nil
		case err == nil:
			// Someone else answered: a race with another start.
			return fmt.Errorf("a daemon is already running (pid %d)", ack.PID)
		}

		time.Sleep(detachPollEvery)
	}
}

// probeDaemon asks the socket for its status and returns the hello acknowledgement.
//
// A missing or refusing socket is "not running", not an error worth showing: the caller
// is starting the daemon, so that is the expected answer.
func probeDaemon(stateDir string, timeout time.Duration) (wire.HelloAck, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	session, err := dialControl(ctx, stateDir)
	if err != nil {
		return wire.HelloAck{}, err
	}
	defer func() {
		_ = session.Close()
	}()

	// The hello's own answer carries the PID and the build.
	return session.HelloAck(), nil
}

// startFailed builds the error for a child that exited before answering.
//
// The log's last lines are the useful part, and no secret is ever written there.
func startFailed(dir string, waitErr error) error {
	tail := logTail(logPath(dir), detachLogTailLines)
	message := "the daemon did not start"
	if waitErr != nil && !errors.Is(waitErr, os.ErrProcessDone) {
		message += " (" + waitErr.Error() + ")"
	}
	message += ". Log: " + logPath(dir)
	if tail != "" {
		message += "\n" + tail
	}
	return errors.New(message)
}

// logTail returns up to n trailing lines of the log, or "" when it cannot be read.
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
