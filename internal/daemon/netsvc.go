package daemon

// The network service: what this daemon says about itself, and what it knows about everyone else.
//
// Three jobs, all bounded and none of them on a window's path:
//
//   - Report about this host while the gateway stream is connected, at most once a second, on a
//     change, every 10 s, or when the link comes back. Nothing is queued or replayed.
//   - Fetch the gateway's snapshot while anybody is looking: every attached window, or one
//     `rtk daemon network` call.
//   - Answer its own windows and the CLI from cached data plus one bounded wait.
//
// The service never holds its own mutex across a link call or across a wait: it copies what it
// needs, releases, and then talks to the network. A window must never wait on a lock the reporter
// holds, and the reporter must never be delayed by a window.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No report has been sent, no fetch made and no
// network answer produced.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// reportEvery is the longest a connected daemon goes without reporting about itself.
	reportEvery = 10 * time.Second
	// reportCoalesce is the shortest gap between two reports: a burst of session changes becomes
	// one report, not one per change.
	reportCoalesce = 1 * time.Second
	// reportSendTimeout bounds one report.
	reportSendTimeout = 5 * time.Second // UNTESTED guess
	// fetchWait is how long one snapshot fetch asks the gateway to wait. It is shorter than the
	// gateway's 20 s maximum on purpose: after every "no change" answer the loop fetches the full
	// body once more (the revision ignores ages, so a quiet healthy network only ever answers 204),
	// so a shown age overstates by at most about this long.
	fetchWait = 10 * time.Second
	// fetchMargin is added to the wait for the fetch's own timeout.
	fetchMargin = 5 * time.Second // UNTESTED guess
	// fetchFloor is the shortest gap between two fetches, so a gateway that answers 204 at once
	// cannot turn the loop hot.
	fetchFloor = 250 * time.Millisecond // UNTESTED guess
	// fetchBackoffMin and fetchBackoffMax bound the retry after a failed fetch.
	fetchBackoffMin = 1 * time.Second  // UNTESTED guess
	fetchBackoffMax = 15 * time.Second // UNTESTED guess
	// fetchIdlePoll is how often a consumer's loop re-checks a link that is not connected.
	fetchIdlePoll = 250 * time.Millisecond // UNTESTED guess
	// networkErrorMax bounds the gateway error text the answer carries.
	networkErrorMax = 200
)

// localSession is one running session as the service needs it.
type localSession struct {
	Name           string
	Agent          protocol.AgentID
	Gateway        string // the gateway's session ID, empty when this daemon has none
	HarnessVersion string
	StartedAt      time.Time
}

// localSource is the session manager.
type localSource interface {
	LocalSessions() []localSession
}

// stateSource is the router snapshot: agent states and the connection view.
type stateSource interface {
	Snapshot() protocol.Snapshot
}

// inputSource is the gate: who holds input, and whether it knows the session at all.
type inputSource interface {
	Holder(agent protocol.AgentID) (string, bool)
}

// netLink is the gateway client.
type netLink interface {
	ReportSelf(ctx context.Context, request protocol.ReportRequest) error
	FetchNetwork(ctx context.Context, since uint64, wait time.Duration) (link.NetworkFetch, error)
}

// stateStamp is one session's last observed state and when it was observed.
type stateStamp struct {
	state protocol.AgentState
	at    time.Time
}

// stateClock remembers when each session's state last changed.
//
// state_at is part of the gateway's revision signature, so it must move only when a state really
// changes. Stamping it on every report would bump the gateway's revision every ten seconds and wake
// every long-poll for nothing.
type stateClock struct {
	mu     sync.Mutex
	stamps map[protocol.AgentID]stateStamp
}

// newStateClock builds an empty clock.
func newStateClock() *stateClock {
	return &stateClock{stamps: make(map[protocol.AgentID]stateStamp, 2)}
}

// observe records the states of the agents it is shown, moving a stamp only when the state differs.
//
// It is called from the event fan-out (every agent event) and at the start of every local build, so
// a dropped event still leaves the state right after the next build.
//
// UNTESTED.
func (c *stateClock) observe(agents []protocol.AgentView, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, agent := range agents {
		previous, known := c.stamps[agent.ID]
		if known && previous.state == agent.State {
			continue
		}
		c.stamps[agent.ID] = stateStamp{state: agent.State, at: now}
	}
}

