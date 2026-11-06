package daemon

// Slice 1 of the daemon: the process, its lifecycle, and its socket.
//
// The daemon runs what `rtk runtime` runs - store, router, MCP/hook server, gateway link -
// without a window of its own and, in this slice, without any sessions. Its start and
// shutdown orders mirror app.Run and app.shutdownRuntime; internal/app is not edited, so
// the few helpers it needs are copied in util.go.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No daemon has started, no socket has
// been bound and no connection has been accepted.

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/mngeow/ratatoskr/internal/agent"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/router"
	"github.com/mngeow/ratatoskr/internal/server"
	"github.com/mngeow/ratatoskr/internal/store"
)

const (
	// firstMessageDeadline bounds reading the hello, so a silent connection cannot hold
	// a goroutine. UNTESTED guess.
	firstMessageDeadline = 5 * time.Second // UNTESTED guess

	// writeDeadline bounds one write to a peer. UNTESTED guess.
	writeDeadline = 5 * time.Second // UNTESTED guess

	// controlIdle bounds how long a control connection may wait between requests.
	// UNTESTED guess.
	controlIdle = 30 * time.Second // UNTESTED guess

	// retentionEvery is how often retention runs while the daemon is up.
	retentionEvery = 24 * time.Hour

	// shutdownWorstCase is the longest a graceful stop can take, by adding up its bounded
	// steps in Run's shutdown: the windows' bye (byeGrace), then eight steps each bounded by
	// shutdownBudget (the socket close, the router, the gateway link close, the run record,
	// the message server's shutdown, its serve wait, the retention wait and the store close),
	// and the panes' close, which share one closeGrace because they close at the same time.
	// With the current constants that is 1 + 8*2 + 5 = 22 s. The CLI's wait is sized from it.
	shutdownWorstCase = byeGrace + 8*shutdownBudget + closeGrace
)

// Config is what the daemon needs to start.
type Config struct {
	// Version is this build, exactly as `rtk --version` prints it. A window must match
	// it; a control connection may not.
	Version string
	// StateDir is the state directory. It is resolved by the caller, so the daemon and
	// the CLI cannot disagree about where the socket is.
	StateDir string
	// Foreground keeps the process in the terminal and logs to stderr. The detached
	// child runs with it set, because its stderr is already the log file.
	Foreground bool
}

