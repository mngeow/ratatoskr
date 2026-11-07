package daemon

// Untested: under GR-3 nothing here has been run. Every collaborator is a fake, so no gateway, no
// socket and no session is involved.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// fakeLocal is the session manager as the service sees it.
type fakeLocal struct {
	mu       sync.Mutex
	sessions []localSession
}

// LocalSessions returns the configured sessions.
func (f *fakeLocal) LocalSessions() []localSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]localSession(nil), f.sessions...)
}

// set replaces the session list.
func (f *fakeLocal) set(sessions ...localSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = sessions
}

// fakeStates is the router snapshot.
type fakeStates struct {
	mu         sync.Mutex
	agents     []protocol.AgentView
	connection *protocol.ConnectionView
}

// Snapshot returns the configured state.
func (f *fakeStates) Snapshot() protocol.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return protocol.Snapshot{
		Agents:     append([]protocol.AgentView(nil), f.agents...),
		Connection: f.connection,
	}
}

// set replaces the state.
func (f *fakeStates) set(agents []protocol.AgentView, connection *protocol.ConnectionView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = agents
	f.connection = connection
}

// fakeInputs is the gate.
type fakeInputs struct {
	mu      sync.Mutex
	holders map[protocol.AgentID]string
}

// Holder reports one session's holder and whether it is known.
func (f *fakeInputs) Holder(agent protocol.AgentID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	holder, ok := f.holders[agent]
	return holder, ok
}

// fakeNetLink is the gateway client.
type fakeNetLink struct {
	mu        sync.Mutex
	reports   []protocol.ReportRequest
	reportErr error
	block     chan struct{}
	fetch     []link.NetworkFetch
	fetchErr  error
	fetches   int
	calls     []fetchCall
}

// fetchCall is the arguments of one FetchNetwork call.
type fetchCall struct {
	since uint64
	wait  time.Duration
}

// ReportSelf records one report.
func (f *fakeNetLink) ReportSelf(ctx context.Context, request protocol.ReportRequest) error {
	f.mu.Lock()
	f.reports = append(f.reports, request)
	block := f.block
	err := f.reportErr
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// FetchNetwork answers from the configured queue.
func (f *fakeNetLink) FetchNetwork(ctx context.Context, since uint64, wait time.Duration) (link.NetworkFetch, error) {
	f.mu.Lock()
	f.fetches++
	f.calls = append(f.calls, fetchCall{since: since, wait: wait})
	queue := f.fetch
	var next link.NetworkFetch
	if len(queue) > 0 {
		next = queue[0]
		f.fetch = queue[1:]
	}
	err := f.fetchErr
	f.mu.Unlock()

	if err != nil {
		return link.NetworkFetch{}, err
	}
	return next, nil
}

// countReports is how many reports were sent.
func (f *fakeNetLink) countReports() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

// lastReport is the most recent report.
func (f *fakeNetLink) lastReport(t *testing.T) protocol.ReportRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reports) == 0 {
		t.Fatal("no report was sent")
	}
	return f.reports[len(f.reports)-1]
}

// netsvcFixture is a service with fakes and a controllable clock.
type netsvcFixture struct {
	service *networkService
	local   *fakeLocal
	states  *fakeStates
	inputs  *fakeInputs
	link    *fakeNetLink
	now     time.Time
}

// newNetsvcFixture builds a connected service with one running session.
func newNetsvcFixture(t *testing.T) *netsvcFixture {
	t.Helper()

	fixture := &netsvcFixture{
		local:  &fakeLocal{},
		states: &fakeStates{},
		inputs: &fakeInputs{holders: map[protocol.AgentID]string{}},
		link:   &fakeNetLink{},
		now:    time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC),
	}
	fixture.states.set(
		[]protocol.AgentView{{ID: "a1", DisplayName: "claude", State: protocol.AgentIdle}},
		&protocol.ConnectionView{State: protocol.ConnConnected},
	)
	fixture.local.set(localSession{
		Name: "claude", Agent: "a1", Gateway: "gw-1", HarnessVersion: "0.9.1",
		StartedAt: fixture.now.Add(-time.Hour),
	})
	fixture.inputs.holders["a1"] = "w1"

	fixture.service = newNetworkService(
		fixture.link, fixture.local, fixture.states, fixture.inputs,
		&logger{out: io.Discard}, func() string { return "laptop" }, "build-1", fixture.now,
	)
	fixture.service.now = func() time.Time { return fixture.now }
	return fixture
}

