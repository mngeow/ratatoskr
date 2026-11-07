package gateway

// The network snapshot: the directory joined with host reports, one revision counter, and a long
// poll that wakes when the revision moves.
//
// Two rules shape this file. First, liveness is HostLive and nothing else; the snapshot never
// invents a second test. Second, a revision rises when content changes but not when only an age
// changes - a heartbeat every few seconds would otherwise wake every waiting client constantly,
// and the ages in the body already tell a reader how old everything is.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// defaultNetworkTick is how often one shared recompute runs while any long poll is parked.
const defaultNetworkTick = 1 * time.Second // UNTESTED guess

// networkState is the snapshot machinery: the revision, the broadcast channel, and the one shared
// recompute ticker.
type networkState struct {
	mu sync.Mutex

	// reports is every host's latest report.
	reports *reportStore

	// revision rises whenever the snapshot's content changes. It starts at 1 on the first
	// computation, so a client's "since" is always a real revision it has seen.
	revision uint64
	// signature is the content hash of the last snapshot, with the ages and generated time
	// removed. A different signature is what bumps the revision.
	signature string
	// changed is closed and replaced on every bump, which wakes every parked poll.
	changed chan struct{}

	// waiting is how many long polls are parked. The 0 -> 1 transition starts the one shared
	// ticker, and the ticker stops when it returns to 0.
	waiting int
	// ticking records that the shared ticker goroutine is running, so it is never started twice.
	ticking bool
	// tickerRuns counts how many times the shared ticker has been started. A test uses it to prove
	// two parked waiters share one ticker; production only ever increments it.
	tickerRuns int
}

// newNetworkState builds the snapshot machinery.
func newNetworkState() *networkState {
	return &networkState{
		reports: newReportStore(),
		changed: make(chan struct{}),
	}
}

// park registers one parked long poll and starts the shared ticker if it is the first.
//
// The returned function must be deferred: it is the only place the count goes down, and the
// ticker's 0 check is what ends the goroutine.
//
// UNTESTED.
func (s *Server) parkNetwork() func() {
	state := s.network

	state.mu.Lock()
	state.waiting++
	start := !state.ticking
	if start {
		state.ticking = true
		state.tickerRuns++
	}
	period := s.networkTick
	state.mu.Unlock()

	if start {
		go s.networkTicker(period)
	}

	return func() {
		state.mu.Lock()
		state.waiting--
		state.mu.Unlock()
	}
}

// networkTicker is the ONE periodic recompute, shared by every parked poll.
//
// It recomputes with a bounded context so a slow database cannot pile work up, and it ends when
// nobody is waiting or the gateway is closing. A waiter itself reads nothing while parked.
//
// UNTESTED.
func (s *Server) networkTicker(period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-s.closing:
			s.network.mu.Lock()
			s.network.ticking = false
			s.network.mu.Unlock()
			return
		case <-ticker.C:
			// The exit decision and the ticking flag change under one lock: a poll that parks
			// just as the ticker leaves must see ticking false and start a new one, not rely on
			// a ticker that is about to return.
			s.network.mu.Lock()
			if s.network.waiting == 0 {
				s.network.ticking = false
				s.network.mu.Unlock()
				return
			}
			s.network.mu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), directoryReadTimeout)
			_, _ = s.compute(ctx)
			cancel()
		}
	}
}

// broadcast wakes every parked poll by closing the current channel and replacing it.
func (s *Server) broadcastNetwork() {
	state := s.network
	close(state.changed)
	state.changed = make(chan struct{})
}

// compute builds the snapshot and raises the revision when its content changed.
//
// Everything happens under the state's mutex so a revision and the body that carries it cannot
// disagree. The reads are bounded by ctx; a caller that gives up leaves the revision where it was.
//
// UNTESTED.
func (s *Server) compute(ctx context.Context) (protocol.NetworkSnapshot, error) {
	state := s.network
	state.mu.Lock()
	defer state.mu.Unlock()

	// The mutex is held across the reads, so every caller's read is bounded here: a plain GET's
	// request context has no deadline, and one wedged read would otherwise stall every poll and
	// every report kick behind it.
	ctx, cancel := context.WithTimeout(ctx, directoryReadTimeout)
	defer cancel()

	snapshot, err := s.buildSnapshot(ctx)
	if err != nil {
		return protocol.NetworkSnapshot{}, err
	}

	signature, err := snapshotSignature(snapshot)
	if err != nil {
		return protocol.NetworkSnapshot{}, err
	}

	if state.revision == 0 {
		// The first snapshot is revision 1: a client's since=0 is then a real earlier revision.
		state.revision = 1
		state.signature = signature
	} else if signature != state.signature {
		state.revision++
		state.signature = signature
		s.broadcastNetwork()
	}

	snapshot.Revision = state.revision
	return snapshot, nil
}

