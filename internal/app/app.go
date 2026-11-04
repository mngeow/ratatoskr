// Package app wires Ratatoskr together: startup order, shutdown order and the
// errors that stop a launch. The contract is docs/implementation/interfaces.md.
//
// Untested: under GR-3 (docs/design/ground-rules.md) nothing here has been run.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/internal/agent"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/router"
	"github.com/mngeow/ratatoskr/internal/server"
	"github.com/mngeow/ratatoskr/internal/store"
	"github.com/mngeow/ratatoskr/internal/tui"
)

const (
	retention      = 30 * 24 * time.Hour // design section 9
	closeGrace     = 5 * time.Second     // UNTESTED guess; shared by all panes
	shutdownBudget = 2 * time.Second     // UNTESTED guess; per step
	uiStopGrace    = 2 * time.Second     // UNTESTED guess

	// The panes start at this size and take the real one from the first
	// WindowSizeMsg. UNTESTED: a harness may draw once at the wrong size.
	initialWidth, initialHeight = 80, 24
)

// harnesses are the two agents of this version, in F1 and F2 order.
var harnesses = []struct{ name, harness string }{
	{"claude", "claude"},
	{"codex", "codex"},
}

// Run starts a run in the current directory and blocks until the user quits. An
// error returned before the panes start means no harness was launched.
func Run(version string) (err error) {
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

	// Held until Run returns, so a signal during shutdown can't take Go's default
	// action and kill rtk before the panes are closed. Bubble Tea's own handler is
	// off: it stops when the program ends, which is before shutdown starts.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()

	// The lock comes first: nothing below may touch a live run's files.
	st, err := store.Open(ctx, stateDir)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, noteOverrun("closing the database", bounded(shutdownBudget, st.Close)))
	}()

	now := time.Now()
	// Recover must precede StartRun: it closes every unclosed run.
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

	r, err := router.New(router.Config{
		RunID:         runID,
		Store:         st,
		StartupBanner: crashBanner(recovered),
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
	go func() { serveErr <- srv.Serve() }()

	slots, panes, err := startAgents(ctx, r, srv, agentParams{
		dir: dir, rtkPath: rtkPath, sessionDir: sessionDir, env: os.Environ(),
	})
	if err == nil {
		var model *tui.Model
		if model, err = tui.New(r, r.Events(), slots); err == nil {
			err = runUI(ctx, tea.NewProgram(model, tea.WithContext(ctx), tea.WithoutSignalHandler()))
		}
	}

	endReason, err := exitReason(ctx.Err() != nil, err)
	return errors.Join(err, shutdown(r, st, srv, panes, serveErr, runID, endReason))
}

// runUI runs the UI until it ends. The event loop notices a cancelled context
// only between steps, so a loop stuck inside one would also ignore every signal.
// After a signal it gets uiStopGrace, then the terminal is restored and the stuck
// goroutine is abandoned, so that shutdown still runs.
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

// exitReason names how the run ended. A signal is the reason for quitting, not a
// failure, so it returns no error.
func exitReason(signalled bool, err error) (string, error) {
	switch {
	case signalled || errors.Is(err, tea.ErrInterrupted):
		return "signal", nil
	case err != nil:
		return "error", err
	}
	return "clean", nil
}

// shutdown runs in the order design section 11 needs: stop the router, so that
// the exits caused below can't change any message, record the end of the run,
// and only then close the panes. The terminal is already restored here, so one
// plain line says why rtk hasn't returned yet. Every step is bounded: a step that
// overruns is noted, not reported as a failure, and ends with the process.
func shutdown(r *router.Router, st *store.Store, srv *server.Server, panes []*pane.Pane, serveErr <-chan error, runID, endReason string) error {
	if len(panes) > 0 {
		fmt.Fprintln(os.Stderr, "rtk: stopping agents...")
	}
	closeRouter(r)
	err := finishRun(st, runID, endReason)
	err = errors.Join(err, closeAll(closeGrace, panes))

	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	// A handler still open at the deadline ends when the process does, moments
	// later, so no forced close is needed. Serve returns as soon as Shutdown
	// starts; the bound only guards that.
	err = errors.Join(err, noteOverrun("closing the message server", srv.Shutdown(ctx)))
	return errors.Join(err, noteOverrun("closing the message server", bounded(shutdownBudget, func() error { return <-serveErr })))
}

func closeRouter(r *router.Router) {
	_ = noteOverrun("stopping the router", bounded(shutdownBudget, func() error {
		r.Close()
		return nil
	}))
}

// paneCloser is the part of a pane shutdown needs.
type paneCloser interface {
	CloseContext(ctx context.Context) error
}

// closeAll closes the panes at the same time under one limit, so that a slow
// harness can't use up another's grace.
func closeAll[P paneCloser](limit time.Duration, panes []P) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	errs := make([]error, len(panes))
	var wg sync.WaitGroup
	for i, p := range panes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = noteOverrun("stopping an agent (check for a leftover process)", p.CloseContext(ctx))
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// bounded runs a shutdown step that has no deadline of its own. One that overruns
// is abandoned and reports context.DeadlineExceeded.
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

// noteOverrun turns a shutdown timeout into one plain line. Quitting still
// succeeded, so it isn't an error; any other error passes through.
func noteOverrun(step string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintf(os.Stderr, "rtk: %s did not finish in time\n", step)
		return nil
	}
	return err
}