// forget drops one session's stamp.
//
// UNTESTED.
func (c *stateClock) forget(agent protocol.AgentID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.stamps, agent)
}

// stamp returns the state and the time it started, stamping now for a state that is new or has
// changed.
//
// UNTESTED.
func (c *stateClock) stamp(agent protocol.AgentID, state protocol.AgentState, now time.Time) (protocol.AgentState, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	previous, known := c.stamps[agent]
	if known && previous.state == state {
		return state, previous.at
	}
	c.stamps[agent] = stateStamp{state: state, at: now}
	return state, now
}

// networkService is the daemon's network state.
type networkService struct {
	link    netLink
	source  localSource
	states  stateSource
	inputs  inputSource
	log     *logger
	clock   *stateClock
	now     func() time.Time
	host    func() string
	build   string
	started time.Time
	// stateDir is where the owner's hosts.json lives, so an enter plan reads the file the owner
	// edits rather than a copy.
	stateDir string

	// kickCh is a one-slot signal: a kick that arrives while a report is being sent stays here for
	// the next tick.
	kickCh chan struct{}

	mu      sync.Mutex
	version uint64
	changed chan struct{}
	ctx     context.Context

	// signature is the content hash of the last view, which is what version tracks.
	signature string

	// Gateway view.
	state    string
	stateErr string
	asOf     time.Time

	// Snapshot: kept across failures, never mutated after install, never presented as current.
	snapshot     *protocol.NetworkSnapshot
	fetchedAt    time.Time
	lastRevision uint64

	// Consumers of the fetch loop.
	consumers    int
	fetchCancel  context.CancelFunc
	fetchRunning bool

	// unsupported is set by a 404 or a too-new schema, and cleared only after the link has been seen
	// non-connected and has come back.
	unsupported     bool
	sawDisconnected bool
	// unsupportedText is why, when it is not simply "the routes are missing".
	unsupportedText string

	// fetchErr and reportErr are the last failure of each job, cleared by that job's next success.
	// The gateway state is derived from them and from the stream at the moment it is read, so a
	// successful report cannot hide a failing fetch (or the other way round).
	fetchErr  string
	reportErr string

	// fetchGen counts completed fetch attempts (a snapshot installed, or a failure). A caller that
	// asks to wait for a first fetch waits for this to move, not for a snapshot to be absent.
	// loopGen is its value when the current loop started, and streamUp is the stream's state as the
	// last view saw it.
	fetchGen uint64
	loopGen  uint64
	streamUp bool

	// Reporter bookkeeping.
	lastAttempt   time.Time
	pendingKick   bool
	lastConnected bool
	seq           uint64
	lastRefusal   string
}

// newNetworkService builds the service. Nothing runs until start.
//
// host is a function so an unpaired daemon (no host name) answers not_configured rather than
// reporting an empty host.
//
// UNTESTED.
func newNetworkService(link netLink, local localSource, states stateSource, inputs inputSource, log *logger, host func() string, build string, started time.Time) *networkService {
	return &networkService{
		link:    link,
		source:  local,
		states:  states,
		inputs:  inputs,
		log:     log,
		clock:   newStateClock(),
		now:     time.Now,
		host:    host,
		build:   build,
		started: started,
		kickCh:  make(chan struct{}, 1),
		changed: make(chan struct{}),
		state:   protocol.GatewayNotConfigured,
	}
}

// start runs the reporter. It is the only goroutine the service owns besides a consumer's fetch
// loop, and it ends with ctx.
//
// UNTESTED.
func (s *networkService) start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()

	go s.reportLoop(ctx)
}

// setStateDir records where the state directory is, for the enter plan's host map.
func (s *networkService) setStateDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateDir = dir
}

// stateDirectory is where hosts.json lives.
func (s *networkService) stateDirectory() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateDir
}

// kick asks for a report soon. It never blocks: a full channel already means one is pending.
//
// UNTESTED.
func (s *networkService) kick() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.pendingKick = true
	s.mu.Unlock()

	select {
	case s.kickCh <- struct{}{}:
	default:
	}

	// A local change (a session, a state, who holds input) is also news for anyone waiting on the
	// answer: without this a window waiting on its version would hear about its own host's changes
	// only when its wait ran out.
	s.bump()
}

