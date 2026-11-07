package gateway

// Untested: under GR-3 nothing here has been run. Everything is in memory or through an httptest
// recorder; no socket is opened.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// networkFixture is a server with one paired, streaming host and one stored report.
type networkFixture struct {
	server     *Server
	store      *Store
	credential string
}

// newNetworkFixture pairs "laptop" and stores one report for it.
func newNetworkFixture(t *testing.T, now time.Time) *networkFixture {
	t.Helper()

	store, _ := openStore(t, false)
	credential := pairHostFrom(t, store, "laptop", now)

	server := NewServer(store, Options{
		Now:   func() time.Time { return now },
		NewID: func() (string, error) { return "session-1", nil },
	})
	server.streamOpen = func(string) bool { return true }
	// A millisecond tick, so the shared-ticker test does not wait a second.
	server.networkTick = 10 * time.Millisecond

	fixture := &networkFixture{server: server, store: store, credential: credential}
	return fixture
}

// put stores one report through the real route.
func (f *networkFixture) put(t *testing.T, request protocol.ReportRequest) {
	t.Helper()
	body := mustJSON(t, request)
	if recorder := call(f.server, "PUT", protocol.ReportPath, version(), f.credential, body); recorder.Code != 204 {
		t.Fatalf("report = %d: %s", recorder.Code, recorder.Body.String())
	}
}

// network GETs the snapshot and decodes it.
func (f *networkFixture) network(t *testing.T, query string) (protocol.NetworkSnapshot, int) {
	t.Helper()
	path := protocol.NetworkPath
	if query != "" {
		path += "?" + query
	}
	recorder := call(f.server, http.MethodGet, path, version(), f.credential, "")
	if recorder.Code != http.StatusOK {
		return protocol.NetworkSnapshot{}, recorder.Code
	}
	var snapshot protocol.NetworkSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode the snapshot: %v", err)
	}
	return snapshot, recorder.Code
}

// TestNetworkSnapshotJoinsTheDirectory: a directory session with a report, and a reported session
// the directory does not list.
func TestNetworkSnapshotJoinsTheDirectory(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")

	fixture.put(t, reportAt(
		sessionAt("session-1", "local-1", "claude"),
		sessionAt("nothing-registered", "local-2", "codex"),
	))

	snapshot, code := fixture.network(t, "")
	if code != http.StatusOK {
		t.Fatalf("network = %d", code)
	}
	if snapshot.Schema != protocol.NetworkSchema {
		t.Fatalf("schema = %d", snapshot.Schema)
	}
	if snapshot.Gateway.Protocol != protocol.WireVersion {
		t.Fatalf("protocol = %d, want %d", snapshot.Gateway.Protocol, protocol.WireVersion)
	}
	if snapshot.Gateway.LiveWindowMS != liveWindow.Milliseconds() {
		t.Fatalf("live_window_ms = %d", snapshot.Gateway.LiveWindowMS)
	}
	if snapshot.Gateway.ReportStaleAfterMS != reportStaleAfter.Milliseconds() {
		t.Fatalf("report_stale_after_ms = %d", snapshot.Gateway.ReportStaleAfterMS)
	}

	if len(snapshot.Hosts) != 1 {
		t.Fatalf("hosts = %+v", snapshot.Hosts)
	}
	host := snapshot.Hosts[0]
	if host.Host != "laptop" || host.ReportStatus != protocol.ReportStatusFresh {
		t.Fatalf("host = %+v", host)
	}
	if !host.Connection.Live || !host.Connection.StreamOpen {
		t.Fatalf("connection = %+v", host.Connection)
	}
	if host.Report == nil || host.Report.Seq != 1 {
		t.Fatalf("report = %+v", host.Report)
	}

	if len(snapshot.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want two", snapshot.Sessions)
	}

	var registered, unregistered *protocol.NetworkSession
	for i := range snapshot.Sessions {
		switch snapshot.Sessions[i].SessionID {
		case "session-1":
			registered = &snapshot.Sessions[i]
		case "nothing-registered":
			unregistered = &snapshot.Sessions[i]
		}
	}
	if registered == nil || unregistered == nil {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}

	if !registered.Directory.Registered || registered.Directory.Live == nil || !*registered.Directory.Live {
		t.Fatalf("registered facts = %+v", registered.Directory)
	}
	if registered.Reported == nil || registered.Reported.LocalID != "local-1" {
		t.Fatalf("registered report = %+v", registered.Reported)
	}

	if unregistered.Directory.Registered {
		t.Fatalf("an unlisted session was reported as registered: %+v", unregistered)
	}
	if unregistered.Directory.Live != nil {
		t.Fatalf("live is set for an unregistered session: %+v", unregistered.Directory)
	}
	if unregistered.Address != "codex@laptop" {
		t.Fatalf("address = %q", unregistered.Address)
	}
	if unregistered.Reported == nil {
		t.Fatal("an unregistered reported session lost its report")
	}
}