// finishRun records the end of a run. It uses its own context because it must
// still run after a signal has cancelled the run's. If storage is down it fails,
// and the next start's recovery closes the run as crashed. The store honours the
// context only until its writer accepts the request, hence the outer bound.
func finishRun(st *store.Store, runID, endReason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	return noteOverrun("recording the end of the run", bounded(shutdownBudget, func() error {
		_, err := st.FinishRun(ctx, runID, endReason, time.Now())
		return err
	}))
}

type agentParams struct {
	dir, rtkPath, sessionDir string
	env                      []string
}

// startAgents registers and launches each harness. A harness that can't start
// isn't fatal: its slot shows the error, the other agent keeps running, and
// sends to it return recipient_offline (design section 11). Only a failure to
// record an agent stops the launch.
func startAgents(ctx context.Context, r *router.Router, srv *server.Server, p agentParams) ([]tui.Slot, []*pane.Pane, error) {
	var slots []tui.Slot
	var panes []*pane.Pane
	for i, h := range harnesses {
		id, err := store.NewMessageID() // new each run; only needs to be unique
		if err != nil {
			return nil, panes, err
		}
		agentID := protocol.AgentID(id)

		var peers []string
		for j, other := range harnesses {
			if j != i {
				peers = append(peers, other.name)
			}
		}
		token := srv.AddAgent(agentID, h.name, peers)

		spec, buildErr := buildSpec(ctx, agentID, h.name, h.harness, token, srv.Addr(), p)
		if err := r.RegisterAgent(ctx, spec); err != nil {
			return nil, panes, err
		}
		slot := tui.Slot{ID: agentID, Name: h.name}
		if buildErr != nil {
			r.PaneFailed(agentID, buildErr)
			slot.StartErr = buildErr
			slots = append(slots, slot)
			continue
		}

		// Assigned to the slot only on success: a nil *pane.Pane in the
		// interface would not compare equal to nil.
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

// buildSpec writes one agent's hook file and launch configuration. It always
// returns a spec the router can register, even on failure.
func buildSpec(ctx context.Context, id protocol.AgentID, name, harness, token, addr string, p agentParams) (protocol.LaunchSpec, error) {
	failed := protocol.LaunchSpec{ID: id, DisplayName: name, Harness: harness}
	if token == "" {
		return failed, fmt.Errorf("server refused agent %q", name)
	}
	if err := server.WriteHookFile(p.sessionDir, name, addr, token); err != nil {
		return failed, err
	}
	spec, err := agent.Build(agent.Params{
		ID: id, DisplayName: name, Harness: harness, Dir: p.dir,
		RtkPath: p.rtkPath, SessionDir: p.sessionDir,
		ServerAddr: addr, Token: token, BaseEnv: p.env,
	})
	if err != nil {
		return failed, err
	}
	spec.HarnessVersion = agent.Version(ctx, harness)
	return spec, nil
}

func crashBanner(recovered []store.RecoveredRun) string {
	if len(recovered) == 0 {
		return ""
	}
	n := 0
	for _, run := range recovered {
		n += run.Interrupted
	}
	return fmt.Sprintf("Previous run did not exit cleanly; %d message(s) interrupted. Harness processes may still be running: check and stop them yourself.", n)
}

// executablePath is the stable absolute path used in hook commands. Codex asks
// the user to re-trust a hook whose command changes, so it resolves symlinks.
func executablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate rtk binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path, nil
}