// buildSnapshot reads the database and assembles the answer. The caller holds the state mutex.
func (s *Server) buildSnapshot(ctx context.Context) (protocol.NetworkSnapshot, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return protocol.NetworkSnapshot{}, err
	}
	rows, err := s.store.Directory(ctx)
	if err != nil {
		return protocol.NetworkSnapshot{}, err
	}

	now := s.opts.Now()

	// A report belongs to the host row it was stored under. An unpair removes the row and a
	// re-pair rewrites its created_at, so this is where a stale report is dropped - with no event
	// to hook, because the unpair command runs in another process.
	positions := make(map[string]int, len(hosts))
	for i, host := range hosts {
		positions[host.Name] = i
	}
	s.network.reports.prune(func(host string, epoch time.Time) bool {
		row, ok := positions[host]
		if !ok {
			return false
		}
		return hosts[row].PairedAt.Equal(epoch)
	})

	// One read of the reports for the whole computation, so a report stored meanwhile cannot make
	// the hosts, the join and the unregistered list disagree.
	kept := make(map[string]storedReport, len(hosts))
	for _, host := range hosts {
		if report, ok := s.network.reports.get(host.Name); ok && report.epoch.Equal(host.PairedAt) {
			kept[host.Name] = report
		}
	}

	snapshot := protocol.NetworkSnapshot{
		Schema:      protocol.NetworkSchema,
		GeneratedAt: protocol.FormatTime(now),
		Gateway: protocol.NetworkGateway{
			Protocol:           protocol.WireVersion,
			StartedAt:          protocol.FormatTime(s.startedAt),
			LiveWindowMS:       liveWindow.Milliseconds(),
			ReportStaleAfterMS: reportStaleAfter.Milliseconds(),
		},
		Hosts:    make([]protocol.NetworkHost, 0, len(hosts)),
		Sessions: make([]protocol.NetworkSession, 0, len(rows)),
	}

	liveByHost := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		streamOpen := s.streamOpen(host.Name)
		live := HostLive(host.LastHeartbeat, streamOpen, now)
		liveByHost[host.Name] = live

		connection := protocol.NetworkConnection{
			Live:       live,
			StreamOpen: streamOpen,
		}
		if !host.LastHeartbeat.IsZero() {
			age := now.Sub(host.LastHeartbeat).Milliseconds()
			connection.LastHeartbeatAt = protocol.FormatTime(host.LastHeartbeat)
			connection.HeartbeatAgeMS = &age
		}

		entry := protocol.NetworkHost{
			Host:         host.Name,
			Connection:   connection,
			ReportStatus: protocol.ReportStatusNone,
		}

		if report, ok := kept[host.Name]; ok {
			age := now.Sub(report.receivedAt).Milliseconds()
			stale := age > reportStaleAfter.Milliseconds() || !live

			entry.ReportStatus = protocol.ReportStatusFresh
			if stale {
				entry.ReportStatus = protocol.ReportStatusStale
			}
			entry.Report = &protocol.HostReport{
				ReceivedAt: protocol.FormatTime(report.receivedAt),
				AgeMS:      age,
				Seq:        report.request.Seq,
				Daemon:     report.request.Daemon,
			}
		}

		snapshot.Hosts = append(snapshot.Hosts, entry)
	}

	// The join: a directory row and a reported session meet on the gateway's session ID, and only
	// on the same host. A report can never touch another host's row.
	reported := make(map[string]map[string]protocol.ReportSession, len(hosts))
	for _, host := range hosts {
		report, ok := kept[host.Name]
		if !ok {
			continue
		}
		bySession := make(map[string]protocol.ReportSession, len(report.request.Sessions))
		for _, session := range report.request.Sessions {
			if session.SessionID == "" {
				continue
			}
			bySession[session.SessionID] = session
		}
		reported[host.Name] = bySession
	}

	seen := make(map[string]map[string]struct{}, len(hosts))

	for _, row := range rows {
		snapshot.Sessions = append(snapshot.Sessions, s.networkSession(row, reported[row.Host], kept[row.Host].receivedAt, now))
		if seen[row.Host] == nil {
			seen[row.Host] = make(map[string]struct{}, 2)
		}
		seen[row.Host][row.SessionID] = struct{}{}
	}

	// A reported session the directory does not list still exists as far as its host is
	// concerned, and the contract includes it with registered=false.
	for _, host := range hosts {
		// Guard on whether a valid report exists, not on whether any session has a gateway ID:
		// a session whose add failed has none, and still has to be listed.
		stored, ok := kept[host.Name]
		if !ok {
			continue
		}

		for _, session := range stored.request.Sessions {
			if session.SessionID != "" {
				if _, listed := seen[host.Name][session.SessionID]; listed {
					continue
				}
			}
			snapshot.Sessions = append(snapshot.Sessions, protocol.NetworkSession{
				Address:   session.Name + "@" + host.Name,
				Host:      host.Name,
				Name:      session.Name,
				SessionID: session.SessionID,
				Directory: protocol.DirectoryFacts{Registered: false},
				Reported:  reportedSession(session, stored.receivedAt, now, !liveByHost[host.Name]),
			})
		}
	}

	sort.SliceStable(snapshot.Sessions, func(i, j int) bool {
		a, b := snapshot.Sessions[i], snapshot.Sessions[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.SessionID < b.SessionID
	})

	return snapshot, nil
}

