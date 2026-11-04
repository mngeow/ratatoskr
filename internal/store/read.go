package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// EventRow is one recorded state change of a message.
type EventRow struct {
	At     time.Time
	From   protocol.MessageState
	To     protocol.MessageState
	Reason string
}

// messageSelect is the column list and joins of ops.go's History, narrowed by the
// caller's WHERE clause.
//
// It is a copy of that query on purpose: History is not this file's to change,
// so the two must be kept in step by hand. The columns are, in order: seq, id,
// run_id, from agent id, to agent id, from display name, to display name,
// reply_to, body, state, latest event reason, created_at, updated_at.
const messageSelect = `
		SELECT m.seq, m.id, m.run_id, m.from_agent_id, m.to_agent_id,
		       COALESCE(f.display_name, ''), COALESCE(t.display_name, ''),
		       m.reply_to, m.body, m.state,
		       COALESCE((SELECT e.reason FROM message_events e WHERE e.message_id=m.id ORDER BY e.id DESC LIMIT 1), ''),
		       m.created_at, m.updated_at
		FROM messages m
		LEFT JOIN agents f ON f.id = m.from_agent_id
		LEFT JOIN agents t ON t.id = m.to_agent_id`

// MessageEvents returns the recorded state changes of one message, oldest first.
//
// The read handle is used, never the writer, and the unavailable latch is not
// consulted: a latched write path must not stop a user from reading history.
// An unknown id is not an error - it simply has no events.
//
// UNTESTED.
func (s *Store) MessageEvents(ctx context.Context, id string) ([]EventRow, error) {
	rows, err := s.rdb.QueryContext(ctx, `
		SELECT at, from_state, to_state, reason
		FROM message_events
		WHERE message_id=? ORDER BY id ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("store: message events %s: %w", id, err)
	}
	defer func() {
		_ = rows.Close()
	}()

	// A non-nil empty slice, so a caller can range over the result without
	// treating "no events" and "no such message" differently.
	events := make([]EventRow, 0)
	for rows.Next() {
		var at int64
		var from, to, reason string
		if err := rows.Scan(&at, &from, &to, &reason); err != nil {
			return nil, fmt.Errorf("store: message events %s: %w", id, err)
		}
		events = append(events, EventRow{
			At:     time.UnixMilli(at),
			From:   protocol.MessageState(from),
			To:     protocol.MessageState(to),
			Reason: reason,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: message events %s: %w", id, err)
	}
	return events, nil
}

// Message returns one stored message, with the same columns and joins the
// history list uses: display names from the agents table and the latest event
// reason. Ids are global, so no run filter is needed.
//
// An unknown id is ErrNotFound, which the caller maps to its own sentinel. As
// with MessageEvents, this goes through the read handle and ignores the write
// latch: recovering a message body is exactly what a user needs to do when
// writing has failed.
//
// UNTESTED.
func (s *Store) Message(ctx context.Context, id string) (MessageRow, error) {
	var row MessageRow
	var from, to, state string
	var created, updated int64

	err := s.rdb.QueryRowContext(ctx, messageSelect+` WHERE m.id=?`, id).Scan(
		&row.Seq, &row.ID, &row.RunID, &from, &to, &row.From, &row.To,
		&row.ReplyTo, &row.Body, &state, &row.Reason, &created, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageRow{}, fmt.Errorf("store: message %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return MessageRow{}, fmt.Errorf("store: message %s: %w", id, err)
	}

	row.FromID, row.ToID = protocol.AgentID(from), protocol.AgentID(to)
	row.State = protocol.MessageState(state)
	row.CreatedAt, row.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return row, nil
}