// TestStateClockStampsOnlyChanges: a state stamp moves only when the state does.
func TestStateClockStampsOnlyChanges(t *testing.T) {
	clock := newStateClock()
	first := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	later := first.Add(time.Minute)

	clock.observe([]protocol.AgentView{{ID: "a1", State: protocol.AgentIdle}}, first)
	state, at := clock.stamp("a1", protocol.AgentIdle, later)
	if state != protocol.AgentIdle || !at.Equal(first) {
		t.Fatalf("stamp = %v %v, want the first observation", state, at)
	}

	clock.observe([]protocol.AgentView{{ID: "a1", State: protocol.AgentBusy}}, later)
	state, at = clock.stamp("a1", protocol.AgentBusy, later.Add(time.Minute))
	if state != protocol.AgentBusy || !at.Equal(later) {
		t.Fatalf("stamp = %v %v, want the change time", state, at)
	}

	// A new agent is stamped at observation.
	clock.observe([]protocol.AgentView{{ID: "a2", State: protocol.AgentIdle}}, later)
	if _, at := clock.stamp("a2", protocol.AgentIdle, later); !at.Equal(later) {
		t.Fatalf("a new agent's stamp = %v", at)
	}

	// A forgotten agent is stamped fresh.
	clock.forget("a1")
	if _, at := clock.stamp("a1", protocol.AgentBusy, later); !at.Equal(later) {
		t.Fatalf("a forgotten agent kept its stamp: %v", at)
	}
}

// TestLocalBuildCarriesOnlyAllowedFields: the report says what the contract allows and nothing else.
func TestLocalBuildCarriesOnlyAllowedFields(t *testing.T) {
	fixture := newNetsvcFixture(t)

	local := fixture.service.local()
	if local.Host != "laptop" {
		t.Fatalf("host = %q", local.Host)
	}
	if len(local.Sessions) != 1 {
		t.Fatalf("sessions = %+v", local.Sessions)
	}

	session := local.Sessions[0]
	if session.SessionID != "gw-1" || session.LocalID != "a1" || session.Name != "claude" {
		t.Fatalf("session = %+v", session)
	}
	if session.Harness != "claude" || session.HarnessVersion != "0.9.1" {
		t.Fatalf("harness = %+v", session)
	}
	if session.State != protocol.SessionStateIdle {
		t.Fatalf("state = %q", session.State)
	}
	if session.Input != protocol.InputHeld {
		t.Fatalf("input = %q, want held", session.Input)
	}
	if session.StartedAt == "" || session.StateAt == "" {
		t.Fatalf("times = %q %q", session.StartedAt, session.StateAt)
	}

	// The encoded form carries no field the contract forbids.
	encoded := encodeJSON(t, local)
	for _, forbidden := range []string{"prompt", "cwd", "env", "credential", "token", "draft", "window", "screen", "path"} {
		if containsFold(encoded, forbidden) {
			t.Fatalf("the local build carries %q: %s", forbidden, encoded)
		}
	}
}

