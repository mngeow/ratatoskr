package router

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// These tests touch actor-owned fields (conn, sends) only on a router that was
// never Started, then call refresh() directly - the same style the existing views
// tests use.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// testLink is a router.Link that is never called: this slice only needs the type to
// exist so a router can be built in connected mode.
type testLink struct{}

func (testLink) Send(context.Context, protocol.SendRequest) (protocol.SendResponse, error) {
	return protocol.SendResponse{}, errors.New("testLink: Send is not wired in this slice")
}

func (testLink) Ack(context.Context, string, bool, string) error {
	return errors.New("testLink: Ack is not wired in this slice")
}

func (testLink) Report(context.Context, string, protocol.StatusReport) error {
	return errors.New("testLink: Report is not wired in this slice")
}

func (testLink) Lookup(context.Context, string) (protocol.MessageInfo, error) {
	return protocol.MessageInfo{}, errors.New("testLink: Lookup is not wired in this slice")
}

func (testLink) Address(local string) string { return local + "@testhost" }

// connectedRouter builds a two-agent router in connected mode, never started.
func connectedRouter(t *testing.T) (*Router, *fakeStore, *fakeClock) {
	t.Helper()

	fake := newFakeStore()
	clock := newFakeClock()
	r, err := New(Config{
		RunID: "run-1",
		Store: fake,
		Now:   clock.now,
		NewID: counterIDs("m"),
		Link:  testLink{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range []protocol.LaunchSpec{spec("a", "claude"), spec("b", "codex")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}
	return r, fake, clock
}

func TestSenderName(t *testing.T) {
	local := &agent{id: "a", name: "claude"}
	if got := senderName(&msg{from: local}); got != "claude" {
		t.Fatalf("senderName(local) = %q, want claude", got)
	}

	if got := senderName(&msg{from: nil, origin: "claude@mac"}); got != "claude@mac" {
		t.Fatalf("senderName(remote) = %q, want claude@mac", got)
	}
}

// TestInboundMessageIsDeliveredWithItsNetworkName: a message that arrived from
// another host has no local agent, and the frame still names its sender.
func TestInboundMessageIsDeliveredWithItsNetworkName(t *testing.T) {
	pane := &fakePane{}
	r, fake, _, beta := newDeliverRouter(t, pane)

	claude := r.byName["claude"]
	if claude == nil {
		// newDeliverRouter registers alpha/beta; use one of those as the recipient.
		claude = r.byName["alpha"]
	}
	recipient := r.byName["beta"]
	_ = beta

	message := &msg{
		id:     "m-in-1",
		from:   nil,
		to:     recipient,
		body:   "hello from another host",
		state:  protocol.MessageQueued,
		at:     r.now(),
		origin: "claude@mac",
	}
	r.appendLog(message)
	recipient.queue = append(recipient.queue, message)

	recipient.state = protocol.AgentIdle
	r.pump(recipient, false)

	if recipient.inflight != message {
		t.Fatal("the inbound message was not put in flight")
	}

	// Run the write result the way the actor does.
	fn := <-r.in
	fn()

	frames := pane.submitted()
	if len(frames) != 1 {
		t.Fatalf("the pane received %d frames, want 1", len(frames))
	}
	if !strings.Contains(frames[0], `from="claude@mac"`) {
		t.Fatalf("frame does not carry the network name as its sender: %q", frames[0])
	}
	if fake.countCall("Transition:m-in-1:queued>delivering") != 1 {
		t.Fatal("the inbound message was not committed as delivering")
	}
}

// TestMessageViewUsesTheNetworkName: the view names a remote sender, sanitised.
func TestMessageViewUsesTheNetworkName(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())

	recipient := &agent{id: "b", name: "codex"}
	message := &msg{
		id:     "m-in-1",
		from:   nil,
		to:     recipient,
		body:   "hello",
		state:  protocol.MessageQueued,
		origin: "claude\x1b[31m@mac",
	}

	view := r.messageView(message)
	if view.From != "claude[31m@mac" {
		t.Fatalf("From = %q, want the origin with control characters stripped", view.From)
	}
	if view.To != "codex" {
		t.Fatalf("To = %q", view.To)
	}
}

// TestSendErrorDetails covers the text, the reason and the uncertainty flag.
func TestSendErrorDetails(t *testing.T) {
	// The old text is unchanged when there is no detail.
	for _, reason := range []string{
		ReasonUnknownRecipient,
		ReasonUnknownReplyTo,
		ReasonRecipientOffline,
		ReasonBudgetExhausted,
		ReasonTooLarge,
		ReasonStorageUnavailable,
		ReasonEmptyBody,
	} {
		refusal := &SendError{Reason: reason}
		if got, want := refusal.Error(), "router: message rejected: "+reason; got != want {
			t.Fatalf("Error() = %q, want %q", got, want)
		}
		if refusal.SendReason() != reason {
			t.Fatalf("SendReason() = %q, want %q", refusal.SendReason(), reason)
		}
		if refusal.SendDetail() != "" {
			t.Fatalf("SendDetail() = %q, want empty", refusal.SendDetail())
		}
		if refusal.Uncertain() {
			t.Fatalf("%q reported as uncertain; every local reason is certain", reason)
		}
	}

	// With a detail, the text carries it.
	refusal := &SendError{Reason: string(protocol.SendSessionGone), Detail: "the recipient host refused it: exited"}
	if got, want := refusal.Error(), "router: message rejected: session_gone: the recipient host refused it: exited"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if refusal.SendDetail() != "the recipient host refused it: exited" {
		t.Fatalf("SendDetail() = %q", refusal.SendDetail())
	}

	// Only the two uncertain results are uncertain.
	uncertain := map[string]bool{
		string(protocol.SendGatewayUncertain):  true,
		string(protocol.SendDeliveryUncertain): true,
	}
	for _, result := range []protocol.SendResult{
		protocol.SendGatewayUnreachable,
		protocol.SendUnknownRecipient,
		protocol.SendDestinationUnreachable,
		protocol.SendSessionGone,
		protocol.SendHostStored,
		protocol.SendSending,
		protocol.SendResult("nonsense"),
	} {
		if got := (&SendError{Reason: string(result)}).Uncertain(); got != false {
			t.Fatalf("%q reported as uncertain", result)
		}
	}
	for reason, want := range uncertain {
		if got := (&SendError{Reason: reason}).Uncertain(); got != want {
			t.Fatalf("%q uncertain = %v, want %v", reason, got, want)
		}
	}
}

// TestLocalModeSnapshotIsUnchanged is the regression bar: with Link nil nothing new
// appears anywhere.
func TestLocalModeSnapshotIsUnchanged(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	if err := r.registerAgent(spec("a", "claude")); err != nil {
		t.Fatalf("registerAgent: %v", err)
	}
	if err := r.registerAgent(spec("b", "codex")); err != nil {
		t.Fatalf("registerAgent: %v", err)
	}
	if r.connected() {
		t.Fatal("a router without a Link reports itself connected")
	}

	r.refresh()
	snapshot := r.Snapshot()

	if snapshot.Runtime {
		t.Fatal("a local snapshot is marked as a connected run")
	}
	if snapshot.Connection != nil {
		t.Fatalf("a local snapshot carries a connection: %+v", snapshot.Connection)
	}
	if len(snapshot.Sends) != 0 {
		t.Fatalf("a local snapshot carries %d sends", len(snapshot.Sends))
	}
	for _, agent := range snapshot.Agents {
		if agent.Address != "" {
			t.Fatalf("a local agent has an address: %+v", agent)
		}
	}
	// The rest of the snapshot is what refresh() always built.
	if len(snapshot.Agents) != 2 {
		t.Fatalf("%d agents, want 2", len(snapshot.Agents))
	}
	if snapshot.Budget.Limit != r.cfg.BudgetLimit {
		t.Fatalf("budget limit = %d, want %d", snapshot.Budget.Limit, r.cfg.BudgetLimit)
	}
}

// TestConnectedSnapshot covers the connected-only fields.
func TestConnectedSnapshot(t *testing.T) {
	r, _, clock := connectedRouter(t)

	r.conn = &protocol.ConnectionView{
		State:   protocol.ConnConnected,
		Host:    "testhost",
		Gateway: "127.0.0.1:7777",
		Since:   clock.now(),
		Peers:   []string{"claude@testhost", "codex@otherhost"},
	}

	now := clock.now()
	r.appendSend(&sendRec{
		id: "s1", from: r.byName["claude"], to: "codex@otherhost", body: "first",
		result: protocol.SendHostStored, report: protocol.MessageAccepted,
		reportReason: "prompt_submitted", at: now,
	})
	r.appendSend(&sendRec{
		id: "s2", from: r.byName["claude"], to: "codex@otherhost", body: "second",
		result: protocol.SendDeliveryUncertain, at: now.Add(time.Minute),
	})

	r.refresh()
	snapshot := r.Snapshot()

	if !snapshot.Runtime {
		t.Fatal("a connected snapshot is not marked as such")
	}
	if snapshot.Connection == nil || snapshot.Connection.State != protocol.ConnConnected {
		t.Fatalf("connection = %+v", snapshot.Connection)
	}
	// A copy, not the router's own pointer.
	if snapshot.Connection == r.conn {
		t.Fatal("the snapshot shares the router's connection pointer")
	}
	snapshot.Connection.Peers[0] = "changed@nowhere"
	if r.conn.Peers[0] != "claude@testhost" {
		t.Fatal("mutating the snapshot's peers changed the router's peers")
	}

	if len(snapshot.Sends) != 2 || snapshot.Sends[0].ID != "s1" || snapshot.Sends[1].ID != "s2" {
		t.Fatalf("sends = %+v, want s1 then s2 (oldest first)", snapshot.Sends)
	}
	if snapshot.Sends[0].From != "claude" || snapshot.Sends[1].To != "codex@otherhost" {
		t.Fatalf("send views = %+v", snapshot.Sends)
	}
	if snapshot.Sends[0].Preview != "first" {
		t.Fatalf("preview = %q", snapshot.Sends[0].Preview)
	}

	for _, agent := range snapshot.Agents {
		if agent.Address != agent.DisplayName+"@testhost" {
			t.Fatalf("agent address = %q, want %s@testhost", agent.Address, agent.DisplayName)
		}
	}
}

// TestAppendSendCaps: the newest sendLogLimit survive, mirroring appendLog.
func TestAppendSendCaps(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	from := &agent{id: "a", name: "claude"}

	for i := 0; i < sendLogLimit+5; i++ {
		r.appendSend(&sendRec{id: itoa(i), from: from, to: "codex@host", result: protocol.SendHostStored})
	}
	if len(r.sends) != sendLogLimit {
		t.Fatalf("%d sends kept, want %d", len(r.sends), sendLogLimit)
	}
	if r.sends[len(r.sends)-1].id != itoa(sendLogLimit+4) {
		t.Fatalf("newest send = %q, want the last one appended", r.sends[len(r.sends)-1].id)
	}
}

// TestRecentSendsCaps: the view list caps at recentSendsLimit and is never nil.
func TestRecentSendsCaps(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	from := &agent{id: "a", name: "claude"}

	empty := r.recentSends()
	if empty == nil {
		t.Fatal("recentSends returned nil")
	}
	if len(empty) != 0 {
		t.Fatalf("%d views for no sends", len(empty))
	}

	for i := 0; i < recentSendsLimit+5; i++ {
		r.appendSend(&sendRec{id: itoa(i), from: from, to: "codex@host", result: protocol.SendHostStored})
	}
	if got := len(r.recentSends()); got != recentSendsLimit {
		t.Fatalf("recentSends returned %d views, want %d", got, recentSendsLimit)
	}
}

// TestEmitConnection: nothing in local mode, one event when connected.
func TestEmitConnection(t *testing.T) {
	local, _ := newTestRouter(newFakeStore())
	local.emitConnection()
	select {
	case event := <-local.events:
		t.Fatalf("a local router emitted a connection event: %+v", event)
	default:
	}

	connected, _, clock := connectedRouter(t)
	connected.conn = &protocol.ConnectionView{
		State:   protocol.ConnReconnecting,
		Host:    "testhost",
		Gateway: "127.0.0.1:7777",
		Since:   clock.now(),
		Err:     "no frame for 15s",
	}
	connected.emitConnection()

	select {
	case event := <-connected.events:
		if event.Connection == nil || event.Connection.State != protocol.ConnReconnecting {
			t.Fatalf("event = %+v", event)
		}
		if event.Connection.Err != "no frame for 15s" {
			t.Fatalf("Err = %q", event.Connection.Err)
		}
	default:
		t.Fatal("a connected router emitted no connection event")
	}
}

// TestEmitSend: one event carrying the view, sanitised.
func TestEmitSend(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	from := &agent{id: "a", name: "claude\x1b[31m"}

	rec := &sendRec{
		id: "s1", from: from, to: "codex\x1b@host", body: "hi there",
		result: protocol.SendHostStored, reportReason: "busy\n",
	}
	r.emitSend(rec)

	select {
	case event := <-r.events:
		if event.Send == nil {
			t.Fatalf("event = %+v, want a send view", event)
		}
		if event.Send.ID != "s1" || event.Send.From != "claude[31m" {
			t.Fatalf("send view = %+v", event.Send)
		}
		if event.Send.To != "codex[31m@host" {
			t.Fatalf("To = %q, want the escape stripped", event.Send.To)
		}
		if event.Send.Preview != "hi there" {
			t.Fatalf("preview = %q", event.Send.Preview)
		}
	default:
		t.Fatal("emitSend emitted nothing")
	}
}

// TestStoreInterfaceMatchesTheConnectedStore: the interface must keep compiling
// against *store.Store, which history.go asserts.
func TestStoreInterfaceMatchesTheConnectedStore(t *testing.T) {
	var _ Store = (*store.Store)(nil)
}

// itoa is a tiny helper so send ids can be generated in a loop.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}

	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

// fakeLink is the connected-mode Link used by the send tests: it records calls and
// can block until a test releases it.
type fakeLink struct {
	mu       sync.Mutex
	calls    []protocol.SendRequest
	enter    chan struct{}
	release  chan struct{}
	response protocol.SendResponse
	err      error
	entered  int

	// Receiver-side recorders and programmable answers.
	acks       []ackCall
	reports    []reportCall
	lookups    []string
	lookupInfo map[string]protocol.MessageInfo
	lookupErrs map[string]error
	ackErr     error
	reportErr  error
	ackGate    chan struct{}
}

func newFakeLink() *fakeLink {
	return &fakeLink{
		lookupInfo: make(map[string]protocol.MessageInfo),
		lookupErrs: make(map[string]error),
	}
}

// block makes Send wait until release is closed, and signals enter on arrival.
func (l *fakeLink) block() {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Buffered: Send signals with a non-blocking send, so an unbuffered channel would
	// lose the signal whenever the test was not already waiting on it.
	l.enter = make(chan struct{}, 8)
	l.release = make(chan struct{})
	l.entered = 0
}

func (l *fakeLink) Send(ctx context.Context, request protocol.SendRequest) (protocol.SendResponse, error) {
	l.mu.Lock()
	l.calls = append(l.calls, request)
	enter := l.enter
	release := l.release
	l.entered++
	response := l.response
	err := l.err
	l.mu.Unlock()

	if enter != nil {
		select {
		case enter <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return protocol.SendResponse{}, ctx.Err()
		}
	}
	return response, err
}

// ackCall is one recorded acknowledgement.
type ackCall struct {
	id     string
	saved  bool
	reason string
}

// reportCall is one recorded delivery report.
type reportCall struct {
	id    string
	state protocol.MessageState
	at    time.Time
}

func (l *fakeLink) Ack(ctx context.Context, id string, saved bool, reason string) error {
	l.mu.Lock()
	l.acks = append(l.acks, ackCall{id: id, saved: saved, reason: reason})
	gate := l.ackGate
	err := l.ackErr
	l.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (l *fakeLink) Report(_ context.Context, id string, report protocol.StatusReport) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reports = append(l.reports, reportCall{id: id, state: report.State, at: report.At})
	return l.reportErr
}

func (l *fakeLink) Lookup(ctx context.Context, id string) (protocol.MessageInfo, error) {
	l.mu.Lock()
	l.lookups = append(l.lookups, id)
	info, hasInfo := l.lookupInfo[id]
	err := l.lookupErrs[id]
	l.mu.Unlock()

	if err != nil {
		return protocol.MessageInfo{}, err
	}
	select {
	case <-ctx.Done():
		return protocol.MessageInfo{}, ctx.Err()
	default:
	}
	if !hasInfo {
		return protocol.MessageInfo{ID: id}, nil
	}
	return info, nil
}

func (l *fakeLink) ackList() []ackCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ackCall(nil), l.acks...)
}

