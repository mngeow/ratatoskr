package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

func ms(t time.Time) int64 { return t.UnixMilli() }

// Message is what InsertMessage records. Body is stored as given; the router
// has already limited its size.
type Message struct {
	ID        string
	RunID     string
	From, To  protocol.AgentID
	ReplyTo   string
	Body      string
	CreatedAt time.Time
}

// Agent is one harness launched in a run.
type Agent struct {
	ID             protocol.AgentID
	RunID          string
	DisplayName    string
	Harness        string
	HarnessVersion string
	StartedAt      time.Time
}

// MessageRow is a stored message with display names and its latest reason.
// Body is raw: sanitise it before showing it (design section 9).
type MessageRow struct {
	Seq                  int64
	ID, RunID            string
	FromID, ToID         protocol.AgentID
	From, To             string
	ReplyTo, Body        string
	State                protocol.MessageState
	Reason               string
	CreatedAt, UpdatedAt time.Time
}

// RunRow is one run with the number of its messages that were interrupted.
type RunRow struct {
	ID          string
	StartedAt   time.Time
	EndedAt     *time.Time
	EndReason   string
	Interrupted int
}

// legal lists the edges of the message lifecycle (design Figure 6). The only
// edge out of a final state is unconfirmed -> accepted.
var legal = map[protocol.MessageState][]protocol.MessageState{
	protocol.MessageQueued:      {protocol.MessageHeld, protocol.MessageDelivering, protocol.MessageFailed, protocol.MessageInterrupted},
	protocol.MessageHeld:        {protocol.MessageDelivering, protocol.MessageFailed, protocol.MessageInterrupted},
	protocol.MessageDelivering:  {protocol.MessagePasted, protocol.MessageFailed, protocol.MessageUnconfirmed, protocol.MessageInterrupted},
	protocol.MessagePasted:      {protocol.MessageAccepted, protocol.MessageUnconfirmed, protocol.MessageInterrupted},
	protocol.MessageUnconfirmed: {protocol.MessageAccepted},
}

func isLegal(from, to protocol.MessageState) bool {
	for _, s := range legal[from] {
		if s == to {
			return true
		}
	}
	return false
}

func (s *Store) StartRun(ctx context.Context, id, appVersion string, at time.Time) error {
	return s.do(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs(id, started_at, app_version) VALUES(?,?,?)`, id, ms(at), appVersion)
		return err
	})
}

func (s *Store) InsertAgent(ctx context.Context, a Agent) error {
	return s.do(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO agents(id, run_id, display_name, harness, harness_version, started_at)
			VALUES(?,?,?,?,?,?)`, string(a.ID), a.RunID, a.DisplayName, a.Harness, a.HarnessVersion, ms(a.StartedAt))
		return err
	})
}

// EndAgent records that an agent's process ended. exitCode is nil when unknown.
func (s *Store) EndAgent(ctx context.Context, id protocol.AgentID, exitCode *int, at time.Time) error {
	return s.do(ctx, func(tx *sql.Tx) error {
		var code any
		if exitCode != nil {
			code = *exitCode
		}
		_, err := tx.Exec(`UPDATE agents SET exited_at=?, exit_code=? WHERE id=?`, ms(at), code, string(id))
		return err
	})
}

// InsertMessage records a new message as queued, with its first event, in one
// transaction. The caller returns "queued" to the sender only after this
// returns nil (invariant 8). It returns the message's delivery-order sequence.
func (s *Store) InsertMessage(ctx context.Context, m Message) (seq int64, err error) {
	err = s.do(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO messages(id, run_id, from_agent_id, to_agent_id, reply_to, body, state, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, m.ID, m.RunID, string(m.From), string(m.To), m.ReplyTo, m.Body,
			string(protocol.MessageQueued), ms(m.CreatedAt), ms(m.CreatedAt))
		if err != nil {
			return err
		}
		if seq, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO message_events(message_id, at, from_state, to_state, reason) VALUES(?,?,?,?,?)`,
			m.ID, ms(m.CreatedAt), "", string(protocol.MessageQueued), "")
		return err
	})
	return seq, err
}

// Transition moves a message from one state to another and appends the event in
// one transaction, only if the message is still in from. It returns
// ErrIllegalTransition (without touching the database) for a non-edge, and
// ErrStateChanged if the message is not in from. Callers commit "delivering"
// before any PTY write and treat any error here as "do not write" (invariant 8).
func (s *Store) Transition(ctx context.Context, id string, from, to protocol.MessageState, reason string, at time.Time) error {
	if !isLegal(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return s.do(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE messages SET state=?, updated_at=? WHERE id=? AND state=?`,
			string(to), ms(at), id, string(from))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrStateChanged
		}
		_, err = tx.Exec(`INSERT INTO message_events(message_id, at, from_state, to_state, reason) VALUES(?,?,?,?,?)`,
			id, ms(at), string(from), string(to), reason)
		return err
	})
}

// MessageExists reports whether id names a message in runID. The router uses it
// to check reply_to (design section 9: a reply_to names a message in the same run).
func (s *Store) MessageExists(ctx context.Context, runID, id string) (bool, error) {
	var one int
	err := s.rdb.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE run_id=? AND id=?`, runID, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// History returns the newest limit messages of a run, oldest first.
func (s *Store) History(ctx context.Context, runID string, limit int) ([]MessageRow, error) {
	rows, err := s.rdb.QueryContext(ctx, `
		SELECT m.seq, m.id, m.run_id, m.from_agent_id, m.to_agent_id,
		       COALESCE(f.display_name, ''), COALESCE(t.display_name, ''),
		       m.reply_to, m.body, m.state,
		       COALESCE((SELECT e.reason FROM message_events e WHERE e.message_id=m.id ORDER BY e.id DESC LIMIT 1), ''),
		       m.created_at, m.updated_at
		FROM messages m
		LEFT JOIN agents f ON f.id = m.from_agent_id
		LEFT JOIN agents t ON t.id = m.to_agent_id
		WHERE m.run_id=? ORDER BY m.seq DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageRow
	for rows.Next() {
		var r MessageRow
		var from, to, state string
		var created, updated int64
		if err := rows.Scan(&r.Seq, &r.ID, &r.RunID, &from, &to, &r.From, &r.To,
			&r.ReplyTo, &r.Body, &state, &r.Reason, &created, &updated); err != nil {
			return nil, err
		}
		r.FromID, r.ToID, r.State = protocol.AgentID(from), protocol.AgentID(to), protocol.MessageState(state)
		r.CreatedAt, r.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Runs returns the newest limit runs, newest first.
func (s *Store) Runs(ctx context.Context, limit int) ([]RunRow, error) {
	rows, err := s.rdb.QueryContext(ctx, `
		SELECT r.id, r.started_at, r.ended_at, COALESCE(r.end_reason, ''),
		       (SELECT COUNT(*) FROM messages m WHERE m.run_id=r.id AND m.state='interrupted')
		FROM runs r ORDER BY r.started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&r.ID, &started, &ended, &r.EndReason, &r.Interrupted); err != nil {
			return nil, err
		}
		r.StartedAt = time.UnixMilli(started)
		if ended.Valid {
			t := time.UnixMilli(ended.Int64)
			r.EndedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
