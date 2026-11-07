package daemon

// The session manager: starting and ending one harness at a time.
//
// One mutex serialises add and end and guards the session table; it is never held while a
// gateway client is written to, so a slow network call cannot block an end and an end
// cannot block an add behind a round trip.
//
// The add order is the contract: every refusal is decided before a harness could start,
// and every failure after the gateway knows about the session is cleaned up.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No session has been started, no
// harness launched and no pane opened.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/mngeow/ratatoskr/internal/agent"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/pane"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/router"
	"github.com/mngeow/ratatoskr/internal/server"
	"github.com/mngeow/ratatoskr/internal/store"
)

const (
	// sessionClaude and sessionCodex are the only agents this build can start, in the
	// order the daemon reports them.
	sessionClaude = "claude"
	sessionCodex  = "codex"
)

var (
	// errUnknownSessionName is a name this build cannot start.
	errUnknownSessionName = errors.New("daemon: that agent name is not one this build can start (claude or codex)")
	// errAlreadyRunning is a name that already has a live session.
	errAlreadyRunning = errors.New("daemon: that session is already running")
	// errNotConnected is the refusal while the gateway has not been reached: no waiting
	// and no queueing.
	errNotConnected = errors.New("daemon: the gateway has not been reached yet")
	// errNoSession is an unknown or already-ended name. The socket answers it as
	// no_such_session, which is the same answer for both.
	errNoSession = errors.New("daemon: no such session")
	// errBusy is a name whose session is being started, or whose gateway end is being
	// reported: the operation is refused rather than raced, because an end that lands after
	// a replacement's add would end the replacement.
	errBusy = errors.New("daemon: a session operation for that name is already running")
	// errShuttingDown is any add after the daemon began to stop.
	errShuttingDown = errors.New("daemon: the daemon is stopping")
)

// gatewayLink is the part of the gateway client the manager needs.
type gatewayLink interface {
	AddSession(ctx context.Context, name string) (protocol.AddSessionResponse, error)
	EndSession(ctx context.Context, name string) error
	Address(local string) string
}

// sessionRouter is the part of the router the manager needs.
type sessionRouter interface {
	AddSession(ctx context.Context, spec protocol.LaunchSpec) error
	SetGatewaySession(name, sessionID string)
	PaneStarted(id protocol.AgentID, w router.PaneWriter)
	PaneExited(id protocol.AgentID, code int)
	PaneFailed(id protocol.AgentID, err error)
}

// sessionServer is the part of the MCP/hook server the manager needs.
type sessionServer interface {
	AddConnectedAgent(id protocol.AgentID, name, address string, peers []string) string
	RemoveAgent(id protocol.AgentID) bool
	Addr() string
}

// sessionPane is a started pane as the manager and the window watcher see it.
type sessionPane interface {
	router.PaneWriter
	Close() error
	CloseContext(ctx context.Context) error
	PID() int
	// The read-only accessors the pane watcher uses to build a complete frame.
	Snapshot() pane.Frame
	MouseModeBits() uint32
	InputReady() bool
	Err() error
	Size() (cols, rows int)

	// The input operations a window's requests reach. Every one of them is applied only
	// after the gate admits the window that sent it.
	SendKey(key uv.KeyPressEvent) error
	Paste(text string) error
	MouseEnabled(event uv.MouseEvent) bool
	SendMouse(event uv.MouseEvent) error
	Resize(width, height int) error
	RequestResize(width, height int) error
}

// connectivity says whether the gateway has been reached yet.
type connectivity interface {
	Connected() bool
	LastError() string
}

// pendingEnder retries the gateway ends that failed once the link is up.
type pendingEnder interface {
	retryPendingEnd(ctx context.Context)
}

// paneStart starts one harness. It is a variable so a test can replace it without a
// process.
var paneStart = func(spec protocol.LaunchSpec, width, height int, onExit func(int)) (sessionPane, error) {
	started, err := pane.Start(spec, width, height, onExit)
	if err != nil {
		return nil, err
	}
	return started, nil
}

// sessionState is where one name is in its life.
type sessionState int