// observe records agent states for state_at stamping.
func (s *networkService) observe(agents []protocol.AgentView, now time.Time) {
	if s == nil {
		return
	}
	s.clock.observe(agents, now)
}

// forget drops one session's stamp when its session is gone.
func (s *networkService) forget(agent protocol.AgentID) {
	if s == nil {
		return
	}
	s.clock.forget(agent)
}

// reportLoop is the reporter: a ticker, plus kicks, coalesced to at most one report a second.
func (s *networkService) reportLoop(ctx context.Context) {
	ticker := time.NewTicker(reportCoalesce)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.kickCh:
		}
		s.maybeReport(ctx)
	}
}

// maybeReport sends a report if one is due.
func (s *networkService) maybeReport(ctx context.Context) {
	now := s.now()

	connected, _ := s.linkConnected()

	s.mu.Lock()
	if !connected {
		// The stream is the report's lifeline: without it nothing is sent, and a 404's
		// "unsupported" only clears after the link has been seen down once and comes back.
		s.lastConnected = false
		s.sawDisconnected = true
		s.mu.Unlock()
		return
	}
	if !s.lastConnected {
		// The link just came up: report at once, so the gateway's "no report" is short-lived.
		s.lastConnected = true
		s.pendingKick = true
	}
	if s.unsupported {
		// A 404 (or a too-new schema) is sticky until the link has been seen down once: an older
		// gateway is not going to grow the routes while this connection lives.
		if s.sawDisconnected {
			s.unsupported = false
			s.unsupportedText = ""
			s.sawDisconnected = false
		} else {
			s.mu.Unlock()
			return
		}
	}

	due := s.pendingKick || s.lastAttempt.IsZero() || now.Sub(s.lastAttempt) >= reportEvery
	coalesced := !s.lastAttempt.IsZero() && now.Sub(s.lastAttempt) < reportCoalesce
	if !due || coalesced {
		s.mu.Unlock()
		return
	}

	s.pendingKick = false
	s.lastAttempt = now
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	// The report itself is built outside the mutex, and the send is outside it too.
	request := s.reportRequest(seq)

	sendCtx, cancel := context.WithTimeout(ctx, reportSendTimeout)
	err := s.link.ReportSelf(sendCtx, request)
	cancel()

	s.finishReport(err)
}

// finishReport records what one report attempt did.
func (s *networkService) finishReport(err error) {
	switch {
	case err == nil:
		s.mu.Lock()
		cleared := s.reportErr != ""
		s.reportErr = ""
		s.mu.Unlock()
		if cleared {
			s.bump()
		}

	case errors.Is(err, link.ErrNetworkUnsupported):
		s.mu.Lock()
		already := s.markUnsupportedLocked("")
		s.mu.Unlock()
		if !already {
			s.log.logf("the gateway has no network routes; reporting stops until the link reconnects")
		}
		s.bump()

	case errors.Is(err, link.ErrNetworkSchemaTooNew):
		// A report is not a schema-carrying body, so this cannot normally happen; treating it like
		// unsupported keeps the rule in one place.
		s.mu.Lock()
		s.markUnsupportedLocked(cleanNetworkError(err.Error()))
		s.mu.Unlock()
		s.bump()

	default:
		text := cleanNetworkError(err.Error())

		var refusal *link.RefusedError
		if errors.As(err, &refusal) {
			// The gateway ANSWERED: it is reachable, and fetches may be fine. A rejected report is
			// logged once per distinct text and the previous one ages out at the gateway; it is not
			// a reason to call the gateway unreachable.
			s.mu.Lock()
			first := s.lastRefusal != text
			s.lastRefusal = text
			cleared := s.reportErr != ""
			s.reportErr = ""
			s.mu.Unlock()
			if first {
				s.log.logf("the gateway refused the report: %s", text)
			}
			if cleared {
				s.bump()
			}
			return
		}

		// A transport failure is remembered for the answer and retried on the next tick, never
		// queued.
		s.mu.Lock()
		s.reportErr = text
		s.mu.Unlock()
		s.bump()
	}
}

