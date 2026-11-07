package daemon

// The CLI side of the socket: `rtk daemon status`, `rtk daemon stop` and
// `rtk daemon stop --session`.
//
// The client dials the Unix socket, says hello as a control connection (allowed across
// builds, so a stop still works after an upgrade) and sends one request.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No socket has been dialled.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// controlTimeout bounds one request and its answer.
	controlTimeout = 5 * time.Second // UNTESTED guess

	// stopWait bounds waiting for the daemon to finish after a stop. It is the worst case of
	// the daemon's own shutdown (shutdownWorstCase, 22 s with the current constants) plus a
	// margin of 8 s for scheduling and a slow disk. UNTESTED guess.
	stopWait = shutdownWorstCase + 8*time.Second

	// stopSessionTimeout bounds one `stop --session`. The daemon's End can take the gateway
	// call (up to shutdownBudget) plus the pane's close (up to closeGrace), so the answer can
	// legitimately arrive that late; the margin is 8 s. UNTESTED guess.
	stopSessionTimeout = shutdownBudget + closeGrace + 8*time.Second
)

// ExitError carries the exit status a command wants, so `rtk` can exit 4 for "no daemon" and 3 for
// "the expected session is not running", while every other failure keeps the code it has today.
//
// An error without ExitCode keeps exiting 1, which is why this is additive.
type ExitError struct {
	Code int
	Err  error
}

// Error renders the cause.
func (e *ExitError) Error() string {
	if e.Err == nil {
		return "rtk: an error with no detail"
	}
	return e.Err.Error()
}

// Unwrap exposes the cause, so errors.Is and errors.As still work.
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode is the status this command wants.
func (e *ExitError) ExitCode() int { return e.Code }

// ErrNotRunning is the one text for "there is no daemon".
var ErrNotRunning = errors.New("no daemon is running (start one with: rtk daemon)")

// SocketPath is the daemon's socket for one state directory.
//
// A window client needs it to dial, and it goes through this package so the layout stays
// defined in one place.
//
// UNTESTED.
func SocketPath(stateDir string) string { return socketPath(stateDir) }

// Ping asks a running daemon to confirm it is there, and returns its hello answer.
//
// One round trip, a control hello only: no status request, so a caller that just wants to know
// whether a daemon exists does not make the daemon do any work. A missing socket, or one that
// refuses the connection, is ErrNotRunning.
//
// UNTESTED.
func Ping(ctx context.Context, stateDir string) (wire.HelloAck, error) {
	session, err := dialControl(ctx, stateDir)
	if err != nil {
		return wire.HelloAck{}, err
	}
	defer func() {
		_ = session.Close()
	}()
	return session.HelloAck(), nil
}

// controlSession is one open control connection.
type controlSession struct {
	conn net.Conn
	ack  wire.HelloAck
}

// HelloAck is what the daemon answered to the hello.
func (c *controlSession) HelloAck() wire.HelloAck { return c.ack }

// Close ends the connection.
func (c *controlSession) Close() error { return c.conn.Close() }

// dialControl opens a control connection and completes the handshake.
//
// A missing socket, or one that refuses the connection because nothing is listening, is
// ErrNotRunning rather than a dial error: the caller's next step is the same either way.
//
// UNTESTED.
func dialControl(ctx context.Context, stateDir string) (*controlSession, error) {
	path := socketPath(stateDir)

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isRefused(err) {
			return nil, ErrNotRunning
		}
		return nil, fmt.Errorf("daemon: connect to %s: %w", path, err)
	}

	session := &controlSession{conn: conn}
	if err := session.handshake(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return session, nil
}

// isRefused reports whether the dial failed because nothing is listening.
func isRefused(err error) bool {
	return errors.Is(err, syscallECONNREFUSED)
}

// handshake sends the control hello and reads the answer.
func (c *controlSession) handshake(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}

	if err := wire.Write(c.conn, wire.TypeHello, 0, wire.Hello{
		Build: buildString,
		Role:  wire.RoleControl,
		PID:   os.Getpid(),
	}); err != nil {
		return fmt.Errorf("daemon: send the hello: %w", err)
	}

	envelope, err := wire.Read(c.conn)
	if err != nil {
		return fmt.Errorf("daemon: read the answer: %w", err)
	}
	switch envelope.Type {
	case wire.TypeHelloAck:
		if err := wire.Decode(envelope, &c.ack); err != nil {
			return err
		}
		return nil
	case wire.TypeRefused:
		var refusal wire.Refusal
		if err := wire.Decode(envelope, &refusal); err != nil {
			return err
		}
		return fmt.Errorf("daemon: the daemon refused this connection: %s", refusal.Message)
	}
	return errors.New("daemon: the daemon did not answer the hello")
}

