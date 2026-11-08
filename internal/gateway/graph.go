package gateway

// GET /v1/graph: every paired host with its connection signal and its card signal, and the sends the
// gateway decided between hosts, with the same revisioned long poll as GET /v1/network.
//
// The two signals stay separate all the way out. `connection` is the heartbeat and stream (HostLive,
// the authority on whether a host is connected); `card` is whether the card endpoint answered a poll.
// Nothing here merges them, derives one from the other or feeds a poll result into liveness.
//
// Any paired host can read every card summary and the edge counts: public cards plus message-pattern
// metadata. That is the same exposure as the network snapshot, and the docs say so.
//
// The revision follows network.go's rule: it rises when content changes and not when only an age
// changes, so a 304 poll or a heartbeat wakes nobody. Content here includes the card state (fresh,
// stale, offline), which moves with time, so a shared one-second ticker recomputes while any poll is
// parked.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// defaultGraphTick is how often the shared recompute runs while a poll is parked.
	defaultGraphTick = 1 * time.Second // UNTESTED guess
	// edgeCacheFor is how long a computed edge set is reused, so the one-second recompute does not
	// walk the ledger every second.
	edgeCacheFor = 5 * time.Second // UNTESTED guess
	// maxEdgeRows bounds one ledger walk. Hitting it sets edges_partial rather than undercounting
	// silently.
	maxEdgeRows = 100000 // UNTESTED guess
	// edgeReadTimeout bounds one ledger walk.
	edgeReadTimeout = 5 * time.Second // UNTESTED guess
)

// cachedCard is a parsed stored card, kept by hash so the per-second recompute does not parse up to
// 64 KiB per host each time.
type cachedCard struct {
	hash string
	card protocol.HostCard
}

// graphState is the graph's revision machinery. It mirrors networkState and shares nothing with it.
type graphState struct {
	mu sync.Mutex

	revision  uint64
	signature string
	changed   chan struct{}

	waiting    int
	ticking    bool
	tickerRuns int

	cards map[string]cachedCard

	edges        []protocol.GraphEdge
	edgesPartial bool
	edgesAt      time.Time
	// edgesFailed says the last walk failed, so the edges in edges are older than the cache window
	// suggests and every answer built from them is marked partial until a walk succeeds.
	edgesFailed bool
}

func newGraphState() *graphState {
	return &graphState{
		changed: make(chan struct{}),
		cards:   make(map[string]cachedCard, 4),
	}
}

// StartCardPoller starts the card poller for the life of the gateway and returns the function that
// stops it. The caller must run the returned function before closing the store. interval 0 means the
// default; an out-of-range value is clamped.
//
// logf receives short operational lines; it never receives a card, a header or a credential.
//
// UNTESTED.
func (s *Server) StartCardPoller(ctx context.Context, interval time.Duration, logf func(string, ...any)) (stop func()) {
	poller := newCardPoller(s.store, interval, s.opts.Now, logf, s.kickGraph)
	s.cards = poller
	poller.start(ctx)
	return poller.stop
}

// kickGraph recomputes the graph in the background so parked polls wake. Bounded and silent on error;
// the next computation tries again.
func (s *Server) kickGraph() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), directoryReadTimeout)
		defer cancel()
		_, _ = s.computeGraph(ctx)
	}()
}

// parkGraph registers one parked long poll and starts the shared ticker if it is the first.
func (s *Server) parkGraph() func() {
	state := s.graph

	state.mu.Lock()
	state.waiting++
	start := !state.ticking
	if start {
		state.ticking = true
		state.tickerRuns++
	}
	state.mu.Unlock()

	if start {
		go s.graphTicker(defaultGraphTick)
	}

	return func() {
		state.mu.Lock()
		state.waiting--
		state.mu.Unlock()
	}
}

// graphTicker is the one periodic recompute, shared by every parked poll. The exit decision and the
// ticking flag change under one lock, as in networkTicker, so a poll that parks as the ticker leaves
// starts a new one.
func (s *Server) graphTicker(period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-s.closing:
			s.graph.mu.Lock()
			s.graph.ticking = false
			s.graph.mu.Unlock()
			return
		case <-ticker.C:
			s.graph.mu.Lock()
			if s.graph.waiting == 0 {
				s.graph.ticking = false
				s.graph.mu.Unlock()
				return
			}
			s.graph.mu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), directoryReadTimeout)
			_, _ = s.computeGraph(ctx)
			cancel()
		}
	}
}