// markUnsupportedLocked records that the gateway has no usable network routes, and reports whether
// it already knew.
//
// Clearing sawDisconnected here is what makes "sticky until the link has been seen down" mean down
// AFTER this point: an earlier outage must not release the flag on the very next tick.
func (s *networkService) markUnsupportedLocked(text string) bool {
	already := s.unsupported
	s.unsupported = true
	s.unsupportedText = text
	s.sawDisconnected = false
	return already
}

// linkConnected reports whether the gateway stream is connected, and its error text when it is not.
func (s *networkService) linkConnected() (bool, string) {
	snapshot := s.states.Snapshot()
	connection := snapshot.Connection
	if connection == nil {
		return false, ""
	}
	if connection.State != protocol.ConnConnected {
		return false, connection.Err
	}
	return true, ""
}

// localSessions is every running session as the answer needs it, in name order.
func (s *networkService) localSessions() []localSession {
	if s.source == nil {
		return nil
	}
	sessions := s.source.LocalSessions()
	return sessions
}

// local builds this host's own picture.
//
// Only the fields the contract allows are ever filled: no prompts, screens, directories, paths,
// environment, credentials, window identities or draft state can reach here, because the inputs do
// not carry them.
//
// UNTESTED.
func (s *networkService) local() protocol.NetworkLocal {
	now := s.now()
	sessions := s.localSessions()
	agents := s.states.Snapshot().Agents
	s.clock.observe(agents, now)

	local := protocol.NetworkLocal{
		Daemon: protocol.ReportDaemon{
			Build:     s.build,
			StartedAt: protocol.FormatTime(s.started),
		},
		Sessions: make([]protocol.ReportSession, 0, len(sessions)),
	}
	if s.host != nil {
		local.Host = s.host()
	}

	for _, session := range sessions {
		state := protocol.AgentUnknown
		for _, agent := range agents {
			if agent.ID == session.Agent {
				state = agent.State
				break
			}
		}
		stateName, at := s.clock.stamp(session.Agent, state, now)

		local.Sessions = append(local.Sessions, protocol.ReportSession{
			SessionID:      session.Gateway,
			LocalID:        string(session.Agent),
			Name:           session.Name,
			Harness:        session.Name,
			HarnessVersion: session.HarnessVersion,
			StartedAt:      protocol.FormatTime(session.StartedAt),
			State:          networkStateName(stateName),
			StateAt:        protocol.FormatTime(at),
			Input:          s.inputName(session.Agent),
		})
	}
	return local
}

// reportRequest builds the body this host reports.
func (s *networkService) reportRequest(seq uint64) protocol.ReportRequest {
	local := s.local()
	return protocol.ReportRequest{
		Schema:     protocol.NetworkSchema,
		Seq:        seq,
		ReportedAt: protocol.FormatTime(s.now()),
		Daemon:     local.Daemon,
		Sessions:   local.Sessions,
	}
}

// inputName maps the gate's answer to the contract's three values.
//
// "held" is a holder existing, "free" is the gate knowing the session with no holder, "unknown" is
// the gate not knowing it at all. Which window holds is deliberately not part of this.
func (s *networkService) inputName(agent protocol.AgentID) string {
	if s.inputs == nil {
		return protocol.InputUnknown
	}
	holder, known := s.inputs.Holder(agent)
	if !known {
		return protocol.InputUnknown
	}
	if holder != "" {
		return protocol.InputHeld
	}
	return protocol.InputFree
}

// networkStateName maps a router agent state to the contract's closed set.
func networkStateName(state protocol.AgentState) string {
	switch state {
	case protocol.AgentStarting:
		return protocol.SessionStateStarting
	case protocol.AgentIdle:
		return protocol.SessionStateIdle
	case protocol.AgentBusy:
		return protocol.SessionStateBusy
	case protocol.AgentNeedsYou:
		return protocol.SessionStateNeedsYou
	case protocol.AgentExited:
		return protocol.SessionStateExited
	}
	return protocol.SessionStateUnknown
}