// TestLocalStateMapping: every router state maps into the contract's set, and unknown stays unknown.
func TestLocalStateMapping(t *testing.T) {
	cases := []struct {
		state protocol.AgentState
		want  string
	}{
		{state: protocol.AgentStarting, want: protocol.SessionStateStarting},
		{state: protocol.AgentIdle, want: protocol.SessionStateIdle},
		{state: protocol.AgentBusy, want: protocol.SessionStateBusy},
		{state: protocol.AgentNeedsYou, want: protocol.SessionStateNeedsYou},
		{state: protocol.AgentExited, want: protocol.SessionStateExited},
		{state: protocol.AgentUnknown, want: protocol.SessionStateUnknown},
		{state: protocol.AgentState("nonsense"), want: protocol.SessionStateUnknown},
	}

	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			fixture := newNetsvcFixture(t)
			fixture.states.set(
				[]protocol.AgentView{{ID: "a1", State: tc.state}},
				&protocol.ConnectionView{State: protocol.ConnConnected},
			)
			local := fixture.service.local()
			if got := local.Sessions[0].State; got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}

	// A session the router does not know reports unknown, never idle.
	fixture := newNetsvcFixture(t)
	fixture.states.set(nil, &protocol.ConnectionView{State: protocol.ConnConnected})
	if got := fixture.service.local().Sessions[0].State; got != protocol.SessionStateUnknown {
		t.Fatalf("an unknown session reported %q", got)
	}
}

// TestLocalInputMapping: held, free and unknown, and never which window.
func TestLocalInputMapping(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		fixture := newNetsvcFixture(t)
		if got := fixture.service.local().Sessions[0].Input; got != protocol.InputHeld {
			t.Fatalf("input = %q", got)
		}
		// The window id never appears in the encoded report.
		if encoded := encodeJSON(t, fixture.service.local()); containsFold(encoded, "w1") {
			t.Fatalf("the window id leaked: %s", encoded)
		}
	})

	t.Run("free", func(t *testing.T) {
		fixture := newNetsvcFixture(t)
		fixture.inputs.holders["a1"] = ""
		if got := fixture.service.local().Sessions[0].Input; got != protocol.InputFree {
			t.Fatalf("input = %q", got)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		fixture := newNetsvcFixture(t)
		fixture.inputs.holders = map[protocol.AgentID]string{}
		if got := fixture.service.local().Sessions[0].Input; got != protocol.InputUnknown {
			t.Fatalf("input = %q", got)
		}
	})
}

// TestReporterSendsOnTheLinkComingUp: connecting reports at once.
func TestReporterSendsOnTheLinkComingUp(t *testing.T) {
	fixture := newNetsvcFixture(t)

	fixture.service.maybeReport(context.Background())
	if fixture.link.countReports() != 1 {
		t.Fatalf("reports = %d, want 1 on the first connected check", fixture.link.countReports())
	}
	if report := fixture.link.lastReport(t); report.Seq != 1 || report.Schema != protocol.NetworkSchema {
		t.Fatalf("report = %+v", report)
	}
}

// TestReporterNeverSendsWhileTheStreamIsDown: no stream, no report.
func TestReporterNeverSendsWhileTheStreamIsDown(t *testing.T) {
	fixture := newNetsvcFixture(t)
	fixture.states.set(nil, &protocol.ConnectionView{State: protocol.ConnReconnecting, Err: "connection reset"})

	fixture.service.maybeReport(context.Background())
	if fixture.link.countReports() != 0 {
		t.Fatal("a report was sent with the stream down")
	}
	view, _ := fixture.service.view()
	if view.Gateway.State != protocol.GatewayUnreachable {
		t.Fatalf("gateway state = %q, want unreachable", view.Gateway.State)
	}
	if view.Gateway.Error != "connection reset" {
		t.Fatalf("gateway error = %q", view.Gateway.Error)
	}
}