// broadcastGraph wakes every parked poll. The caller holds the state mutex.
func (s *Server) broadcastGraph() {
	close(s.graph.changed)
	s.graph.changed = make(chan struct{})
}

// computeGraph builds the snapshot and raises the revision when its content changed. Everything runs
// under the state mutex, so a revision and the body that carries it cannot disagree, and the reads are
// bounded.
//
// UNTESTED.
func (s *Server) computeGraph(ctx context.Context) (protocol.GraphSnapshot, error) {
	state := s.graph
	state.mu.Lock()
	defer state.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, directoryReadTimeout)
	defer cancel()

	snapshot, err := s.buildGraph(ctx)
	if err != nil {
		return protocol.GraphSnapshot{}, err
	}

	signature, err := graphSignature(snapshot)
	if err != nil {
		return protocol.GraphSnapshot{}, err
	}

	if state.revision == 0 {
		state.revision = 1
		state.signature = signature
	} else if signature != state.signature {
		state.revision++
		state.signature = signature
		s.broadcastGraph()
	}

	snapshot.Revision = state.revision
	return snapshot, nil
}

// buildGraph reads the database and assembles the answer. The caller holds the state mutex.
func (s *Server) buildGraph(ctx context.Context) (protocol.GraphSnapshot, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return protocol.GraphSnapshot{}, err
	}
	rows, err := s.store.CardRows(ctx)
	if err != nil {
		return protocol.GraphSnapshot{}, err
	}

	now := s.opts.Now()
	interval := s.cards.pollInterval()

	snapshot := protocol.GraphSnapshot{
		Schema:      protocol.GraphSchema,
		GeneratedAt: protocol.FormatTime(now),
		Hosts:       make([]protocol.GraphHost, 0, len(hosts)),
	}

	paired := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		paired[host.Name] = true

		live := HostLive(host.LastHeartbeat, s.streamOpen(host.Name), now)
		entry := protocol.GraphHost{
			Name: host.Name,
			Connection: protocol.GraphConnection{
				Live:   live,
				Source: protocol.GraphSourceStream,
			},
		}
		if !host.LastHeartbeat.IsZero() {
			age := now.Sub(host.LastHeartbeat).Milliseconds()
			entry.Connection.HeartbeatAgeMS = &age
		}

		// A report belongs to the host row it was stored under; one from an earlier pairing is not
		// this host's.
		if report, ok := s.network.reports.get(host.Name); ok && report.epoch.Equal(host.PairedAt) {
			age := now.Sub(report.receivedAt).Milliseconds()
			entry.ReportAgeMS = &age
		}

		entry.Card = s.graphCard(s.graph, host.Name, rows[host.Name], now, interval)
		snapshot.Hosts = append(snapshot.Hosts, entry)
	}

	// Forget parsed cards of hosts that are gone.
	for name := range s.graph.cards {
		if !paired[name] {
			delete(s.graph.cards, name)
		}
	}

	// The edges are the one part of the graph that walks the ledger, and the ledger can be slow. A
	// failed or timed-out walk must not take every host's summary down with it: keep the last good
	// edges (or none), say they may be incomplete, and carry on.
	edges, partial, err := s.graphEdges(ctx, paired, now)
	if err != nil {
		edges, partial = s.graph.edges, true
		s.cards.logOnce("edges", "the message edges could not be read; the graph keeps its last edges and marks them partial")
	}
	snapshot.Edges = edges
	snapshot.EdgesPartial = partial

	if err := fitGraph(&snapshot); err != nil {
		return protocol.GraphSnapshot{}, err
	}
	return snapshot, nil
}