// acquire registers one consumer and returns an idempotent release.
//
// The 0 -> 1 transition is the only thing that starts a fetch loop, so a daemon nobody is watching
// makes no network requests at all.
//
// UNTESTED.
func (s *networkService) acquire() func() {
	if s == nil {
		return func() {}
	}

	s.mu.Lock()
	s.consumers++
	start := s.consumers == 1 && !s.fetchRunning
	var loopCtx context.Context
	if start {
		s.fetchRunning = true
		// A new loop starts clean: a failure from an earlier loop must not make a later caller
		// see "unreachable" and leave before this loop has tried.
		s.fetchErr = ""
		s.loopGen = s.fetchGen
		parent := s.ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		s.fetchCancel = cancel
		loopCtx = ctx
	}
	s.mu.Unlock()

	if start {
		go s.fetchLoop(loopCtx)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.consumers--
			if s.consumers <= 0 && s.fetchCancel != nil {
				s.fetchCancel()
				s.fetchCancel = nil
				s.fetchRunning = false
			}
			s.mu.Unlock()
		})
	}
}

// fetchLoop reads the snapshot while somebody is looking.
func (s *networkService) fetchLoop(ctx context.Context) {
	backoff := fetchBackoffMin
	// immediate asks for the full body at once, without since or wait: the first fetch of a loop
	// (so a caller is answered now, whatever the gateway's revision) and the one after every
	// "no change" (so the kept body and its fetch time keep moving).
	immediate := true

	for {
		if ctx.Err() != nil {
			return
		}

		if !s.fetchable() {
			if !sleepContext(ctx, fetchIdlePoll) {
				return
			}
			continue
		}

		var since uint64
		wait := fetchWait
		if immediate {
			wait = 0
		} else {
			s.mu.Lock()
			since = s.lastRevision
			s.mu.Unlock()
		}

		started := s.now()
		fetchCtx, cancel := context.WithTimeout(ctx, wait+fetchMargin)
		fetch, err := s.link.FetchNetwork(fetchCtx, since, wait)
		cancel()

		if ctx.Err() != nil {
			return
		}

		switch {
		case err == nil && fetch.Unchanged:
			s.clearFetchErr()
			immediate = true
			// The gateway waited, but a gateway that answers at once must not turn this into a hot
			// loop.
			if elapsed := s.now().Sub(started); elapsed < fetchFloor {
				if !sleepContext(ctx, fetchFloor-elapsed) {
					return
				}
			}
			backoff = fetchBackoffMin

		case err == nil && fetch.Snapshot != nil:
			s.install(fetch.Snapshot)
			immediate = false
			backoff = fetchBackoffMin

		case errors.Is(err, link.ErrNetworkUnsupported):
			s.mu.Lock()
			already := s.markUnsupportedLocked("")
			s.mu.Unlock()
			if !already {
				s.log.logf("the gateway has no network routes; fetching stops until the link reconnects")
			}
			s.bump()
			if !sleepContext(ctx, fetchIdlePoll) {
				return
			}

		case errors.Is(err, link.ErrNetworkSchemaTooNew):
			// Never installed: a newer schema is unavailable, not best-effort.
			s.mu.Lock()
			s.markUnsupportedLocked("the gateway's network data is newer than this daemon understands")
			s.mu.Unlock()
			s.bump()
			if !sleepContext(ctx, fetchIdlePoll) {
				return
			}

		default:
			text := "the gateway could not be reached"
			if err != nil {
				text = cleanNetworkError(err.Error())
			}
			s.mu.Lock()
			s.fetchErr = text
			s.fetchGen++
			s.mu.Unlock()
			s.bump()

			if !sleepContext(ctx, backoff) {
				return
			}
			backoff *= 2
			if backoff > fetchBackoffMax {
				backoff = fetchBackoffMax
			}
		}
	}
}

// configured reports whether this daemon has a gateway to talk to at all.
func (s *networkService) configured() bool {
	return s.link != nil && s.host != nil && s.host() != ""
}

// clearFetchErr records a successful fetch, waking waiters if it ended an error.
func (s *networkService) clearFetchErr() {
	s.mu.Lock()
	cleared := s.fetchErr != ""
	s.fetchErr = ""
	s.mu.Unlock()
	if cleared {
		s.bump()
	}
}

// fetchable reports whether the link is usable for a fetch right now.
//
// The gateway state itself is not set here: it is derived when it is read.
func (s *networkService) fetchable() bool {
	if !s.configured() {
		return false
	}

	connected, _ := s.linkConnected()

	s.mu.Lock()
	unsupported := s.unsupported
	if !connected {
		s.sawDisconnected = true
	}
	s.mu.Unlock()

	return connected && !unsupported
}

