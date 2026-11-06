package daemon

// Untested: under GR-3 nothing here has been run. Every collaborator is a fake, and
// paneStart is replaced, so no gateway is called, no harness starts and no pane opens.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/router"
)

// callLog records the order of the calls a test cares about.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, entry)
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func (l *callLog) contains(entry string) bool {
	for _, got := range l.all() {
		if got == entry {
			return true
		}
	}
	return false
}

// fakeLink is the gateway client.
type fakeLink struct {
	log *callLog

	mu        sync.Mutex
	addErr    error
	endErr    error
	added     []string
	ended     []string
	directory []protocol.DirectoryEntry
}

func (f *fakeLink) AddSession(_ context.Context, name string) (protocol.AddSessionResponse, error) {
	f.log.add("gateway:add:" + name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return protocol.AddSessionResponse{}, f.addErr
	}
	f.added = append(f.added, name)
	return protocol.AddSessionResponse{
		Host:      "mac",
		Session:   protocol.SessionInfo{Name: name, Address: name + "@mac", SessionID: "gw-" + name},
		Directory: f.directory,
	}, nil
}

func (f *fakeLink) EndSession(_ context.Context, name string) error {
	f.log.add("gateway:end:" + name)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, name)
	return f.endErr
}

func (f *fakeLink) Address(local string) string { return local + "@mac" }

// fakeSessionServer is the MCP/hook server.
type fakeSessionServer struct {
	log *callLog

	mu      sync.Mutex
	agents  map[protocol.AgentID]string // id -> token
	refuse  bool
	removed []protocol.AgentID
}

func newFakeSessionServer(log *callLog) *fakeSessionServer {
	return &fakeSessionServer{log: log, agents: make(map[protocol.AgentID]string, 2)}
}

func (f *fakeSessionServer) AddConnectedAgent(id protocol.AgentID, name, address string, peers []string) string {
	f.log.add("server:add:" + name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse {
		return ""
	}
	token := "token-" + string(id)
	f.agents[id] = token
	return token
}

func (f *fakeSessionServer) RemoveAgent(id protocol.AgentID) bool {
	f.log.add("server:remove:" + string(id))
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.agents[id]; !ok {
		return false
	}
	delete(f.agents, id)
	f.removed = append(f.removed, id)
	return true
}

func (f *fakeSessionServer) Addr() string { return "127.0.0.1:9" }

// fakeSessionRouter is the router.
type fakeSessionRouter struct {
	log *callLog

	mu       sync.Mutex
	addErr   error
	added    []protocol.AgentID
	sessions map[string]string
	started  []protocol.AgentID
	failed   []protocol.AgentID
	exited   []protocol.AgentID
}

func newFakeSessionRouter(log *callLog) *fakeSessionRouter {
	return &fakeSessionRouter{log: log, sessions: make(map[string]string, 2)}
}

func (f *fakeSessionRouter) AddSession(_ context.Context, spec protocol.LaunchSpec) error {
	f.log.add("router:add:" + string(spec.ID))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	f.added = append(f.added, spec.ID)
	return nil
}

func (f *fakeSessionRouter) SetGatewaySession(name, sessionID string) {
	f.log.add("router:session:" + name)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[name] = sessionID
}

func (f *fakeSessionRouter) PaneStarted(id protocol.AgentID, w router.PaneWriter) {
	f.log.add("router:started:" + string(id))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
}

func (f *fakeSessionRouter) PaneExited(id protocol.AgentID, code int) {
	f.log.add("router:exited:" + string(id))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exited = append(f.exited, id)
}

func (f *fakeSessionRouter) PaneFailed(id protocol.AgentID, err error) {
	f.log.add("router:failed:" + string(id))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, id)
}

// fakeConnectivity is the connector.
type fakeConnectivity struct {
	mu        sync.Mutex
	connected bool
	lastErr   string
}

func (f *fakeConnectivity) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeConnectivity) LastError() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastErr
}

// fakePane is a started pane.
//
// It records what a window sends it, so the request tests can assert the exact ultraviolet
// events without a PTY.
type fakePane struct {
	pid    int
	closed bool
	onExit func(int)
	mu     sync.Mutex

	frame  pane.Frame
	cols   int
	rows   int
	mouse  uint32
	ready  bool
	err    error
	mouseY bool // whether the child asked for the mouse

	keys    []uv.KeyPressEvent
	pastes  []string
	mice    []uv.MouseEvent
	resizes [][2]int
	request [][2]int
}