const (
	// stateReserved: an add (or a queued gateway end) owns the name. Nothing else may.
	stateReserved sessionState = iota + 1
	// stateRunning: the harness is up and the session is current.
	stateRunning
	// stateEnding: an end is in progress, from a manual end or the harness exiting. The
	// name stays in the table until the gateway, the token and the directory are dealt
	// with, so an add cannot start a replacement that a late gateway end would then kill.
	stateEnding
)

// sessionEntry is one name's session. Every field is read and written under the manager's
// mutex.
type sessionEntry struct {
	name      string
	id        protocol.AgentID
	gateway   string
	address   string
	dir       string
	startedAt time.Time
	pane      sessionPane
	state     sessionState
	// exited is set by the pane's exit callback: the harness is gone, so nothing may close
	// its pane again, and a start that was still in flight sees it and cleans up.
	exited bool
}

// sessionView is one running session as the window side needs it: enough to build a slot
// and to address its pane, and nothing that could be used to reach a replacement.
type sessionView struct {
	Name      string
	Agent     protocol.AgentID
	Address   string
	StartedAt time.Time
	Pane      sessionPane
}

// sessionObserver is told when a session starts, when its harness exits and when the
// session is finally gone.
//
// A session that exits by itself sees sessionExited and then sessionEnded, so an
// implementation must be idempotent. The daemon's window side is the observer; a nil
// observer is allowed, which is what a plain manager test has.
type sessionObserver interface {
	sessionStarted(view sessionView)
	sessionExited(view sessionView, exitCode *int)
	sessionEnded(view sessionView)
}

// sessionManager owns every running session.
type sessionManager struct {
	link       gatewayLink
	srv        sessionServer
	router     sessionRouter
	log        *logger
	connect    connectivity
	env        []string
	rtkPath    string
	sessionDir string

	now   func() time.Time
	newID func() (string, error)
	// harnessVersion asks a harness for its version. It is a field so a test never has to
	// run a harness binary: agent.Version looks one up and executes it.
	harnessVersion func(ctx context.Context, harness string) string

	mu         sync.Mutex
	observer   sessionObserver
	sessions   map[string]*sessionEntry
	pendingEnd map[string]struct{}
	// versions is each session's harness version, recorded when the launch spec was built, for the
	// network report. It is a copy of what the router was told, not a second lookup.
	versions map[protocol.AgentID]string
	// closed is set when the daemon begins to stop: no session may start after it.
	closed bool
}

// newSessionManager builds the manager. Nothing is started until Add.
//
// env is the environment captured once when the daemon started: every harness gets it, so a
// session started later cannot pick up a different terminal's environment.
//
// UNTESTED.
func newSessionManager(link gatewayLink, srv sessionServer, r sessionRouter, log *logger, connect connectivity, env []string, rtkPath, sessionDir string) *sessionManager {
	return &sessionManager{
		link:           link,
		srv:            srv,
		router:         r,
		log:            log,
		connect:        connect,
		env:            append([]string(nil), env...),
		rtkPath:        rtkPath,
		sessionDir:     sessionDir,
		now:            time.Now,
		newID:          store.NewMessageID,
		harnessVersion: agent.Version,
		sessions:       make(map[string]*sessionEntry, 2),
		pendingEnd:     make(map[string]struct{}, 2),
		versions:       make(map[protocol.AgentID]string, 2),
	}
}

// setObserver records who is told about session starts and ends. It must be called before
// the first session starts.
//
// UNTESTED.
func (m *sessionManager) setObserver(observer sessionObserver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observer = observer
}

// view is one entry as the observer sees it.
func (m *sessionManager) view(entry *sessionEntry) sessionView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sessionView{
		Name:      entry.name,
		Agent:     entry.id,
		Address:   entry.address,
		StartedAt: entry.startedAt,
		Pane:      entry.pane,
	}
}

// Running is every running session, in claude, codex order.
//
// UNTESTED.
func (m *sessionManager) Running() []sessionView {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]sessionView, 0, len(m.sessions))
	for _, name := range []string{sessionClaude, sessionCodex} {
		entry, ok := m.sessions[name]
		if !ok || entry.state != stateRunning {
			continue
		}
		out = append(out, sessionView{
			Name:      entry.name,
			Agent:     entry.id,
			Address:   entry.address,
			StartedAt: entry.startedAt,
			Pane:      entry.pane,
		})
	}
	return out
}

