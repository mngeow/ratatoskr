// Package router holds the delivery router: the actor that owns queues, agent
// state, the delivery gate and the frames written to panes.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing in this package has
// been run: only gofmt, go vet and go build have been applied. The pure helpers
// in frame.go and gate.go are frozen; this file and its neighbours are the first
// router draft.
package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// PaneWriter is everything the router may do to a pane.
type PaneWriter interface {
	// PasteSubmit writes one bracketed paste, then Enter as a separate write.
	// It returns nil only if every byte reached the PTY. If ctx ends first it
	// returns an error and must not write Enter afterwards. It does not gate,
	// retry or sanitize-and-continue.
	PasteSubmit(ctx context.Context, text string) error
}

// Store is the part of the store the router needs. It is satisfied by
// *store.Store.
type Store interface {
	InsertAgent(ctx context.Context, a store.Agent) error
	EndAgent(ctx context.Context, id protocol.AgentID, exitCode *int, at time.Time) error
	InsertMessage(ctx context.Context, m store.Message) (int64, error)
	Transition(ctx context.Context, id string, from, to protocol.MessageState, reason string, at time.Time) error
	MessageExists(ctx context.Context, runID, id string) (bool, error)
	History(ctx context.Context, runID string, limit int) ([]store.MessageRow, error)
	Runs(ctx context.Context, limit int) ([]store.RunRow, error)
	Message(ctx context.Context, id string) (store.MessageRow, error)
	MessageEvents(ctx context.Context, id string) ([]store.EventRow, error)
	InsertSend(ctx context.Context, s store.Send) error
	FinishSend(ctx context.Context, id string, from, to protocol.SendResult, reason string, at time.Time) error
	ReportSend(ctx context.Context, id string, state protocol.MessageState, reason string, at time.Time) error
	Sends(ctx context.Context, runID string, limit int) ([]store.SendRow, error)
	Send(ctx context.Context, id string) (store.SendRow, []store.SendEventRow, error)
	InsertInbound(ctx context.Context, m store.Message, origin string) (inserted bool, seq int64, err error)
	MarkReported(ctx context.Context, id string, state protocol.MessageState) error
	InboundUnreported(ctx context.Context, runID string) ([]store.InboundRow, error)
}

// Config is everything the router needs to be built. Zero fields are filled
// with the defaults noted below.
type Config struct {
	RunID string
	Store Store
	Now   func() time.Time       // default time.Now
	NewID func() (string, error) // default store.NewMessageID; tests inject

	BudgetLimit int
	MaxBody     int

	StartupTimeout time.Duration
	BusyTimeout    time.Duration
	ConfirmTimeout time.Duration
	WriteTimeout   time.Duration
	TickEvery      time.Duration

	EventBuffer int

	// StartupBanner is shown from the first snapshot under its own banner key,
	// next to any later storage banner. app composes the text, for example the
	// "previous run did not exit cleanly" warning from Recover's result.
	StartupBanner string

	// Link is the gateway connection of a connected run. Nil means local mode,
	// which is today's behaviour and the regression bar. Declared only so far:
	// nothing reads it yet. UNTESTED.
	Link Link
}

// Defaults. Every one is a guess that nothing has exercised: UNTESTED guess.
const (
	defaultBudgetLimit    = 20               // UNTESTED guess
	defaultMaxBody        = 64 << 10         // UNTESTED guess
	defaultStartupTimeout = 60 * time.Second // UNTESTED guess
	defaultBusyTimeout    = 5 * time.Minute  // UNTESTED guess
	defaultConfirmTimeout = 20 * time.Second // UNTESTED guess
	defaultWriteTimeout   = 10 * time.Second // UNTESTED guess
	defaultTickEvery      = time.Second      // UNTESTED guess
	defaultEventBuffer    = 256              // UNTESTED guess

	// inboxCapacity is the actor inbox depth. It is a FIFO so the notify
	// methods apply in the order they were called.
	inboxCapacity = 256

	historyLimit     = 200 // default History limit
	logLimit         = 500 // messages kept for the snapshot
	unconfirmedLimit = 16  // most recent unconfirmed, for the late rule

	storageBannerKey  = "storage"
	startupBannerKey  = "startup"
	hooksBannerPrefix = "hooks:"
	bannerSeparator   = " | "
)

// ErrClosed is returned by work submitted after Close, and by a caller waiting
// for a result when the router stops first.
var ErrClosed = errors.New("router: closed")

