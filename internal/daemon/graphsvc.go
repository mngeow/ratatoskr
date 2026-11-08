package daemon

// The graph service: the daemon fetches the gateway's graph snapshot while somebody is looking, keeps
// the last one, and answers its own windows from it.
//
// It is netsvc.go's fetch half with its own consumer count, its own loop and its own gateway state, so
// the Network tab is untouched: an older gateway that answers 404 on the graph route is
// gateway_too_old here and nothing else changes. A window is a consumer from a successful attach until
// it closes, exactly like the network consumer (the tab is always present, so its data is ready when it
// is opened).
//
// A kept snapshot is never presented as current. Gateway.State says what is true now; FetchedAt says
// how old the snapshot is; and the snapshot's own ages are as of its fetch. After every "no change"
// answer the loop fetches the full body once more, because the gateway's revision ignores ages.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// graphLink is the gateway client's graph half.
type graphLink interface {
	FetchGraph(ctx context.Context, since uint64, wait time.Duration) (link.GraphFetch, error)
}

// graphService fetches and keeps the graph.
type graphService struct {
	link   graphLink
	states stateSource
	log    *logger
	now    func() time.Time
	host   func() string

	mu      sync.Mutex
	version uint64
	changed chan struct{}
	ctx     context.Context

	signature string

	state    string
	stateErr string
	asOf     time.Time

	snapshot     *protocol.GraphSnapshot
	fetchedAt    time.Time
	lastRevision uint64

	consumers    int
	fetchCancel  context.CancelFunc
	fetchRunning bool

	// tooOld is set by a 404 or a too-new schema and cleared only after the link has been seen down
	// and has come back, like the network service's unsupported flag.
	tooOld          bool
	sawDisconnected bool
	tooOldText      string

	fetchErr string

	fetchGen uint64
	loopGen  uint64
	streamUp bool
}

// newGraphService builds the service. Nothing runs until a consumer acquires.
//
// UNTESTED.
func newGraphService(link graphLink, states stateSource, log *logger, host func() string) *graphService {
	return &graphService{
		link:    link,
		states:  states,
		log:     log,
		now:     time.Now,
		host:    host,
		changed: make(chan struct{}),
		state:   protocol.GatewayNotConfigured,
	}
}

// start records the daemon's context; a fetch loop is a child of it.
func (s *graphService) start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
}

