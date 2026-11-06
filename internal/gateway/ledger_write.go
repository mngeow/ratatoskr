package gateway

// The write side of the ledger. The routing slice decides what to write; these
// methods only record it, one transaction each, always with the matching event.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No message has been stored,
// resolved or reported.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// resultStored is the first result of every message: the gateway has it, and
// nothing has been decided about delivery yet. It is gateway-internal, which is
// why it is not a protocol.SendResult.
const resultStored = "stored"

// MessageIn is one message to store.
type MessageIn struct {
	ID          string
	From        string // full network name, name@host
	To          string // full network name, name@host
	ReplyTo     string
	Body        string
	FromSession string
	At          time.Time
}

// InsertMessage stores a message as "stored" and appends its first event.
//
// The same id applied twice stores nothing and returns the row that is already
// there, so a sender that lost a response learns the real outcome instead of
// causing a second delivery.
//
// UNTESTED.
func (s *Store) InsertMessage(ctx context.Context, m MessageIn) (inserted bool, existing MessageRow, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, MessageRow{}, fmt.Errorf("gateway: begin insert message: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO messages(id, from_address, to_address, reply_to, body, result, created_at, updated_at, from_session)
		 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		m.ID, m.From, m.To, m.ReplyTo, m.Body, resultStored, m.At.UnixMilli(), m.At.UnixMilli(), m.FromSession)
	if err != nil {
		return false, MessageRow{}, fmt.Errorf("gateway: insert message: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, MessageRow{}, fmt.Errorf("gateway: insert message: %w", err)
	}
	if affected == 0 {
		// Already stored: read it back in the same transaction so the caller sees
		// the row that caused the conflict, and write nothing else.
		existing, err := scanLedgerMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+messageFrom+` WHERE m.id=?`, m.ID))
		if err != nil {
			return false, MessageRow{}, err
		}
		return false, existing, nil
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_events(message_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
		m.ID, m.At.UnixMilli(), "result", resultStored, ""); err != nil {
		return false, MessageRow{}, fmt.Errorf("gateway: insert message event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, MessageRow{}, fmt.Errorf("gateway: commit insert message: %w", err)
	}
	return true, MessageRow{}, nil
}

// SetResult records what happened to a message and appends an event.
//
// An empty toSession leaves the stored one alone, so a later result does not have
// to repeat where the message was sent.
//
// UNTESTED.
func (s *Store) SetResult(ctx context.Context, id, result, reason, toSession string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway: begin set result: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	res, err := tx.ExecContext(ctx,
		`UPDATE messages SET result=?, result_reason=?, updated_at=?,
		   to_session=CASE WHEN ?='' THEN to_session ELSE ? END
		 WHERE id=?`,
		result, reason, at.UnixMilli(), toSession, toSession, id)
	if err != nil {
		return fmt.Errorf("gateway: set result: %w", err)
	}
	if affected, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("gateway: set result: %w", err)
	} else if affected == 0 {
		return ErrNoSuchMessage
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_events(message_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
		id, at.UnixMilli(), "result", result, reason); err != nil {
		return fmt.Errorf("gateway: set result event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway: commit set result: %w", err)
	}
	return nil
}

// SetReport records what the recipient host said. It never touches the result.
//
// It is idempotent - the same state and reason twice writes nothing and adds no
// event - and a report can be set but never cleared, so an empty state is refused
// before the database is touched.
//
// UNTESTED.
func (s *Store) SetReport(ctx context.Context, id, state, reason string, at time.Time) error {
	if state == "" {
		return errors.New("gateway: report state must not be empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway: begin set report: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var storedState, storedReason string
	err = tx.QueryRowContext(ctx, `SELECT report, report_reason FROM messages WHERE id=?`, id).
		Scan(&storedState, &storedReason)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNoSuchMessage
	case err != nil:
		return fmt.Errorf("gateway: set report: %w", err)
	case storedState == state && storedReason == reason:
		return nil
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET report=?, report_reason=?, updated_at=? WHERE id=?`,
		state, reason, at.UnixMilli(), id); err != nil {
		return fmt.Errorf("gateway: set report: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_events(message_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
		id, at.UnixMilli(), "report", state, reason); err != nil {
		return fmt.Errorf("gateway: set report event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway: commit set report: %w", err)
	}
	return nil
}

// ReplyTarget returns the addresses and sessions of a stored message, so the
// routing slice can judge a reply_to: a message whose sender session has ended
// cannot be replied to.
//
// UNTESTED.
func (s *Store) ReplyTarget(ctx context.Context, id string) (from, fromSession, to, toSession string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT from_address, from_session, to_address, to_session FROM messages WHERE id=?`, id).
		Scan(&from, &fromSession, &to, &toSession)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", "", "", ErrNoSuchMessage
	case err != nil:
		return "", "", "", "", fmt.Errorf("gateway: reply target: %w", err)
	}
	return from, fromSession, to, toSession, nil
}

// SetResultFrom records a result only if the message is still in one of the given
// results.
//
// This is the compare-and-set the routing slice needs: an ack and the inline
// timeout can race, and only one of them may decide the outcome. A row that is
// already out of reach is not an error - changed is false - because losing that
// race is normal. A row that does not exist at all is ErrNoSuchMessage.
//
// UNTESTED.
func (s *Store) SetResultFrom(ctx context.Context, id string, from []string, result, reason, toSession string, at time.Time) (changed bool, err error) {
	if len(from) == 0 {
		return false, errors.New("gateway: set result from: empty source list")
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	arguments := make([]any, 0, 5+1+len(from))
	arguments = append(arguments, result, reason, at.UnixMilli(), toSession, toSession, id)
	for _, state := range from {
		arguments = append(arguments, state)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("gateway: begin set result: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	res, err := tx.ExecContext(ctx,
		`UPDATE messages SET result=?, result_reason=?, updated_at=?,
		   to_session=CASE WHEN ?='' THEN to_session ELSE ? END
		 WHERE id=? AND result IN (`+placeholders+`)`, arguments...)
	if err != nil {
		return false, fmt.Errorf("gateway: set result: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("gateway: set result: %w", err)
	}
	if affected == 0 {
		// Out of reach is fine; a missing row is not.
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE id=?`, id).Scan(&one)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return false, ErrNoSuchMessage
		case err != nil:
			return false, fmt.Errorf("gateway: set result: %w", err)
		}
		return false, nil
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_events(message_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
		id, at.UnixMilli(), "result", result, reason); err != nil {
		return false, fmt.Errorf("gateway: set result event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("gateway: commit set result: %w", err)
	}
	return true, nil
}

// SetToSession records which recipient session a message was addressed to, before
// it is offered. It appends no event: the session is plumbing, not an outcome.
//
// UNTESTED.
func (s *Store) SetToSession(ctx context.Context, id, toSession string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET to_session=? WHERE id=?`, toSession, id)
	if err != nil {
		return fmt.Errorf("gateway: set to session: %w", err)
	}
	if affected, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("gateway: set to session: %w", err)
	} else if affected == 0 {
		return ErrNoSuchMessage
	}
	return nil
}

// MessageInfoFor renders a stored row as the wire view a runtime keeps.
//
// A message still at "stored" is reported as gateway_uncertain: the gateway has it
// and has decided nothing, and "uncertain" is the only honest word for that. A
// runtime treats uncertain-to-uncertain as no change.
//
// UNTESTED.
func MessageInfoFor(row MessageRow) protocol.MessageInfo {
	result := protocol.SendResult(row.Result)
	if result == resultStored {
		result = protocol.SendGatewayUncertain
	}

	return protocol.MessageInfo{
		ID:           row.ID,
		Result:       result,
		ResultReason: row.ResultReason,
		Report:       protocol.MessageState(row.Report),
		ReportReason: row.ReportReason,
	}
}
