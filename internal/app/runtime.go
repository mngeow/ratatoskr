package app

// The connected runtime: `rtk runtime [--agents claude,codex]`.
//
// It is the twin of Run and keeps Run's startup and shutdown discipline. Run and
// everything it calls are untouched; this file calls those helpers and adds the
// gateway steps: load the pairing, register before any harness exists, then start
// the link once every agent is registered.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No agent has been started and
// no gateway has been contacted.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/internal/agent"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/router"
	"github.com/mngeow/ratatoskr/internal/server"
	"github.com/mngeow/ratatoskr/internal/store"
	"github.com/mngeow/ratatoskr/internal/tui"
)

const (
	// registerTimeout bounds the sessions registration that must succeed before any
	// harness is launched.
	registerTimeout = 10 * time.Second // UNTESTED guess
)

// The router is the link's receiver: its six methods are the seam the stream calls.
var _ link.Receiver = (*router.Router)(nil)

// RunRuntime runs a connected runtime: every send goes through the gateway, and the
// panes show the agents this machine registered.
//
// The order is the contract. The flag value is validated first, so a bad --agents
// touches nothing. The pairing is loaded second, so an unpaired machine exits with
// the instruction to pair, before any harness could start. Registration is third,
// so an unreachable or refusing gateway also exits before any harness starts.
//
// UNTESTED.
func RunRuntime(version, agentsFlag string) (err error) {
	names, err := tui.ParseAgents(agentsFlag)
	if err != nil {
		return err
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	rtkPath, err := executablePath()
	if err != nil {
		return err
	}
	stateDir, err := store.DefaultDir()
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}

	// The credential lives in this value and in the client, and nowhere else: it is
	// never passed to an agent, a launch spec, an environment slice or a hook file.
	pairing, err := link.LoadPairing(stateDir)
	if err != nil {
		return err
	}
	client, err := link.NewClient(pairing, link.Options{})
	if err != nil {
		return err
	}

	// Held until RunRuntime returns, so a signal during shutdown cannot kill the
	// process before the panes and the link are closed.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, noteOverrun("closing the database", bounded(shutdownBudget, st.Close)))
	}()

	now := time.Now()
	recovered, err := st.Recover(ctx, now)
	if err != nil {
		return err
	}
	if _, err := st.Retain(ctx, now.Add(-retention)); err != nil {
		return err
	}
	sessionDir := filepath.Join(stateDir, "session")
	if err := agent.PrepareSessionDir(sessionDir); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(sessionDir)) }()

	runID, err := store.NewMessageID()
	if err != nil {
		return err
	}
	if err := st.StartRun(ctx, runID, version, now); err != nil {
		return err
	}

	// Register before any harness exists. A refusal here means no agent is started,
	// which is the contract's "exits with a clear error before any harness starts".
	regCtx, cancelRegister := context.WithTimeout(ctx, registerTimeout)
	registered, registerErr := client.Register(regCtx, names)
	cancelRegister()
	if registerErr != nil {
		_ = finishRun(st, runID, "startup_failed")
		if refusal, ok := link.Refusal(registerErr); ok {
			return fmt.Errorf("cannot start: %s. No agent was started.", refusal)
		}
		return fmt.Errorf("cannot start: %w. No agent was started.", registerErr)
	}

	// From here the gateway holds this machine's sessions. A failure before the link
	// is running must still end them, or the gateway would keep offering messages to
	// agents that were never started. Close works without Run and is idempotent.
	endSessions := func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		defer cancel()
		_ = noteOverrun("ending this machine's sessions at the gateway", client.Close(ctx))
	}

	sessions := make(map[string]string, len(registered.Sessions))
	for _, session := range registered.Sessions {
		sessions[session.Name] = session.SessionID
	}
	for _, name := range names {
		if _, ok := sessions[name]; !ok {
			endSessions()
			_ = finishRun(st, runID, "startup_failed")
			return fmt.Errorf("cannot start: no session for %s. No agent was started.", name)
		}
	}

	r, err := router.New(router.Config{
		RunID:         runID,
		Store:         st,
		StartupBanner: crashBanner(recovered),
		Link:          client,
	})
	if err != nil {
		endSessions()
		return err
	}
	r.Start()

	srv, err := server.New(r, server.Options{})
	if err != nil {
		closeRouter(r)
		endSessions()
		return errors.Join(err, finishRun(st, runID, "startup_failed"))
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve()
	}()

	slots, panes, err := startRuntimeAgents(ctx, r, srv, client, agentParams{
		dir: dir, rtkPath: rtkPath, sessionDir: sessionDir, env: os.Environ(),
	}, names, sessions, registered.Directory)

	// The link starts only now: an offer arriving before its recipient is
	// registered would be refused for no good reason.
	runCtx, cancelRun := context.WithCancel(ctx)
	if err == nil {
		go client.Run(runCtx, r)

		var model *tui.Model
		if model, err = tui.NewRuntime(r, r.Events(), slots); err == nil {
			err = runUI(ctx, tea.NewProgram(model, tea.WithContext(ctx), tea.WithoutSignalHandler()))
		}
	}

	endReason, err := exitReason(ctx.Err() != nil, err)
	return errors.Join(err, shutdownRuntime(r, st, srv, panes, serveErr, runID, endReason, client, cancelRun))
}