// TestReporterCoalescesAndReportsEveryTenSeconds.
func TestReporterCoalescesAndReportsEveryTenSeconds(t *testing.T) {
	fixture := newNetsvcFixture(t)

	fixture.service.maybeReport(context.Background())
	if got := fixture.link.countReports(); got != 1 {
		t.Fatalf("reports = %d", got)
	}

	// Immediately again: coalesced.
	fixture.service.kick()
	fixture.service.maybeReport(context.Background())
	if got := fixture.link.countReports(); got != 1 {
		t.Fatalf("a coalesced kick sent a second report: %d", got)
	}

	// A second later, a kick is honoured.
	fixture.now = fixture.now.Add(time.Second)
	fixture.service.maybeReport(context.Background())
	if got := fixture.link.countReports(); got != 2 {
		t.Fatalf("a kick after the coalesce window sent %d reports, want 2", got)
	}

	// Ten seconds later the periodic report goes out even with no kick.
	fixture.now = fixture.now.Add(reportEvery)
	fixture.service.maybeReport(context.Background())
	if got := fixture.link.countReports(); got != 3 {
		t.Fatalf("the periodic report did not go out: %d", got)
	}

	// The sequence rises by one per attempt.
	if report := fixture.link.lastReport(t); report.Seq != 3 {
		t.Fatalf("seq = %d, want 3", report.Seq)
	}
}

// TestReporterStopsOn404UntilReconnect: unsupported is sticky until the link has been down and back.
func TestReporterStopsOn404UntilReconnect(t *testing.T) {
	fixture := newNetsvcFixture(t)
	fixture.link.reportErr = link.ErrNetworkUnsupported

	fixture.service.maybeReport(context.Background())
	if got := fixture.link.countReports(); got != 1 {
		t.Fatalf("reports = %d", got)
	}

	// Subsequent ticks send nothing, even with a kick.
	fixture.link.reportErr = nil
	fixture.now = fixture.now.Add(2 * reportEvery)
	fixture.service.kick()
	fixture.service.maybeReport(context.Background())
	if got := fixture.link.countReports(); got != 1 {
		t.Fatalf("a report was sent while unsupported: %d", got)
	}

	view, _ := fixture.service.view()
	if view.Gateway.State != protocol.GatewayUnsupported {
		t.Fatalf("gateway state = %q, want unsupported", view.Gateway.State)
	}

	// The link goes down and comes back: reporting resumes.
	fixture.states.set(nil, &protocol.ConnectionView{State: protocol.ConnConnecting})
	fixture.service.maybeReport(context.Background())
	fixture.states.set(nil, &protocol.ConnectionView{State: protocol.ConnConnected})
	fixture.now = fixture.now.Add(time.Second)
	fixture.service.maybeReport(context.Background())

	if got := fixture.link.countReports(); got != 2 {
		t.Fatalf("reporting did not resume after a reconnect: %d", got)
	}
}

// TestReporterRefusalIsLoggedOnceAndKeepsGoing: a 400 does not stop the ticker.
func TestReporterRefusalIsLoggedOnceAndKeepsGoing(t *testing.T) {
	fixture := newNetsvcFixture(t)
	fixture.link.reportErr = &link.RefusedError{Status: 400, Code: "bad_request", Message: "the report is too large"}

	fixture.service.maybeReport(context.Background())
	fixture.now = fixture.now.Add(2 * time.Second)
	fixture.service.maybeReport(context.Background())
	if got := fixture.link.countReports(); got != 2 {
		t.Fatalf("a refusal stopped the reporter: %d", got)
	}

	view, _ := fixture.service.view()
	if view.Gateway.State != protocol.GatewayUnreachable {
		t.Fatalf("gateway state = %q, want unreachable with the refusal text", view.Gateway.State)
	}
}

// TestReporterDoesNotBlockTheKickPath: a slow link does not hold the caller.
func TestReporterDoesNotBlockTheKickPath(t *testing.T) {
	fixture := newNetsvcFixture(t)
	blocked := make(chan struct{})
	fixture.link.block = blocked

	done := make(chan struct{})
	go func() {
		fixture.service.maybeReport(context.Background())
		close(done)
	}()

	// While the send is blocked in another goroutine, a kick returns immediately.
	finished := make(chan struct{})
	go func() {
		fixture.service.kick()
		fixture.service.kick()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("kick blocked behind a slow report")
	}

	close(blocked)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the report never finished")
	}
}