// TestNetworkJoinIsHostScoped: a report can never describe another host's row.
func TestNetworkJoinIsHostScoped(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")

	// Another host claims the same session id.
	other := pairHostFrom(t, fixture.store, "desktop", gatewayNow)
	otherReport := mustJSON(t, reportAt(sessionAt("session-1", "local-x", "claude")))
	if recorder := call(fixture.server, "PUT", protocol.ReportPath, version(), other, otherReport); recorder.Code != 204 {
		t.Fatalf("the other host's report = %d", recorder.Code)
	}

	snapshot, _ := fixture.network(t, "")
	for _, session := range snapshot.Sessions {
		if session.Host != "laptop" {
			continue
		}
		if session.Reported != nil {
			t.Fatalf("laptop's session picked up another host's report: %+v", session.Reported)
		}
	}
}

// TestNetworkStaleness: fresh by age, stale by age, stale when the host is not live, and none when
// there is no report.
func TestNetworkStaleness(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")
	fixture.put(t, reportAt(sessionAt("session-1", "local-1", "claude")))

	t.Run("fresh while young and live", func(t *testing.T) {
		snapshot, _ := fixture.network(t, "")
		if snapshot.Hosts[0].ReportStatus != protocol.ReportStatusFresh {
			t.Fatalf("status = %q", snapshot.Hosts[0].ReportStatus)
		}
		var reported *protocol.ReportedSession
		for _, session := range snapshot.Sessions {
			if session.Reported != nil {
				reported = session.Reported
			}
		}
		if reported == nil || reported.Stale {
			t.Fatalf("reported = %+v, want fresh", reported)
		}
	})

	t.Run("stale by age", func(t *testing.T) {
		later := gatewayNow.Add(reportStaleAfter + time.Second)
		fixture.server.opts.Now = func() time.Time { return later }
		// A heartbeat keeps the host live, so only the age can make it stale.
		if err := fixture.store.TouchHeartbeat(context.Background(), "laptop", later); err != nil {
			t.Fatalf("TouchHeartbeat: %v", err)
		}

		snapshot, _ := fixture.network(t, "")
		if snapshot.Hosts[0].ReportStatus != protocol.ReportStatusStale {
			t.Fatalf("status = %q, want stale", snapshot.Hosts[0].ReportStatus)
		}
	})

	t.Run("stale when the host is not live", func(t *testing.T) {
		fixture.server.streamOpen = func(string) bool { return false }
		snapshot, _ := fixture.network(t, "")
		if snapshot.Hosts[0].ReportStatus != protocol.ReportStatusStale {
			t.Fatalf("status = %q, want stale", snapshot.Hosts[0].ReportStatus)
		}
		if snapshot.Hosts[0].Connection.Live {
			t.Fatal("a host with no stream is reported live")
		}
	})

	t.Run("none with no report", func(t *testing.T) {
		empty := newNetworkFixture(t, gatewayNow)
		snapshot, _ := empty.network(t, "")
		if snapshot.Hosts[0].ReportStatus != protocol.ReportStatusNone {
			t.Fatalf("status = %q, want none", snapshot.Hosts[0].ReportStatus)
		}
		if snapshot.Hosts[0].Report != nil {
			t.Fatalf("report = %+v, want nil", snapshot.Hosts[0].Report)
		}
	})
}