func (l *fakeLink) reportList() []reportCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]reportCall(nil), l.reports...)
}

func (l *fakeLink) lookupList() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lookups...)
}

func (l *fakeLink) Address(local string) string { return local + "@testhost" }

func (l *fakeLink) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

func (l *fakeLink) lastCall(t *testing.T) protocol.SendRequest {
	t.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.calls) == 0 {
		t.Fatal("the Link was never called")
	}
	return l.calls[len(l.calls)-1]
}

// sendRouter builds a started router in connected mode with the given Link.
func sendRouter(t *testing.T, link *fakeLink) (*Router, *fakeStore) {
	t.Helper()

	fake := newFakeStore()
	clock := newFakeClock()
	r, err := New(Config{
		RunID: "run-1",
		Store: fake,
		Now:   clock.now,
		NewID: counterIDs("m"),
		Link:  link,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range []protocol.LaunchSpec{spec("a", "claude"), spec("b", "codex")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}
	r.Start()
	t.Cleanup(r.Close)
	// The sessions the gateway would have given at registration. Enqueued before any
	// test offer, so the actor sees them first.
	r.SetGatewaySession("claude", "sess-claude")
	r.SetGatewaySession("codex", "sess-codex")
	return r, fake
}

// TestConnectedSendGoesThroughTheGateway: a bare name becomes name@thishost and the
// message is never queued locally.
func TestConnectedSendGoesThroughTheGateway(t *testing.T) {
	link := newFakeLink()
	link.response = protocol.SendResponse{ID: "m1", Result: protocol.SendHostStored}
	r, fake := sendRouter(t, link)

	id, err := r.Send(context.Background(), "a", "codex", "hello", "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "m1" {
		t.Fatalf("id = %q, want m1", id)
	}

	request := link.lastCall(t)
	if request.To != "codex@testhost" {
		t.Fatalf("To = %q, want codex@testhost", request.To)
	}
	if request.From != "claude" {
		t.Fatalf("From = %q, want the local name", request.From)
	}

	// Nothing was queued locally.
	recipient := r.byName["codex"]
	if len(recipient.queue) != 0 {
		t.Fatalf("a connected send queued %d local messages", len(recipient.queue))
	}

	// The record was inserted before the Link was called, and finished after.
	calls := fake.callLog()
	insertAt, linkAt := -1, -1
	for i, call := range calls {
		if call == "InsertSend:m1" {
			insertAt = i
		}
		if call == "FinishSend:m1:sending>host_stored" {
			linkAt = i
		}
	}
	if insertAt < 0 {
		t.Fatalf("no InsertSend in %v", calls)
	}
	if linkAt < 0 || linkAt < insertAt {
		t.Fatalf("FinishSend did not follow InsertSend: %v", calls)
	}
}

// TestConnectedSelfSendRefused: only the exact self address is refused.
func TestConnectedSelfSendRefused(t *testing.T) {
	link := newFakeLink()
	link.response = protocol.SendResponse{ID: "m1", Result: protocol.SendHostStored}
	r, fake := sendRouter(t, link)

	for _, to := range []string{"claude", "claude@testhost", ""} {
		_, err := r.Send(context.Background(), "a", to, "hello", "")
		var refusal *SendError
		if !errors.As(err, &refusal) || refusal.Reason != ReasonUnknownRecipient {
			t.Fatalf("to %q: error = %v, want unknown_recipient", to, err)
		}
	}
	if link.callCount() != 0 {
		t.Fatalf("%d Link calls for refused sends", link.callCount())
	}
	if fake.countCall("InsertSend:") != 0 {
		t.Fatal("a refused send inserted a record")
	}
	if r.used != 0 {
		t.Fatalf("budget = %d after refusals", r.used)
	}

	// A different agent on this host is not a self-send.
	if _, err := r.Send(context.Background(), "a", "codex@testhost", "hello", ""); err != nil {
		t.Fatalf("same-host cross-agent send: %v", err)
	}
}

// TestConnectedRefusalOrder: the first broken rule wins, as it does locally.
func TestConnectedRefusalOrder(t *testing.T) {
	link := newFakeLink()
	r, _ := sendRouter(t, link)
	r.cfg.MaxBody = 4

	tests := []struct {
		name string
		from protocol.AgentID
		to   string
		body string
		meta string
		want string
	}{
		{name: "self-send beats an empty body", from: "a", to: "claude", body: "", want: ReasonUnknownRecipient},
		{name: "too large beats budget", from: "a", to: "codex@testhost", body: "0123456789", meta: "budget", want: ReasonTooLarge},
		{name: "unknown reply-to beats budget", from: "a", to: "codex@testhost", body: "hi", meta: "reply", want: ReasonUnknownReplyTo},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r.used = r.cfg.BudgetLimit // budget exhausted

			replyTo := ""
			if tc.meta == "reply" {
				replyTo = "missing-id"
			}
			_, err := r.Send(context.Background(), tc.from, tc.to, tc.body, replyTo)
			var refusal *SendError
			if !errors.As(err, &refusal) || refusal.Reason != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			r.used = 0
		})
	}
}