// TestKickReturnsWhenTheChannelIsFull: a full one-slot channel is not a block.
func TestKickReturnsWhenTheChannelIsFull(t *testing.T) {
	fixture := newNetsvcFixture(t)
	fixture.service.kickCh <- struct{}{}

	done := make(chan struct{})
	go func() {
		fixture.service.kick()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("kick blocked on a full channel")
	}
}

// TestFetchLoopStartsAndStopsWithConsumers: the loop exists only while somebody is looking.
func TestFetchLoopStartsAndStopsWithConsumers(t *testing.T) {
	fixture := newNetsvcFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.service.start(ctx)

	fixture.link.fetch = []link.NetworkFetch{{Snapshot: &protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: 4}}}

	release := fixture.service.acquire()
	waitFor(t, "the first fetch", func() bool {
		fixture.service.mu.Lock()
		defer fixture.service.mu.Unlock()
		return fixture.service.snapshot != nil
	})

	fixture.service.mu.Lock()
	if fixture.service.consumers != 1 || !fixture.service.fetchRunning {
		fixture.service.mu.Unlock()
		t.Fatalf("consumers = %d, running = %t", fixture.service.consumers, fixture.service.fetchRunning)
	}
	fixture.service.mu.Unlock()

	release()
	// Twice: the release is idempotent.
	release()

	fixture.service.mu.Lock()
	consumers, running := fixture.service.consumers, fixture.service.fetchRunning
	fixture.service.mu.Unlock()
	if consumers != 0 || running {
		t.Fatalf("after release: consumers = %d, running = %t", consumers, running)
	}
}

// TestFetchInstallsAndKeepsAcrossFailures: an error keeps the last snapshot.
func TestFetchInstallsAndKeepsAcrossFailures(t *testing.T) {
	fixture := newNetsvcFixture(t)
	view, _ := fixture.service.view()
	if view.Snapshot != nil {
		t.Fatal("a snapshot exists before any fetch")
	}

	fixture.service.install(&protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: 9})
	view, _ = fixture.service.view()
	if view.Snapshot == nil || view.Snapshot.Revision != 9 || view.FetchedAt == "" {
		t.Fatalf("view = %+v", view)
	}

	// A failed fetch leaves the snapshot in place and marks the gateway unreachable.
	fixture.service.mu.Lock()
	fixture.service.fetchErr = "connection refused"
	fixture.service.mu.Unlock()

	view, _ = fixture.service.view()
	if view.Snapshot == nil || view.Snapshot.Revision != 9 {
		t.Fatalf("a failure dropped the snapshot: %+v", view.Snapshot)
	}
	if view.Gateway.State != protocol.GatewayUnreachable {
		t.Fatalf("gateway state = %q", view.Gateway.State)
	}
}

// TestVersionBumps: content and state bump it; a re-stamp does not.
func TestVersionBumps(t *testing.T) {
	fixture := newNetsvcFixture(t)

	_, first := fixture.service.view()

	// The same content again: no bump, even a minute later.
	fixture.now = fixture.now.Add(time.Minute)
	if _, again := fixture.service.view(); again != first {
		t.Fatalf("the version moved without a change: %d -> %d", first, again)
	}

	// A state change bumps it.
	fixture.states.set(
		[]protocol.AgentView{{ID: "a1", State: protocol.AgentBusy}},
		&protocol.ConnectionView{State: protocol.ConnConnected},
	)
	_, second := fixture.service.view()
	if second <= first {
		t.Fatalf("the version did not move on a state change: %d -> %d", first, second)
	}

	// A snapshot install bumps it.
	fixture.service.install(&protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: 3})
	_, third := fixture.service.view()
	if third <= second {
		t.Fatalf("the version did not move on an install: %d -> %d", second, third)
	}

	// A gateway state change bumps it.
	fixture.states.set(
		[]protocol.AgentView{{ID: "a1", State: protocol.AgentBusy}},
		&protocol.ConnectionView{State: protocol.ConnReconnecting, Err: "reset"},
	)
	fixture.service.mu.Lock()
	fixture.service.fetchErr = "reset"
	fixture.service.mu.Unlock()
	_, fourth := fixture.service.view()
	if fourth <= third {
		t.Fatalf("the version did not move on a gateway state change: %d -> %d", third, fourth)
	}
}