// networkSession joins one directory row with its host's report, if the report lists it.
func (s *Server) networkSession(row DirectoryRow, bySession map[string]protocol.ReportSession, receivedAt, now time.Time) protocol.NetworkSession {
	live := HostLive(row.LastHeartbeat, s.streamOpen(row.Host), now)

	session := protocol.NetworkSession{
		Address:   row.Name + "@" + row.Host,
		Host:      row.Host,
		Name:      row.Name,
		SessionID: row.SessionID,
		Directory: protocol.DirectoryFacts{Registered: true, Live: &live},
	}

	if bySession != nil {
		if claim, ok := bySession[row.SessionID]; ok {
			session.Reported = reportedSession(claim, receivedAt, now, !live)
		}
	}
	return session
}

// reportedSession renders one reported session with its own age and staleness.
func reportedSession(claim protocol.ReportSession, receivedAt, now time.Time, hostDown bool) *protocol.ReportedSession {
	age := now.Sub(receivedAt).Milliseconds()

	return &protocol.ReportedSession{
		LocalID:        claim.LocalID,
		Harness:        claim.Harness,
		HarnessVersion: claim.HarnessVersion,
		StartedAt:      claim.StartedAt,
		State:          claim.State,
		StateAt:        claim.StateAt,
		Input:          claim.Input,
		AgeMS:          age,
		Stale:          hostDown || age > reportStaleAfter.Milliseconds(),
	}
}