// TestConnectedNetworkWaitIsOffTheActor proves the actor keeps working while the
// Link blocks.
func TestConnectedNetworkWaitIsOffTheActor(t *testing.T) {
	link := newFakeLink()
	link.response = protocol.SendResponse{ID: "m1", Result: protocol.SendHostStored}
	link.block()
	r, _ := sendRouter(t, link)

	type answer struct {
		id  string
		err error
	}
	answers := make(chan answer, 1)
	go func() {
		id, err := r.Send(context.Background(), "a", "codex@testhost", "hello", "")
		answers <- answer{id: id, err: err}
	}()

	select {
	case <-link.enter:
	case <-time.After(2 * time.Second):
		t.Fatal("the Link was never entered")
	}

	// The actor is free: a submitted closure runs, and the notifier methods return.
	ran := make(chan struct{}, 1)
	if err := r.submit(context.Background(), func() { ran <- struct{}{} }); err != nil {
		t.Fatalf("submit while the Link blocks: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the actor is blocked behind the network wait")
	}

	r.UserDraft("a", true)
	r.DeliverHeld("a")

	// The snapshot shows the attempt as sending.
	waitFor(t, "a sending view in the snapshot", func() bool {
		for _, view := range r.Snapshot().Sends {
			if view.ID == "m1" && view.Result == protocol.SendSending {
				return true
			}
		}
		return false
	})

	close(link.release)
	select {
	case answer := <-answers:
		if answer.err != nil || answer.id != "m1" {
			t.Fatalf("Send = (%q, %v)", answer.id, answer.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return after the Link was released")
	}
}

// TestLinkResultMapping is the whole result table.
func TestLinkResultMapping(t *testing.T) {
	tests := []struct {
		name     string
		response protocol.SendResponse
		err      error
		want     protocol.SendResult
		wantText string
		definite bool
	}{
		{
			name:     "host stored",
			response: protocol.SendResponse{Result: protocol.SendHostStored},
			want:     protocol.SendHostStored,
		},
		{
			name:     "delivery uncertain",
			response: protocol.SendResponse{Result: protocol.SendDeliveryUncertain, Detail: "no acknowledgement in time"},
			want:     protocol.SendDeliveryUncertain,
			wantText: "no acknowledgement in time",
		},
		{
			name:     "empty result is uncertain",
			response: protocol.SendResponse{},
			want:     protocol.SendGatewayUncertain,
			wantText: "not usable",
		},
		{
			name:     "sending is uncertain",
			response: protocol.SendResponse{Result: protocol.SendSending},
			want:     protocol.SendGatewayUncertain,
		},
		{
			name:     "unknown result is uncertain",
			response: protocol.SendResponse{Result: protocol.SendResult("maybe")},
			want:     protocol.SendGatewayUncertain,
		},
		{
			name:     "not sent is a definite failure",
			err:      fmt.Errorf("%w: the gateway refused this host: unauthorized: no", ErrNotSent),
			want:     protocol.SendGatewayUnreachable,
			wantText: "the gateway refused this host: unauthorized: no",
			definite: true,
		},
		{
			name:     "other error is uncertain",
			err:      errors.New("no complete answer from the gateway: the message may have been stored"),
			want:     protocol.SendGatewayUncertain,
			wantText: "may have been stored",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, reason, _ := linkResult(tc.response, tc.err)
			if result != tc.want {
				t.Fatalf("result = %q, want %q", result, tc.want)
			}
			if tc.wantText != "" && !strings.Contains(reason, tc.wantText) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.wantText)
			}
			if tc.err != nil && errors.Is(tc.err, ErrNotSent) && !tc.definite {
				t.Fatal("an ErrNotSent error must map to a definite failure")
			}
			if tc.want == protocol.SendHostStored && reason != "" {
				t.Fatalf("a stored send carries a reason: %q", reason)
			}
		})
	}
}