// TestAnswerFirstWait: a caller with no snapshot waits for the first one.
func TestAnswerFirstWait(t *testing.T) {
	fixture := newNetsvcFixture(t)

	go func() {
		waitFor(t, "the request to park", func() bool { return true })
		time.Sleep(5 * time.Millisecond)
		fixture.service.install(&protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: 2})
	}()

	view, _ := fixture.service.answer(context.Background(), wire.NetworkRequest{FirstWaitMS: 2000})
	if view.Snapshot == nil {
		t.Fatal("the first wait returned without the snapshot")
	}
}

// TestAnswerFirstWaitEndsOnUnreachable: a caller is not made to wait for a gateway that has failed.
func TestAnswerFirstWaitEndsOnUnreachable(t *testing.T) {
	fixture := newNetsvcFixture(t)

	go func() {
		time.Sleep(5 * time.Millisecond)
		fixture.service.mu.Lock()
		fixture.service.fetchErr = "connection refused"
		fixture.service.mu.Unlock()
		fixture.service.bump()
	}()

	started := time.Now()
	view, _ := fixture.service.answer(context.Background(), wire.NetworkRequest{FirstWaitMS: 5000})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the wait did not end when the gateway became unreachable: %v", elapsed)
	}
	if view.Gateway.State != protocol.GatewayUnreachable {
		t.Fatalf("gateway state = %q", view.Gateway.State)
	}
}

// TestAnswerSinceWaitsForAChange: a since equal to the version waits, then answers.
func TestAnswerSinceWaitsForAChange(t *testing.T) {
	fixture := newNetsvcFixture(t)
	_, version := fixture.service.view()

	answered := make(chan protocol.NetworkView, 1)
	go func() {
		view, _ := fixture.service.answer(context.Background(), wire.NetworkRequest{Since: version, WaitMS: 2000})
		answered <- view
	}()

	time.Sleep(5 * time.Millisecond)
	fixture.states.set(
		[]protocol.AgentView{{ID: "a1", State: protocol.AgentBusy}},
		&protocol.ConnectionView{State: protocol.ConnConnected},
	)
	fixture.service.bump()

	select {
	case view := <-answered:
		if len(view.Local.Sessions) != 1 || view.Local.Sessions[0].State != protocol.SessionStateBusy {
			t.Fatalf("view = %+v", view.Local.Sessions)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the since wait never answered")
	}
}

// TestAnswerClampsAndCancels: an oversized wait is clamped, and a cancelled context answers at once.
func TestAnswerClampsAndCancels(t *testing.T) {
	fixture := newNetsvcFixture(t)
	_, version := fixture.service.view()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	view, got := fixture.service.answer(ctx, wire.NetworkRequest{Since: version, WaitMS: 999999})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("a cancelled context waited %v", elapsed)
	}
	if got != version || view.Schema != protocol.NetworkSchema {
		t.Fatalf("answer = %d %+v", got, view)
	}
}

// encodeJSON is a compact JSON rendering for the "no forbidden field" assertions.
func encodeJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// containsFold is a case-insensitive substring test, so a camel-cased field is still caught.
func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// TestUnsupportedStaysStickyAfterAnEarlierOutage: an outage BEFORE the 404 must not release the flag
// on the next tick (that would resend a report every second to a gateway that already said no).
func TestUnsupportedStaysStickyAfterAnEarlierOutage(t *testing.T) {
	fixture := newNetsvcFixture(t)

	// An outage first: the service sees the link down once.
	fixture.states.set(nil, &protocol.ConnectionView{State: protocol.ConnReconnecting})
	fixture.service.maybeReport(context.Background())
	fixture.states.set(
		[]protocol.AgentView{{ID: "a1", State: protocol.AgentIdle}},
		&protocol.ConnectionView{State: protocol.ConnConnected},
	)

	// Then the 404.
	fixture.link.reportErr = link.ErrNetworkUnsupported
	fixture.service.maybeReport(context.Background())
	sent := fixture.link.countReports()

	// Several later ticks, kicks included, send nothing.
	fixture.link.reportErr = nil
	for i := 0; i < 3; i++ {
		fixture.now = fixture.now.Add(2 * reportEvery)
		fixture.service.kick()
		fixture.service.maybeReport(context.Background())
	}
	if got := fixture.link.countReports(); got != sent {
		t.Fatalf("reports went from %d to %d while the gateway is unsupported", sent, got)
	}
}

