package daemon

// Tests for the session table's states: a name is owned by exactly one operation at a time.
//
// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// TestAddAndEndAreBusyWhileANameIsReserved: an add in flight owns the name, so a second add
// and an end are refused rather than raced.
func TestAddAndEndAreBusyWhileANameIsReserved(t *testing.T) {
	fixture := newSessionFixture(t)
	fixture.manager.sessions["claude"] = &sessionEntry{name: "claude", state: stateReserved}

	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); !errors.Is(err, errBusy) {
		t.Fatalf("Add on a reserved name = %v, want errBusy", err)
	}
	if err := fixture.manager.End(context.Background(), "claude"); !errors.Is(err, errBusy) {
		t.Fatalf("End on a reserved name = %v, want errBusy", err)
	}
	if len(fixture.link.added) != 0 {
		t.Fatalf("the gateway was asked to add: %v", fixture.link.added)
	}
}

// TestAddIsBusyWhileANameIsEnding: until the gateway, the token and the directory are dealt
// with, a replacement cannot start - a late gateway end would end it.
func TestAddIsBusyWhileANameIsEnding(t *testing.T) {
	fixture := newSessionFixture(t)
	fixture.manager.sessions["claude"] = &sessionEntry{name: "claude", state: stateEnding}

	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); !errors.Is(err, errBusy) {
		t.Fatalf("Add on an ending name = %v, want errBusy", err)
	}
	if err := fixture.manager.End(context.Background(), "claude"); !errors.Is(err, errNoSession) {
		t.Fatalf("End on an ending name = %v, want errNoSession", err)
	}
}

// TestAddClearsAQueuedGatewayEnd: a new add replaces the old session at the gateway, so a
// queued end for the name must not run later and end the new session.
func TestAddClearsAQueuedGatewayEnd(t *testing.T) {
	fixture := newSessionFixture(t)

	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add: %v", err)
	}
	fixture.link.endErr = errors.New("gateway unreachable")
	if err := fixture.manager.End(context.Background(), "claude"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if _, queued := fixture.manager.pendingEnd["claude"]; !queued {
		t.Fatal("the failed gateway end was not queued")
	}

	fixture.link.endErr = nil
	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if len(fixture.manager.pendingEnd) != 0 {
		t.Fatalf("pendingEnd = %v, want empty after the replacement was added", fixture.manager.pendingEnd)
	}

	before := len(fixture.link.ended)
	fixture.manager.retryPendingEnd(context.Background())
	if got := len(fixture.link.ended); got != before {
		t.Fatalf("a retry ended %d more session(s); the new session must be left alone", got-before)
	}
}

// TestRetryLeavesAnOwnedNameAlone: a queued end is not retried while the name has a
// session, and the name is released again afterwards.
func TestRetryLeavesAnOwnedNameAlone(t *testing.T) {
	fixture := newSessionFixture(t)
	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add: %v", err)
	}
	fixture.manager.pendingEnd["claude"] = struct{}{}

	before := len(fixture.link.ended)
	fixture.manager.retryPendingEnd(context.Background())
	if got := len(fixture.link.ended); got != before {
		t.Fatalf("a retry ended a running session")
	}

	// A name nothing owns is retried, and not left reserved afterwards.
	fixture.manager.pendingEnd["codex"] = struct{}{}
	fixture.manager.retryPendingEnd(context.Background())
	if _, still := fixture.manager.sessions["codex"]; still {
		t.Fatal("the retry left the name reserved")
	}
	if _, queued := fixture.manager.pendingEnd["codex"]; queued {
		t.Fatal("a successful retry left the end queued")
	}
}

// TestAddAfterStopIsRefused: once the daemon is stopping no pane may start.
func TestAddAfterStopIsRefused(t *testing.T) {
	fixture := newSessionFixture(t)
	fixture.manager.stop()

	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); !errors.Is(err, errShuttingDown) {
		t.Fatalf("Add after stop = %v, want errShuttingDown", err)
	}
	if len(fixture.link.added) != 0 {
		t.Fatalf("the gateway was asked to add after stop: %v", fixture.link.added)
	}
}

// TestAddWithoutADirectoryFails: a harness never starts in a directory nobody chose, and
// the gateway's session is ended again.
func TestAddWithoutADirectoryFails(t *testing.T) {
	fixture := newSessionFixture(t)

	if _, err := fixture.manager.Add(context.Background(), "claude", "", 80, 24); err == nil {
		t.Fatal("Add without a directory succeeded")
	}
	if fixture.log.contains("pane:start:") {
		t.Fatal("a pane was started without a directory")
	}
	if len(fixture.link.ended) != 1 || fixture.link.ended[0] != "claude" {
		t.Fatalf("gateway ends = %v, want [claude]", fixture.link.ended)
	}
	if _, still := fixture.manager.sessions["claude"]; still {
		t.Fatal("the failed add left the name in the table")
	}
}

