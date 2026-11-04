package store

import (
	"context"
	"database/sql"
	"time"
)

// Why a message that was unfinished at the end of a run became interrupted
// (design section 9, "After a restart").
const (
	ReasonNeverWritten      = "never_written"
	ReasonWriteUnknown      = "write_unknown"
	ReasonPastedUnconfirmed = "pasted_unconfirmed"
)

// RecoveredRun is an unclosed run found at startup.
type RecoveredRun struct {
	RunID       string
	Interrupted int
}

// FinishRun ends a run: every unfinished message becomes interrupted with the
// matching reason, open agents get an exit time, and the run gets endReason
// ("clean" on quit), in one transaction. It never resends anything. It returns
// the number of messages interrupted.
func (s *Store) FinishRun(ctx context.Context, runID, endReason string, at time.Time) (n int, err error) {
	err = s.do(ctx, func(tx *sql.Tx) error {
		n, err = finishRun(tx, runID, endReason, at)
		return err
	})
	return n, err
}

// Recover closes every run left unclosed by a crash as "crashed". It is safe
// only because Open holds the per-machine lock, so no live run can exist. Run
// it before Retain and before any harness starts.
func (s *Store) Recover(ctx context.Context, at time.Time) (out []RecoveredRun, err error) {
	err = s.do(ctx, func(tx *sql.Tx) error {
		out = nil
		rows, err := tx.Query(`SELECT id FROM runs WHERE ended_at IS NULL ORDER BY started_at`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			n, err := finishRun(tx, id, "crashed", at)
			if err != nil {
				return err
			}
			out = append(out, RecoveredRun{RunID: id, Interrupted: n})
		}
		return nil
	})
	return out, err
}

func finishRun(tx *sql.Tx, runID, endReason string, at time.Time) (int, error) {
	const unfinished = `state IN ('queued','held','delivering','pasted')`
	// Events first: they read each message's state before the update changes it.
	if _, err := tx.Exec(`
		INSERT INTO message_events(message_id, at, from_state, to_state, reason)
		SELECT id, ?, state, 'interrupted',
		       CASE state WHEN 'delivering' THEN ? WHEN 'pasted' THEN ? ELSE ? END
		FROM messages WHERE run_id=? AND `+unfinished,
		ms(at), ReasonWriteUnknown, ReasonPastedUnconfirmed, ReasonNeverWritten, runID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE messages SET state='interrupted', updated_at=? WHERE run_id=? AND `+unfinished, ms(at), runID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE agents SET exited_at=? WHERE run_id=? AND exited_at IS NULL`, ms(at), runID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE runs SET ended_at=?, end_reason=? WHERE id=?`, ms(at), endReason, runID); err != nil {
		return 0, err
	}
	return int(n), nil
}

// Retain deletes runs that ended before cutoff, with their agents, messages and
// events (cascade). It returns the number of runs deleted.
func (s *Store) Retain(ctx context.Context, cutoff time.Time) (n int, err error) {
	err = s.do(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM runs WHERE ended_at IS NOT NULL AND ended_at < ?`, ms(cutoff))
		if err != nil {
			return err
		}
		c, err := res.RowsAffected()
		n = int(c)
		return err
	})
	return n, err
}
