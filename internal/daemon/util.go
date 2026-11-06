package daemon

// Small helpers copied from internal/app.
//
// internal/app may not change in this work, so the daemon keeps its own copies rather
// than exporting them. The bodies are verbatim; if app ever exports these, the copies go.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mngeow/ratatoskr/internal/router"
	"github.com/mngeow/ratatoskr/internal/store"
)

const (
	// retention is how long history is kept. It is a copy of app's constant: the same
	// design section 9 value, so the two paths cannot drift apart by accident.
	retention = 30 * 24 * time.Hour // design section 9

	// shutdownBudget bounds each shutdown step. UNTESTED guess; a copy of app's.
	shutdownBudget = 2 * time.Second // UNTESTED guess; per step

	// closeGrace bounds waiting for a pane to exit before it is killed. UNTESTED guess;
	// a copy of app's. Unused in slice 1, which starts no panes, and kept so slice 2
	// does not have to reinvent it.
	closeGrace = 5 * time.Second // UNTESTED guess; shared by all panes
)

// paneCloser is what closeAll needs from a pane.
type paneCloser interface {
	Close() error
}

// bounded runs one step with a deadline, so a wedged step cannot hang shutdown.
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

// noteOverrun turns a deadline into a line on stderr rather than a failure: a step that
// did not finish in time is reported, and shutdown continues.
func noteOverrun(step string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintf(os.Stderr, "rtk: %s did not finish in time\n", step)
		return nil
	}
	return err
}

// finishRun records the end of the run.
func finishRun(st *store.Store, runID, endReason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	return noteOverrun("recording the end of the run", bounded(shutdownBudget, func() error {
		_, err := st.FinishRun(ctx, runID, endReason, time.Now())
		return err
	}))
}

// closeRouter stops the router, so an exit caused later cannot change a message.
func closeRouter(r *router.Router) {
	if r == nil {
		return
	}
	_ = noteOverrun("closing the router", bounded(shutdownBudget, func() error {
		r.Close()
		return nil
	}))
}

// closeAll closes every pane, bounded as a group and one by one.
func closeAll[P paneCloser](limit time.Duration, panes []P) error {
	if len(panes) == 0 {
		return nil
	}
	// Each pane closes in its own goroutine, so one wedged pane cannot keep the others
	// open; the group gets one budget.
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		for _, pn := range panes {
			if closeErr := pn.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}
	}()

	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return err
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

// crashBanner is the startup banner for a previous run that did not exit cleanly.
func crashBanner(recovered []store.RecoveredRun) string {
	if len(recovered) == 0 {
		return ""
	}
	messages := 0
	for _, run := range recovered {
		messages += run.Interrupted
	}
	switch {
	case messages == 0:
		return "previous run did not exit cleanly"
	case messages == 1:
		return "previous run did not exit cleanly (1 message interrupted)"
	default:
		return fmt.Sprintf("previous run did not exit cleanly (%d messages interrupted)", messages)
	}
}

// executablePath is this binary's real path, so a detached child runs the same build.
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