// request sends one message and reads its answer, within timeout.
//
// The handshake left the connection with its own short deadline, which must not decide how
// long a request may take: ending a session can legitimately take longer than a hello.
func (c *controlSession) request(typ wire.Type, body any, timeout time.Duration) (wire.Envelope, error) {
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return wire.Envelope{}, fmt.Errorf("daemon: set the request deadline: %w", err)
	}
	if err := wire.Write(c.conn, typ, 1, body); err != nil {
		return wire.Envelope{}, fmt.Errorf("daemon: send the request: %w", err)
	}
	envelope, err := wire.Read(c.conn)
	if err != nil {
		return wire.Envelope{}, fmt.Errorf("daemon: read the answer: %w", err)
	}
	return envelope, nil
}

// buildString is this CLI's build, as set by cmd/rtk. It is a variable so a test can pin
// it, and so the daemon package never has to know how the version is injected.
var buildString = "dev"

// SetBuild records this binary's build, for the hello and for `rtk daemon status`'s
// comparison.
func SetBuild(build string) { buildString = build }

// Status asks a running daemon for its status.
//
// UNTESTED.
func Status(ctx context.Context, stateDir string) (wire.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()

	session, err := dialControl(ctx, stateDir)
	if err != nil {
		return wire.Status{}, err
	}
	defer func() {
		_ = session.Close()
	}()

	envelope, err := session.request(wire.TypeStatus, nil, controlTimeout)
	if err != nil {
		return wire.Status{}, err
	}
	if envelope.Type != wire.TypeResult {
		return wire.Status{}, errors.New("daemon: the daemon did not answer the status request")
	}
	var result wire.Result
	if err := wire.Decode(envelope, &result); err != nil {
		return wire.Status{}, err
	}
	if result.Error != nil {
		return wire.Status{}, fmt.Errorf("daemon: %s", result.Error.Error())
	}
	if result.Status == nil {
		return wire.Status{}, errors.New("daemon: the status answer was empty")
	}
	return *result.Status, nil
}

// Stop stops the daemon and every session it runs.
//
// It asks for the status first: with sessions running and no --yes, it prints them and
// refuses, so a user cannot end a colleague's agent by accident.
//
// UNTESTED.
func Stop(ctx context.Context, stateDir string, yes bool, out io.Writer) error {
	status, err := Status(ctx, stateDir)
	if err != nil {
		return err
	}

	if len(status.Sessions) > 0 && !yes {
		fmt.Fprintln(out, "the daemon is running these sessions:")
		for _, session := range status.Sessions {
			holder := session.Holder
			if holder == "" {
				holder = "no holder"
			}
			fmt.Fprintf(out, "  %s  %s  %s\n", session.Name, session.Address, holder)
		}
		return errors.New("refusing to stop the daemon while sessions are running: pass --yes to stop them too")
	}

	stopCtx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()

	session, err := dialControl(stopCtx, stateDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = session.Close()
	}()

	envelope, err := session.request(wire.TypeStop, nil, controlTimeout)
	if err != nil {
		return err
	}
	if envelope.Type != wire.TypeAck {
		return errors.New("daemon: the daemon did not acknowledge the stop")
	}
	var ack wire.Ack
	if err := wire.Decode(envelope, &ack); err != nil {
		return err
	}
	if !ack.OK {
		if ack.Error != nil {
			return fmt.Errorf("daemon: %s", ack.Error.Error())
		}
		return errors.New("daemon: the daemon refused to stop")
	}

	// "Stopped" means the process has exited and so released the store lock: the socket goes
	// away early in the shutdown, long before the router, the panes and the lock, and a
	// command that starts right after would find "another ratatoskr is already running". So
	// both must hold: nothing answers on the socket, and the pid from the status is gone
	// (signal 0, which delivers nothing).
	if status.PID <= 0 {
		return errors.New("the daemon was asked to stop but did not report its process id, so its exit cannot be confirmed")
	}
	deadline := time.Now().Add(stopWait)
	for {
		_, statusErr := Status(context.Background(), stateDir)
		socketGone := errors.Is(statusErr, ErrNotRunning)
		if socketGone && !processExists(status.PID) {
			fmt.Fprintln(out, "daemon stopped")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the daemon (pid %d) was asked to stop but is still finishing its shutdown; wait a moment, then check with: rtk daemon status", status.PID)
		}
		time.Sleep(detachPollEvery)
	}
}