// startRuntimeAgents launches the selected agents in the order they were named.
//
// It mirrors startAgents rather than sharing code with it, because startAgents is
// existing local-mode code and must not change. The differences are the connected
// registration, the network-name peer list, and the gateway session recorded on the
// router after RegisterAgent (which looks the agent up by name).
//
// UNTESTED.
func startRuntimeAgents(ctx context.Context, r *router.Router, srv *server.Server, client *link.Client, p agentParams, names []string, sessions map[string]string, directory []protocol.DirectoryEntry) ([]tui.Slot, []*pane.Pane, error) {
	var slots []tui.Slot
	var panes []*pane.Pane

	for _, name := range names {
		id, err := store.NewMessageID()
		if err != nil {
			return nil, panes, err
		}
		agentID := protocol.AgentID(id)

		peers := peersFor(directory, client.Address(name))
		token := srv.AddConnectedAgent(agentID, name, client.Address(name), peers)

		spec, buildErr := buildSpec(ctx, agentID, name, peers, name, token, srv.Addr(), p)
		if err := r.RegisterAgent(ctx, spec); err != nil {
			return nil, panes, err
		}

		// After RegisterAgent, because it looks the agent up by name, and before the
		// link starts, so the first offer already knows the session.
		r.SetGatewaySession(name, sessions[name])

		slot := tui.Slot{ID: agentID, Name: name}
		if buildErr != nil {
			r.PaneFailed(agentID, buildErr)
			slot.StartErr = buildErr
			slots = append(slots, slot)
			continue
		}

		// Assigned to the slot only on success: a nil *pane.Pane in the interface
		// would not compare equal to nil.
		pn, err := pane.Start(spec, initialWidth, initialHeight, func(code int) { r.PaneExited(agentID, code) })
		if err != nil {
			r.PaneFailed(agentID, err)
			slot.StartErr = err
		} else {
			r.PaneStarted(agentID, pn)
			slot.Pane = pn
			panes = append(panes, pn)
		}
		slots = append(slots, slot)
	}
	return slots, panes, nil
}

// shutdownRuntime ends a connected run.
//
// The order adds one step to shutdown's: the router closes first, so an exit caused
// by the link cannot change a message; then the link stops its stream and ends this
// machine's sessions at the gateway; then the run ends, the panes close and the
// server stops.
//
// UNTESTED.
func shutdownRuntime(r *router.Router, st *store.Store, srv *server.Server, panes []*pane.Pane, serveErr <-chan error, runID, endReason string, client *link.Client, cancelRun context.CancelFunc) error {
	if len(panes) > 0 {
		fmt.Fprintln(os.Stderr, "rtk: stopping agents...")
	}

	closeRouter(r)

	if cancelRun != nil {
		cancelRun()
	}

	var linkErr error
	if client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
		linkErr = client.Close(ctx)
		cancel()
		// An error here is reported, never turned into the run's failure: the panes
		// and the store still have to be closed.
		linkErr = noteOverrun("closing the gateway link", linkErr)
	}
	if linkErr != nil {
		fmt.Fprintln(os.Stderr, "rtk:", linkErr)
	}

	err := finishRun(st, runID, endReason)
	err = errors.Join(err, closeAll(closeGrace, panes))

	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	err = errors.Join(err, noteOverrun("closing the message server", srv.Shutdown(ctx)))
	return errors.Join(err, noteOverrun("closing the message server", bounded(shutdownBudget, func() error { return <-serveErr })))
}

// peersFor lists the network names one agent should see as its peers: every
// directory address except its own, in the gateway's order, de-duplicated.
//
// UNTESTED.
func peersFor(directory []protocol.DirectoryEntry, own string) []string {
	peers := make([]string, 0, len(directory))
	seen := make(map[string]struct{}, len(directory))

	for _, entry := range directory {
		address := entry.Address
		if address == "" || address == own {
			continue
		}
		if _, duplicate := seen[address]; duplicate {
			continue
		}
		seen[address] = struct{}{}
		peers = append(peers, address)
	}
	return peers
}