// agent is the router's per-agent state. It is owned by the actor goroutine:
// nothing outside the actor reads or writes it.
type agent struct {
	id      protocol.AgentID
	name    string
	harness string

	// gatewaySession is the session id the gateway gave this agent when the run
	// registered (connected runs only; empty in local mode and until it is set).
	gatewaySession string

	state    protocol.AgentState
	drafting bool

	w         PaneWriter
	startedAt time.Time
	lastEvent time.Time

	// boundSession is the harness session the router trusts. Empty means the
	// agent has not bound yet; until it does, only a binding SessionStart is
	// accepted (brief 2b).
	boundSession string
	ignoredHooks int

	queue    []*msg
	inflight *msg

	confirmDeadline time.Time
	earlyConfirm    bool
	unconfirmed     []*msg

	exitCode *int
	err      string
}

// msg is one message. It is owned by the actor goroutine.
type msg struct {
	id      string
	from    *agent
	to      *agent
	replyTo string
	body    string

	state  protocol.MessageState
	reason string
	at     time.Time

	// origin is the sender's network name (name@host) for a message that arrived
	// from another host; empty for every local message.
	origin string
}

// Router is the delivery router. One actor goroutine owns every field marked
// actor-owned; the atomic fields are written by the actor and read by the UI
// without ever touching it, so a slow disk cannot freeze the screen.
//
// UNTESTED.
type Router struct {
	cfg Config

	in     chan func() // buffered FIFO: notify methods only enqueue
	quit   chan struct{}
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	events chan protocol.Event

	snap    atomic.Pointer[protocol.Snapshot]
	pending atomic.Int32
	started atomic.Bool
	closed  atomic.Bool

	// Actor-owned.
	agents        map[protocol.AgentID]*agent
	byName        map[string]*agent
	order         []*agent
	log           []*msg
	used          int
	banners       map[string]string
	storageDown   bool
	resyncPending bool
	conn          *protocol.ConnectionView
	sends         []*sendRec
	peers         []string

	// reports carries delivery reports off the actor. The goroutine is started
	// lazily by the first report, so New and Start stay untouched.
	reports    chan reportJob
	reportOnce sync.Once

	startOnce sync.Once
	closeOnce sync.Once
}

// New builds a router. It fails on an empty RunID or a nil Store; everything
// else gets a default.
//
// UNTESTED.
func New(cfg Config) (*Router, error) {
	if cfg.RunID == "" {
		return nil, errors.New("router: New: RunID must not be empty")
	}
	if cfg.Store == nil {
		return nil, errors.New("router: New: Store must not be nil")
	}

	if cfg.Now == nil {
		cfg.Now = time.Now // UNTESTED guess
	}
	if cfg.NewID == nil {
		cfg.NewID = store.NewMessageID // UNTESTED guess
	}
	if cfg.BudgetLimit == 0 {
		cfg.BudgetLimit = defaultBudgetLimit
	}
	if cfg.MaxBody == 0 {
		cfg.MaxBody = defaultMaxBody
	}
	if cfg.StartupTimeout == 0 {
		cfg.StartupTimeout = defaultStartupTimeout
	}
	if cfg.BusyTimeout == 0 {
		cfg.BusyTimeout = defaultBusyTimeout
	}
	if cfg.ConfirmTimeout == 0 {
		cfg.ConfirmTimeout = defaultConfirmTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.TickEvery == 0 {
		cfg.TickEvery = defaultTickEvery
	}
	if cfg.EventBuffer == 0 {
		cfg.EventBuffer = defaultEventBuffer
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &Router{
		cfg:     cfg,
		in:      make(chan func(), inboxCapacity),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		events:  make(chan protocol.Event, cfg.EventBuffer),
		agents:  make(map[protocol.AgentID]*agent),
		byName:  make(map[string]*agent),
		banners: make(map[string]string),
	}
	if text := stripControl(cfg.StartupBanner); text != "" {
		r.banners[startupBannerKey] = text
	}
	// Publish an empty snapshot so a caller that reads before Start sees a run
	// with no agents, not a nil snapshot.
	r.refresh()
	return r, nil
}

// Start runs the actor. Calling it again does nothing: a wiring mistake must not
// start a second actor on the same state.
//
// UNTESTED.
func (r *Router) Start() {
	r.startOnce.Do(func() {
		r.started.Store(true)
		go r.loop()
	})
}

// Close stops the actor, waits for it, then closes the event channel. It is
// idempotent. Work submitted afterwards gets ErrClosed, and the Pane* and
// HookEvent notifications are dropped without blocking.
//
// Quit order for app: Close the router first, then store.FinishRun (which turns
// every unfinished message into interrupted), and only then close the panes. A
// PaneExited that arrives after Close is a no-op, so the exits caused by closing
// the panes cannot turn in-flight messages into unconfirmed.
//
// The event channel is closed last, and only here: emit is called only by the
// actor, which has already stopped by that point.
//
// UNTESTED.
func (r *Router) Close() {
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		r.cancel()
		close(r.quit)
		if r.started.Load() {
			<-r.done
		}
		close(r.events)
	})
}