// graphCard renders one host's card signal from its stored row.
func (s *Server) graphCard(st *graphState, host string, row CardRow, now time.Time, interval time.Duration) protocol.GraphCard {
	haveCard := row.CardJSON != ""

	out := protocol.GraphCard{
		State:         Freshness(now, row.LastOKAt, haveCard, row.LastResult, interval),
		Restored:      row.Host != "" && !s.cards.polledSinceBoot(host),
		Revision:      row.Revision,
		ETag:          row.ETag,
		LastOKAt:      protocol.FormatTime(row.LastOKAt),
		LastAttemptAt: protocol.FormatTime(row.LastAttemptAt),
		LastResult:    row.LastResult,
		Source:        protocol.GraphSourcePoll,
		UnknownFields: row.UnknownFields,
	}
	if !row.LastOKAt.IsZero() {
		age := now.Sub(row.LastOKAt).Milliseconds()
		out.AgeMS = &age
	}
	if !Succeeded(row.LastResult) {
		// A reason is for a failed or rejected poll; after a success it would be stale text.
		out.Reason = row.LastReason
	}

	if !haveCard {
		return out
	}

	cached, ok := st.cards[host]
	if !ok || cached.hash != row.Hash {
		parsed, problems := card.Parse([]byte(row.CardJSON), card.Polled, true)
		if len(problems) > 0 {
			// A stored card this build no longer accepts is shown as rejected, with the card left out:
			// it was good when stored, and the rules have moved since.
			out.State = protocol.CardRejected
			out.Reason = "the stored card is no longer accepted by this build"
			return out
		}
		cached = cachedCard{hash: row.Hash, card: parsed.Card}
		st.cards[host] = cached
	}
	copied := cached.card
	out.Card = &copied
	return out
}

// fitGraph keeps the encoded snapshot under MaxGraphBytes by omitting card bodies, largest first with
// ties broken by host name, so the same inputs always omit the same bodies. Each omitted card sets
// card_omitted on its host and truncated is set overall; every host keeps its summary and state.
func fitGraph(snapshot *protocol.GraphSnapshot) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("gateway: encode graph: %w", err)
	}
	if len(encoded) <= protocol.MaxGraphBytes {
		return nil
	}

	type sized struct {
		index int
		size  int
	}
	var withBodies []sized
	for index, host := range snapshot.Hosts {
		if host.Card.Card == nil {
			continue
		}
		body, err := json.Marshal(host.Card.Card)
		if err != nil {
			return fmt.Errorf("gateway: encode card: %w", err)
		}
		withBodies = append(withBodies, sized{index: index, size: len(body)})
	}
	sort.Slice(withBodies, func(i, j int) bool {
		if withBodies[i].size != withBodies[j].size {
			return withBodies[i].size > withBodies[j].size
		}
		return snapshot.Hosts[withBodies[i].index].Name < snapshot.Hosts[withBodies[j].index].Name
	})

	remaining := len(encoded)
	for _, item := range withBodies {
		if remaining <= protocol.MaxGraphBytes {
			break
		}
		snapshot.Hosts[item.index].Card.Card = nil
		snapshot.Hosts[item.index].Card.CardOmitted = true
		snapshot.Truncated = true
		// The body shrinks to "null" (4 bytes) and the flag adds its key.
		remaining -= item.size - len("null") - len(`,"card_omitted":true`)
	}
	return nil
}