// expectedSession finds one running session by its exact agent id.
//
// An `rtk attach --expect-session` window is given this session and nothing else, so the lookup is an
// exact string compare: a near-match is not a match.
//
// UNTESTED.
func (m *sessionManager) expectedSession(id protocol.AgentID) (sessionView, bool) {
	for _, view := range m.Running() {
		if view.Agent == id {
			return view, true
		}
	}
	return sessionView{}, false
}

// Names is the startable names, in the order the daemon reports them.
func (m *sessionManager) Names() []string { return []string{sessionClaude, sessionCodex} }

// Add starts one session.
//
// The order is the contract: refuse an unknown name, a running one, a busy one or an
// unreached gateway first; ask the gateway second, so a failure there means no harness
// exists and the reason is shown; then the token, the session directory and the hook file;
// then the router; then the pane. A failure after the gateway accepted the session ends it
// again, removes the token and the directory, and tells the caller why.
//
// UNTESTED.
func (m *sessionManager) Add(ctx context.Context, name, dir string, width, height int) (*sessionEntry, error) {
	if !m.startable(name) {
		return nil, errUnknownSessionName
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errShuttingDown
	}
	if existing, ok := m.sessions[name]; ok {
		running := existing.state == stateRunning
		m.mu.Unlock()
		if running {
			return nil, errAlreadyRunning
		}
		return nil, errBusy
	}
	if m.connect == nil || !m.connect.Connected() {
		reason := ""
		if m.connect != nil {
			reason = m.connect.LastError()
		}
		m.mu.Unlock()
		if reason == "" {
			return nil, fmt.Errorf("%w: no session was started", errNotConnected)
		}
		return nil, fmt.Errorf("%w (%s): no session was started", errNotConnected, reason)
	}
	// The name is owned from here to the end of this call: a second add, an end, and the
	// gateway-end retry all find it reserved.
	entry := &sessionEntry{name: name, state: stateReserved}
	m.sessions[name] = entry
	m.mu.Unlock()

	if err := m.start(ctx, entry, dir, width, height); err != nil {
		m.mu.Lock()
		if m.sessions[name] == entry {
			delete(m.sessions, name)
		}
		m.mu.Unlock()
		return nil, err
	}
	return entry, nil
}

