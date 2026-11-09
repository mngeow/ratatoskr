// UNTESTED (GR-3): written, never run.

package sidecar

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/gw"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/ipc"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/journal"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/limits"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/rigcli"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeGW struct {
	mu        sync.Mutex
	acks      []ackCall
	statuses  []statusCall
	ackErr    *gw.Error
	ackDelay  time.Duration // advances the fake clock inside Ack
	clock     *fakeClock
	pollOut   []pollStep
	sendOut   gw.SendResult
	sendErr   *gw.Error
	sessionOK bool
}

type ackCall struct {
	id     string
	saved  bool
	reason string
}
type statusCall struct{ id, state, reason string }
type pollStep struct {
	inbox gw.Inbox
	err   *gw.Error
}

func (f *fakeGW) Discover(context.Context) (gw.Discovery, *gw.Error) {
	return gw.Discovery{Contract: "1.0", Versions: []string{"1"}}, nil
}
func (f *fakeGW) AddSession(_ context.Context, name string) (gw.SessionInfo, []gw.DirectoryEntry, *gw.Error) {
	return gw.SessionInfo{Name: name, Address: name + "@openrig-main"}, nil, nil
}
func (f *fakeGW) Directory(context.Context) ([]gw.DirectoryEntry, *gw.Error) { return nil, nil }
func (f *fakeGW) Poll(ctx context.Context) (gw.Inbox, *gw.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pollOut) == 0 {
		return gw.Inbox{}, &gw.Error{Kind: gw.KindNetwork, Msg: "script exhausted"}
	}
	step := f.pollOut[0]
	f.pollOut = f.pollOut[1:]
	return step.inbox, step.err
}
func (f *fakeGW) Ack(_ context.Context, id string, saved bool, reason string) *gw.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, ackCall{id, saved, reason})
	if f.clock != nil && f.ackDelay > 0 {
		f.clock.advance(f.ackDelay)
	}
	return f.ackErr
}
func (f *fakeGW) ReportStatus(_ context.Context, id, state, reason string) *gw.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, statusCall{id, state, reason})
	return nil
}
func (f *fakeGW) Send(_ context.Context, r gw.SendRequest) (gw.SendResult, *gw.Error) {
	res := f.sendOut
	res.ID = r.ID
	return res, f.sendErr
}

type fakeSeat struct {
	mu     sync.Mutex
	ready  bool
	word   string
	sent   []string
	result rigcli.SendResult
}

func (s *fakeSeat) Version(context.Context) (string, error) { return "rig 0.0.0", nil }
func (s *fakeSeat) Ready(context.Context) (bool, string)    { return s.ready, s.word }
func (s *fakeSeat) Send(_ context.Context, text string) rigcli.SendResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, text)
	return s.result
}

type rig struct {
	sc    *Sidecar
	gw    *fakeGW
	seat  *fakeSeat
	clock *fakeClock
	cfg   *config.Config
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	cfg := &config.Config{
		Participant: "openrig-main", Session: "operator", OperatorSeat: "operator-agent@kernel", StateDir: dir,
		Limits: config.Limits{InlineBodyBytes: 4096, PerSenderPerMinute: 6, TotalPerMinute: 20},
	}
	g := &fakeGW{clock: clock}
	seat := &fakeSeat{ready: true, result: rigcli.SendResult{Outcome: rigcli.Pasted}}
	sc := &Sidecar{Cfg: cfg, GW: g, Seat: seat, Now: clock.now, Sleep: func(context.Context, time.Duration) {}}
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	sc.journal = j
	sc.limiter = limits.New(limits.Config{InlineBodyBytes: 4096, PerSenderPerMinute: 6, TotalPerMinute: 20}, clock.now)
	sc.slot = make(chan struct{}, 1)
	t.Cleanup(sc.Close)
	return &rig{sc: sc, gw: g, seat: seat, clock: clock, cfg: cfg}
}

func offer(id, from, body string) gw.Offer {
	raw, _ := json.Marshal(body)
	return gw.Offer{ID: id, From: from, To: "operator", Body: raw}
}

func TestOfferIsAckedThenTypedOnceThenReported(t *testing.T) {
	r := newRig(t)
	r.sc.handleOffer(context.Background(), offer("m1", "claude@vmb", "hello"), r.clock.now())
	if len(r.gw.acks) != 1 || !r.gw.acks[0].saved {
		t.Fatalf("acks = %+v", r.gw.acks)
	}
	if len(r.seat.sent) != 1 || !strings.HasPrefix(r.seat.sent[0], "Ratatoskr message m1 from claude@vmb") {
		t.Fatalf("typed %q", r.seat.sent)
	}
	if len(r.gw.statuses) != 1 || r.gw.statuses[0].state != "pasted" {
		t.Fatalf("statuses = %+v", r.gw.statuses)
	}
	if last, _ := r.sc.journal.Last("m1"); last.Event != journal.Pasted {
		t.Fatalf("journal last = %v", last.Event)
	}
}