// Network asks a running daemon for the network answer.
//
// The request's own bounds decide the timeout: the daemon may wait for the first snapshot and then
// for a version change, so the client must outlive both with a margin.
//
// UNTESTED.
func Network(ctx context.Context, stateDir string, request wire.NetworkRequest) (protocol.NetworkView, uint64, error) {
	first := request.FirstWaitMS
	if first < 0 {
		first = 0
	}
	if first > wire.MaxNetworkFirstMS {
		first = wire.MaxNetworkFirstMS
	}
	wait := request.WaitMS
	if wait < 0 {
		wait = 0
	}
	if wait > wire.MaxNetworkWaitMS {
		wait = wire.MaxNetworkWaitMS
	}
	budget := time.Duration(first+wait)*time.Millisecond + controlTimeout

	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	session, err := dialControl(ctx, stateDir)
	if err != nil {
		return protocol.NetworkView{}, 0, err
	}
	defer func() {
		_ = session.Close()
	}()

	envelope, err := session.request(wire.TypeNetwork, request, budget)
	if err != nil {
		return protocol.NetworkView{}, 0, err
	}
	if envelope.Type != wire.TypeResult {
		return protocol.NetworkView{}, 0, errors.New("daemon: the daemon did not answer the network request")
	}

	var result wire.Result
	if err := wire.Decode(envelope, &result); err != nil {
		return protocol.NetworkView{}, 0, err
	}
	if result.Error != nil {
		return protocol.NetworkView{}, 0, fmt.Errorf("daemon: %s", result.Error.Error())
	}
	if result.Network == nil {
		return protocol.NetworkView{}, 0, errors.New("daemon: the network answer was empty")
	}
	return *result.Network, result.NetworkVersion, nil
}

// StopSession ends one session by name.
//
// UNTESTED.
func StopSession(ctx context.Context, stateDir, name string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()

	session, err := dialControl(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = session.Close()
	}()

	envelope, err := session.request(wire.TypeStopSession, wire.StopSession{Name: name}, stopSessionTimeout)
	if err != nil {
		return err
	}
	if envelope.Type != wire.TypeAck {
		return errors.New("daemon: the daemon did not acknowledge the request")
	}
	var ack wire.Ack
	if err := wire.Decode(envelope, &ack); err != nil {
		return err
	}
	if !ack.OK {
		if ack.Error != nil {
			return fmt.Errorf("daemon: %s", ack.Error.Error())
		}
		return errors.New("daemon: the daemon refused to end that session")
	}

	fmt.Fprintf(out, "session ended: %s\n", name)
	return nil
}

// PrintStatus writes what `rtk daemon status` shows, one item per line.
//
// The daemon's own status line and this printer are the only place the shape is decided,
// so a test can pin it without a socket.
//
// It is a pure function so the format can be tested without a daemon. cliBuild is this
// binary's build: when it differs from the daemon's, both are shown, because the
// difference is what explains a refused window.
//
// UNTESTED.
func PrintStatus(w io.Writer, status wire.Status, cliBuild string, now time.Time) {
	fmt.Fprintf(w, "pid: %d\n", status.PID)
	if cliBuild != "" && cliBuild != status.Build {
		fmt.Fprintf(w, "build: %s (this CLI: %s)\n", status.Build, cliBuild)
	} else {
		fmt.Fprintf(w, "build: %s\n", status.Build)
	}
	fmt.Fprintf(w, "uptime: %s\n", shortDuration(now.Sub(status.StartedAt)))
	if status.LogPath == "" {
		fmt.Fprintln(w, "log: stderr (foreground)")
	} else {
		fmt.Fprintf(w, "log: %s\n", status.LogPath)
	}
	if status.Link != "" {
		fmt.Fprintf(w, "link: %s\n", status.Link)
	}
	if status.LinkErr != "" {
		fmt.Fprintf(w, "link error: %s\n", status.LinkErr)
	}
	fmt.Fprintf(w, "windows: %d\n", status.Windows)

	if len(status.Sessions) == 0 {
		fmt.Fprintln(w, "sessions: none")
		return
	}
	fmt.Fprintf(w, "sessions: %d\n", len(status.Sessions))
	for _, session := range status.Sessions {
		holder := session.Holder
		if holder == "" {
			holder = "no holder"
		} else {
			holder = "holder " + holder
		}
		line := fmt.Sprintf("  %s  %s  %s", session.Name, session.Address, holder)
		if session.Drafting {
			line += "  drafting"
		}
		fmt.Fprintln(w, line)
	}
}

// shortDuration renders an uptime: seconds under a minute, then minutes, hours and days.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