// start performs the add on a reserved entry.
func (m *sessionManager) start(ctx context.Context, entry *sessionEntry, dir string, width, height int) error {
	name := entry.name

	// The gateway first: a failure here means no harness exists at all.
	added, err := m.link.AddSession(ctx, name)
	if err != nil {
		return fmt.Errorf("daemon: the gateway did not start the session: %w", err)
	}
	// The add replaced any open session of this name at the gateway, so a queued end for
	// it is moot - and must not run later, when it would end this new one.
	m.clearPendingEnd(name)

	m.mu.Lock()
	entry.gateway = added.Session.SessionID
	entry.address = added.Session.Address
	entry.startedAt = m.now()
	m.mu.Unlock()

	// Everything from here must undo itself and the gateway's session.
	fail := func(reason error) error {
		m.mu.Lock()
		id := entry.id
		m.mu.Unlock()
		if id != "" {
			m.router.PaneFailed(id, reason)
		}
		m.finishEntry(context.Background(), entry, false)
		return reason
	}

	rawID, err := m.newID()
	if err != nil {
		return fail(err)
	}
	id := protocol.AgentID(rawID)
	m.mu.Lock()
	entry.id = id
	m.mu.Unlock()

	address := m.link.Address(name)
	peers := peersFor(added.Directory, address)
	token := m.srv.AddConnectedAgent(id, name, address, peers)
	if token == "" {
		return fail(fmt.Errorf("daemon: the server refused the agent %q", name))
	}

	// The directory is session/<name>/, with no agent id in it: the hook command carries this
	// path (agent.HookCommand, --session-dir), and a harness that is asked to trust a changed
	// hook command skips it until the user answers, so the command must be the same for every
	// session of a name and across daemon restarts. A name stays reserved in the table until
	// finishEntry has removed its directory, so a normal replacement never finds one; a leftover
	// fails the add here with a clear error and is NOT deleted.
	//
	// agent.PrepareSessionDir is not used on purpose: it empties whatever it is given and
	// refuses any name but "session", which is what keeps it from wiping the wrong directory.
	sessionDir := m.sessionDirFor(name)
	if err := makeSessionDir(sessionDir); err != nil {
		return fail(err)
	}
	// Recorded only now that this add made it: fail() and finishEntry remove entry.dir, and a
	// directory this add did not create must never be removed by its failure path.
	m.mu.Lock()
	entry.dir = sessionDir
	m.mu.Unlock()

	spec, err := m.buildSpec(ctx, id, sessionDir, name, dir, peers, token)
	if err != nil {
		return fail(err)
	}

	// The router knows the agent before the pane exists, so the first offer already has
	// somewhere to go, and the gateway's session id is recorded first.
	if err := m.router.AddSession(ctx, spec); err != nil {
		return fail(err)
	}
	m.router.SetGatewaySession(name, entry.gateway)

	// A daemon that began to stop while this add was in flight (it can be in the gateway
	// call or in the harness version lookup for a while) must not start a harness the
	// shutdown has already finished closing.
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return fail(errShuttingDown)
	}

	started, err := paneStart(spec, width, height, m.exitCallback(entry, id))
	if err != nil {
		return fail(err)
	}

	m.mu.Lock()
	m.versions[id] = spec.HarnessVersion
	entry.pane = started
	exitedEarly := entry.exited
	stoppedMeanwhile := m.closed
	if !exitedEarly && !stoppedMeanwhile {
		entry.state = stateRunning
	}
	m.mu.Unlock()

	// stop() sets closed under the same mutex and runs before the shutdown collects the
	// panes. So either this add set entry.pane first and the shutdown's closeAll sees it,
	// or it sees closed here and closes its own pane; a harness cannot be left behind.
	if stoppedMeanwhile && !exitedEarly {
		m.router.PaneStarted(id, started)
		m.finishEntry(context.Background(), entry, true)
		return errShuttingDown
	}
	m.router.PaneStarted(id, started)

	if exitedEarly {
		// The harness was gone before the add returned. The exit callback could not clean
		// up (the entry was still reserved), so this does it.
		m.finishEntry(context.Background(), entry, false)
		return fmt.Errorf("daemon: the %s harness exited as soon as it started", name)
	}

	m.mu.Lock()
	observer := m.observer
	m.mu.Unlock()
	if observer != nil {
		observer.sessionStarted(m.view(entry))
	}

	m.log.logf("session started: %s (%s)", name, id)
	return nil
}

// makeSessionDir creates one session's directory at 0700. It must not exist yet: a name is
// reserved until finishEntry has removed its directory, so finding one means a removal failed
// or something else wrote there, and Mkdir refuses rather than reuse it. Its parent, session/,
// exists from the daemon's start.
func makeSessionDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("daemon: session directory %q must be absolute", dir)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: create the session directory %q: %w", dir, err)
	}
	// Mkdir is subject to the umask; the mode is set explicitly, as the other directories are.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: chmod the session directory %q: %w", dir, err)
	}
	return nil
}

// sessionDirFor is one session's own directory, session/<name>, which holds its hook file.
// It has no agent id, so the hook command that names it is the same for every session of that
// name; see start.
func (m *sessionManager) sessionDirFor(name string) string {
	return filepath.Join(m.sessionDir, name)
}