// acquire registers one consumer and returns an idempotent release. The 0 to 1 transition starts the
// one fetch loop; the 1 to 0 transition cancels it.
//
// UNTESTED.
func (s *graphService) acquire() func() {
	if s == nil {
		return func() {}
	}

	s.mu.Lock()
	s.consumers++
	start := s.consumers == 1 && !s.fetchRunning
	var loopCtx context.Context
	if start {
		s.fetchRunning = true
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

// configured reports whether this daemon has a gateway to talk to at all.
func (s *graphService) configured() bool {
	return s.link != nil && s.host != nil && s.host() != ""
}

// linkConnected reports whether the gateway stream is connected, and its error text when it is not.
func (s *graphService) linkConnected() (bool, string) {
	connection := s.states.Snapshot().Connection
	if connection == nil {
		return false, ""
	}
	if connection.State != protocol.ConnConnected {
		return false, connection.Err
	}
	return true, ""
}

// fetchable reports whether the link is usable for a fetch right now.
func (s *graphService) fetchable() bool {
	if !s.configured() {
		return false
	}
	connected, _ := s.linkConnected()

	s.mu.Lock()
	if !connected {
		s.sawDisconnected = true
	}
	// A too-old flag clears once the link has been seen down and has come back.
	if s.tooOld && connected && s.sawDisconnected {
		s.tooOld = false
		s.tooOldText = ""
		s.sawDisconnected = false
	}
	tooOld := s.tooOld
	s.mu.Unlock()

	return connected && !tooOld
}

// markTooOldLocked records that the gateway has no usable graph route and reports whether it already
// knew. Clearing sawDisconnected makes "sticky until the link has been seen down" mean down AFTER
// this point.
func (s *graphService) markTooOldLocked(text string) bool {
	already := s.tooOld
	s.tooOld = true
	s.tooOldText = text
	s.sawDisconnected = false
	return already
}

// fetchLoop reads the graph while somebody is looking. Its structure is the network loop's.
func (s *graphService) fetchLoop(ctx context.Context) {
	backoff := fetchBackoffMin
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
		fetch, err := s.link.FetchGraph(fetchCtx, since, wait)
		cancel()

		if ctx.Err() != nil {
			return
		}

		switch {
		case err == nil && fetch.Unchanged:
			s.clearFetchErr()
			immediate = true
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

		case errors.Is(err, link.ErrGraphUnsupported):
			s.mu.Lock()
			already := s.markTooOldLocked("")
			s.mu.Unlock()
			if !already {
				s.log.logf("the gateway has no graph route; graph fetching stops until the link reconnects")
			}
			s.bump()
			if !sleepContext(ctx, fetchIdlePoll) {
				return
			}

		case errors.Is(err, link.ErrGraphSchemaTooNew):
			// Never installed: newer data is unavailable, not best-effort.
			s.mu.Lock()
			s.markTooOldLocked("the gateway's graph data is newer than this daemon understands")
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

// clearFetchErr records a successful fetch, waking waiters if it ended an error.
func (s *graphService) clearFetchErr() {
	s.mu.Lock()
	cleared := s.fetchErr != ""
	s.fetchErr = ""
	s.mu.Unlock()
	if cleared {
		s.bump()
	}
}

// install keeps a fetched snapshot, replacing the previous one whole. A lower revision means the
// gateway restarted; it is installed like any other, and two gateway lifetimes are never reconciled.
func (s *graphService) install(snapshot *protocol.GraphSnapshot) {
	if snapshot == nil {
		return
	}
	copied := *snapshot

	s.mu.Lock()
	s.snapshot = &copied
	s.fetchedAt = s.now()
	s.lastRevision = copied.Revision
	s.tooOld = false
	s.tooOldText = ""
	s.fetchErr = ""
	s.fetchGen++
	s.mu.Unlock()
	s.bump()
}

// view builds the answer and the daemon's own version for it.
//
// The local identity is the paired host's name and needs no gateway: a window shows who it is even
// when the graph cannot be fetched.
//
// UNTESTED.
func (s *graphService) view() (protocol.GraphView, uint64) {
	streamUp, streamErr := s.linkConnected()
	configured := s.configured()

	view := protocol.GraphView{Schema: protocol.GraphSchema}
	if s.host != nil {
		view.Local.Host = s.host()
	}

	s.mu.Lock()
	s.refreshGatewayLocked(configured, streamUp, streamErr, s.now())
	if s.snapshot != nil {
		copied := *s.snapshot
		view.Snapshot = &copied
		view.FetchedAt = protocol.FormatTime(s.fetchedAt)
	}
	view.Gateway = protocol.NetworkGatewayView{
		State: s.state,
		Error: s.stateErr,
		AsOf:  protocol.FormatTime(s.asOf),
	}

	var revision uint64
	if view.Snapshot != nil {
		revision = view.Snapshot.Revision
	}
	signature := view.Local.Host + "|" + view.Gateway.State + "|" + view.Gateway.Error + "|" + strconvFormatUint(revision) + "|" + strconvFormatUint(uint64(boolToInt(view.Snapshot != nil)))
	if signature != s.signature {
		s.signature = signature
		s.version++
		s.broadcastLocked()
	}
	version := s.version
	s.mu.Unlock()

	return view, version
}

// boolToInt is 1 for true.
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// refreshGatewayLocked derives the gateway state from what is true now: no gateway at all; a gateway
// that cannot do this (404 or a newer schema); a stream that is not connected; a failed fetch; else
// connected. An absent snapshot alone never makes it unreachable.
func (s *graphService) refreshGatewayLocked(configured, streamUp bool, streamErr string, now time.Time) {
	s.streamUp = streamUp
	switch {
	case !configured:
		s.setGatewayLocked(protocol.GatewayNotConfigured, "", now)
	case s.tooOld:
		s.setGatewayLocked(protocol.GatewayTooOld, s.tooOldText, now)
	case !streamUp:
		s.setGatewayLocked(protocol.GatewayUnreachable, cleanNetworkError(streamErr), now)
	case s.fetchErr != "":
		s.setGatewayLocked(protocol.GatewayUnreachable, s.fetchErr, now)
	default:
		s.setGatewayLocked(protocol.GatewayConnected, "", now)
	}
}

// setGatewayLocked records the gateway state, moving its timestamp only when something changed.
func (s *graphService) setGatewayLocked(state, errText string, now time.Time) {
	if s.state == state && s.stateErr == errText {
		return
	}
	s.state = state
	s.stateErr = errText
	s.asOf = now
}

// broadcastLocked wakes every waiter by closing and replacing the changed channel.
func (s *graphService) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// bump wakes every waiter after a change made outside the mutex.
func (s *graphService) bump() {
	s.mu.Lock()
	s.broadcastLocked()
	s.mu.Unlock()
}

// answer produces the graph view a window asked for: the first wait for a caller whose fetch has not
// completed yet, the since wait for a caller that is up to date, otherwise now. A cancelled context
// answers with what exists rather than an error.
//
// UNTESTED.
func (s *graphService) answer(ctx context.Context, request wire.GraphRequest) (protocol.GraphView, uint64) {
	wait := clampMillis(request.WaitMS, wire.MaxGraphWaitMS)
	first := clampMillis(request.FirstWaitMS, wire.MaxGraphFirstMS)

	s.mu.Lock()
	startGen := s.fetchGen
	s.mu.Unlock()

	view, version := s.view()

	state := view.Gateway.State
	if first > 0 && state != protocol.GatewayNotConfigured && state != protocol.GatewayTooOld {
		return s.waitFor(ctx, first, func(_ protocol.GraphView, _ uint64) bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			floor := startGen
			if s.loopGen < floor {
				floor = s.loopGen
			}
			return s.fetchGen > floor || s.fetchErr != "" || s.tooOld || !s.streamUp
		})
	}

	if request.Since != 0 && request.Since == version && wait > 0 {
		return s.waitFor(ctx, wait, func(_ protocol.GraphView, current uint64) bool {
			return current != request.Since
		})
	}
	return view, version
}

// waitFor waits for a condition on the view, bounded, and answers with the current view either way.
func (s *graphService) waitFor(ctx context.Context, wait time.Duration, done func(protocol.GraphView, uint64) bool) (protocol.GraphView, uint64) {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	for {
		// The channel is read before the view is built, so a change between the two wakes the next
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