// TestNetworkRevision: content bumps, ages and sequence do not, and liveness does.
func TestNetworkRevision(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")
	fixture.put(t, reportAt(sessionAt("session-1", "local-1", "claude")))

	first, _ := fixture.network(t, "")
	if first.Revision == 0 {
		t.Fatal("the first revision is zero")
	}

	// A minute later, nothing but the ages changed.
	later := gatewayNow.Add(time.Minute)
	fixture.server.opts.Now = func() time.Time { return later }
	if err := fixture.store.TouchHeartbeat(context.Background(), "laptop", later); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	second, _ := fixture.network(t, "")
	if second.Revision != first.Revision {
		t.Fatalf("revision moved on ages and harness heartbeat: %d -> %d", first.Revision, second.Revision)
	}

	// The same report again, with a higher sequence, is not a content change.
	same := reportAt(sessionAt("session-1", "local-1", "claude"))
	same.Seq = 2
	fixture.put(t, same)
	third, _ := fixture.network(t, "")
	if third.Revision != first.Revision {
		t.Fatalf("revision moved on a repeated report: %d -> %d", first.Revision, third.Revision)
	}

	// A state change is a content change.
	changed := reportAt(sessionAt("session-1", "local-1", "claude"))
	changed.Seq = 3
	changed.Sessions[0].State = protocol.SessionStateIdle
	fixture.put(t, changed)
	fourth, _ := fixture.network(t, "")
	if fourth.Revision <= first.Revision {
		t.Fatalf("revision did not move on a state change: %d -> %d", first.Revision, fourth.Revision)
	}

	// A liveness change is one too, with no event to announce it.
	fixture.server.streamOpen = func(string) bool { return false }
	fifth, _ := fixture.network(t, "")
	if fifth.Revision <= fourth.Revision {
		t.Fatalf("revision did not move on a liveness change: %d -> %d", fourth.Revision, fifth.Revision)
	}
}

// TestNetworkRevisionOnDirectoryChange: a registration changes the snapshot and wakes a waiter.
func TestNetworkRevisionOnDirectoryChange(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	fixture.put(t, reportAt(sessionAt("session-1", "local-1", "claude")))

	first, _ := fixture.network(t, "")

	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")
	fixture.server.kickNetwork()
	waitFor(t, "the directory change to reach a revision", func() bool {
		snapshot, _ := fixture.network(t, "")
		return snapshot.Revision > first.Revision
	})
}

// TestNetworkLongPoll: a matching since waits, then answers 200 when the revision moves.
func TestNetworkLongPoll(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")

	snapshot, _ := fixture.network(t, "")

	answered := make(chan int, 1)
	go func() {
		recorder := call(fixture.server, http.MethodGet,
			protocol.NetworkPath+"?since="+utoa(snapshot.Revision)+"&wait_ms=5000",
			version(), fixture.credential, "")
		answered <- recorder.Code
	}()

	waitFor(t, "the poll to park", func() bool {
		fixture.server.network.mu.Lock()
		defer fixture.server.network.mu.Unlock()
		return fixture.server.network.waiting > 0
	})

	// A report changes the snapshot, which must wake the poll.
	changed := reportAt(sessionAt("session-1", "local-1", "claude"))
	changed.Seq = 7
	fixture.put(t, changed)

	select {
	case code := <-answered:
		if code != http.StatusOK {
			t.Fatalf("the woke poll answered %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poll never woke")
	}
}

// TestNetworkLongPollTimeout: an unchanged revision answers 204 when the wait expires.
func TestNetworkLongPollTimeout(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")

	snapshot, _ := fixture.network(t, "")
	recorder := call(fixture.server, http.MethodGet,
		protocol.NetworkPath+"?since="+utoa(snapshot.Revision)+"&wait_ms=30",
		version(), fixture.credential, "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("poll = %d, want 204", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("a 204 carried a body: %q", recorder.Body.String())
	}
}

// TestNetworkSinceGreaterThanRevision: a client that outlived a restart answers at once.
func TestNetworkSinceGreaterThanRevision(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	snapshot, code := fixture.network(t, "since=9999&wait_ms=5000")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if snapshot.Revision == 0 || snapshot.Revision >= 9999 {
		t.Fatalf("revision = %d", snapshot.Revision)
	}
}

// TestNetworkParameters: the bad values are 400, and a missing one is not.
func TestNetworkParameters(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)

	bad := []string{
		"since=",
		"since=-1",
		"since=abc",
		"wait_ms=",
		"wait_ms=-5",
		"wait_ms=abc",
	}
	for _, query := range bad {
		t.Run(query, func(t *testing.T) {
			_, code := fixture.network(t, query)
			if code != http.StatusBadRequest {
				t.Fatalf("%q answered %d, want 400", query, code)
			}
		})
	}

	// The wait is clamped rather than refused.
	if _, code := fixture.network(t, "wait_ms=999999"); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("a huge wait answered %d", code)
	}
}