// TestAHarnessThatExitsAtOnceIsCleanedUp: an exit that arrives before Add returns is seen by
// Add, which does the cleanup the callback could not.
func TestAHarnessThatExitsAtOnceIsCleanedUp(t *testing.T) {
	fixture := newSessionFixture(t)

	paneStart = func(spec protocol.LaunchSpec, width, height int, onExit func(int)) (sessionPane, error) {
		started := &fakePane{pid: 4242, onExit: onExit}
		fixture.panes = append(fixture.panes, started)
		// The process is gone before pane.Start returns.
		onExit(1)
		return started, nil
	}

	_, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err == nil {
		t.Fatal("Add of a harness that exited at once succeeded")
	}
	if _, still := fixture.manager.sessions["claude"]; still {
		t.Fatal("the table still holds the dead session")
	}
	if len(fixture.server.removed) != 1 {
		t.Fatalf("the token was removed %d times, want 1", len(fixture.server.removed))
	}
	if len(fixture.link.ended) != 1 {
		t.Fatalf("gateway ends = %v, want one", fixture.link.ended)
	}
	if len(fixture.panes) == 1 && fixture.panes[0].didClose() {
		t.Fatal("a pane that had already exited was closed")
	}
}

// TestASessionGetsItsOwnDirectoryUnderSession is the path the other tests' fakes do not
// replace: the real directory creation, with a parent named "session" as the daemon
// prepares it, and the real hook file written into the new directory.
func TestASessionGetsItsOwnDirectoryUnderSession(t *testing.T) {
	fixture := newSessionFixture(t)

	entry, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if filepath.Base(entry.dir) != "claude" || filepath.Base(filepath.Dir(entry.dir)) != "session" {
		t.Fatalf("directory %q is not directly under a session/ directory", entry.dir)
	}
	info, err := os.Stat(entry.dir)
	if err != nil {
		t.Fatalf("the session directory does not exist: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("session directory mode = %v, want 0700", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(entry.dir, "hook-claude.json")); err != nil {
		t.Fatalf("the hook file is not in the session's own directory: %v", err)
	}
}

// TestAddFailsWhenTheSessionDirectoryAlreadyExists: a leftover directory is refused with a
// clear error, nothing starts, and the leftover is still there afterwards: a failed add removes
// only a directory it made itself, so the first attempt errors and the second does not quietly
// succeed over somebody's files.
func TestAddFailsWhenTheSessionDirectoryAlreadyExists(t *testing.T) {
	fixture := newSessionFixture(t)

	leftover := fixture.manager.sessionDirFor("claude")
	if err := os.Mkdir(leftover, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	marker := filepath.Join(leftover, "keep-me")
	if err := os.WriteFile(marker, []byte("not ours"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err == nil {
			t.Fatalf("attempt %d reused an existing session directory", attempt)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("attempt %d removed a directory it did not create: %v", attempt, err)
		}
	}
	if fixture.log.contains("pane:start:") {
		t.Fatal("a pane was started")
	}
	if _, still := fixture.manager.sessions["claude"]; still {
		t.Fatal("the failed add left the name in the table")
	}
}

// TestAStopDuringAnAddLeavesNoHarness: the daemon began to stop while the add was starting
// its pane. The add closes the pane itself and reports it, so nothing is left running that
// the shutdown's closeAll never saw.
func TestAStopDuringAnAddLeavesNoHarness(t *testing.T) {
	fixture := newSessionFixture(t)

	paneStart = func(spec protocol.LaunchSpec, width, height int, onExit func(int)) (sessionPane, error) {
		started := &fakePane{pid: 777, onExit: onExit}
		fixture.panes = append(fixture.panes, started)
		// The shutdown reached stop() while the pane was being started.
		fixture.manager.stop()
		return started, nil
	}

	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); !errors.Is(err, errShuttingDown) {
		t.Fatalf("Add = %v, want errShuttingDown", err)
	}
	if len(fixture.panes) != 1 || !fixture.panes[0].didClose() {
		t.Fatal("the pane started during the stop was not closed")
	}
	if got := fixture.manager.Panes(); len(got) != 0 {
		t.Fatalf("Panes() = %d, want none", len(got))
	}
	if _, still := fixture.manager.sessions["claude"]; still {
		t.Fatal("the table still holds the session")
	}
}

// TestAStopBeforeThePaneStartsStartsNoPane: stop() landed before the add reached paneStart.
func TestAStopBeforeThePaneStartsStartsNoPane(t *testing.T) {
	fixture := newSessionFixture(t)

	// harnessVersion runs inside the add, before the pane: stop() lands there.
	fixture.manager.harnessVersion = func(context.Context, string) string {
		fixture.manager.stop()
		return "test-version"
	}

	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); !errors.Is(err, errShuttingDown) {
		t.Fatalf("Add = %v, want errShuttingDown", err)
	}
	if fixture.log.contains("pane:start:") {
		t.Fatal("a pane was started after stop()")
	}
}