// Run is the daemon process.
//
// The order is the design's start order, and each step exists so that a failure before it
// leaves nothing behind: the pairing is read before the lock, so a machine that was never
// paired says so and creates nothing; the lock comes before every file the run owns; the
// socket is bound last, so a failed bind cannot leave a socket a client would connect to.
//
// UNTESTED.
func Run(ctx context.Context, cfg Config) (err error) {
	if cfg.StateDir == "" {
		return errors.New("daemon: no state directory")
	}

	// The environment is captured once: it is what harnesses will get, because the daemon
	// passes its own through to every session it later starts.
	env := os.Environ()

	// SIGHUP is caught and discarded, first, in both modes: surviving a lost terminal is
	// the point of a daemon. app.Run stops on SIGHUP, so the two differ deliberately.
	//
	// It is Notify and not Ignore on purpose. Ignore sets the signal to SIG_IGN, and SIG_IGN
	// survives exec: every harness, and everything a harness spawns, would then ignore SIGHUP
	// too, which plain `rtk` does not do and which leaves stray tools behind when a pane
	// closes. A caught signal is reset to the default by exec, so children get the default
	// action. The channel is never read: Notify does not block on a full channel, so a
	// signal that finds it full is simply dropped.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	// The pairing is read before the lock: never paired means nothing to do, and nothing
	// created. A missing gateway is fine - the link keeps retrying.
	pairing, err := link.LoadPairing(cfg.StateDir)
	if err != nil {
		return err
	}
	client, err := link.NewClient(pairing, link.Options{})
	if err != nil {
		return err
	}

	// Held until Run returns, so a signal during shutdown cannot take Go's default action
	// and kill the process before the socket and the lock are released.
	runCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	// The lock comes first: nothing below may touch a live run's files. Plain `rtk` and a
	// daemon exclude each other through it.
	st, err := store.Open(runCtx, cfg.StateDir)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, noteOverrun("closing the database", bounded(shutdownBudget, st.Close)))
	}()

	now := time.Now()
	recovered, err := st.Recover(runCtx, now)
	if err != nil {
		return err
	}
	if _, err := st.Retain(runCtx, now.Add(-retention)); err != nil {
		return err
	}
	sessionDir := filepath.Join(cfg.StateDir, "session")
	if err := agent.PrepareSessionDir(sessionDir); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(sessionDir)) }()

	runID, err := store.NewMessageID()
	if err != nil {
		return err
	}
	if err := st.StartRun(runCtx, runID, cfg.Version, now); err != nil {
		return err
	}

	r, err := router.New(router.Config{
		RunID:         runID,
		Store:         st,
		StartupBanner: crashBanner(recovered),
		Link:          client,
	})
	if err != nil {
		return err
	}
	r.Start()

	srv, err := server.New(r, server.Options{})
	if err != nil {
		closeRouter(r)
		return errors.Join(err, finishRun(st, runID, "startup_failed"))
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve()
	}()

	// Every failure between here and the accept loop stops what is already running: the
	// listener (if it was bound), the router, the message server and the run row. The
	// process is about to exit, but a daemon is the kind of process that must not leave a
	// half-started state behind.
	var listener net.Listener
	startupFailed := func(cause error) error {
		if listener != nil {
			_ = listener.Close()
		}
		closeRouter(r)
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownBudget)
		_ = srv.Shutdown(shutdownCtx)
		cancelShutdown()
		return errors.Join(cause, finishRun(st, runID, "startup_failed"))
	}

	// daemon/ is prepared before the socket: the log lives there and the socket will too.
	dir := daemonDir(cfg.StateDir)
	if err := prepareDir(dir); err != nil {
		return startupFailed(err)
	}

	// The log path is reported when stderr really IS the log file. The detached child runs
	// with --foreground too (its stderr was redirected to the file by `rtk daemon`), so the
	// flag cannot tell the two apart; the file can. A real foreground run has a terminal on
	// stderr and reports none.
	committedLog := ""
	if stderrIsFile(logPath(dir)) {
		committedLog = logPath(dir)
	}
	log := &logger{out: os.Stderr}

	rtkPath, err := executablePath()
	if err != nil {
		return startupFailed(err)
	}

	bound, socketFile, err := listenSocket(cfg.StateDir)
	if err != nil {
		return startupFailed(err)
	}
	listener = bound

	// The connector connects once; the manager needs to know whether that has happened, and
	// the connector needs the manager for its queued session ends, so the connector comes
	// first and the manager is attached to it.
	connector := newConnector(client, log)
	sessions := newSessionManager(client, srv, r, log, connector, env, rtkPath, sessionDir)
	connector.setPending(sessions)

	// The hub owns the windows and is the ONLY consumer of the router's events. Its gate is
	// created here because the hub's notifications are what tell windows about a holder.
	gate := newGate()
	windowHub := newHub(log, gate)
	windowHub.router = r
	windowHub.pending = r.PendingOnQuit

	// The manager tells the window side when a session starts, exits and is gone.
	sessions.setObserver(newPaneObserver(windowHub, log))

	self := &daemonServer{
		build:      cfg.Version,
		log:        log,
		router:     r,
		sessions:   sessions,
		harnesses:  sessions,
		startedAt:  now,
		logPath:    committedLog,
		hub:        windowHub,
		manager:    sessions,
		requestEnd: make(chan struct{}),
	}

	log.logf("started: build %s, state %s, socket %s", cfg.Version, cfg.StateDir, socketFile)

	// Everything that runs for the life of the daemon shares one context that the shutdown
	// cancels, so no background loop can outlive the store: the stop path (a request over the
	// socket) does not cancel runCtx, only a signal does.
	bgCtx, cancelBackground := context.WithCancel(runCtx)
	defer cancelBackground()

	// The gateway link starts after the socket exists, so a window that attaches at once
	// finds a daemon that is already listening.
	go client.Run(bgCtx, r)

	// One connect for the life of this daemon. A second would end the sessions it just
	// started, so there is deliberately no reconnect here.
	go connector.run(bgCtx)

	// The events fan-out and the pending ticker: the hub is the ONLY consumer of
	// r.Events(), and nothing else may read that channel.
	go windowHub.run(r.Events())

	retentionDone := make(chan struct{})
	go retentionLoop(bgCtx, st, log, retentionDone)

	acceptErr := make(chan error, 1)
	go func() {
		acceptErr <- self.serve(listener)
	}()

	// One way out: a signal, a stop request and an accept failure all leave this select and
	// take the same shutdown below.
	endReason := "quit"
	var crash error
	select {
	case <-runCtx.Done():
	case <-self.requestEnd:
	case err := <-acceptErr:
		// A nil error is the listener closing, which only shutdown does.
		if err != nil {
			endReason = "crashed"
			crash = err
			log.logf("the accept loop failed: %v", err)
		}
	}

	log.logf("stopping: %s", endReason)

	// No session may start from here on. An add already in flight either sees this before it
	// starts its pane, or closes the pane it just started itself (see sessionManager.start),
	// so no pane can appear after the panes are closed below.
	sessions.stop()

	// Shutdown, in the design's order. The windows go first: they are told the daemon is
	// stopping and closed, and their holds are released, so nothing waits on a client while
	// the rest is torn down. Then the listener closes, which unlinks the socket.
	windowHub.closeAll("the daemon is stopping")

	closeErr := noteOverrun("closing the socket", bounded(shutdownBudget, listener.Close))
	if closeErr != nil {
		log.logf("closing the socket: %v", closeErr)
	}

	closeRouter(r)

	// The link, the connector and retention stop together, and the link's stream is gone
	// before client.Close ends this host's sessions at the gateway.
	cancelBackground()

	closeCtx, cancelClose := context.WithTimeout(context.Background(), shutdownBudget)
	closeLinkErr := client.Close(closeCtx)
	cancelClose()
	if closeLinkErr != nil {
		// Reported, never a failure: the rest still has to be closed.
		log.logf("closing the gateway link: %v", closeLinkErr)
	}

	err = errors.Join(crash, finishRun(st, runID, endReason))

	// The sessions end after the run is recorded and before the server stops, so a harness
	// that exits now cannot write to a closed store and its token stops working first.
	// client.Close above already ended every session at the gateway.
	if paneErr := closeAll(closeGrace, sessions.Panes()); paneErr != nil {
		log.logf("closing the sessions' panes: %v", paneErr)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownBudget)
	shutdownErr := srv.Shutdown(shutdownCtx)
	cancelShutdown()
	if shutdownErr != nil {
		log.logf("closing the message server: %v", shutdownErr)
	}
	select {
	case serveErrValue := <-serveErr:
		if serveErrValue != nil {
			log.logf("closing the message server: %v", serveErrValue)
		}
	case <-time.After(shutdownBudget):
		log.logf("closing the message server: it did not finish in time")
	}

	// Still under the lock: this is the only explicit socket removal.
	if removeErr := removeStaleSocket(socketFile); removeErr != nil {
		log.logf("%v", removeErr)
	}

	// Retention was cancelled with the rest and must be gone before the store closes. The
	// wait is bounded like every other step: a store call that is stuck must not keep the
	// lock held for ever.
	select {
	case <-retentionDone:
	case <-time.After(shutdownBudget):
		log.logf("retention did not stop in time")
	}
	log.logf("stopped: %s", endReason)
	return err
}

// sessionControl is what the socket can ask about sessions.
//
// Slice 1 has none, so its implementation answers "no such session". Slice 2 supplies the
// session manager here, which is also why this is an interface rather than a call into
// the router.
type sessionControl interface {
	Status() []wire.SessionStatus
	End(ctx context.Context, name string) error
}

// daemonServer is the socket side: the accept loop, the handshake and the requests this
// slice serves.
type daemonServer struct {
	build     string
	log       *logger
	router    *router.Router
	sessions  sessionControl
	harnesses harnessPIDs
	startedAt time.Time
	logPath   string

	// The window side: the hub owns the windows, the manager starts sessions, and the gate
	// decides who may type.
	hub     *hub
	manager *sessionManager

	requestEnd chan struct{}
	endOnce    sync.Once
}