// TestNetworkOneSharedTicker: two parked polls share one ticker, and it stops after both leave.
func TestNetworkOneSharedTicker(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")

	snapshot, _ := fixture.network(t, "")
	since := utoa(snapshot.Revision)

	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			recorder := call(fixture.server, http.MethodGet,
				protocol.NetworkPath+"?since="+since+"&wait_ms=40",
				version(), fixture.credential, "")
			results <- recorder.Code
		}()
	}

	waitFor(t, "both polls to park", func() bool {
		fixture.server.network.mu.Lock()
		defer fixture.server.network.mu.Unlock()
		return fixture.server.network.waiting == 2
	})

	fixture.server.network.mu.Lock()
	runs := fixture.server.network.tickerRuns
	fixture.server.network.mu.Unlock()
	if runs != 1 {
		t.Fatalf("the shared ticker started %d times, want 1", runs)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-results:
		case <-time.After(5 * time.Second):
			t.Fatal("a poll did not finish")
		}
	}

	waitFor(t, "the ticker to stop", func() bool {
		fixture.server.network.mu.Lock()
		defer fixture.server.network.mu.Unlock()
		return !fixture.server.network.ticking
	})
}

// TestNetworkUnpairDropsTheReport: the epoch is the pairing, so an unpair removes the report.
func TestNetworkUnpairDropsTheReport(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")
	fixture.put(t, reportAt(sessionAt("session-1", "local-1", "claude")))

	if _, ok := fixture.server.network.reports.get("laptop"); !ok {
		t.Fatal("the report was not stored")
	}

	if err := fixture.store.Unpair(context.Background(), "laptop", gatewayNow); err != nil {
		t.Fatalf("Unpair: %v", err)
	}

	// The next computation prunes it.
	if _, code := fixture.network(t, ""); code != http.StatusOK {
		t.Fatalf("network = %d", code)
	}
	if _, ok := fixture.server.network.reports.get("laptop"); ok {
		t.Fatal("an unpaired host's report survived")
	}
}

// TestNetworkShutdownWakesAParkedPoll: closing the gateway answers the poll with 204.
func TestNetworkShutdownWakesAParkedPoll(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	snapshot, _ := fixture.network(t, "")

	answered := make(chan int, 1)
	go func() {
		recorder := call(fixture.server, http.MethodGet,
			protocol.NetworkPath+"?since="+utoa(snapshot.Revision)+"&wait_ms=10000",
			version(), fixture.credential, "")
		answered <- recorder.Code
	}()

	waitFor(t, "the poll to park", func() bool {
		fixture.server.network.mu.Lock()
		defer fixture.server.network.mu.Unlock()
		return fixture.server.network.waiting > 0
	})

	if err := fixture.server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case code := <-answered:
		if code != http.StatusNoContent {
			t.Fatalf("a shut-down poll answered %d, want 204", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a shut-down poll never left")
	}
}

// TestChiRoutes: every route answers, and the bare error shapes survive.
func TestChiRoutes(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)

	routes := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{method: http.MethodPost, path: protocol.ConnectPath, want: http.StatusOK},
		{method: http.MethodPut, path: protocol.ReportPath, body: mustJSON(t, reportAt()), want: http.StatusNoContent},
		{method: http.MethodGet, path: protocol.NetworkPath, want: http.StatusOK},
		{method: http.MethodPost, path: protocol.SessionsPath, body: `{"name":"claude"}`, want: http.StatusOK},
		{method: http.MethodDelete, path: protocol.SessionsPath, want: http.StatusNoContent},
		{method: http.MethodDelete, path: protocol.SessionsPath + "/claude", want: http.StatusNoContent},
		{method: http.MethodPost, path: "/v1/heartbeat", want: http.StatusNoContent},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			recorder := call(fixture.server, route.method, route.path, version(), fixture.credential, route.body)
			if recorder.Code != route.want {
				t.Fatalf("%s %s = %d, want %d: %s", route.method, route.path, recorder.Code, route.want, recorder.Body.String())
			}
		})
	}

	// An unknown path is the wire error shape.
	recorder := call(fixture.server, http.MethodGet, "/v1/nope", version(), fixture.credential, "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown route = %d", recorder.Code)
	}
	if code := wireCode(t, recorder); code != protocol.WireErrNotFound {
		t.Fatalf("unknown route code = %q", code)
	}

	// A wrong method keeps Allow and the wire shape.
	if recorder := call(fixture.server, http.MethodPost, protocol.NetworkPath, version(), fixture.credential, ""); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method = %d, want 405", recorder.Code)
	} else if recorder.Header().Get("Allow") == "" {
		t.Fatal("a 405 lost its Allow header")
	}

	// An escaped name is judged as it was meant.
	if recorder := call(fixture.server, http.MethodDelete, protocol.SessionsPath+"/co%64ex", version(), fixture.credential, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("escaped name = %d, want 204", recorder.Code)
	}
}