func (p *fakePane) PasteSubmit(ctx context.Context, text string) error { return p.Paste(text) }

func (p *fakePane) SendKey(key uv.KeyPressEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, key)
	return nil
}

func (p *fakePane) Paste(text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pastes = append(p.pastes, text)
	return nil
}

func (p *fakePane) MouseEnabled(uv.MouseEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mouseY
}

func (p *fakePane) SendMouse(event uv.MouseEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.mouseY {
		return pane.ErrMouseDisabled
	}
	p.mice = append(p.mice, event)
	return nil
}

func (p *fakePane) Resize(width, height int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, [2]int{width, height})
	return nil
}

func (p *fakePane) RequestResize(width, height int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.request = append(p.request, [2]int{width, height})
	return nil
}

func (p *fakePane) Snapshot() pane.Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.frame
}

func (p *fakePane) MouseModeBits() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mouse
}

func (p *fakePane) InputReady() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

func (p *fakePane) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *fakePane) Size() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}

func (p *fakePane) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *fakePane) CloseContext(context.Context) error { return p.Close() }

func (p *fakePane) PID() int { return p.pid }

func (p *fakePane) didClose() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// sessionFixture is everything a manager test needs.
type sessionFixture struct {
	manager *sessionManager
	link    *fakeLink
	server  *fakeSessionServer
	router  *fakeSessionRouter
	connect *fakeConnectivity
	log     *callLog
	panes   []*fakePane
	paneErr error
}

// newSessionFixture builds a manager whose panes are fakes.
func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()

	log := &callLog{}
	fixture := &sessionFixture{
		log:     log,
		link:    &fakeLink{log: log},
		server:  newFakeSessionServer(log),
		router:  newFakeSessionRouter(log),
		connect: &fakeConnectivity{connected: true},
	}
	fixture.link.directory = []protocol.DirectoryEntry{
		{Address: "claude@mac"},
		{Address: "codex@ubuntu-b"},
	}

	stateDir := t.TempDir()
	// session/ exists from the daemon's start; each session makes its own directory in it.
	if err := os.MkdirAll(filepath.Join(stateDir, "session"), 0o700); err != nil {
		t.Fatalf("create session/: %v", err)
	}
	fixture.manager = newSessionManager(
		fixture.link, fixture.server, fixture.router,
		&logger{out: io.Discard}, fixture.connect,
		[]string{"PATH=/usr/bin"}, "/usr/local/bin/rtk",
		filepath.Join(stateDir, "session"),
	)

	// No harness binary is ever executed: the version is a fixed string.
	fixture.manager.harnessVersion = func(context.Context, string) string { return "test-version" }

	// Every pane the manager starts is recorded, so a test can close or observe it.
	previous := paneStart
	t.Cleanup(func() { paneStart = previous })
	paneStart = func(spec protocol.LaunchSpec, width, height int, onExit func(int)) (sessionPane, error) {
		log.add("pane:start:" + string(spec.ID))
		if fixture.paneErr != nil {
			return nil, fixture.paneErr
		}
		started := &fakePane{pid: 1000 + len(fixture.panes), onExit: onExit}
		fixture.panes = append(fixture.panes, started)
		return started, nil
	}
	return fixture
}

// TestAddSessionOrder: the gateway, then the token, then the router, then the pane.
func TestAddSessionOrder(t *testing.T) {
	fixture := newSessionFixture(t)

	entry, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if entry == nil || entry.id == "" {
		t.Fatal("Add returned no session")
	}

	calls := fixture.log.all()
	if len(calls) < 5 {
		t.Fatalf("calls = %v, want at least five", calls)
	}
	want := []string{
		"gateway:add:claude",
		"server:add:claude",
		"router:add:" + string(entry.id),
		"router:session:claude",
		"pane:start:" + string(entry.id),
		"router:started:" + string(entry.id),
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("call order =\n%v\nwant\n%v", calls, want)
	}
	if fixture.router.sessions["claude"] != "gw-claude" {
		t.Fatalf("gateway session = %q, want gw-claude", fixture.router.sessions["claude"])
	}

	// The session's own directory exists and holds its hook file.
	if _, err := os.Stat(entry.dir); err != nil {
		t.Fatalf("the session directory is missing: %v", err)
	}
}