// buildSpec mirrors app's buildSpec: the hook file in the session's own directory, then the
// harness launch spec built from the daemon's captured environment.
func (m *sessionManager) buildSpec(ctx context.Context, id protocol.AgentID, sessionDir, name, dir string, peers []string, token string) (protocol.LaunchSpec, error) {
	failed := protocol.LaunchSpec{ID: id, DisplayName: name, Harness: name}
	if token == "" {
		return failed, fmt.Errorf("server refused agent %q", name)
	}
	if dir == "" {
		// A harness is never started in a directory nobody chose: the window that asks for
		// a session names it.
		return failed, errors.New("daemon: no working directory was given for the session")
	}
	if err := server.WriteHookFile(sessionDir, name, m.srv.Addr(), token); err != nil {
		return failed, err
	}

	spec, err := agent.Build(agent.Params{
		ID:          id,
		DisplayName: name,
		Harness:     name,
		Dir:         dir,
		RtkPath:     m.rtkPath,
		SessionDir:  sessionDir,
		ServerAddr:  m.srv.Addr(),
		Token:       token,
		BaseEnv:     m.env,
		Peers:       peers,
	})
	if err != nil {
		return failed, err
	}
	if m.harnessVersion != nil {
		spec.HarnessVersion = m.harnessVersion(ctx, name)
	}
	return spec, nil
}

// exitCallback runs when the harness exits by itself.
//
// It reports the exit to the router, marks the entry gone, and starts the cleanup in its own
// goroutine: pane's close waits for the goroutine that runs this callback, so closing the
// pane, or waiting for anything that does, from inside it would deadlock.
//
// UNTESTED.
func (m *sessionManager) exitCallback(entry *sessionEntry, id protocol.AgentID) func(int) {
	return func(code int) {
		m.router.PaneExited(id, code)

		m.mu.Lock()
		if entry.id != id {
			m.mu.Unlock()
			return
		}
		entry.exited = true
		switch entry.state {
		case stateRunning:
			entry.state = stateEnding
			observer := m.observer
			m.mu.Unlock()
			if observer != nil {
				exit := code
				observer.sessionExited(m.view(entry), &exit)
			}
			go m.finishEntry(context.Background(), entry, false)
		default:
			// Still starting: start() sees exited and cleans up. Already ending: the end in
			// progress finishes it.
			m.mu.Unlock()
		}
	}
}

// finishEntry takes one session out of the world: the gateway, the pane, the token, the
// directory, and last the table entry.
//
// The entry stays in the table until the end, so no add for the same name can overtake the
// gateway end below. closePane is false when the harness is already gone, or never started.
func (m *sessionManager) finishEntry(ctx context.Context, entry *sessionEntry, closePane bool) {
	m.mu.Lock()
	name, id, dir, pane := entry.name, entry.id, entry.dir, entry.pane
	gone := entry.exited
	m.mu.Unlock()

	// The gateway first: new sends get session_gone at once. A failure is queued for the
	// connector, and the router already refuses offers to an ended agent.
	endCtx, cancel := context.WithTimeout(ctx, shutdownBudget)
	if err := m.link.EndSession(endCtx, name); err != nil {
		m.queuePendingEnd(name)
	}
	cancel()

	if closePane && pane != nil && !gone {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), closeGrace)
		_ = pane.CloseContext(closeCtx)
		cancelClose()
	}

	if id != "" {
		m.srv.RemoveAgent(id)
	}
	if dir != "" {
		// A removal that fails leaves the directory behind, and the next add of this name then
		// fails with "file exists" rather than reusing it. The log says why, so that error is
		// not a mystery.
		if err := os.RemoveAll(dir); err != nil {
			m.log.logf("could not remove the %s session directory %s: %v", name, dir, err)
		}
	}

	m.mu.Lock()
	if m.sessions[name] == entry {
		delete(m.sessions, name)
	}
	delete(m.versions, id)
	observer := m.observer
	m.mu.Unlock()
	if observer != nil {
		observer.sessionEnded(m.view(entry))
	}
	m.log.logf("session ended: %s", name)
}

// End ends one session: the gateway first, so new sends fail promptly, then the pane, the
// token and the directory.
//
// An unknown name, an already-ended one and one already ending all return errNoSession; a
// name that is still being started returns errBusy.
//
// UNTESTED.
func (m *sessionManager) End(ctx context.Context, name string) error {
	m.mu.Lock()
	entry, ok := m.sessions[name]
	switch {
	case !ok:
		m.mu.Unlock()
		return errNoSession
	case entry.state == stateReserved:
		m.mu.Unlock()
		return errBusy
	case entry.state == stateEnding:
		m.mu.Unlock()
		return errNoSession
	}
	entry.state = stateEnding
	m.mu.Unlock()

	m.finishEntry(ctx, entry, true)
	return nil
}

