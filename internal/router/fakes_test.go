package router

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// These fakes are written but UNRUN: GR-3 forbids running tests. They exist so
// the router's ordering and blocking behaviour are pinned for whoever may run
// them later.

// fakeStore records an ordered call log, keeps the rows a test needs, and can
// block or fail on demand.
type fakeStore struct {
	mu sync.Mutex

	calls   []string
	agents  map[protocol.AgentID]store.Agent
	rows    map[string]store.MessageRow
	history []store.MessageRow

	// insertGate blocks InsertMessage until it is closed. A nil gate never
	// blocks.
	insertGate chan struct{}
	// unavailable makes every write fail as store.ErrUnavailable.
	unavailable bool
	// historyErr is returned by History when set.
	historyErr error

	historyRunID string
	historyLimit int

	// read-path seeds: runs and per-message events. Message reads reuse rows.
	runs      []store.RunRow
	events    map[string][]store.EventRow
	runsLimit int

	// connected-mode seeds: outbound attempts, their events, and inbound origins.
	sends      map[string]store.SendRow
	sendEvents map[string][]store.SendEventRow
	// sendReasons keeps the reason each attempt finished with: store.SendRow has no
	// such column, and a test may want to assert what was written.
	sendReasons map[string]string
	inbound     map[string]string
	// inboundRows is the richer inbound model InboundUnreported reads: state,
	// reason and time, with reportedState holding what has been reported.
	inboundRows   map[string]store.InboundRow
	reportedState map[string]protocol.MessageState
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		agents: make(map[protocol.AgentID]store.Agent),
		rows:   make(map[string]store.MessageRow),
	}
}

func (s *fakeStore) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *fakeStore) callLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fakeStore) countCall(call string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, seen := range s.calls {
		if seen == call {
			count++
		}
	}
	return count
}

func (s *fakeStore) setUnavailable(unavailable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailable = unavailable
}

func (s *fakeStore) blockInserts() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate := make(chan struct{})
	s.insertGate = gate
	return gate
}

func (s *fakeStore) InsertAgent(_ context.Context, a store.Agent) error {
	s.record("InsertAgent:" + string(a.ID))
	if s.failing() {
		return store.ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[a.ID] = a
	return nil
}

func (s *fakeStore) EndAgent(_ context.Context, id protocol.AgentID, exitCode *int, _ time.Time) error {
	s.record("EndAgent:" + string(id))
	if s.failing() {
		return store.ErrUnavailable
	}
	return nil
}

func (s *fakeStore) InsertMessage(_ context.Context, m store.Message) (int64, error) {
	s.record("InsertMessage:" + m.ID)

	s.mu.Lock()
	gate := s.insertGate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}

	if s.failing() {
		return 0, store.ErrUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[m.ID] = store.MessageRow{
		Seq:       int64(len(s.rows) + 1),
		ID:        m.ID,
		RunID:     m.RunID,
		FromID:    m.From,
		ToID:      m.To,
		Body:      m.Body,
		CreatedAt: m.CreatedAt,
	}
	return int64(len(s.rows)), nil
}

func (s *fakeStore) Transition(_ context.Context, id string, from, to protocol.MessageState, reason string, _ time.Time) error {
	s.record(fmt.Sprintf("Transition:%s:%s>%s", id, from, to))
	if s.failing() {
		return store.ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok {
		row.State = to
		row.Reason = reason
		s.rows[id] = row
	}
	return nil
}

func (s *fakeStore) MessageExists(_ context.Context, runID, id string) (bool, error) {
	s.record("MessageExists:" + id)
	if s.failing() {
		return false, store.ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	return ok && row.RunID == runID, nil
}

func (s *fakeStore) History(_ context.Context, runID string, limit int) ([]store.MessageRow, error) {
	s.record(fmt.Sprintf("History:%s:%d", runID, limit))
	s.mu.Lock()
	s.historyRunID, s.historyLimit = runID, limit
	err := s.historyErr
	rows := append([]store.MessageRow(nil), s.history...)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows, nil
}

func (s *fakeStore) failing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unavailable
}

// fakePane records what the router asked of it. Brief 2b uses it for real; 2a
// only needs it to satisfy PaneWriter.
type fakePane struct {
	mu      sync.Mutex
	texts   []string
	pending chan struct{} // when non-nil, PasteSubmit blocks until closed
	err     error
}

func (p *fakePane) PasteSubmit(ctx context.Context, text string) error {
	p.mu.Lock()
	gate := p.pending
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts = append(p.texts, text)
	return p.err
}

func (p *fakePane) submitted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...)
}