// snapshotSignature is the content of a snapshot with everything that changes on its own removed.
//
// Generated time, every age, the last heartbeat, the received time and the report sequence are
// zeroed: they change on every heartbeat or report, and waking every long poll for them would be
// noise. What remains is what a reader would act on.
//
// UNTESTED.
func snapshotSignature(snapshot protocol.NetworkSnapshot) (string, error) {
	copied := snapshot
	copied.GeneratedAt = ""
	copied.Revision = 0

	copied.Hosts = make([]protocol.NetworkHost, len(snapshot.Hosts))
	for i, host := range snapshot.Hosts {
		host.Connection.LastHeartbeatAt = ""
		host.Connection.HeartbeatAgeMS = nil
		if host.Report != nil {
			report := *host.Report
			report.ReceivedAt = ""
			report.AgeMS = 0
			report.Seq = 0
			host.Report = &report
		}
		copied.Hosts[i] = host
	}

	copied.Sessions = make([]protocol.NetworkSession, len(snapshot.Sessions))
	for i, session := range snapshot.Sessions {
		if session.Reported != nil {
			report := *session.Reported
			report.AgeMS = 0
			session.Reported = &report
		}
		copied.Sessions[i] = session
	}

	encoded, err := json.Marshal(copied)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// handleReport stores one host's report.
//
// The host comes from the credential, never from the body; the epoch comes from the database, so a
// re-pair drops the old report; and the answer is always 204 once the report is stored, because
// "replaced" is the whole meaning of the request.
//
// UNTESTED.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request, host string) {
	var request protocol.ReportRequest
	if !decodeReportBody(w, r, &request) {
		return
	}

	clean, err := sanitizeReport(request)
	if err != nil {
		writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest, err.Error())
		return
	}

	pairedAt, ok, err := s.store.HostPairedAt(r.Context(), host)
	switch {
	case err != nil:
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	case !ok:
		// The host was unpaired between the authentication check and here.
		writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, "the credential was not accepted")
		return
	}

	if s.network.reports.put(host, pairedAt, clean, s.opts.Now()) {
		s.kickNetwork()
	}

	w.WriteHeader(http.StatusNoContent)
}

// kickNetwork recomputes the snapshot in the background so parked polls wake.
//
// It is the same shape as pushDirectory: bounded, off the request's goroutine, and silent when the
// read fails, because the next computation will try again.
func (s *Server) kickNetwork() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), directoryReadTimeout)
		defer cancel()
		_, _ = s.compute(ctx)
	}()
}

// handleNetwork answers the snapshot, waiting for a change when the caller asked for one.
//
// A request without since answers at once. With since, the first computation decides: a different
// revision answers immediately, an equal one parks until the revision moves, the wait expires, the
// client goes away or the gateway closes. A parked request does no database read of its own; one
// shared ticker recomputes while any poll waits.
//
// UNTESTED.
func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request, host string) {
	query := r.URL.Query()

	since, hasSince, ok := parseSince(query)
	if !ok {
		writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest, "since must be a non-negative whole number")
		return
	}
	wait, ok := parseWait(query)
	if !ok {
		writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest, "wait_ms must be a non-negative whole number")
		return
	}

	snapshot, err := s.compute(r.Context())
	if err != nil {
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	}

	if !hasSince || snapshot.Revision != since {
		writeJSON(w, http.StatusOK, snapshot)
		return
	}

	// Nothing is new. A caller that will not wait is answered before it parks, so it never
	// starts the shared ticker for nothing.
	if wait <= 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	release := s.parkNetwork()
	defer release()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	for {
		s.network.mu.Lock()
		changed := s.network.changed
		revision := s.network.revision
		s.network.mu.Unlock()

		if revision != since {
			// The ticker (or a report) moved it while this request was being set up.
			snapshot, err = s.compute(r.Context())
			if err != nil {
				writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
				return
			}
			writeJSON(w, http.StatusOK, snapshot)
			return
		}

		select {
		case <-changed:
			snapshot, err = s.compute(r.Context())
			if err != nil {
				writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
				return
			}
			writeJSON(w, http.StatusOK, snapshot)
			return
		case <-timer.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			// The client gave up: nothing to write.
			return
		case <-s.closing:
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
}

// parseSince reads the optional since parameter.
//
// An absent parameter is "no since"; a present but empty one is a bad request, because a client
// that sent the parameter meant to name a revision.
func parseSince(query url.Values) (uint64, bool, bool) {
	if !query.Has("since") {
		return 0, false, true
	}
	value := query.Get("since")
	if value == "" {
		return 0, true, false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, true, false
	}
	return parsed, true, true
}

// parseWait reads the optional wait_ms parameter, clamped to the contract's maximum.
func parseWait(query url.Values) (time.Duration, bool) {
	if !query.Has("wait_ms") {
		return 0, true
	}
	value := query.Get("wait_ms")
	if value == "" {
		return 0, false
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil || ms < 0 {
		return 0, false
	}
	wait := time.Duration(ms) * time.Millisecond
	if wait > protocol.MaxNetworkWait {
		wait = protocol.MaxNetworkWait
	}
	return wait, true
}