// TestNetworkRoutesNeedACredential: both new routes are behind withAuth.
func TestNetworkRoutesNeedACredential(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)

	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPut, path: protocol.ReportPath, body: mustJSON(t, reportAt())},
		{method: http.MethodGet, path: protocol.NetworkPath},
	} {
		recorder := call(fixture.server, route.method, route.path, version(), "wrong-credential", route.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a credential = %d, want 401", route.method, route.path, recorder.Code)
		}
	}
}

// utoa renders a revision for a query string.
func utoa(value uint64) string {
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

// ensure strings is used even if a case above is trimmed.
var _ = strings.TrimSpace

// TestNetworkNullsArePresentNotOmitted reads the RAW JSON: decoding into a nil pointer cannot tell
// "null" from "absent", and the contract says null.
func TestNetworkNullsArePresentNotOmitted(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)
	startSessionsForTest(t, fixture.store, "laptop", gatewayNow, "claude")

	recorder := call(fixture.server, http.MethodGet, protocol.NetworkPath, version(), fixture.credential, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("network = %d", recorder.Code)
	}

	var raw struct {
		Hosts    []map[string]json.RawMessage `json:"hosts"`
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Hosts) != 1 || len(raw.Sessions) != 1 {
		t.Fatalf("hosts=%d sessions=%d", len(raw.Hosts), len(raw.Sessions))
	}
	if value, present := raw.Hosts[0]["report"]; !present || string(value) != "null" {
		t.Fatalf("host report = %q (present %v), want present null", value, present)
	}
	if value, present := raw.Sessions[0]["reported"]; !present || string(value) != "null" {
		t.Fatalf("session reported = %q (present %v), want present null", value, present)
	}
}

// TestNetworkListsReportedSessionsWithNoGatewayID: a report whose sessions all lack a gateway ID
// (a session whose add failed) is still listed, registered=false.
func TestNetworkListsReportedSessionsWithNoGatewayID(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)

	fixture.put(t, reportAt(sessionAt("", "local-1", "claude")))

	snapshot, status := fixture.network(t, "")
	if status != http.StatusOK {
		t.Fatalf("network = %d", status)
	}
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want the one unregistered session", snapshot.Sessions)
	}
	session := snapshot.Sessions[0]
	if session.Directory.Registered || session.Reported == nil || session.Reported.LocalID != "local-1" {
		t.Fatalf("session = %+v", session)
	}
	if session.SessionID != "" {
		t.Fatalf("session_id = %q, want empty for a session with no gateway registration", session.SessionID)
	}
}

// TestNetworkPlainGetWithoutWaitDoesNotParkOrTick: since with no wait is answered 204 before the
// poll parks, so the shared ticker is never started for it.
func TestNetworkPlainGetWithoutWaitDoesNotParkOrTick(t *testing.T) {
	fixture := newNetworkFixture(t, gatewayNow)

	snapshot, _ := fixture.network(t, "")
	recorder := call(fixture.server, http.MethodGet, protocol.NetworkPath+"?since="+utoa(snapshot.Revision), version(), fixture.credential, "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unchanged since = %d, want 204", recorder.Code)
	}

	fixture.server.network.mu.Lock()
	runs := fixture.server.network.tickerRuns
	fixture.server.network.mu.Unlock()
	if runs != 0 {
		t.Fatalf("ticker started %d times for a request that does not wait", runs)
	}
}