// TestAddSessionGatewayFailureStartsNothing: the first failure ends the add before a
// harness, a token or a directory exists.
func TestAddSessionGatewayFailureStartsNothing(t *testing.T) {
	fixture := newSessionFixture(t)
	fixture.link.addErr = errors.New("the gateway is unreachable")

	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err == nil {
		t.Fatal("Add succeeded although the gateway failed")
	}
	if fixture.log.contains("server:add:claude") {
		t.Fatal("a token was minted although the gateway failed")
	}
	if fixture.log.contains("pane:start:"+"") || len(fixture.panes) != 0 {
		t.Fatal("a pane started although the gateway failed")
	}
	if len(fixture.panes) != 0 {
		t.Fatalf("%d panes started", len(fixture.panes))
	}
}

// TestAddSessionPaneFailureCleansUp: a pane that cannot start ends the gateway's session,
// removes the token and the directory, and reports the failure to the router.
func TestAddSessionPaneFailureCleansUp(t *testing.T) {
	fixture := newSessionFixture(t)
	fixture.paneErr = errors.New("no such binary")

	_, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err == nil {
		t.Fatal("Add succeeded although the pane failed")
	}
	if !fixture.log.contains("gateway:end:claude") {
		t.Fatalf("the gateway session was not ended: %v", fixture.log.all())
	}
	if len(fixture.server.removed) != 1 {
		t.Fatalf("removed %d agents, want 1", len(fixture.server.removed))
	}
	if len(fixture.router.failed) != 1 {
		t.Fatalf("failed %d agents, want 1", len(fixture.router.failed))
	}
	if got := fixture.manager.Status(); len(got) != 0 {
		t.Fatalf("status = %+v, want no sessions", got)
	}
}

// TestAddSessionRefusals: an unknown name, an already running one and an unreached gateway
// are all decided before the gateway is called.
func TestAddSessionRefusals(t *testing.T) {
	t.Run("an unknown name", func(t *testing.T) {
		fixture := newSessionFixture(t)
		if _, err := fixture.manager.Add(context.Background(), "gemini", t.TempDir(), 80, 24); !errors.Is(err, errUnknownSessionName) {
			t.Fatalf("Add = %v, want errUnknownSessionName", err)
		}
		if len(fixture.log.all()) != 0 {
			t.Fatalf("calls = %v, want none", fixture.log.all())
		}
	})

	t.Run("already running", func(t *testing.T) {
		fixture := newSessionFixture(t)
		if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
			t.Fatalf("first Add: %v", err)
		}
		if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); !errors.Is(err, errAlreadyRunning) {
			t.Fatalf("second Add = %v, want errAlreadyRunning", err)
		}
	})

	t.Run("the gateway has not been reached", func(t *testing.T) {
		fixture := newSessionFixture(t)
		fixture.connect.connected = false
		fixture.connect.lastErr = "connection refused"

		_, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
		if !errors.Is(err, errNotConnected) {
			t.Fatalf("Add = %v, want errNotConnected", err)
		}
		if !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("error = %q, want the last gateway error in it", err)
		}
		if !strings.Contains(err.Error(), "no session was started") {
			t.Fatalf("error = %q, want it to say nothing started", err)
		}
		if len(fixture.log.all()) != 0 {
			t.Fatalf("calls = %v, want none", fixture.log.all())
		}
	})
}

// TestAddSessionReplacementAfterEnd: a session may be replaced, and the new one gets a new
// agent id and the same directory path (session/<name>), which the End removed first.
func TestAddSessionReplacementAfterEnd(t *testing.T) {
	fixture := newSessionFixture(t)

	first, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := fixture.manager.End(context.Background(), "claude"); err != nil {
		t.Fatalf("End: %v", err)
	}

	second, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if second.id == first.id {
		t.Fatalf("the replacement reused the agent id %q", second.id)
	}
	// The directory has no id in it: the hook command that names it must be identical for every
	// session of a name, or a harness would ask to trust it again each time.
	if second.dir != first.dir {
		t.Fatalf("the replacement used directory %q, want the same %q", second.dir, first.dir)
	}
	if filepath.Base(second.dir) != "claude" {
		t.Fatalf("directory = %q", second.dir)
	}
}

