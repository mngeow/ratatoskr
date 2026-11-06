package store

// The cross-machine half of the runtime store: outbound attempts (sends), their
// events, and the inbound bookkeeping the gateway needs.
//
// Everything here is additive. A database that never runs connected has no rows
// in these tables, so every existing query returns exactly what it returned
// before.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No send has been recorded,
// reported or read.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	sendEventResult = "result"
	sendEventReport = "report"

	// defaultSendsLimit is how many sends a read returns when no limit is given.
	defaultSendsLimit = 50 // UNTESTED guess
)

// Send is one outbound attempt to record. The caller has already chosen the ID.
type Send struct {
	ID        string
	RunID     string
	From      protocol.AgentID
	To        string
	ReplyTo   string
	Body      string
	CreatedAt time.Time
}

// SendRow is one stored outbound attempt.
type SendRow struct {
	Seq    int64
	ID     string
	RunID  string
	FromID protocol.AgentID
	// From is the sender's display name from the agents table, empty when no
	// agent row matches.
	From         string
	To           string
	ReplyTo      string
	Body         string
	Result       protocol.SendResult
	Report       protocol.MessageState
	ReportReason string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SendEventRow is one recorded result or report of a send.
type SendEventRow struct {
	At     time.Time
	Kind   string
	Value  string
	Reason string
}

// InboundRow is one inbound message whose state the gateway has not been told.
type InboundRow struct {
	ID     string
	State  protocol.MessageState
	Reason string
	At     time.Time
}

// sendLegal is the edge set for an outbound attempt's result, in the same style
// as legal in ops.go. Design: "Send path (connected mode)" and "Link".
//
// A result reached from sending is final except for the two uncertain ones, and
// those can only ever improve: gateway_uncertain means a request may have been
// stored, so a later lookup can report any truth; delivery_uncertain means the
// recipient was offered the message, so all that is left to learn is that the
// host stored it or that it refused it. Nothing moves out of a definitely-failed or definitely-saved
// result.
var sendLegal = map[protocol.SendResult][]protocol.SendResult{
	protocol.SendSending: {
		protocol.SendGatewayUnreachable,
		protocol.SendGatewayUncertain,
		protocol.SendUnknownRecipient,
		protocol.SendDestinationUnreachable,
		protocol.SendSessionGone,
		protocol.SendDeliveryUncertain,
		protocol.SendHostStored,
	},
	protocol.SendGatewayUncertain: {
		// gateway_unreachable stays a legal edge, but the router does NOT use it
		// for "the gateway has no record": that answer rests on timing (a request
		// the gateway had received may still have been in flight), so the send stays
		// gateway_uncertain. An uncertain case is never reported as a definite
		// failure.
		protocol.SendGatewayUnreachable,
		protocol.SendUnknownRecipient,
		protocol.SendDestinationUnreachable,
		protocol.SendSessionGone,
		protocol.SendDeliveryUncertain,
		protocol.SendHostStored,
	},
	protocol.SendDeliveryUncertain: {
		// A late ack: the host saved it, or (a late refusal) it certainly did not
		// and will not arrive. Either way the sender's record must stop saying
		// "may still arrive".
		protocol.SendHostStored,
		protocol.SendSessionGone,
	},
}

// isSendLegal reports whether an outbound attempt may move from one result to
// another. No entry means no edge.
func isSendLegal(from, to protocol.SendResult) bool {
	for _, candidate := range sendLegal[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// InsertSend records one attempt as "sending" and appends its first event.
//
// It goes through the single writer like every other write, so it honours the
// unavailable latch and never runs concurrently with a message write.
//
// UNTESTED.
func (s *Store) InsertSend(ctx context.Context, send Send) error {
	return s.do(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO sends(id, run_id, from_agent_id, to_address, reply_to, body, result, report, report_reason, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,'','',?,?)`,
			send.ID, send.RunID, string(send.From), send.To, send.ReplyTo, send.Body,
			string(protocol.SendSending), ms(send.CreatedAt), ms(send.CreatedAt)); err != nil {
			// A duplicate id is a caller bug: every attempt gets a fresh one.
			return fmt.Errorf("store: insert send %s: %w", send.ID, err)
		}

		if _, err := tx.Exec(`INSERT INTO send_events(send_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
			send.ID, ms(send.CreatedAt), sendEventResult, string(protocol.SendSending), ""); err != nil {
			return fmt.Errorf("store: insert send event %s: %w", send.ID, err)
		}
		return nil
	})
}

// FinishSend records the result of an attempt.
//
// It is a compare-and-set exactly like Transition: an edge that is not in
// sendLegal is refused without touching the database, and a row that is not in
// the expected result gives ErrStateChanged.
//
// UNTESTED.
func (s *Store) FinishSend(ctx context.Context, id string, from, to protocol.SendResult, reason string, at time.Time) error {
	if !isSendLegal(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}

	return s.do(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE sends SET result=?, updated_at=? WHERE id=? AND result=?`,
			string(to), ms(at), id, string(from))
		if err != nil {
			return fmt.Errorf("store: finish send %s: %w", id, err)
		}
		if affected, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("store: finish send %s: %w", id, err)
		} else if affected == 0 {
			return ErrStateChanged
		}

		if _, err := tx.Exec(`INSERT INTO send_events(send_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
			id, ms(at), sendEventResult, string(to), reason); err != nil {
			return fmt.Errorf("store: insert send event %s: %w", id, err)
		}
		return nil
	})
}

// ReportSend records what the recipient host said about an inbound message.
//
// It never changes the attempt's result. It is idempotent: the same state and
// reason twice writes nothing and adds no second event, because reports are
// resent after a reconnect. A report can be set but never cleared: an empty
// state is a programming error and is refused before the database is touched.
//
// UNTESTED.
func (s *Store) ReportSend(ctx context.Context, id string, state protocol.MessageState, reason string, at time.Time) error {
	if state == "" {
		return errors.New("store: report state must not be empty")
	}

	return s.do(ctx, func(tx *sql.Tx) error {
		// One read answers both questions: does the row exist, and is this report
		// the one already stored?
		var storedReport, storedReason string
		err := tx.QueryRow(`SELECT report, report_reason FROM sends WHERE id=?`, id).
			Scan(&storedReport, &storedReason)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("store: report send %s: %w", id, err)
		case protocol.MessageState(storedReport) == state && storedReason == reason:
			return nil
		}

		if _, err := tx.Exec(`UPDATE sends SET report=?, report_reason=?, updated_at=? WHERE id=?`,
			string(state), reason, ms(at), id); err != nil {
			return fmt.Errorf("store: report send %s: %w", id, err)
		}
		if _, err := tx.Exec(`INSERT INTO send_events(send_id, at, kind, value, reason) VALUES(?,?,?,?,?)`,
			id, ms(at), sendEventReport, string(state), reason); err != nil {
			return fmt.Errorf("store: report send event %s: %w", id, err)
		}
		return nil
	})
}