// install keeps a fetched snapshot, replacing the previous one whole.
//
// A lower revision than the last one means the gateway restarted, so it is installed like any other
// snapshot: the client never tries to reconcile two gateway lifetimes.
//
// UNTESTED.
func (s *networkService) install(snapshot *protocol.NetworkSnapshot) {
	if snapshot == nil {
		return
	}
	copied := *snapshot

	s.mu.Lock()
	s.snapshot = &copied
	s.fetchedAt = s.now()
	s.lastRevision = copied.Revision
	s.unsupported = false
	s.unsupportedText = ""
	s.fetchErr = ""
	s.fetchGen++
	s.mu.Unlock()
	s.bump()
}

// view builds the answer and the daemon's own version for it.
//
// The local build happens outside the mutex (it reads the manager, the router and the gate), then
// the version is bumped under it if the content changed.
//
// UNTESTED.
func (s *networkService) view() (protocol.NetworkView, uint64) {
	local := s.local()
	streamUp, streamErr := s.linkConnected()
	configured := s.configured()

	view := protocol.NetworkView{
		Schema: protocol.NetworkSchema,
		Local:  local,
	}

	s.mu.Lock()
	s.refreshGatewayLocked(configured, streamUp, streamErr, s.now())
	if s.snapshot == nil {
		view.Snapshot = nil
	} else {
		copied := *s.snapshot
		view.Snapshot = &copied
		view.FetchedAt = protocol.FormatTime(s.fetchedAt)
	}
	view.Gateway = protocol.NetworkGatewayView{
		State: s.state,
		Error: s.stateErr,
		AsOf:  protocol.FormatTime(s.asOf),
	}

	signature := networkSignature(snapshotRevision(view.Snapshot), view)
	if signature != s.signature {
		s.signature = signature
		s.version++
		s.broadcastLocked()
	}
	version := s.version
	s.mu.Unlock()

	return view, version
}

// snapshotRevision is the kept snapshot's revision, or zero.
func snapshotRevision(snapshot *protocol.NetworkSnapshot) uint64 {
	if snapshot == nil {
		return 0
	}
	return snapshot.Revision
}

// networkSignature is the content hash that decides the daemon's version.
//
// The local sessions' states and inputs, the host and daemon identity, the gateway state and its
// error all count. Ages do not, and neither does state_at, because it only moves with a state.
func networkSignature(revision uint64, view protocol.NetworkView) string {
	var builder strings.Builder

	builder.WriteString(view.Local.Host)
	builder.WriteString("|")
	builder.WriteString(view.Local.Daemon.Build)
	builder.WriteString("|")
	builder.WriteString(view.Gateway.State)
	builder.WriteString("|")
	builder.WriteString(view.Gateway.Error)
	builder.WriteString("|")
	for _, session := range view.Local.Sessions {
		builder.WriteString(session.LocalID)
		builder.WriteString(",")
		builder.WriteString(session.SessionID)
		builder.WriteString(",")
		builder.WriteString(session.Name)
		builder.WriteString(",")
		builder.WriteString(session.HarnessVersion)
		builder.WriteString(",")
		builder.WriteString(session.StartedAt)
		builder.WriteString(",")
		builder.WriteString(session.State)
		builder.WriteString(",")
		builder.WriteString(session.Input)
		builder.WriteString(";")
	}
	builder.WriteString("|")
	builder.WriteString(strconvFormatUint(revision))
	return builder.String()
}

// strconvFormatUint renders a revision without pulling strconv in for one call.
func strconvFormatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// refreshGatewayLocked derives the gateway state from what is true now.
//
// Order: no gateway at all; a gateway that cannot do this (404 or a newer schema); a stream that is
// not connected; a failed fetch or report that has not since succeeded; otherwise connected. An
// absent snapshot alone never makes it unreachable: a healthy link with nothing fetched yet is
// connected, with a null snapshot.
func (s *networkService) refreshGatewayLocked(configured, streamUp bool, streamErr string, now time.Time) {
	s.streamUp = streamUp
	switch {
	case !configured:
		s.setGatewayLocked(protocol.GatewayNotConfigured, "", now)
	case s.unsupported:
		s.setGatewayLocked(protocol.GatewayUnsupported, s.unsupportedText, now)
	case !streamUp:
		s.setGatewayLocked(protocol.GatewayUnreachable, cleanNetworkError(streamErr), now)
	case s.fetchErr != "":
		s.setGatewayLocked(protocol.GatewayUnreachable, s.fetchErr, now)
	case s.reportErr != "":
		s.setGatewayLocked(protocol.GatewayUnreachable, s.reportErr, now)
	default:
		s.setGatewayLocked(protocol.GatewayConnected, "", now)
	}
}