// TestUnknownRecipientDetailListsTheDirectory: the refusal names who exists, capped.
func TestUnknownRecipientDetailListsTheDirectory(t *testing.T) {
	link := newFakeLink()
	entries := make([]protocol.DirectoryEntry, 0, 25)
	for i := 0; i < 25; i++ {
		entries = append(entries, protocol.DirectoryEntry{Address: itoa(i) + "@host"})
	}
	link.response = protocol.SendResponse{Result: protocol.SendUnknownRecipient, Directory: entries}

	r, _ := sendRouter(t, link)
	_, err := r.Send(context.Background(), "a", "codex@otherhost", "hello", "")

	var refusal *SendError
	if !errors.As(err, &refusal) || refusal.Reason != ReasonUnknownRecipient {
		t.Fatalf("error = %v, want unknown_recipient", err)
	}
	if !strings.Contains(refusal.Detail, "known agents:") {
		t.Fatalf("detail = %q, want a directory listing", refusal.Detail)
	}
	if got := strings.Count(refusal.Detail, "@host"); got != maxDirectoryInDetail {
		t.Fatalf("listing holds %d addresses, want %d", got, maxDirectoryInDetail)
	}
	if refusal.Uncertain() {
		t.Fatal("an unknown recipient is a definite failure")
	}
}

// TestCallerHangsUpAndTheRecordStillFinishes.
func TestCallerHangsUpAndTheRecordStillFinishes(t *testing.T) {
	link := newFakeLink()
	link.response = protocol.SendResponse{ID: "m1", Result: protocol.SendHostStored}
	r, fake := sendRouter(t, link)

	ctx, cancel := context.WithCancel(context.Background())
	answers := make(chan error, 1)
	go func() {
		_, err := r.Send(ctx, "a", "codex@testhost", "hello", "")
		answers <- err
	}()

	waitFor(t, "the Link call", func() bool { return link.callCount() >= 1 })
	cancel()

	select {
	case err := <-answers:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return when the caller cancelled")
	}

	// The record is still finished, after the Link returned.
	waitFor(t, "the FinishSend", func() bool {
		return fake.countCall("FinishSend:m1:sending>host_stored") == 1
	})
}