// stop makes every later add fail. The daemon calls it first when it begins to stop, so no
// pane can start after the shutdown closed them.
func (m *sessionManager) stop() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// Status reports the running sessions, in name order.
//
// UNTESTED.
func (m *sessionManager) Status() []wire.SessionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, 0, len(m.sessions))
	for name, entry := range m.sessions {
		if entry.state == stateRunning {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	out := make([]wire.SessionStatus, 0, len(names))
	for _, name := range names {
		entry := m.sessions[name]
		out = append(out, wire.SessionStatus{
			Name:      entry.name,
			Agent:     string(entry.id),
			Address:   entry.address,
			StartedAt: entry.startedAt,
		})
	}
	return out
}

// HarnessPIDs is the live processes this daemon started, read fresh on every call.
//
// It is what the ancestry check asks about. A process that double-forks is not in it, which
// is why that check is best effort and not a boundary.
//
// UNTESTED.
func (m *sessionManager) HarnessPIDs() map[int]struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	set := make(map[int]struct{}, len(m.sessions))
	for _, entry := range m.sessions {
		if entry.pane == nil || entry.exited {
			continue
		}
		if pid := entry.pane.PID(); pid > 0 {
			set[pid] = struct{}{}
		}
	}
	return set
}

// Panes is every live pane, for the shutdown's closeAll.
//
// UNTESTED.
func (m *sessionManager) Panes() []sessionPane {
	m.mu.Lock()
	defer m.mu.Unlock()

	panes := make([]sessionPane, 0, len(m.sessions))
	for _, entry := range m.sessions {
		if entry.pane != nil && !entry.exited {
			panes = append(panes, entry.pane)
		}
	}
	return panes
}

// queuePendingEnd remembers a session the gateway could not be told about.
func (m *sessionManager) queuePendingEnd(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingEnd[name] = struct{}{}
	m.log.logf("session end queued for the gateway: %s", name)
}

// clearPendingEnd drops a queued end that a newer add has made moot.
func (m *sessionManager) clearPendingEnd(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pendingEnd, name)
}

// retryPendingEnd tries the queued ends once. The connector calls it after a connect and
// then at every retry interval.
//
// A name is retried only while nothing else owns it, and it is reserved for the duration of
// the call: an end by name that landed after a new add of the same name would end the new
// session.
//
// UNTESTED.
func (m *sessionManager) retryPendingEnd(ctx context.Context) {
	m.mu.Lock()
	names := make([]string, 0, len(m.pendingEnd))
	for name := range m.pendingEnd {
		names = append(names, name)
	}
	m.mu.Unlock()

	for _, name := range names {
		m.mu.Lock()
		if _, owned := m.sessions[name]; owned || m.closed {
			m.mu.Unlock()
			continue
		}
		hold := &sessionEntry{name: name, state: stateReserved}
		m.sessions[name] = hold
		m.mu.Unlock()

		err := m.link.EndSession(ctx, name)

		m.mu.Lock()
		if m.sessions[name] == hold {
			delete(m.sessions, name)
		}
		if err == nil {
			delete(m.pendingEnd, name)
		}
		m.mu.Unlock()
		if err == nil {
			m.log.logf("queued session end reported to the gateway: %s", name)
		}
	}
}

// isAlreadyRunning reports whether an add failed only because the session was already running,
// which is what `rtk attach` treats as "join the one that is there".
//
// UNTESTED.
func isAlreadyRunning(err error) bool {
	return errors.Is(err, errAlreadyRunning)
}

// startable reports whether this build can start that name.
func (m *sessionManager) startable(name string) bool {
	return name == sessionClaude || name == sessionCodex
}

// peersFor lists the network names one agent should see as its peers: every directory
// address except its own, in the gateway's order, de-duplicated.
//
// It is a copy of app's function, which goes with that file in slice 5.
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