func TestDuplicateIsNeverTypedAgain(t *testing.T) {
	r := newRig(t)
	o := offer("m1", "claude@vmb", "hello")
	r.sc.handleOffer(context.Background(), o, r.clock.now())
	r.sc.handleOffer(context.Background(), o, r.clock.now())
	if len(r.seat.sent) != 1 {
		t.Fatalf("typed %d times", len(r.seat.sent))
	}
}

func TestNotReadyRefusesWithTheWordAndTypesNothing(t *testing.T) {
	r := newRig(t)
	r.seat.ready, r.seat.word = false, rigcli.WordNotRunning
	r.sc.handleOffer(context.Background(), offer("m1", "claude@vmb", "hello"), r.clock.now())
	if len(r.gw.acks) != 1 || r.gw.acks[0].saved || r.gw.acks[0].reason != "operator seat not ready: not running" {
		t.Fatalf("acks = %+v", r.gw.acks)
	}
	if len(r.seat.sent) != 0 {
		t.Fatal("typed into a seat that was not ready")
	}
	if _, seen := r.sc.journal.Last("m1"); seen {
		t.Fatal("a refused offer was journalled as accepted")
	}
}

func TestOversizeAndMalformedAreRefused(t *testing.T) {
	r := newRig(t)
	r.sc.handleOffer(context.Background(), offer("m1", "claude@vmb", strings.Repeat("x", 4097)), r.clock.now())
	r.sc.handleOffer(context.Background(), offer("m2", "NOT AN ADDRESS", "hi"), r.clock.now())
	if len(r.gw.acks) != 2 || r.gw.acks[0].saved || r.gw.acks[1].saved {
		t.Fatalf("acks = %+v", r.gw.acks)
	}
	if !strings.HasPrefix(r.gw.acks[0].reason, "message too large for the operator (limit 4096 bytes)") || r.gw.acks[1].reason != "malformed offer" {
		t.Fatalf("reasons = %+v", r.gw.acks)
	}
	if len(r.seat.sent) != 0 {
		t.Fatal("typed a refused offer")
	}
}

func TestInvalidIDGetsNoAcknowledgement(t *testing.T) {
	r := newRig(t)
	r.sc.handleOffer(context.Background(), offer("bad id!", "claude@vmb", "hi"), r.clock.now())
	if len(r.gw.acks) != 0 || len(r.seat.sent) != 0 {
		t.Fatalf("acks %+v sent %q", r.gw.acks, r.seat.sent)
	}
}

func TestAckFailureMeansNothingIsTyped(t *testing.T) {
	r := newRig(t)
	r.gw.ackErr = &gw.Error{Kind: gw.KindNetwork, Msg: "boom", Wrote: true}
	r.sc.handleOffer(context.Background(), offer("m1", "claude@vmb", "hello"), r.clock.now())
	if len(r.seat.sent) != 0 {
		t.Fatal("typed after the acknowledgement failed")
	}
	if last, _ := r.sc.journal.Last("m1"); last.Event != journal.AckFailed {
		t.Fatalf("journal last = %v", last.Event)
	}
}

func TestNothingIsTypedAfterTheWindow(t *testing.T) {
	r := newRig(t)
	r.gw.ackDelay = 4 * time.Second // the acknowledgement is answered, but at t0 + 4 s
	r.sc.handleOffer(context.Background(), offer("m1", "claude@vmb", "hello"), r.clock.now())
	if len(r.seat.sent) != 0 {
		t.Fatal("typed after the window elapsed")
	}
	if last, _ := r.sc.journal.Last("m1"); last.Event != journal.Expired {
		t.Fatalf("journal last = %v", last.Event)
	}
	if len(r.gw.statuses) != 1 || r.gw.statuses[0].state != "failed" || r.gw.statuses[0].reason != "window elapsed before typing" {
		t.Fatalf("statuses = %+v", r.gw.statuses)
	}
}

func TestUnconfirmedIsReportedAndNeverRetried(t *testing.T) {
	r := newRig(t)
	r.seat.result = rigcli.SendResult{Outcome: rigcli.Unconfirmed, ExitCode: 1, Note: "boom"}
	r.sc.handleOffer(context.Background(), offer("m1", "claude@vmb", "hello"), r.clock.now())
	if len(r.seat.sent) != 1 || r.gw.statuses[0].state != "unconfirmed" {
		t.Fatalf("sent %d statuses %+v", len(r.seat.sent), r.gw.statuses)
	}
}