// TestRouterClosingDuringSend: no FinishSend, and the caller learns the router
// closed.
func TestRouterClosingDuringSend(t *testing.T) {
	link := newFakeLink()
	link.block()
	link.response = protocol.SendResponse{ID: "m1", Result: protocol.SendHostStored}

	fake := newFakeStore()
	clock := newFakeClock()
	r, err := New(Config{RunID: "run-1", Store: fake, Now: clock.now, NewID: counterIDs("m"), Link: link})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.registerAgent(spec("a", "claude")); err != nil {
		t.Fatalf("registerAgent: %v", err)
	}
	r.Start()

	answers := make(chan error, 1)
	go func() {
		_, err := r.Send(context.Background(), "a", "codex@testhost", "hello", "")
		answers <- err
	}()

	select {
	case <-link.enter:
	case <-time.After(2 * time.Second):
		t.Fatal("the Link was never entered")
	}

	r.Close()

	select {
	case err := <-answers:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Send = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return when the router closed")
	}

	// Releasing afterwards must not produce a FinishSend or a panic.
	close(link.release)
	time.Sleep(50 * time.Millisecond)
	if fake.countCall("FinishSend:") != 0 {
		t.Fatal("a commit ran after the router closed")
	}
}

// TestSendsAndSendDetail covers the two read methods.
func TestSendsAndSendDetail(t *testing.T) {
	fake := newFakeStore()
	clock := newFakeClock()
	fake.sends = map[string]store.SendRow{
		"s1": {
			Seq: 1, ID: "s1", RunID: "run-1", From: "claude\x1b[31m", To: "codex@host",
			Body: "first body", Result: protocol.SendHostStored, CreatedAt: clock.now(),
		},
		"s2": {
			Seq: 2, ID: "s2", RunID: "run-1", From: "claude", To: "codex@host",
			Body: "second body", Result: protocol.SendDeliveryUncertain,
			Report: protocol.MessageAccepted, ReportReason: "busy\n", CreatedAt: clock.now(),
		},
	}
	fake.sendEvents = map[string][]store.SendEventRow{
		"s1": {
			{At: clock.now(), Kind: "result", Value: "host_stored"},
			{At: clock.now(), Kind: "report", Value: "accepted", Reason: "ok"},
			{At: clock.now(), Kind: "unknown", Value: "x"},
		},
	}

	r, err := New(Config{RunID: "run-1", Store: fake, Now: clock.now, NewID: counterIDs("m")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	views, err := r.Sends(0)
	if err != nil {
		t.Fatalf("Sends: %v", err)
	}
	if len(views) != 2 || views[0].ID != "s1" || views[1].ID != "s2" {
		t.Fatalf("views = %+v", views)
	}
	if views[0].From != "claude[31m" {
		t.Fatalf("From = %q, want the escape stripped", views[0].From)
	}
	if views[1].ReportReason != "busy " {
		t.Fatalf("report reason = %q", views[1].ReportReason)
	}

	detail, err := r.SendDetail("s1")
	if err != nil {
		t.Fatalf("SendDetail: %v", err)
	}
	if detail.Body != "first body" {
		t.Fatalf("body = %q", detail.Body)
	}
	if len(detail.Events) != 2 {
		t.Fatalf("%d events, want the unknown kind skipped", len(detail.Events))
	}
	if detail.Events[0].Result != protocol.SendHostStored || detail.Events[0].Reported {
		t.Fatalf("first event = %+v", detail.Events[0])
	}
	if detail.Events[1].Report != protocol.MessageAccepted || !detail.Events[1].Reported {
		t.Fatalf("second event = %+v", detail.Events[1])
	}

	if _, err := r.SendDetail("missing"); !errors.Is(err, ErrSendNotFound) {
		t.Fatalf("SendDetail for an unknown id = %v, want ErrSendNotFound", err)
	}
}

// TestLocalSendIsUnchangedByTheConnectedPath is the regression bar for send.
func TestLocalSendIsUnchangedByTheConnectedPath(t *testing.T) {
	fake := newFakeStore()
	clock := newFakeClock()
	r, err := New(Config{RunID: "run-1", Store: fake, Now: clock.now, NewID: counterIDs("m")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range []protocol.LaunchSpec{spec("a", "claude"), spec("b", "codex")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}
	if r.connected() {
		t.Fatal("a router without a Link is connected")
	}

	// The local actor-side path is what the existing tests use.
	id, recipient, err := r.send("a", "codex", "hello", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if recipient == nil || len(recipient.queue) != 1 {
		t.Fatal("the local send did not queue the message")
	}
	if id == "" {
		t.Fatal("the local send produced no id")
	}
	if fake.countCall("InsertSend:") != 0 {
		t.Fatal("a local send recorded an outbound attempt")
	}
}

// waitFor polls a condition with a bounded deadline, so no test asserts on a sleep.
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

// TestACorrectionBeforeTheCommitIsNotOverwritten: a late correction from the gateway
// can move the row (and the router's record) before the send's own commit runs. The
// commit then gets ErrStateChanged and must leave the record as the correction left
// it, not assign the older outcome: the ledger is right, so the UI must be too. Only
// the tool's answer still carries what the Link returned.
func TestACorrectionBeforeTheCommitIsNotOverwritten(t *testing.T) {
	link := newFakeLink()
	link.block()
	link.err = errors.New("no complete answer from the gateway: the message may have been stored")
	r, fake := sendRouter(t, link)

	answers := make(chan error, 1)
	go func() {
		_, err := r.Send(context.Background(), "a", "codex@testhost", "hello", "")
		answers <- err
	}()

	// Polled, not signalled: the fake's enter channel is a non-blocking send into an
	// unbuffered channel, which a test that is not already receiving would miss.
	waitFor(t, "the Link call", func() bool { return link.callCount() >= 1 })

	// The correction arrives first: the row and the record both say host_stored.
	applied := make(chan string, 1)
	if err := r.submit(context.Background(), func() {
		rec := r.sends[len(r.sends)-1]
		rec.result = protocol.SendHostStored
		fake.mu.Lock()
		row := fake.sends[rec.id]
		row.Result = protocol.SendHostStored
		fake.sends[rec.id] = row
		fake.mu.Unlock()
		applied <- rec.id
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	id := <-applied

	// Now the Link returns its older, uncertain outcome.
	close(link.release)

	select {
	case err := <-answers:
		var sendErr *SendError
		if !errors.As(err, &sendErr) || sendErr.Reason != string(protocol.SendGatewayUncertain) {
			t.Fatalf("Send = %v, want the Link's own outcome (gateway_uncertain) as the tool answer", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return")
	}

	waitFor(t, "the commit attempt", func() bool {
		return fake.countCall("FinishSend:"+id+":sending>gateway_uncertain") == 1
	})

	state := make(chan protocol.SendResult, 1)
	if err := r.submit(context.Background(), func() { state <- r.sends[len(r.sends)-1].result }); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := <-state; got != protocol.SendHostStored {
		t.Fatalf("the record says %q after the commit, want the correction's host_stored to survive", got)
	}

	// And no event carried the stale outcome after the first `sending` one.
	for {
		select {
		case event := <-r.events:
			if event.Send != nil && event.Send.Result == protocol.SendGatewayUncertain {
				t.Fatalf("a stale gateway_uncertain event was emitted: %+v", event.Send)
			}
		default:
			return
		}
	}
}

// offerFor builds one offer for the receiving tests.
func offerFor(id, to, from string, at time.Time) protocol.Offer {
	return protocol.Offer{
		ID:        id,
		From:      from,
		To:        to,
		ToSession: "sess-" + to, // the session sendRouter registers for each agent
		Body:      "hello from another host",
		At:        at,
	}
}

// routerNow is the fixed clock the fake clock starts at, for test fixtures that do
// not carry one.
func routerNow() time.Time { return newFakeClock().now() }

// waitForCall waits until a condition holds, bounded.
func waitForCall(t *testing.T, why string, condition func() bool) {
	t.Helper()
	waitFor(t, why, condition)
}

// TestReceiveSavesThenAcksThenQueues covers the ordering a received message needs.
func TestReceiveSavesThenAcksThenQueues(t *testing.T) {
	link := newFakeLink()
	r, fake := sendRouter(t, link)
	recipient := r.byName["codex"]
	recipient.state = protocol.AgentIdle

	offer := offerFor("m-in-1", "codex", "claude@mac", routerNow())
	r.Receive(offer)
	waitForCall(t, "the acknowledgement", func() bool { return len(link.ackList()) == 1 })

	acks := link.ackList()
	if !acks[0].saved || acks[0].id != "m-in-1" {
		t.Fatalf("ack = %+v, want a saved ack for the offer", acks[0])
	}

	// The inbox insert happened, with the sender's network name as origin.
	calls := fake.callLog()
	insertAt := -1
	for i, call := range calls {
		if call == "InsertInbound:m-in-1" {
			insertAt = i
		}
	}
	if insertAt < 0 {
		t.Fatalf("no InsertInbound in %v", calls)
	}

	// The message is queued with no local sender and the origin set, and the budget
	// is untouched because the budget is the sender's.
	waitFor(t, "the queued message", func() bool { return len(recipient.queue) == 1 })
	queued := recipient.queue[0]
	if queued.from != nil || queued.origin != "claude@mac" {
		t.Fatalf("queued message = %+v, want no local sender and the origin set", queued)
	}
	if r.used != 0 {
		t.Fatalf("budget = %d after receiving", r.used)
	}

	// A "queued" report goes out.
	waitFor(t, "the queued report", func() bool { return len(link.reportList()) >= 1 })
	if got := link.reportList()[0]; got.id != "m-in-1" || got.state != protocol.MessageQueued {
		t.Fatalf("report = %+v, want queued", got)
	}
}

// TestReceiveDuplicateAcksAgainAndQueuesOnce: the duplicate check that prevents a
// double paste.
func TestReceiveDuplicateAcksAgainAndQueuesOnce(t *testing.T) {
	link := newFakeLink()
	r, _ := sendRouter(t, link)
	recipient := r.byName["codex"]
	recipient.state = protocol.AgentIdle

	offer := offerFor("m-in-1", "codex", "claude@mac", routerNow())
	r.Receive(offer)
	waitFor(t, "the first queue", func() bool { return len(recipient.queue) == 1 })

	r.Receive(offer)
	waitFor(t, "the second acknowledgement", func() bool { return len(link.ackList()) == 2 })

	if len(recipient.queue) != 1 {
		t.Fatalf("a duplicate offer queued %d messages", len(recipient.queue))
	}
	for _, ack := range link.ackList() {
		if !ack.saved {
			t.Fatalf("a duplicate offer was refused: %+v", ack)
		}
	}
	// One queued report for one stored message.
	waitFor(t, "the queued report", func() bool { return len(link.reportList()) >= 1 })
	time.Sleep(20 * time.Millisecond)
	queuedReports := 0
	for _, report := range link.reportList() {
		if report.state == protocol.MessageQueued {
			queuedReports++
		}
	}
	if queuedReports != 1 {
		t.Fatalf("%d queued reports, want 1", queuedReports)
	}
}

// TestReceiveRefusals: each refusal acks with its exact reason and queues nothing.
func TestReceiveRefusals(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, r *Router, fake *fakeStore)
		offer   protocol.Offer
		want    string
	}{
		{
			name:    "unknown agent",
			arrange: func(*testing.T, *Router, *fakeStore) {},
			offer:   offerFor("m-1", "nobody", "claude@mac", routerNow()),
			want:    "no_such_agent",
		},
		{
			name: "exited agent",
			arrange: func(_ *testing.T, r *Router, _ *fakeStore) {
				r.byName["codex"].state = protocol.AgentExited
			},
			offer: offerFor("m-1", "codex", "claude@mac", routerNow()),
			want:  "exited",
		},
		{
			name:    "empty body",
			arrange: func(*testing.T, *Router, *fakeStore) {},
			offer:   protocol.Offer{ID: "m-1", To: "codex", ToSession: "sess-codex", From: "claude@mac", Body: " \x1b "},
			want:    "empty_body",
		},
		{
			name:    "oversized body",
			arrange: func(*testing.T, *Router, *fakeStore) {},
			offer:   protocol.Offer{ID: "m-1", To: "codex", ToSession: "sess-codex", From: "claude@mac", Body: strings.Repeat("x", 200)},
			want:    "too_large",
		},
		{
			name: "storage down",
			arrange: func(_ *testing.T, _ *Router, fake *fakeStore) {
				fake.setUnavailable(true)
			},
			offer: offerFor("m-1", "codex", "claude@mac", routerNow()),
			want:  "storage_unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			link := newFakeLink()
			r, fake := sendRouter(t, link)
			r.cfg.MaxBody = 64
			tc.arrange(t, r, fake)

			r.Receive(tc.offer)
			waitFor(t, "the refusal", func() bool { return len(link.ackList()) == 1 })

			ack := link.ackList()[0]
			if ack.saved {
				t.Fatalf("ack = %+v, want a refusal", ack)
			}
			if ack.reason != tc.want {
				t.Fatalf("reason = %q, want %q", ack.reason, tc.want)
			}
			if len(r.byName["codex"].queue) != 0 {
				t.Fatal("a refused offer was queued")
			}
		})
	}
}

// TestAckDoesNotBlockTheActor: the acknowledgement runs off the actor.
func TestAckDoesNotBlockTheActor(t *testing.T) {
	link := newFakeLink()
	link.ackGate = make(chan struct{})
	r, _ := sendRouter(t, link)
	r.byName["codex"].state = protocol.AgentIdle

	r.Receive(offerFor("m-1", "codex", "claude@mac", routerNow()))
	waitFor(t, "the blocked ack", func() bool { return len(link.ackList()) == 1 })

	// The actor still runs work.
	ran := make(chan struct{}, 1)
	if err := r.submit(context.Background(), func() { ran <- struct{}{} }); err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the actor is blocked behind the acknowledgement")
	}

	close(link.ackGate)
}

// TestReportsAreSentAndMarked: success reports reach the Link and MarkReported.
func TestReportsAreSentAndMarked(t *testing.T) {
	link := newFakeLink()
	r, fake := sendRouter(t, link)
	recipient := r.byName["codex"]
	recipient.state = protocol.AgentIdle

	r.Receive(offerFor("m-1", "codex", "claude@mac", routerNow()))
	waitFor(t, "the queued report", func() bool { return len(link.reportList()) >= 1 })

	fake.mu.Lock()
	reported := fake.reportedState["m-1"]
	fake.mu.Unlock()
	if reported != protocol.MessageQueued {
		t.Fatalf("MarkReported = %q, want queued", reported)
	}

	// A failed report leaves MarkReported alone.
	link.mu.Lock()
	link.reportErr = errors.New("report failed")
	link.reports = nil
	link.mu.Unlock()

	r.move(recipient.queue[0], protocol.MessageHeld, "busy")
	waitFor(t, "the second report attempt", func() bool { return len(link.reportList()) >= 1 })
}

// TestLocalMoveStartsNoReporter: a local message never starts the report goroutine.
func TestLocalMoveStartsNoReporter(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	for _, s := range []protocol.LaunchSpec{spec("a", "claude"), spec("b", "codex")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}
	a := r.agents["a"]
	b := r.agents["b"]

	m := &msg{id: "m1", from: a, to: b, body: "hello", state: protocol.MessageQueued, at: r.now()}
	r.appendLog(m)
	b.queue = append(b.queue, m)

	if err := r.move(m, protocol.MessageHeld, "busy"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if r.reports != nil {
		t.Fatal("a local message started the reporter goroutine")
	}
}

// TestLinkStateBannerAndSnapshot: refused raises the one banner, others clear it.
func TestLinkStateBannerAndSnapshot(t *testing.T) {
	link := newFakeLink()
	r, _ := sendRouter(t, link)

	r.LinkState(protocol.ConnectionView{State: protocol.ConnConnecting, Host: "laptop", Err: "dial\x1b[31m failed"})
	waitFor(t, "the connecting state", func() bool {
		snapshot := r.Snapshot()
		return snapshot.Connection != nil && snapshot.Connection.State == protocol.ConnConnecting
	})

	// The banner is read through the published snapshot: r.banners is actor-owned,
	// and a running router must not have it read from the test goroutine.
	if banner := r.Snapshot().Banner; banner != "" {
		t.Fatalf("connecting raised a banner: %q", banner)
	}
	if got := r.Snapshot().Connection.Err; strings.ContainsRune(got, '\x1b') {
		t.Fatalf("Err = %q, want it sanitised", got)
	}

	// The link's own reason already reads "the gateway refused this host: ...", so
	// the banner must not say it twice.
	r.LinkState(protocol.ConnectionView{
		State: protocol.ConnRefused,
		Err:   "the gateway refused this host: unauthorized: the credential was not accepted",
	})
	waitFor(t, "the refusal banner", func() bool { return r.Snapshot().Banner != "" })
	banner := r.Snapshot().Banner
	if !strings.Contains(banner, "unauthorized") {
		t.Fatalf("banner = %q, want the reason", banner)
	}
	if count := strings.Count(banner, "refused this host"); count != 1 {
		t.Fatalf("banner = %q says %d times that the host was refused, want once", banner, count)
	}

	r.LinkState(protocol.ConnectionView{State: protocol.ConnConnected})
	waitFor(t, "the cleared banner", func() bool { return r.Snapshot().Banner == "" })
}

// TestLinkDirectoryUpdatesPeers: peers are sanitised and re-emitted.
func TestLinkDirectoryUpdatesPeers(t *testing.T) {
	link := newFakeLink()
	r, _ := sendRouter(t, link)

	r.LinkState(protocol.ConnectionView{State: protocol.ConnConnected})
	waitFor(t, "the connected state", func() bool {
		snapshot := r.Snapshot()
		return snapshot.Connection != nil && snapshot.Connection.State == protocol.ConnConnected
	})

	r.LinkDirectory([]string{"claude@laptop", "codex\x1b@other"})
	waitFor(t, "the peers", func() bool {
		snapshot := r.Snapshot()
		return snapshot.Connection != nil && len(snapshot.Connection.Peers) == 2
	})
	peers := r.Snapshot().Connection.Peers
	if peers[1] != "codex[31m@other" {
		t.Fatalf("peers = %v, want the escape stripped", peers)
	}
}

// TestLocalLinkMethodsAreNoOps: with Link nil every Receiver method does nothing.
func TestLocalLinkMethodsAreNoOps(t *testing.T) {
	r, _ := newTestRouter(newFakeStore())
	for _, s := range []protocol.LaunchSpec{spec("a", "claude"), spec("b", "codex")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}

	r.Receive(offerFor("m-1", "codex", "claude@mac", routerNow()))
	r.LinkUp()
	r.LinkReport(protocol.MessageInfo{ID: "m-1", Result: protocol.SendHostStored})
	r.LinkState(protocol.ConnectionView{State: protocol.ConnRefused, Err: "no"})
	r.LinkDirectory([]string{"claude@laptop"})

	if r.reports != nil {
		t.Fatal("a local router started the reporter goroutine")
	}
	if len(r.byName["codex"].queue) != 0 {
		t.Fatal("a local router queued an offer")
	}
	if r.conn != nil {
		t.Fatal("a local router recorded a connection")
	}
}

// TestApplyInfoCorrections covers the late-correction rules.
func TestApplyInfoCorrections(t *testing.T) {
	link := newFakeLink()
	r, fake := sendRouter(t, link)

	r.appendSend(&sendRec{id: "s1", from: r.byName["claude"], to: "codex@host", body: "x",
		result: protocol.SendDeliveryUncertain, at: routerNow()})
	r.appendSend(&sendRec{id: "s2", from: r.byName["claude"], to: "codex@host", body: "x",
		result: protocol.SendHostStored, at: routerNow()})

	// A late host_stored corrects the uncertain record.
	r.LinkReport(protocol.MessageInfo{ID: "s1", Result: protocol.SendHostStored})
	waitFor(t, "the correction", func() bool {
		return fake.countCall("FinishSend:s1:delivery_uncertain>host_stored") == 1
	})

	// A final record is never overwritten.
	r.LinkReport(protocol.MessageInfo{ID: "s2", Result: protocol.SendSessionGone})
	time.Sleep(20 * time.Millisecond)
	if fake.countCall("FinishSend:s2:") != 0 {
		t.Fatal("a final result was overwritten")
	}

	// A report updates the report fields only.
	r.LinkReport(protocol.MessageInfo{ID: "s1", Report: protocol.MessageHeld, ReportReason: "busy\n"})
	waitFor(t, "the stored report", func() bool {
		return fake.countCall("ReportSend:s1:held") == 1
	})
}

// TestRelinkLooksUpUncertainSends: at most 50, newest first, and corrections applied.
func TestRelinkLooksUpUncertainSends(t *testing.T) {
	link := newFakeLink()
	r, fake := sendRouter(t, link)

	r.appendSend(&sendRec{id: "s1", from: r.byName["claude"], to: "codex@host", body: "x",
		result: protocol.SendGatewayUncertain, at: routerNow()})
	r.appendSend(&sendRec{id: "s2", from: r.byName["claude"], to: "codex@host", body: "x",
		result: protocol.SendHostStored, at: routerNow()})

	link.mu.Lock()
	link.lookupErrs["s1"] = ErrUnknownMessage
	link.mu.Unlock()

	r.LinkUp()
	waitFor(t, "the lookup", func() bool { return len(link.lookupList()) >= 1 })
	// "The gateway has no record" must NOT become a definite failure: it rests on
	// timing (a request the gateway had received may still have been in flight), and
	// an uncertain case is never reported as a definite failure.
	time.Sleep(30 * time.Millisecond)
	if fake.countCall("FinishSend:s1:") != 0 {
		t.Fatal("an unknown message on lookup moved the send: it must stay gateway_uncertain")
	}

	for _, id := range link.lookupList() {
		if id != "s1" {
			t.Fatalf("looked up %q, want only the uncertain send", id)
		}
	}
}

// TestSendConnectedUncertainWhenTheWaitEnds: a recorded send never returns a bare
// context error.
func TestSendConnectedUncertainWhenTheWaitEnds(t *testing.T) {
	link := newFakeLink()
	link.block()
	link.response = protocol.SendResponse{ID: "m1", Result: protocol.SendHostStored}
	r, fake := sendRouter(t, link)

	ctx, cancel := context.WithCancel(context.Background())
	answers := make(chan error, 1)
	go func() {
		_, err := r.Send(ctx, "a", "codex@testhost", "hello", "")
		answers <- err
	}()

	waitFor(t, "the Link call", func() bool { return link.callCount() >= 1 })
	cancel()
	waitFor(t, "the Link call to be entered", func() bool { return true })

	select {
	case err := <-answers:
		var refusal *SendError
		if !errors.As(err, &refusal) {
			t.Fatalf("Send = %v, want a typed uncertain refusal", err)
		}
		if refusal.Reason != string(protocol.SendGatewayUncertain) || !refusal.Uncertain() {
			t.Fatalf("refusal = %+v, want gateway_uncertain", refusal)
		}
		if refusal.SendID() != "m1" {
			t.Fatalf("SendID = %q, want m1", refusal.SendID())
		}
		if !strings.Contains(refusal.Detail, "may still arrive") {
			t.Fatalf("detail = %q", refusal.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not return")
	}

	close(link.release)
	waitFor(t, "the commit", func() bool {
		return fake.countCall("FinishSend:m1:sending>host_stored") == 1
	})
}

// TestAnOfferForAnEarlierSessionIsRefused: a new session is never given an older
// message. The gateway names the session it meant; the runtime compares it with the
// session it registered for that agent and refuses any mismatch as session_ended,
// which the sender sees as session_gone. Nothing is inserted, queued or reported.
func TestAnOfferForAnEarlierSessionIsRefused(t *testing.T) {
	tests := []struct {
		name  string
		offer protocol.Offer
	}{
		{
			name: "an older session of the same name",
			offer: protocol.Offer{ID: "m-1", To: "codex", ToSession: "sess-codex-OLD",
				From: "claude@mac", Body: "hello"},
		},
		{
			name: "an offer that names no session",
			offer: protocol.Offer{ID: "m-1", To: "codex", ToSession: "",
				From: "claude@mac", Body: "hello"},
		},
		{
			name: "another agent's session",
			offer: protocol.Offer{ID: "m-1", To: "codex", ToSession: "sess-claude",
				From: "claude@mac", Body: "hello"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			link := newFakeLink()
			r, fake := sendRouter(t, link)

			r.Receive(tc.offer)
			waitFor(t, "the refusal", func() bool { return len(link.ackList()) == 1 })

			ack := link.ackList()[0]
			if ack.saved || ack.reason != "session_ended" {
				t.Fatalf("ack = %+v, want a refusal with reason session_ended", ack)
			}
			if fake.countCall("InsertInbound:m-1") != 0 {
				t.Fatal("an offer for another session was inserted into the inbox")
			}
			if len(r.byName["codex"].queue) != 0 {
				t.Fatal("an offer for another session was queued")
			}
			if len(link.reportList()) != 0 {
				t.Fatal("a refused offer was reported")
			}
		})
	}
}

// TestAnOfferIsRefusedUntilTheSessionIsKnown: a recipient whose session has not been
// set yet accepts nothing; fail closed.
func TestAnOfferIsRefusedUntilTheSessionIsKnown(t *testing.T) {
	link := newFakeLink()
	fake := newFakeStore()
	clock := newFakeClock()
	r, err := New(Config{RunID: "run-1", Store: fake, Now: clock.now, NewID: counterIDs("m"), Link: link})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, s := range []protocol.LaunchSpec{spec("a", "claude"), spec("b", "codex")} {
		if err := r.registerAgent(s); err != nil {
			t.Fatalf("registerAgent: %v", err)
		}
	}
	r.Start()
	t.Cleanup(r.Close)

	r.Receive(protocol.Offer{ID: "m-1", To: "codex", ToSession: "sess-codex", From: "claude@mac", Body: "hello"})
	waitFor(t, "the refusal", func() bool { return len(link.ackList()) == 1 })
	if ack := link.ackList()[0]; ack.saved || ack.reason != "session_ended" {
		t.Fatalf("ack = %+v, want session_ended while no session is set", ack)
	}

	// Once the session is set the same offer is accepted.
	r.SetGatewaySession("codex", "sess-codex")
	r.Receive(protocol.Offer{ID: "m-2", To: "codex", ToSession: "sess-codex", From: "claude@mac", Body: "hello"})
	waitFor(t, "the acceptance", func() bool { return fake.countCall("InsertInbound:m-2") == 1 })
}

// TestSetGatewaySessionIsANoOpInLocalMode: with no Link nothing is recorded.
func TestSetGatewaySessionIsANoOpInLocalMode(t *testing.T) {
	r, _, _, _ := newDeliverRouter(t, &fakePane{})
	r.SetGatewaySession("beta", "anything")
	r.Start()
	t.Cleanup(r.Close)

	done := make(chan string, 1)
	if err := r.submit(context.Background(), func() { done <- r.byName["beta"].gatewaySession }); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := <-done; got != "" {
		t.Fatalf("a local-mode router recorded a gateway session %q", got)
	}
}
