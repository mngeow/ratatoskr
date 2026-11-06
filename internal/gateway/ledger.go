package gateway

// The read side of the ledger. The routing slice writes these rows; nothing here
// writes or deletes any.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// defaultLogLimit is how many ledger rows a read returns when no limit is given.
const defaultLogLimit = 50 // UNTESTED guess

// ErrNoSuchMessage reports a message id the gateway has never stored.
var ErrNoSuchMessage = errors.New("gateway: no such message")

// MessageRow is one message as the ledger shows it.
type MessageRow struct {
	Seq          int64
	ID           string
	From         string
	To           string
	ReplyTo      string
	Body         string
	Result       string
	Report       string
	ReportReason string
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// The session fields were added with the cross-machine slice.
	FromSession  string
	ToSession    string
	ResultReason string
	// ToSessionEnded is true when the message was addressed to a session that has
	// since ended, which is why a report may never arrive.
	ToSessionEnded bool
}

// EventRow is one recorded thing that happened to a message.
type EventRow struct {
	At     time.Time
	Kind   string
	Value  string
	Reason string
}

// messageColumns is the ledger's column list, in the order every scan uses. The
// last column is derived from the sessions join in messageFrom.
const messageColumns = `m.seq, m.id, m.from_address, m.to_address, m.reply_to, m.body, m.result,
	m.report, m.report_reason, m.created_at, m.updated_at,
	m.from_session, m.to_session, m.result_reason,
	CASE WHEN ts.ended_at IS NOT NULL THEN 1 ELSE 0 END`

// messageFrom is the table plus the sessions join the last column needs. The join
// is what lets a reader see that a message's recipient session has ended.
const messageFrom = `
	FROM messages m
	LEFT JOIN sessions ts ON ts.id = m.to_session`

// Log returns rows after afterSeq, oldest first, at most limit of them.
//
// UNTESTED.
func (s *Store) Log(ctx context.Context, afterSeq int64, limit int) ([]MessageRow, error) {
	if limit <= 0 {
		limit = defaultLogLimit
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageColumns+messageFrom+` WHERE m.seq > ? ORDER BY m.seq ASC LIMIT ?`,
		afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("gateway: read ledger: %w", err)
	}
	return scanMessages(rows)
}

// Tail returns the newest limit rows, oldest first.
//
// The order matters to the caller: `log` prints them in the order a person reads
// them, and follow mode continues from the last seq.
//
// UNTESTED.
func (s *Store) Tail(ctx context.Context, limit int) ([]MessageRow, error) {
	if limit <= 0 {
		limit = defaultLogLimit
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageColumns+messageFrom+` ORDER BY m.seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("gateway: read ledger: %w", err)
	}
	messages, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	// Newest first from SQL, oldest first for the reader.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

// Message returns one message with its events, oldest first.
//
// UNTESTED.
func (s *Store) Message(ctx context.Context, id string) (MessageRow, []EventRow, error) {
	message, err := scanLedgerMessage(s.db.QueryRowContext(ctx,
		`SELECT `+messageColumns+messageFrom+` WHERE m.id=?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return MessageRow{}, nil, ErrNoSuchMessage
	case err != nil:
		return MessageRow{}, nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT at, kind, value, reason FROM message_events WHERE message_id=? ORDER BY id ASC`, id)
	if err != nil {
		return MessageRow{}, nil, fmt.Errorf("gateway: read message events: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	events := make([]EventRow, 0)
	for rows.Next() {
		var at int64
		var event EventRow
		if err := rows.Scan(&at, &event.Kind, &event.Value, &event.Reason); err != nil {
			return MessageRow{}, nil, fmt.Errorf("gateway: read message events: %w", err)
		}
		event.At = time.UnixMilli(at)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return MessageRow{}, nil, fmt.Errorf("gateway: read message events: %w", err)
	}
	return message, events, nil
}

// scanMessages reads the shared column list out of a row set.
func scanMessages(rows *sql.Rows) ([]MessageRow, error) {
	defer func() {
		_ = rows.Close()
	}()

	messages := make([]MessageRow, 0)
	for rows.Next() {
		message, err := scanLedgerMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: read ledger row: %w", err)
	}
	return messages, nil
}

// ledgerScanner is what both *sql.Row and *sql.Rows satisfy, so one scan helper
// serves every read of the ledger column list.
type ledgerScanner interface {
	Scan(dest ...any) error
}

// scanLedgerMessage reads one row of messageColumns.
func scanLedgerMessage(row ledgerScanner) (MessageRow, error) {
	var message MessageRow
	var created, updated int64
	var ended int

	err := row.Scan(&message.Seq, &message.ID, &message.From, &message.To,
		&message.ReplyTo, &message.Body, &message.Result, &message.Report,
		&message.ReportReason, &created, &updated,
		&message.FromSession, &message.ToSession, &message.ResultReason, &ended)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MessageRow{}, sql.ErrNoRows
		}
		return MessageRow{}, fmt.Errorf("gateway: read ledger row: %w", err)
	}

	message.CreatedAt, message.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	message.ToSessionEnded = ended != 0
	return message, nil
}