// InsertInbound stores a message that arrived from another host.
//
// The same id applied twice is not an error: the first call wins and the second
// reports inserted=false without writing anything at all, which is how a repeated
// offer is re-acknowledged without queueing the message twice.
//
// The caller puts the sender's network name in m.From, because a remote sender has
// no agents row on this machine; the inbound row keeps that name so History can
// display it.
//
// UNTESTED.
func (s *Store) InsertInbound(ctx context.Context, m Message, origin string) (inserted bool, seq int64, err error) {
	err = s.do(ctx, func(tx *sql.Tx) error {
		inserted, seq = false, 0

		res, execErr := tx.Exec(`INSERT INTO messages(id, run_id, from_agent_id, to_agent_id, reply_to, body, state, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			m.ID, m.RunID, string(m.From), string(m.To), m.ReplyTo, m.Body,
			string(protocol.MessageQueued), ms(m.CreatedAt), ms(m.CreatedAt))
		if execErr != nil {
			return fmt.Errorf("store: insert inbound %s: %w", m.ID, execErr)
		}
		affected, affectedErr := res.RowsAffected()
		if affectedErr != nil {
			return fmt.Errorf("store: insert inbound %s: %w", m.ID, affectedErr)
		}
		if affected == 0 {
			// Already there: ack again, write nothing.
			return nil
		}

		if seq, execErr = res.LastInsertId(); execErr != nil {
			return fmt.Errorf("store: insert inbound %s: %w", m.ID, execErr)
		}
		if _, execErr := tx.Exec(`INSERT INTO message_events(message_id, at, from_state, to_state, reason) VALUES(?,?,?,?,?)`,
			m.ID, ms(m.CreatedAt), "", string(protocol.MessageQueued), ""); execErr != nil {
			return fmt.Errorf("store: insert inbound event %s: %w", m.ID, execErr)
		}
		if _, execErr := tx.Exec(`INSERT INTO inbound(message_id, origin, reported_state) VALUES(?,?,'')`,
			m.ID, origin); execErr != nil {
			return fmt.Errorf("store: insert inbound origin %s: %w", m.ID, execErr)
		}

		inserted = true
		return nil
	})
	return inserted, seq, err
}

// MarkReported records that the gateway has been told this message's state.
//
// UNTESTED.
func (s *Store) MarkReported(ctx context.Context, id string, state protocol.MessageState) error {
	return s.do(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE inbound SET reported_state=? WHERE message_id=?`, string(state), id)
		if err != nil {
			return fmt.Errorf("store: mark reported %s: %w", id, err)
		}
		if affected, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("store: mark reported %s: %w", id, err)
		} else if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// sendSelect is the one column list both send reads use.
const sendSelect = `
		SELECT s.seq, s.id, s.run_id, s.from_agent_id, COALESCE(f.display_name, ''),
		       s.to_address, s.reply_to, s.body, s.result, s.report, s.report_reason,
		       s.created_at, s.updated_at
		FROM sends s
		LEFT JOIN agents f ON f.id = s.from_agent_id`

// Sends returns a run's outbound attempts: the newest limit of them, oldest
// first, like History.
//
// It reads through the read handle and ignores the unavailable latch, because a
// latched write path must not stop someone reading what was already recorded.
//
// UNTESTED.
func (s *Store) Sends(ctx context.Context, runID string, limit int) ([]SendRow, error) {
	if limit <= 0 {
		limit = defaultSendsLimit
	}

	rows, err := s.rdb.QueryContext(ctx, sendSelect+`
		WHERE s.run_id=? ORDER BY s.seq DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read sends: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	sends := make([]SendRow, 0)
	for rows.Next() {
		row, err := scanSend(rows)
		if err != nil {
			return nil, err
		}
		sends = append(sends, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read sends: %w", err)
	}

	// Newest first from SQL, oldest first for the reader.
	for i, j := 0, len(sends)-1; i < j; i, j = i+1, j-1 {
		sends[i], sends[j] = sends[j], sends[i]
	}
	return sends, nil
}

// Send returns one attempt with its events, oldest first.
//
// UNTESTED.
func (s *Store) Send(ctx context.Context, id string) (SendRow, []SendEventRow, error) {
	row, err := scanSend(s.rdb.QueryRowContext(ctx, sendSelect+` WHERE s.id=?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SendRow{}, nil, ErrNotFound
	case err != nil:
		return SendRow{}, nil, err
	}

	rows, err := s.rdb.QueryContext(ctx,
		`SELECT at, kind, value, reason FROM send_events WHERE send_id=? ORDER BY id ASC`, id)
	if err != nil {
		return SendRow{}, nil, fmt.Errorf("store: read send events: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	events := make([]SendEventRow, 0)
	for rows.Next() {
		var at int64
		var event SendEventRow
		if err := rows.Scan(&at, &event.Kind, &event.Value, &event.Reason); err != nil {
			return SendRow{}, nil, fmt.Errorf("store: read send events: %w", err)
		}
		event.At = time.UnixMilli(at)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return SendRow{}, nil, fmt.Errorf("store: read send events: %w", err)
	}
	return row, events, nil
}

// InboundUnreported lists this run's inbound messages whose state the gateway has
// not been told, oldest first.
//
// A message that was just stored reports "queued" first, so it is expected to
// appear here until the report is acknowledged.
//
// UNTESTED.
func (s *Store) InboundUnreported(ctx context.Context, runID string) ([]InboundRow, error) {
	rows, err := s.rdb.QueryContext(ctx, `
		SELECT m.id, m.state,
		       COALESCE((SELECT e.reason FROM message_events e WHERE e.message_id=m.id ORDER BY e.id DESC LIMIT 1), ''),
		       m.updated_at
		FROM inbound i
		JOIN messages m ON m.id = i.message_id
		WHERE m.run_id=? AND m.state <> i.reported_state
		ORDER BY m.seq ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: read unreported inbound: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	inbound := make([]InboundRow, 0)
	for rows.Next() {
		var id, state, reason string
		var updated int64
		if err := rows.Scan(&id, &state, &reason, &updated); err != nil {
			return nil, fmt.Errorf("store: read unreported inbound: %w", err)
		}
		inbound = append(inbound, InboundRow{
			ID:     id,
			State:  protocol.MessageState(state),
			Reason: reason,
			At:     time.UnixMilli(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read unreported inbound: %w", err)
	}
	return inbound, nil
}

// scanner is what both *sql.Row and *sql.Rows satisfy, so one scan helper serves
// the single-row and multi-row reads.
type scanner interface {
	Scan(dest ...any) error
}

// scanSend reads one row of sendSelect.
func scanSend(row scanner) (SendRow, error) {
	var send SendRow
	var from, result, report string
	var created, updated int64

	if err := row.Scan(&send.Seq, &send.ID, &send.RunID, &from, &send.From,
		&send.To, &send.ReplyTo, &send.Body, &result, &report, &send.ReportReason,
		&created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SendRow{}, sql.ErrNoRows
		}
		return SendRow{}, fmt.Errorf("store: read send: %w", err)
	}

	send.FromID = protocol.AgentID(from)
	send.Result = protocol.SendResult(result)
	send.Report = protocol.MessageState(report)
	send.CreatedAt, send.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	return send, nil
}