// graphSignature is the content of a snapshot with everything that changes on its own removed: the
// generated time, every age, the last-attempt and last-ok times, and the revision. What remains is what
// a reader would act on, including the card state, the restored flag and the omission flags.
//
// UNTESTED.
func graphSignature(snapshot protocol.GraphSnapshot) (string, error) {
	copied := snapshot
	copied.GeneratedAt = ""
	copied.Revision = 0

	copied.Hosts = make([]protocol.GraphHost, len(snapshot.Hosts))
	for index, host := range snapshot.Hosts {
		host.Connection.HeartbeatAgeMS = nil
		host.ReportAgeMS = nil
		host.Card.AgeMS = nil
		host.Card.LastOKAt = ""
		host.Card.LastAttemptAt = ""
		copied.Hosts[index] = host
	}

	encoded, err := json.Marshal(copied)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// graphEdges returns the edge set, recomputing it at most every edgeCacheFor. The caller holds the
// state mutex. The cache is dropped by its age only: edges are metadata, not a latency-critical fact.
func (s *Server) graphEdges(ctx context.Context, paired map[string]bool, now time.Time) ([]protocol.GraphEdge, bool, error) {
	state := s.graph
	if !state.edgesAt.IsZero() && now.Sub(state.edgesAt) < edgeCacheFor {
		return state.edges, state.edgesPartial || state.edgesFailed, nil
	}

	edgeCtx, cancel := context.WithTimeout(ctx, edgeReadTimeout)
	defer cancel()

	cutoff := now.Add(-protocol.GraphEdgeWindowHours * time.Hour)
	edges, partial, err := s.store.MessageEdges(edgeCtx, paired, cutoff)
	if err != nil {
		// Do not retry on the very next recompute: a slow ledger would be walked every second. The
		// failure is remembered for one cache window, and the caller keeps the last good edges.
		state.edgesAt = now
		state.edgesFailed = true
		return nil, false, err
	}
	state.edges, state.edgesPartial, state.edgesAt, state.edgesFailed = edges, partial, now, false
	return edges, partial, nil
}

// MessageEdges counts, per ordered pair of currently paired hosts, the sends the gateway decided in the
// window: every message whose result is not "stored". HostStored is the subset a host saved.
//
// It walks messages by seq, newest first, reading only the addresses, the result and the time (never a
// body), and stops at the first row older than the cutoff. That relies on created_at following seq; a
// clock set backwards can end the walk early. At most maxEdgeRows rows are read; hitting the cap
// returns partial = true instead of a silently low count. Addresses that do not parse, and sends
// between two agents on one host, are skipped.
//
// UNTESTED.
func (s *Store) MessageEdges(ctx context.Context, paired map[string]bool, cutoff time.Time) ([]protocol.GraphEdge, bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_address, to_address, result, created_at FROM messages ORDER BY seq DESC`)
	if err != nil {
		return nil, false, fmt.Errorf("gateway: read edges: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	type pair struct{ from, to string }
	type tally struct {
		count, hostStored int
		last              int64
	}
	counts := make(map[pair]*tally, 4)

	read := 0
	partial := false
	for rows.Next() {
		if read >= maxEdgeRows {
			partial = true
			break
		}
		read++

		var from, to, result string
		var created int64
		if err := rows.Scan(&from, &to, &result, &created); err != nil {
			return nil, false, fmt.Errorf("gateway: read edges: %w", err)
		}
		if created < cutoff.UnixMilli() {
			break
		}
		if result == resultStored {
			continue
		}

		_, fromHost, okFrom := parseAddress(from)
		_, toHost, okTo := parseAddress(to)
		if !okFrom || !okTo || fromHost == toHost || !paired[fromHost] || !paired[toHost] {
			continue
		}

		key := pair{fromHost, toHost}
		entry := counts[key]
		if entry == nil {
			entry = &tally{}
			counts[key] = entry
		}
		entry.count++
		if result == string(protocol.SendHostStored) {
			entry.hostStored++
		}
		if created > entry.last {
			entry.last = created
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("gateway: read edges: %w", err)
	}

	edges := make([]protocol.GraphEdge, 0, len(counts))
	for key, entry := range counts {
		edges = append(edges, protocol.GraphEdge{
			From:        key.from,
			To:          key.to,
			Kind:        protocol.GraphEdgeMessagesSeen,
			WindowHours: protocol.GraphEdgeWindowHours,
			Count:       entry.count,
			HostStored:  entry.hostStored,
			LastAt:      protocol.FormatTime(time.UnixMilli(entry.last)),
			Source:      protocol.GraphSourceLedger,
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges, partial, nil
}

// handleGraph answers the graph, waiting for a change when the caller asked for one. It is
// handleNetwork's shape exactly: a request without since answers at once; with since, an equal
// revision parks until it moves, the wait expires, the client leaves or the gateway closes.
//
// UNTESTED.
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request, _ string) {
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

	snapshot, err := s.computeGraph(r.Context())
	if err != nil {
		writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
		return
	}

	if !hasSince || snapshot.Revision != since {
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	if wait <= 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	release := s.parkGraph()
	defer release()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	for {
		s.graph.mu.Lock()
		changed := s.graph.changed
		revision := s.graph.revision
		s.graph.mu.Unlock()

		if revision != since {
			snapshot, err = s.computeGraph(r.Context())
			if err != nil {
				writeWireError(w, http.StatusInternalServerError, protocol.WireErrInternal, "the request could not be completed")
				return
			}
			writeJSON(w, http.StatusOK, snapshot)
			return
		}

		select {
		case <-changed:
			snapshot, err = s.computeGraph(r.Context())
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
			return
		case <-s.closing:
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
}