// TestKickWakesAWaitingAnswer: a local change reaches a caller waiting on its version, instead of
// being heard only when the wait runs out.
func TestKickWakesAWaitingAnswer(t *testing.T) {
	fixture := newNetsvcFixture(t)
	_, version := fixture.service.view()

	done := make(chan uint64, 1)
	go func() {
		_, got := fixture.service.answer(context.Background(), wire.NetworkRequest{Since: version, WaitMS: 5000})
		done <- got
	}()

	time.Sleep(10 * time.Millisecond)
	fixture.states.set(
		[]protocol.AgentView{{ID: "a1", State: protocol.AgentBusy}},
		&protocol.ConnectionView{State: protocol.ConnConnected},
	)
	fixture.service.kick()

	select {
	case got := <-done:
		if got == version {
			t.Fatalf("the answer returned the same version %d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a local change did not wake the waiting answer")
	}
}

// TestGatewayStateIsDerivedNotLatched: a successful report cannot hide a failing fetch, and a
// healthy link with nothing fetched yet reads connected with a null snapshot.
func TestGatewayStateIsDerivedNotLatched(t *testing.T) {
	fixture := newNetsvcFixture(t)

	view, _ := fixture.service.view()
	if view.Gateway.State != protocol.GatewayConnected || view.Snapshot != nil {
		t.Fatalf("healthy link, nothing fetched: state %q snapshot %v", view.Gateway.State, view.Snapshot)
	}

	fixture.service.mu.Lock()
	fixture.service.fetchErr = "connection refused"
	fixture.service.mu.Unlock()
	fixture.service.finishReport(nil) // a report succeeds

	view, _ = fixture.service.view()
	if view.Gateway.State != protocol.GatewayUnreachable || view.Gateway.Error != "connection refused" {
		t.Fatalf("a good report hid the failed fetch: %q %q", view.Gateway.State, view.Gateway.Error)
	}
}

// TestRepeatedOneShotsSeeChangedGatewayData: the user's own test path. Each one-shot starts a fetch
// loop, waits for it to complete once, and answers with what it fetched, even though a snapshot was
// already kept from the first one.
func TestRepeatedOneShotsSeeChangedGatewayData(t *testing.T) {
	fixture := newNetsvcFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.service.start(ctx)

	oneShot := func(revision uint64) protocol.NetworkView {
		fixture.link.mu.Lock()
		fixture.link.fetch = []link.NetworkFetch{{Snapshot: &protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: revision}}}
		fixture.link.mu.Unlock()

		release := fixture.service.acquire()
		defer release()
		view, _ := fixture.service.answer(ctx, wire.NetworkRequest{FirstWaitMS: 2000})
		return view
	}

	first := oneShot(1)
	if first.Snapshot == nil || first.Snapshot.Revision != 1 {
		t.Fatalf("first one-shot = %+v", first.Snapshot)
	}
	second := oneShot(2)
	if second.Snapshot == nil || second.Snapshot.Revision != 2 {
		t.Fatalf("second one-shot showed the first snapshot ever fetched: %+v", second.Snapshot)
	}
}