// fakeClock is a hand-advanced clock for timer tests.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// counterIDs hands out deterministic IDs so a test can predict them.
func counterIDs(prefix string) func() (string, error) {
	var (
		mu  sync.Mutex
		seq int
	)
	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		seq++
		return fmt.Sprintf("%s%d", prefix, seq), nil
	}
}

// newTestRouter builds a router with fakes and a hand-advanced clock, without
// starting the actor. Tests that need the actor call Start themselves.
func newTestRouter(store2 Store) (*Router, *fakeClock) {
	clock := newFakeClock()
	router, err := New(Config{
		RunID: "run-1",
		Store: store2,
		Now:   clock.now,
		NewID: counterIDs("m"),
	})
	if err != nil {
		panic(err)
	}
	return router, clock
}

// fakeWriteError is a pane error that reports whether any bytes reached the PTY.
// The router reads that structurally, never by importing the pane package.
type fakeWriteError struct {
	written bool
	text    string
}

func (e *fakeWriteError) Error() string { return e.text }

func (e *fakeWriteError) BytesWritten() bool { return e.written }

// newBareRouter builds a router with fakes and no agents, without starting the
// actor. Tests register the agents they need.
func newBareRouter(t *testing.T) (*Router, *fakeStore, *fakeClock) {
	t.Helper()
	fake := newFakeStore()
	clock := newFakeClock()
	r, err := New(Config{
		RunID: "run-1",
		Store: fake,
		Now:   clock.now,
		NewID: counterIDs("m"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, fake, clock
}

// newDeliverRouter builds a two-agent router whose recipient is idle with a live
// pane, ready for pump tests.
func newDeliverRouter(t *testing.T, pane PaneWriter) (*Router, *fakeStore, *fakeClock, *agent) {
	t.Helper()
	r, fake, clock := newBareRouter(t)

	for _, s := range []protocol.LaunchSpec{spec("a", "alpha"), spec("b", "beta")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}

	beta := r.agents["b"]
	beta.w = pane
	beta.state = protocol.AgentIdle
	beta.startedAt = clock.now()
	return r, fake, clock, beta
}

// boundSpec is a launch spec whose session is already bound, as Claude does at
// registration.
func boundSpec(id protocol.AgentID, name, session string) protocol.LaunchSpec {
	s := spec(id, name)
	s.SessionID = session
	return s
}

// queueMessage adds one message to an agent's queue and to the log.
func queueMessage(t *testing.T, r *Router, to *agent, state protocol.MessageState) *msg {
	t.Helper()
	from, ok := r.byName["alpha"]
	if !ok {
		t.Fatalf("alpha is not registered")
	}
	m := &msg{
		id:    fmt.Sprintf("m%d", len(r.log)+1),
		from:  from,
		to:    to,
		body:  "hello",
		state: state,
		at:    r.now(),
	}
	r.appendLog(m)
	to.queue = append(to.queue, m)
	return m
}

// The read methods: runs, one message, and its events. They are seeded directly
// and, like the real store's read handle, they ignore the unavailable switch - a
// latched write path must not stop a reader.
func (s *fakeStore) Runs(_ context.Context, limit int) ([]store.RunRow, error) {
	s.record("Runs")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runsLimit = limit
	return append([]store.RunRow(nil), s.runs...), nil
}

func (s *fakeStore) Message(_ context.Context, id string) (store.MessageRow, error) {
	s.record("Message:" + id)
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return store.MessageRow{}, fmt.Errorf("store: message %s: %w", id, store.ErrNotFound)
	}
	return row, nil
}

func (s *fakeStore) MessageEvents(_ context.Context, id string) ([]store.EventRow, error) {
	s.record("MessageEvents:" + id)
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.EventRow(nil), s.events[id]...), nil
}

// The connected-mode store methods: outbound attempts, their events, and the
// inbound bookkeeping. Writes honour the unavailable switch; reads ignore it, like
// the other read methods here.
var _ Store = (*fakeStore)(nil)

func (s *fakeStore) InsertSend(_ context.Context, send store.Send) error {
	s.record("InsertSend:" + send.ID)
	if s.failing() {
		return store.ErrUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sends == nil {
		s.sends = make(map[string]store.SendRow)
	}
	s.sends[send.ID] = store.SendRow{
		Seq:       int64(len(s.sends) + 1),
		ID:        send.ID,
		RunID:     send.RunID,
		FromID:    send.From,
		To:        send.To,
		ReplyTo:   send.ReplyTo,
		Body:      send.Body,
		Result:    protocol.SendSending,
		CreatedAt: send.CreatedAt,
		UpdatedAt: send.CreatedAt,
	}
	return nil
}

func (s *fakeStore) FinishSend(_ context.Context, id string, from, to protocol.SendResult, reason string, at time.Time) error {
	s.record("FinishSend:" + id + ":" + string(from) + ">" + string(to))
	if s.failing() {
		return store.ErrUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.sends[id]
	if !ok {
		return store.ErrNotFound
	}
	if row.Result != from {
		return store.ErrStateChanged
	}
	row.Result = to
	row.UpdatedAt = at
	s.sends[id] = row
	if s.sendReasons == nil {
		s.sendReasons = make(map[string]string)
	}
	s.sendReasons[id] = reason
	return nil
}

func (s *fakeStore) ReportSend(_ context.Context, id string, state protocol.MessageState, reason string, _ time.Time) error {
	s.record("ReportSend:" + id + ":" + string(state))
	if s.failing() {
		return store.ErrUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.sends[id]
	if !ok {
		return store.ErrNotFound
	}
	row.Report = state
	row.ReportReason = reason
	s.sends[id] = row
	return nil
}

func (s *fakeStore) Sends(_ context.Context, runID string, limit int) ([]store.SendRow, error) {
	s.record("Sends:" + runID)

	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]store.SendRow, 0, len(s.sends))
	for _, row := range s.sends {
		if runID == "" || row.RunID == runID {
			rows = append(rows, row)
		}
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows, nil
}

func (s *fakeStore) Send(_ context.Context, id string) (store.SendRow, []store.SendEventRow, error) {
	s.record("Send:" + id)

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.sends[id]
	if !ok {
		return store.SendRow{}, nil, store.ErrNotFound
	}
	return row, append([]store.SendEventRow(nil), s.sendEvents[id]...), nil
}

func (s *fakeStore) InsertInbound(_ context.Context, m store.Message, origin string) (bool, int64, error) {
	s.record("InsertInbound:" + m.ID)
	if s.failing() {
		return false, 0, store.ErrUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inbound == nil {
		s.inbound = make(map[string]string)
	}
	if _, exists := s.inbound[m.ID]; exists {
		return false, 0, nil
	}
	s.inbound[m.ID] = origin
	if s.inboundRows == nil {
		s.inboundRows = make(map[string]store.InboundRow)
	}
	s.inboundRows[m.ID] = store.InboundRow{
		ID:    m.ID,
		State: protocol.MessageQueued,
		At:    m.CreatedAt,
	}
	return true, int64(len(s.inbound)), nil
}

func (s *fakeStore) MarkReported(_ context.Context, id string, state protocol.MessageState) error {
	s.record("MarkReported:" + id + ":" + string(state))
	if s.failing() {
		return store.ErrUnavailable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.inbound[id]; !exists {
		return store.ErrNotFound
	}
	if s.reportedState == nil {
		s.reportedState = make(map[string]protocol.MessageState)
	}
	s.reportedState[id] = state
	return nil
}

func (s *fakeStore) InboundUnreported(_ context.Context, runID string) ([]store.InboundRow, error) {
	s.record("InboundUnreported:" + runID)

	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]store.InboundRow, 0, len(s.inbound))
	for id := range s.inbound {
		row := s.inboundRows[id]
		if row.ID == "" {
			row.ID = id
		}
		if s.reportedState[id] == row.State {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}