// Events is the stream of router updates for the UI. It closes when the router
// closes.
func (r *Router) Events() <-chan protocol.Event { return r.events }

// loop is the actor: it runs submitted work and the tick, then republishes the
// snapshot after every step.
func (r *Router) loop() {
	defer close(r.done)

	ticker := time.NewTicker(r.cfg.TickEvery)
	defer ticker.Stop()

	for {
		// Close wins over a backlog, so shutdown does not queue behind it.
		select {
		case <-r.quit:
			return
		default:
		}

		select {
		case fn := <-r.in:
			fn()
		case <-ticker.C:
			// Use the injected clock, not the ticker's own time, so deadlines
			// and the tick agree about what "now" is.
			r.tick(r.now())
		case <-r.quit:
			return
		}
		r.refresh()
	}
}

// submit queues fn for the actor. It never blocks after Close: a closed router
// answers ErrClosed rather than accepting work that would never run.
func (r *Router) submit(ctx context.Context, fn func()) error {
	select {
	case <-r.quit:
		return ErrClosed
	default:
	}

	select {
	case r.in <- fn:
		return nil
	case <-r.quit:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// call submits fn and waits for the answer, or for the context or the router to
// end first.
func (r *Router) call(ctx context.Context, fn func() error) error {
	done := make(chan error, 1)
	if err := r.submit(ctx, func() { done <- fn() }); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return ErrClosed
	}
}

// enqueue is the fire-and-forget variant: it applies in call order and, after
// Close, is dropped silently. A full inbox blocks the caller, which is the
// accepted back-pressure.
func (r *Router) enqueue(fn func()) {
	_ = r.submit(context.Background(), fn)
}

func (r *Router) now() time.Time { return r.cfg.Now() }

// emit publishes one event without blocking the actor. When the buffer is full
// the oldest event is dropped, and the next event that gets through carries
// Resync, so the UI takes a fresh snapshot instead of trusting a gap.
//
// Nothing is emitted after Close: only the actor writes to the channel, and this
// guard covers the step that is still running when shutdown begins.
func (r *Router) emit(event protocol.Event) {
	if r.closed.Load() {
		return
	}

	if r.resyncPending {
		event.Resync = true
	}

	select {
	case r.events <- event:
		r.resyncPending = false
		return
	default:
	}

	// Full: drop the oldest and send this one marked Resync, so the UI takes a
	// fresh snapshot right after the gap instead of at some later event.
	select {
	case <-r.events:
	default:
	}
	event.Resync = true
	select {
	case r.events <- event:
		r.resyncPending = false
	default:
		r.resyncPending = true // still no room: the next event carries it
	}
}

// setBanner records a banner under a key and emits the joined set when it
// changed. An empty text removes the key.
func (r *Router) setBanner(key, text string) {
	current, exists := r.banners[key]
	if text == "" {
		if !exists {
			return
		}
		delete(r.banners, key)
	} else {
		if exists && current == text {
			return
		}
		r.banners[key] = text
	}
	r.emit(protocol.Event{Banner: r.joinedBanners(), Resync: true})
}

// setStorageDown latches the storage failure and shows it as a banner. It stays
// latched: every later write fails too.
func (r *Router) setStorageDown(err error) {
	r.storageDown = true
	r.setBanner(storageBannerKey, stripControl(err.Error()))
}

// move is the only way a message changes state. The store commits first; memory
// and the UI follow only when it did.
func (r *Router) move(m *msg, to protocol.MessageState, reason string) error {
	from := m.state
	if err := r.cfg.Store.Transition(r.ctx, m.id, from, to, reason, r.now()); err != nil {
		if errors.Is(err, store.ErrUnavailable) {
			r.setStorageDown(err)
		}
		return err
	}
	m.state = to
	m.reason = reason
	r.emit(protocol.Event{Message: r.messageViewPtr(m)})
	if m.origin != "" {
		r.queueReport(m)
	}
	return nil
}

// appendLog keeps the newest logLimit messages.
func (r *Router) appendLog(m *msg) {
	r.log = append(r.log, m)
	if len(r.log) > logLimit {
		r.log = append([]*msg(nil), r.log[len(r.log)-logLimit:]...)
	}
}