// TestAFailedFetchDoesNotOutliveItsLoop: a fetch error from an earlier loop must not make a later
// one-shot see "unreachable" and leave before its own loop has tried.
func TestAFailedFetchDoesNotOutliveItsLoop(t *testing.T) {
	fixture := newNetsvcFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.service.start(ctx)

	fixture.service.mu.Lock()
	fixture.service.fetchErr = "connection refused"
	fixture.service.mu.Unlock()

	fixture.link.mu.Lock()
	fixture.link.fetch = []link.NetworkFetch{{Snapshot: &protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: 3}}}
	fixture.link.mu.Unlock()

	release := fixture.service.acquire()
	defer release()
	view, _ := fixture.service.answer(ctx, wire.NetworkRequest{FirstWaitMS: 2000})
	if view.Snapshot == nil || view.Snapshot.Revision != 3 || view.Gateway.State != protocol.GatewayConnected {
		t.Fatalf("view = state %q snapshot %+v", view.Gateway.State, view.Snapshot)
	}
}

// TestNoChangeRefreshesTheBody: after a 204 the loop fetches the full body once (no since, no wait),
// so the kept body and its fetch time keep moving on a quiet network; the long-poll wait is 10 s.
func TestNoChangeRefreshesTheBody(t *testing.T) {
	fixture := newNetsvcFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.service.start(ctx)

	snapshot := &protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: 5}
	fixture.link.mu.Lock()
	fixture.link.fetch = []link.NetworkFetch{{Snapshot: snapshot}, {Unchanged: true}, {Snapshot: snapshot}}
	fixture.link.mu.Unlock()

	release := fixture.service.acquire()
	defer release()

	waitFor(t, "three fetches", func() bool {
		fixture.link.mu.Lock()
		defer fixture.link.mu.Unlock()
		return len(fixture.link.calls) >= 3
	})

	fixture.link.mu.Lock()
	calls := append([]fetchCall(nil), fixture.link.calls[:3]...)
	fixture.link.mu.Unlock()

	if calls[0].since != 0 || calls[0].wait != 0 {
		t.Fatalf("the first fetch of a loop must ask for the body at once: %+v", calls[0])
	}
	if calls[1].since != 5 || calls[1].wait != fetchWait || fetchWait != 10*time.Second {
		t.Fatalf("the long poll must carry the revision and a 10 s wait: %+v", calls[1])
	}
	if calls[2].since != 0 || calls[2].wait != 0 {
		t.Fatalf("after a 204 the full body must be fetched again: %+v", calls[2])
	}
}

// TestARefusedReportDoesNotMakeTheGatewayUnreachable: the gateway answered, so it is reachable.
func TestARefusedReportDoesNotMakeTheGatewayUnreachable(t *testing.T) {
	fixture := newNetsvcFixture(t)

	fixture.service.finishReport(&link.RefusedError{Status: 413, Code: "bad_request", Message: "too large"})

	view, _ := fixture.service.view()
	if view.Gateway.State != protocol.GatewayConnected {
		t.Fatalf("a refused report made the gateway %q", view.Gateway.State)
	}

	// A transport failure still does.
	fixture.service.finishReport(errors.New("link: no connection to the gateway: refused"))
	view, _ = fixture.service.view()
	if view.Gateway.State != protocol.GatewayUnreachable {
		t.Fatalf("a transport failure left the gateway %q", view.Gateway.State)
	}
}

// TestNetworkQueryAfterTheWindowClosedDoesNotAcquire: a first network query in flight when the window
// closes must not register a consumer nobody will release.
func TestNetworkQueryAfterTheWindowClosedDoesNotAcquire(t *testing.T) {
	fixture := newNetsvcFixture(t)
	hub := newHub(&logger{out: io.Discard}, newGate())
	hub.network = fixture.service

	w := newWindow("w1", &recordingConn{}, hub, &logger{out: io.Discard})
	w.closeWith("test")
	w.acquireNetwork()

	fixture.service.mu.Lock()
	consumers := fixture.service.consumers
	fixture.service.mu.Unlock()
	if consumers != 0 {
		t.Fatalf("a closed window acquired a consumer: %d", consumers)
	}
}