// setGatewayLocked records the gateway state, moving its timestamp only when something changed.
func (s *networkService) setGatewayLocked(state, errText string, now time.Time) {
	if s.state == state && s.stateErr == errText {
		return
	}
	s.state = state
	s.stateErr = errText
	s.asOf = now
}

// broadcastLocked wakes every waiter by closing and replacing the changed channel.
func (s *networkService) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// bump wakes every waiter after a change made outside the mutex.
func (s *networkService) bump() {
	s.mu.Lock()
	s.broadcastLocked()
	s.mu.Unlock()
}

// currentVersion is the daemon's version without building a body.
func (s *networkService) currentVersion() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// answer produces the network view a window or a control client asked for.
//
// Three cases, in this order: a caller with no snapshot yet may wait briefly for the first one; a
// caller whose since is the current version waits for a change; everyone else is answered now. The
// waits are clamped, and a cancelled context answers with what exists rather than an error.
//
// UNTESTED.
func (s *networkService) answer(ctx context.Context, request wire.NetworkRequest) (protocol.NetworkView, uint64) {
	wait := clampMillis(request.WaitMS, wire.MaxNetworkWaitMS)
	first := clampMillis(request.FirstWaitMS, wire.MaxNetworkFirstMS)

	s.mu.Lock()
	startGen := s.fetchGen
	s.mu.Unlock()

	view, version := s.view()

	state := view.Gateway.State
	if first > 0 && state != protocol.GatewayNotConfigured && state != protocol.GatewayUnsupported {
		// Wait for a fetch to COMPLETE or FAIL since this request began (or since the loop that is
		// serving it began, whichever is earlier), not for a snapshot to be absent: a one-shot that
		// finds an old snapshot kept must still get a fresh one. A caller is not made to wait for a
		// gateway that cannot answer (no stream, unsupported).
		return s.waitFor(ctx, first, func(_ protocol.NetworkView, _ uint64) bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			floor := startGen
			if s.loopGen < floor {
				floor = s.loopGen
			}
			return s.fetchGen > floor || s.fetchErr != "" || s.unsupported || !s.streamUp
		})
	}

	if request.Since != 0 && request.Since == version && wait > 0 {
		return s.waitFor(ctx, wait, func(_ protocol.NetworkView, current uint64) bool {
			return current != request.Since
		})
	}

	return view, version
}

// waitFor waits for a condition on the view, bounded, and answers with the current view either way.
func (s *networkService) waitFor(ctx context.Context, wait time.Duration, done func(protocol.NetworkView, uint64) bool) (protocol.NetworkView, uint64) {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	for {
		// The channel is read BEFORE the view is built, so a change between the two wakes the next
		// select instead of being missed.
		s.mu.Lock()
		changed := s.changed
		s.mu.Unlock()

		view, version := s.view()
		if done(view, version) {
			return view, version
		}

		select {
		case <-changed:
		case <-timer.C:
			return s.view()
		case <-ctx.Done():
			return s.view()
		}
	}
}

// clampMillis converts one request bound to a duration, clamped to the wire's maximum.
func clampMillis(value, maximum int) time.Duration {
	if value < 0 {
		value = 0
	}
	if value > maximum {
		value = maximum
	}
	return time.Duration(value) * time.Millisecond
}

// cleanNetworkError makes an error text safe for the answer: one line, bounded, and without control
// characters that could rewrite a terminal.
//
// UNTESTED.
func cleanNetworkError(text string) string {
	cleaned := make([]rune, 0, len(text))
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			cleaned = append(cleaned, ' ')
			continue
		}
		cleaned = append(cleaned, r)
		if len(cleaned) == networkErrorMax {
			break
		}
	}
	return strings.TrimSpace(string(cleaned))
}

// sleepContext waits, or reports that the context ended first.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