func TestRecoveryNeverTypesAndSettlesEveryUnfinishedOffer(t *testing.T) {
	r := newRig(t)
	for _, rec := range []journal.Record{
		{ID: "t1", Event: journal.Typing, At: r.clock.now()},
		{ID: "a1", Event: journal.Accepted, At: r.clock.now()},
		{ID: "k1", Event: journal.Acked, At: r.clock.now()},
		{ID: "old", Event: journal.Typing, At: r.clock.now().Add(-48 * time.Hour)},
	} {
		if err := r.sc.journal.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	r.sc.recover(context.Background())
	if len(r.seat.sent) != 0 {
		t.Fatal("recovery typed")
	}
	want := map[string]journal.Event{"t1": journal.Unconfirmed, "a1": journal.AckFailed, "k1": journal.Expired, "old": journal.Unconfirmed}
	for id, event := range want {
		if last, _ := r.sc.journal.Last(id); last.Event != event {
			t.Errorf("%s: last = %v, want %v", id, last.Event, event)
		}
	}
	reported := map[string]string{}
	for _, s := range r.gw.statuses {
		reported[s.id] = s.state
	}
	if reported["t1"] != "unconfirmed" || reported["k1"] != "failed" || reported["a1"] != "" || reported["old"] != "" {
		t.Fatalf("reported = %+v", reported)
	}
}

func TestPollLoopStopsOnRevokeAndReplace(t *testing.T) {
	for _, tc := range []struct {
		name string
		step pollStep
		code int
		why  string
	}{
		{"revoked", pollStep{err: &gw.Error{Kind: gw.KindRevoked, Msg: "revoked"}}, ExitRevoked, "revoked"},
		{"replaced", pollStep{inbox: gw.Inbox{Replaced: true}}, ExitReplaced, "replaced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.gw.pollOut = []pollStep{tc.step}
			if code := r.sc.PollLoop(context.Background()); code != tc.code {
				t.Fatalf("exit = %d, want %d", code, tc.code)
			}
			st, ok := ReadStopped(r.cfg.StoppedPath())
			if !ok || st.Reason != tc.why {
				t.Fatalf("stopped = %+v %v", st, ok)
			}
		})
	}
}

func TestHandleSendMapsResults(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		result  string
		outcome string
	}{
		{"host_stored", ipc.OutcomeStored},
		{"session_gone", ipc.OutcomeNotDelivered},
		{"unknown_recipient", ipc.OutcomeNotDelivered},
		{"destination_unreachable", ipc.OutcomeNotDelivered},
		{"delivery_uncertain", ipc.OutcomeUncertain},
		{"gateway_uncertain", ipc.OutcomeUncertain},
		{"something_new", ipc.OutcomeUncertain},
	} {
		r.gw.sendOut = gw.SendResult{Result: tc.result}
		resp := r.sc.Handle(context.Background(), ipc.Request{V: 1, Op: ipc.OpSend, ID: "c1", To: "claude@vmb", Body: "hi"})
		if resp.Outcome != tc.outcome {
			t.Errorf("%s -> %s, want %s", tc.result, resp.Outcome, tc.outcome)
		}
	}
	// A network failure after the request was written is uncertain; before, it is not sent.
	r.gw.sendErr = &gw.Error{Kind: gw.KindNetwork, Wrote: true, Msg: "x"}
	if resp := r.sc.Handle(context.Background(), ipc.Request{V: 1, Op: ipc.OpSend, ID: "c1", To: "claude@vmb", Body: "hi"}); resp.Outcome != ipc.OutcomeUncertain {
		t.Errorf("wrote: %s", resp.Outcome)
	}
	r.gw.sendErr = &gw.Error{Kind: gw.KindNetwork, Wrote: false, Msg: "x"}
	if resp := r.sc.Handle(context.Background(), ipc.Request{V: 1, Op: ipc.OpSend, ID: "c1", To: "claude@vmb", Body: "hi"}); resp.Outcome != ipc.OutcomeNotSent {
		t.Errorf("not wrote: %s", resp.Outcome)
	}
	// Validation.
	if resp := r.sc.Handle(context.Background(), ipc.Request{V: 1, Op: ipc.OpSend, ID: "c1", To: "bare", Body: "hi"}); resp.Outcome != ipc.OutcomeRejected {
		t.Errorf("bare to: %s", resp.Outcome)
	}
}
