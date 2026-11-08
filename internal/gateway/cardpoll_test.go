package gateway

// Tests for full-body card polling every 5 seconds (contract section 16). UNTESTED: written, not run
// (GR-3).

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func TestPollIntervalDefaultAndRange(t *testing.T) {
	if DefaultCardPollInterval != 5*time.Second {
		t.Fatalf("default = %v", DefaultCardPollInterval)
	}
	for _, tc := range []struct{ in, want time.Duration }{
		{0, 5 * time.Second},
		{time.Second, 2 * time.Second},
		{2 * time.Second, 2 * time.Second},
		{30 * time.Second, 30 * time.Second},
		{2 * time.Hour, time.Hour},
	} {
		if got := ClampCardPollInterval(tc.in); got != tc.want {
			t.Fatalf("Clamp(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestPollTimeoutsAreShorterThanTheInterval: a hung host must not outlast its tick.
func TestPollTimeoutsAreShorterThanTheInterval(t *testing.T) {
	for _, tc := range []struct{ interval, total time.Duration }{
		{2 * time.Second, 1600 * time.Millisecond},
		{5 * time.Second, 4 * time.Second},
		{60 * time.Second, 5 * time.Second},
	} {
		connect, total := cardTimeouts(tc.interval)
		if total != tc.total || total >= tc.interval || connect != total*3/5 {
			t.Fatalf("interval %v: connect %v total %v, want total %v", tc.interval, connect, total, tc.total)
		}
	}
}

// TestFixedRateScheduleDoesNotDrift: lateness does not accumulate, and a host far behind starts over.
func TestFixedRateScheduleDoesNotDrift(t *testing.T) {
	start := time.Unix(1700000000, 0)
	interval := 5 * time.Second
	due := start
	// Every scan is 200 ms late; after 100 polls the schedule is still on the 5 s grid.
	for i := 0; i < 100; i++ {
		now := due.Add(200 * time.Millisecond)
		due = nextDue(due, now, interval)
	}
	if want := start.Add(100 * interval); !due.Equal(want) {
		t.Fatalf("after 100 late scans next due = %v, want %v", due.Sub(start), want.Sub(start))
	}
	// More than one interval behind: re-based to now.
	now := start.Add(time.Minute)
	if got := nextDue(start, now, interval); !got.Equal(now.Add(interval)) {
		t.Fatalf("re-base = %v", got.Sub(start))
	}
}

// pollFixture is a gateway store with one paired host and a stub card endpoint whose answers the test sets.
type pollFixture struct {
	store      *Store
	target     CardTarget
	credential string
	poller     *cardPoller
	clock      time.Time
	changes    int

	mu       sync.Mutex
	body     []byte
	status   int
	requests []*http.Request
}

func newPollFixture(t *testing.T) *pollFixture {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	f := &pollFixture{status: 200, clock: time.Unix(1700000000, 0)}
	stub := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r)
		if f.status != 200 {
			w.WriteHeader(f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.body)
	}))
	stub.Listener.Close()
	stub.Listener = listener
	stub.Start()
	t.Cleanup(stub.Close)

	addr := netip.MustParseAddrPort(listener.Addr().String())
	f.body, _ = validTargetCard(t, "http://"+addr.String()+protocol.CardPath)

	f.store, _ = openStore(t, false)
	f.credential = pairHostFrom(t, f.store, "vmb", f.clock)
	f.target = CardTarget{Host: "vmb", Scheme: "http", Addr: addr, Class: ClassLoopback}
	if _, err := f.store.SetCardTarget(context.Background(), f.target, f.clock); err != nil {
		t.Fatalf("SetCardTarget: %v", err)
	}
	targets, _, err := f.store.CardTargets(context.Background())
	if err != nil || len(targets) != 1 {
		t.Fatalf("CardTargets: %v %v", targets, err)
	}
	f.target = targets[0]
	f.poller = newCardPoller(f.store, 0, func() time.Time { return f.clock }, nil, func() { f.changes++ })
	return f
}

// poll advances the clock and makes one poll.
func (f *pollFixture) poll(after time.Duration) CardRow {
	f.clock = f.clock.Add(after)
	f.poller.pollOne(context.Background(), f.target)
	rows, _ := f.store.CardRows(context.Background())
	return rows["vmb"]
}

func (f *pollFixture) attempts(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM poll_attempts WHERE host='vmb'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *pollFixture) revisions(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM card_revisions WHERE host='vmb'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEveryPollIsAnUnconditionalFull200: the request carries no If-None-Match, the default interval is 5 s,
// an unchanged 200 makes no revision, advances last_ok_at, adds no attempt row and does not signal a
// change; the graph built after it marks the card fresh.
func TestEveryPollIsAnUnconditionalFull200(t *testing.T) {
	f := newPollFixture(t)
	start := f.clock

	first := f.poll(0)
	if first.CardJSON == "" || first.LastResult != protocol.PollOK {
		t.Fatalf("first poll: %+v", first)
	}
	if f.changes != 1 || f.attempts(t) != 1 {
		t.Fatalf("first poll: changes %d, attempts %d", f.changes, f.attempts(t))
	}

	// Ten minutes later, so a graph that did not see this poll would be stale or offline.
	second := f.poll(10 * time.Minute)
	if second.LastResult != protocol.PollOK || second.CardJSON != first.CardJSON || second.Hash != first.Hash {
		t.Fatalf("second poll: %+v", second)
	}
	if !second.LastOKAt.After(first.LastOKAt) || !second.LastOKAt.Equal(time.UnixMilli(f.clock.UnixMilli())) {
		t.Fatalf("last_ok_at %v did not advance from %v", second.LastOKAt, first.LastOKAt)
	}
	if f.revisions(t) != 0 {
		t.Fatalf("an unchanged 200 made %d revisions", f.revisions(t))
	}
	if f.changes != 1 || f.attempts(t) != 1 {
		t.Fatalf("an unchanged 200 signalled or logged: changes %d, attempts %d", f.changes, f.attempts(t))
	}

	f.mu.Lock()
	for i, request := range f.requests {
		if got := request.Header.Get("If-None-Match"); got != "" {
			t.Fatalf("request %d sent If-None-Match %q", i, got)
		}
	}
	f.mu.Unlock()

	read := f.clock.Add(time.Second)
	server := NewServer(f.store, Options{Now: func() time.Time { return read }})
	server.streamOpen = func(string) bool { return true }
	snapshot := graphOf(t, server, f.credential)
	if got := snapshot.Hosts[0].Card; got.State != protocol.CardFresh || got.Card == nil {
		t.Fatalf("card after an unchanged 200 = %+v, want fresh", got)
	}
	_ = start
}

// TestAChangedCardMakesOneRevisionAndSignals.
func TestAChangedCardMakesOneRevisionAndSignals(t *testing.T) {
	f := newPollFixture(t)
	f.poll(0)
	before := f.changes
	f.mu.Lock()
	f.body, _ = validTargetCard(t, "http://"+f.target.Addr.String()+protocol.CardPath)
	f.body = []byte(strings.Replace(string(f.body), `"version":"4"`, `"version":"5"`, 1))
	f.mu.Unlock()

	row := f.poll(5 * time.Second)
	if row.Hash != card.Hash(f.body) || f.revisions(t) != 1 || f.changes != before+1 {
		t.Fatalf("changed card: hash ok %v, revisions %d, changes %d->%d", row.Hash == card.Hash(f.body), f.revisions(t), before, f.changes)
	}
	if f.attempts(t) != 2 {
		t.Fatalf("a card change must log an attempt: %d", f.attempts(t))
	}
}

// TestAFailureAfterSuccessKeepsTheCardAndTheNextSuccessIsLogged: attempts are written for every failure and
// the first success after one, never for consecutive identical successes.
func TestAFailureAfterSuccessKeepsTheCardAndTheNextSuccessIsLogged(t *testing.T) {
	f := newPollFixture(t)
	good := f.poll(0)
	f.poll(5 * time.Second)
	f.poll(5 * time.Second)
	if f.attempts(t) != 1 {
		t.Fatalf("identical successes logged %d attempts, want 1", f.attempts(t))
	}

	f.mu.Lock()
	f.status = 503
	f.mu.Unlock()
	changesBefore := f.changes
	failed := f.poll(5 * time.Second)
	if failed.CardJSON != good.CardJSON || failed.LastResult != protocol.PollHTTPStatus || failed.LastReason != reasonNoCard {
		t.Fatalf("after a 503: %+v", failed)
	}
	if !failed.LastOKAt.Equal(good.LastOKAt) && !failed.LastOKAt.After(good.LastOKAt) {
		t.Fatalf("last_ok_at went backwards: %v", failed.LastOKAt)
	}
	if f.changes != changesBefore+1 {
		t.Fatalf("a new failure must signal: %d -> %d", changesBefore, f.changes)
	}
	f.poll(5 * time.Second) // the same failure again: logged, not a new signal
	if f.attempts(t) != 3 {
		t.Fatalf("failures logged %d attempts, want 3 in total", f.attempts(t))
	}

	f.mu.Lock()
	f.status = 200
	f.mu.Unlock()
	f.poll(5 * time.Second)
	if f.attempts(t) != 4 {
		t.Fatalf("the first success after a failure must be logged: %d", f.attempts(t))
	}
	f.poll(5 * time.Second)
	if f.attempts(t) != 4 {
		t.Fatalf("a second success logged again: %d", f.attempts(t))
	}
}

// graphOf reads the graph as the host would.
func graphOf(t *testing.T, server *Server, credential string) protocol.GraphSnapshot {
	t.Helper()
	recorder := call(server, http.MethodGet, protocol.GraphPath, version(), credential, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("graph = %d: %s", recorder.Code, recorder.Body.String())
	}
	var snapshot protocol.GraphSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil || len(snapshot.Hosts) != 1 {
		t.Fatalf("snapshot: %v %+v", err, snapshot)
	}
	return snapshot
}