// TestEndCleansUp: ending a session ends it at the gateway first, closes the pane, removes
// the token and the directory, and a second End says there was no session.
func TestEndCleansUp(t *testing.T) {
	fixture := newSessionFixture(t)
	dir := t.TempDir()

	entry, err := fixture.manager.Add(context.Background(), "codex", dir, 80, 24)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := fixture.manager.End(context.Background(), "codex"); err != nil {
		t.Fatalf("End: %v", err)
	}

	calls := fixture.log.all()
	endAt, closeAt := -1, -1
	for i, call := range calls {
		if call == "gateway:end:codex" {
			endAt = i
		}
		if call == "server:remove:"+string(entry.id) {
			closeAt = i
		}
	}
	if endAt < 0 || closeAt < 0 || endAt > closeAt {
		t.Fatalf("calls = %v, want the gateway end before the token removal", calls)
	}
	if !fixture.panes[0].didClose() {
		t.Fatal("the pane was not closed")
	}
	if _, err := os.Stat(entry.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the session directory still exists: %v", err)
	}
	if err := fixture.manager.End(context.Background(), "codex"); !errors.Is(err, errNoSession) {
		t.Fatalf("second End = %v, want errNoSession", err)
	}
	if err := fixture.manager.End(context.Background(), "claude"); !errors.Is(err, errNoSession) {
		t.Fatalf("End of an unknown name = %v, want errNoSession", err)
	}
}

// TestEndQueuesTheGatewayFailure: an end the gateway cannot be told about is retried, not
// lost.
func TestEndQueuesTheGatewayFailure(t *testing.T) {
	fixture := newSessionFixture(t)
	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add: %v", err)
	}

	fixture.link.mu.Lock()
	fixture.link.endErr = errors.New("connection refused")
	fixture.link.mu.Unlock()

	if err := fixture.manager.End(context.Background(), "claude"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if len(fixture.manager.pendingEnd) != 1 {
		t.Fatalf("pendingEnd = %v, want claude queued", fixture.manager.pendingEnd)
	}

	// The retry succeeds and the queue empties.
	fixture.link.mu.Lock()
	fixture.link.endErr = nil
	fixture.link.mu.Unlock()
	fixture.manager.retryPendingEnd(context.Background())
	if len(fixture.manager.pendingEnd) != 0 {
		t.Fatalf("pendingEnd = %v, want empty after a successful retry", fixture.manager.pendingEnd)
	}
}

// TestExitCallbackCleansUpWithoutClosingThePane: a harness that exits by itself reports to
// the router and cleans up, and the pane is not closed again.
func TestExitCallbackCleansUpWithoutClosingThePane(t *testing.T) {
	fixture := newSessionFixture(t)
	entry, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// The fake pane keeps the callback: run it as the pane would.
	started := fixture.panes[0]
	started.onExit(3)

	waitFor(t, "the exit cleanup", func() bool {
		return fixture.log.contains("gateway:end:claude") && len(fixture.server.removed) == 1
	})
	if len(fixture.router.failed) != 0 {
		t.Fatalf("a clean exit was reported as a failure: %v", fixture.router.failed)
	}
	if fixture.router.exited[0] != entry.id {
		t.Fatalf("PaneExited got %q, want %q", fixture.router.exited[0], entry.id)
	}
	if started.didClose() {
		t.Fatal("the exit cleanup closed a pane that had already exited")
	}
	if _, err := os.Stat(entry.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the session directory still exists: %v", err)
	}
}

// TestStatusAndHarnessPIDs: the running sessions are reported in name order, and their
// processes are what the ancestry check sees.
func TestStatusAndHarnessPIDs(t *testing.T) {
	fixture := newSessionFixture(t)
	if _, err := fixture.manager.Add(context.Background(), "codex", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add codex: %v", err)
	}
	if _, err := fixture.manager.Add(context.Background(), "claude", t.TempDir(), 80, 24); err != nil {
		t.Fatalf("Add claude: %v", err)
	}

	status := fixture.manager.Status()
	if len(status) != 2 {
		t.Fatalf("status = %+v, want two sessions", status)
	}
	if status[0].Name != "claude" || status[1].Name != "codex" {
		t.Fatalf("status order = %s, %s; want claude then codex", status[0].Name, status[1].Name)
	}
	if status[0].Address != "claude@mac" {
		t.Fatalf("address = %q", status[0].Address)
	}

	pids := fixture.manager.HarnessPIDs()
	if len(pids) != 2 {
		t.Fatalf("pids = %v, want two", pids)
	}
	for _, pane := range fixture.panes {
		if _, ok := pids[pane.pid]; !ok {
			t.Fatalf("pid %d is missing from %v", pane.pid, pids)
		}
	}
	if len(fixture.manager.Panes()) != 2 {
		t.Fatalf("Panes = %d, want 2", len(fixture.manager.Panes()))
	}
}

// waitFor polls a condition, bounded, rather than sleeping a fixed time.
func waitFor(t *testing.T, why string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}
