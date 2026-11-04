package router

import (
	"sort"

	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/store"
)

// Snapshot returns the last published state. It reads an atomic pointer, so it
// is safe from the UI goroutine and never waits for the actor or the disk.
//
// UNTESTED.
func (r *Router) Snapshot() protocol.Snapshot {
	if snapshot := r.snap.Load(); snapshot != nil {
		return *snapshot
	}
	return protocol.Snapshot{}
}

// PendingOnQuit is how many messages have not reached a final state: queued,
// held, delivering or pasted. Unconfirmed is final, so it does not count.
//
// It reads an atomic counter for the same reason Snapshot does.
//
// UNTESTED.
func (r *Router) PendingOnQuit() int { return int(r.pending.Load()) }

// History reads the stored message history for one run. An empty RunID means the
// run this router was built for; a non-positive Limit means historyLimit.
//
// It goes straight to the store rather than through the actor - the read handle
// does not need the router's state - and reduces every body to its preview, so a
// control sequence stored in a body cannot reach the UI.
//
// UNTESTED.
func (r *Router) History(q protocol.HistoryQuery) ([]protocol.MessageView, error) {
	runID := q.RunID
	if runID == "" {
		runID = r.cfg.RunID
	}
	limit := q.Limit
	if limit <= 0 {
		limit = historyLimit
	}

	rows, err := r.cfg.Store.History(r.ctx, runID, limit)
	if err != nil {
		return nil, err
	}

	views := make([]protocol.MessageView, 0, len(rows))
	for _, row := range rows {
		views = append(views, storeMessageRowToView(row))
	}
	return views, nil
}

// refresh rebuilds the published snapshot and the pending count. Only the actor
// calls it, so the atomics move in step with the state they describe.
func (r *Router) refresh() {
	snapshot := protocol.Snapshot{
		Agents:   make([]protocol.AgentView, 0, len(r.order)),
		Messages: r.recentMessages(),
		Budget:   protocol.BudgetView{Used: r.used, Limit: r.cfg.BudgetLimit},
		Banner:   r.joinedBanners(),
	}
	for _, a := range r.order {
		snapshot.Agents = append(snapshot.Agents, r.agentView(a))
	}
	r.snap.Store(&snapshot)
	r.pending.Store(int32(r.pendingCount()))
}

// recentMessages returns the newest messages for the snapshot, bodies reduced to
// their preview.
func (r *Router) recentMessages() []protocol.MessageView {
	start := len(r.log) - historyLimit
	if start < 0 {
		start = 0
	}
	views := make([]protocol.MessageView, 0, len(r.log)-start)
	for _, m := range r.log[start:] {
		views = append(views, r.messageView(m))
	}
	return views
}

// pendingCount counts messages that have not reached a final state: every
// message waiting in a queue plus every one in flight (delivering or pasted). It
// counts from the queues, not the capped log, so it stays exact.
func (r *Router) pendingCount() int {
	count := 0
	for _, a := range r.order {
		count += len(a.queue)
		if a.inflight != nil {
			count++
		}
	}
	return count
}

func (r *Router) agentView(a *agent) protocol.AgentView {
	held := 0
	for _, m := range a.queue {
		if m.state == protocol.MessageHeld {
			held++
		}
	}

	view := protocol.AgentView{
		ID:          a.id,
		DisplayName: stripControl(a.name),
		State:       a.state,
		Held:        held,
		Err:         a.err,
	}
	if a.exitCode != nil {
		code := *a.exitCode
		view.ExitCode = &code
	}
	return view
}

func (r *Router) messageView(m *msg) protocol.MessageView {
	return protocol.MessageView{
		ID:      m.id,
		RunID:   r.cfg.RunID,
		From:    stripControl(m.from.name),
		To:      stripControl(m.to.name),
		State:   m.state,
		Reason:  m.reason,
		Preview: preview(m.body),
		At:      m.at,
	}
}

func (r *Router) messageViewPtr(m *msg) *protocol.MessageView {
	view := r.messageView(m)
	return &view
}

func (r *Router) agentViewPtr(a *agent) *protocol.AgentView {
	view := r.agentView(a)
	return &view
}

func (r *Router) budgetViewPtr() *protocol.BudgetView {
	return &protocol.BudgetView{Used: r.used, Limit: r.cfg.BudgetLimit}
}

// joinedBanners renders the banner texts, ordered by key, joined with " | ". The
// key is not shown: the UI has one line.
func (r *Router) joinedBanners() string {
	if len(r.banners) == 0 {
		return ""
	}

	keys := make([]string, 0, len(r.banners))
	for key := range r.banners {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	joined := ""
	for _, key := range keys {
		if joined != "" {
			joined += bannerSeparator
		}
		joined += r.banners[key]
	}
	return joined
}

// storeMessageRowToView reduces a stored row to what the UI may see: names and
// reasons stripped of control characters, body reduced to its preview.
func storeMessageRowToView(row store.MessageRow) protocol.MessageView {
	return protocol.MessageView{
		ID:      row.ID,
		RunID:   row.RunID,
		From:    stripControl(row.From),
		To:      stripControl(row.To),
		State:   row.State,
		Reason:  stripControl(row.Reason),
		Preview: preview(row.Body),
		At:      row.CreatedAt,
	}
}
