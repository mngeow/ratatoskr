package gateway

// The gateway's stored cards: targets, the last good card per host, the older good cards and the poll
// attempts. All of it is in SQLite (0002_host_cards.sql).
//
// A stored card is a record of what the gateway last saw, never proof of anything live. Nothing here
// is held across a network call: each function is one short statement or one short transaction.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"net/netip"
	"time"
)

const (
	// maxCardRevisionsKept is how many older good cards are kept per host.
	maxCardRevisionsKept = 20 // UNTESTED guess; contract default
	// maxPollAttemptsKept and pollAttemptsKeptFor bound the attempts per host: whichever limit is
	// smaller wins.
	maxPollAttemptsKept = 1000               // UNTESTED guess; contract default
	pollAttemptsKeptFor = 7 * 24 * time.Hour // UNTESTED guess; contract default
)

// ErrNoSuchCardTarget is returned when a host has no registered card target.
var ErrNoSuchCardTarget = errors.New("gateway: that host has no card target")

// CardRow is one host's stored card state.
type CardRow struct {
	Host          string
	CardJSON      string // canonical bytes of the last good card; "" when none
	Revision      string
	Hash          string
	UnknownFields int
	FetchedAt     time.Time
	LastOKAt      time.Time
	LastAttemptAt time.Time
	LastResult    string
	LastReason    string
}

// PollRecord is one finished poll, as the poller hands it over.
//
// TargetUpdatedAt is the target's updated_at when the poll STARTED. If the target changed while the
// poll ran (another process ran `card-target set` or `clear`), the result belongs to a target that no
// longer exists and is dropped rather than stored.
//
// The card fields are set only for a good 200.
type PollRecord struct {
	Host            string
	TargetUpdatedAt time.Time
	At              time.Time
	Duration        time.Duration
	Outcome         string
	Status          int
	Reason          string

	CardJSON      string
	Revision      string
	Hash          string
	UnknownFields int
}

// msOrNull stores a zero time as NULL.
func msOrNull(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at.UnixMilli()
}

// timeFrom reads a nullable millisecond column.
func timeFrom(value sql.NullInt64) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return time.UnixMilli(value.Int64)
}

// SetCardTarget registers or replaces a host's card target.
//
// The host must be paired. A target that differs from the stored one (scheme, address or pin) clears
// the host's stored card and revisions in the same transaction: they came from a different endpoint,
// and a 304 must never confirm a card fetched from somewhere else. Setting the identical target again
// only moves updated_at, which is how an owner resumes a host paused on a certificate mismatch.
//
// UNTESTED.
func (s *Store) SetCardTarget(ctx context.Context, target CardTarget, now time.Time) (changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("gateway: begin card target: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM hosts WHERE name=?`, target.Host).Scan(&exists); err != nil {
		return false, fmt.Errorf("gateway: look up host: %w", err)
	}
	if exists == 0 {
		return false, ErrNoSuchHost
	}

	var scheme, addr, fingerprint string
	err = tx.QueryRowContext(ctx,
		`SELECT scheme, addr, fingerprint FROM host_card_targets WHERE host=?`, target.Host).Scan(&scheme, &addr, &fingerprint)
	previous := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("gateway: read card target: %w", err)
	}

	changed = !previous || scheme != target.Scheme || addr != target.Addr.String() || fingerprint != target.Fingerprint

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO host_card_targets(host, scheme, addr, class, fingerprint, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(host) DO UPDATE SET scheme=excluded.scheme, addr=excluded.addr, class=excluded.class,
		   fingerprint=excluded.fingerprint, updated_at=excluded.updated_at`,
		target.Host, target.Scheme, target.Addr.String(), classKey(target.Class), target.Fingerprint,
		now.UnixMilli(), now.UnixMilli()); err != nil {
		return false, fmt.Errorf("gateway: store card target: %w", err)
	}

	if changed {
		if err := clearCardsTx(ctx, tx, target.Host); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("gateway: commit card target: %w", err)
	}
	return changed, nil
}

// ClearCardTarget removes a host's target, stored card, revisions and attempts.
//
// UNTESTED.
func (s *Store) ClearCardTarget(ctx context.Context, host string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway: begin clear card target: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	result, err := tx.ExecContext(ctx, `DELETE FROM host_card_targets WHERE host=?`, host)
	if err != nil {
		return fmt.Errorf("gateway: clear card target: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("gateway: clear card target: %w", err)
	} else if affected == 0 {
		return ErrNoSuchCardTarget
	}
	if err := clearCardsTx(ctx, tx, host); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM poll_attempts WHERE host=?`, host); err != nil {
		return fmt.Errorf("gateway: clear poll attempts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway: commit clear card target: %w", err)
	}
	return nil
}

// clearCardsTx drops a host's stored card and revisions in the caller's transaction.
func clearCardsTx(ctx context.Context, tx *sql.Tx, host string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM host_cards WHERE host=?`, host); err != nil {
		return fmt.Errorf("gateway: clear stored card: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM card_revisions WHERE host=?`, host); err != nil {
		return fmt.Errorf("gateway: clear card revisions: %w", err)
	}
	return nil
}

// CardTargets lists every registered target, by host name. A row whose address or class this build
// cannot read is skipped, not guessed at: it is reported through the second result so the caller can
// log it once.
//
// UNTESTED.
func (s *Store) CardTargets(ctx context.Context) ([]CardTarget, []string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT host, scheme, addr, class, fingerprint, created_at, updated_at FROM host_card_targets ORDER BY host`)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: list card targets: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var targets []CardTarget
	var unreadable []string
	for rows.Next() {
		var host, scheme, addr, class, fingerprint string
		var created, updated int64
		if err := rows.Scan(&host, &scheme, &addr, &class, &fingerprint, &created, &updated); err != nil {
			return nil, nil, fmt.Errorf("gateway: list card targets: %w", err)
		}
		parsed, parseErr := netip.ParseAddrPort(addr)
		classValue, classOK := classFromKey(class)
		if parseErr != nil || !classOK || (scheme != "http" && scheme != "https") {
			unreadable = append(unreadable, host)
			continue
		}
		targets = append(targets, CardTarget{
			Host: host, Scheme: scheme, Addr: parsed, Class: classValue, Fingerprint: fingerprint,
			CreatedAt: time.UnixMilli(created), UpdatedAt: time.UnixMilli(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("gateway: list card targets: %w", err)
	}
	return targets, unreadable, nil
}

// CardRows returns every host's stored card state, keyed by host.
//
// UNTESTED.
func (s *Store) CardRows(ctx context.Context) (map[string]CardRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT host, card_json, revision, hash, unknown_fields, fetched_at, last_ok_at, last_attempt_at,
		        last_result, last_reason FROM host_cards`)
	if err != nil {
		return nil, fmt.Errorf("gateway: list cards: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	out := make(map[string]CardRow, 4)
	for rows.Next() {
		var row CardRow
		var fetched, lastOK, lastAttempt sql.NullInt64
		if err := rows.Scan(&row.Host, &row.CardJSON, &row.Revision, &row.Hash, &row.UnknownFields,
			&fetched, &lastOK, &lastAttempt, &row.LastResult, &row.LastReason); err != nil {
			return nil, fmt.Errorf("gateway: list cards: %w", err)
		}
		row.FetchedAt = timeFrom(fetched)
		row.LastOKAt = timeFrom(lastOK)
		row.LastAttemptAt = timeFrom(lastAttempt)
		out[row.Host] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list cards: %w", err)
	}
	return out, nil
}

// RecordPoll stores one finished poll and, for a good 200, the card.
//
// It reports whether the result was applied: false means the target changed or was cleared while the
// poll ran, so the result described an endpoint that is no longer registered and was dropped. One
// short transaction; the network call is long over by the time this runs.
//
// What each outcome does to the stored card:
//
//   - 200 (card fields set): the previous card, if its hash differs, becomes an older revision (the
//     newest 20 are kept) and the new one replaces it;
//   - 200 with the same hash: no revision; last_ok_at and fetched_at move;
//   - anything else: only the attempt is recorded and the last good card stays, so a failed poll can
//     never erase what the gateway last saw.
//
// UNTESTED.
func (s *Store) RecordPoll(ctx context.Context, record PollRecord) (applied bool, err error) {
	applied, _, err = s.RecordPollChange(ctx, record)
	return applied, err
}

// RecordPollChange is RecordPoll that also says whether anything a reader sees changed: the stored card,
// the result class or the reason. A repeated identical success moves last_ok_at only, and is not a change
// (contract section 16.5), so it must not wake the graph's long polls.
//
// Attempt history at a 5 s rate: a row is written for every failure and for the first success after a
// failure, after a card change or with no history; consecutive identical successes update host_cards
// only.
//
// UNTESTED.
func (s *Store) RecordPollChange(ctx context.Context, record PollRecord) (applied, changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("gateway: begin poll record: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var updated int64
	err = tx.QueryRowContext(ctx, `SELECT updated_at FROM host_card_targets WHERE host=?`, record.Host).Scan(&updated)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && updated != record.TargetUpdatedAt.UnixMilli()) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("gateway: read card target: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO host_cards(host) VALUES(?)`, record.Host); err != nil {
		return false, false, fmt.Errorf("gateway: create card row: %w", err)
	}
	var prevResult, prevReason, prevHash string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(last_result,''), COALESCE(last_reason,''), COALESCE(hash,'') FROM host_cards WHERE host=?`, record.Host).
		Scan(&prevResult, &prevReason, &prevHash); err != nil {
		return false, false, fmt.Errorf("gateway: read card row: %w", err)
	}
	cardChanged := record.CardJSON != "" && record.Hash != prevHash
	changed = cardChanged || record.Outcome != prevResult || record.Reason != prevReason

	if record.Outcome != protocol.PollOK || prevResult != protocol.PollOK || cardChanged {
		var status any
		if record.Status != 0 {
			status = record.Status
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO poll_attempts(host, at, outcome, http_status, duration_ms, reason) VALUES(?,?,?,?,?,?)`,
			record.Host, record.At.UnixMilli(), record.Outcome, status, record.Duration.Milliseconds(), record.Reason); err != nil {
			return false, false, fmt.Errorf("gateway: record poll attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM poll_attempts WHERE host=? AND (at < ? OR id NOT IN
			   (SELECT id FROM poll_attempts WHERE host=? ORDER BY id DESC LIMIT ?))`,
			record.Host, record.At.Add(-pollAttemptsKeptFor).UnixMilli(), record.Host, maxPollAttemptsKept); err != nil {
			return false, false, fmt.Errorf("gateway: prune poll attempts: %w", err)
		}
	}

	switch {
	case record.CardJSON != "":
		if err := replaceCardTx(ctx, tx, record); err != nil {
			return false, false, err
		}
	default:
		if _, err := tx.ExecContext(ctx,
			`UPDATE host_cards SET last_attempt_at=?, last_result=?, last_reason=? WHERE host=?`,
			record.At.UnixMilli(), record.Outcome, record.Reason, record.Host); err != nil {
			return false, false, fmt.Errorf("gateway: record failed poll: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("gateway: commit poll record: %w", err)
	}
	return true, changed, nil
}

// replaceCardTx stores a good card, moving a different previous one into the revision history.
func replaceCardTx(ctx context.Context, tx *sql.Tx, record PollRecord) error {
	var oldJSON, oldRevision, oldHash string
	var oldFetched sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT card_json, revision, hash, fetched_at FROM host_cards WHERE host=?`, record.Host).
		Scan(&oldJSON, &oldRevision, &oldHash, &oldFetched); err != nil {
		return fmt.Errorf("gateway: read stored card: %w", err)
	}

	if oldJSON != "" && oldHash != record.Hash {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO card_revisions(host, revision, etag, hash, card_json, fetched_at) VALUES(?,?,'',?,?,?)`,
			record.Host, oldRevision, oldHash, oldJSON, timeFrom(oldFetched).UnixMilli()); err != nil {
			return fmt.Errorf("gateway: keep older card: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM card_revisions WHERE host=? AND id NOT IN
			   (SELECT id FROM card_revisions WHERE host=? ORDER BY id DESC LIMIT ?)`,
			record.Host, record.Host, maxCardRevisionsKept); err != nil {
			return fmt.Errorf("gateway: prune card revisions: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE host_cards SET card_json=?, revision=?, hash=?, unknown_fields=?, fetched_at=?, last_ok_at=?,
		   last_attempt_at=?, last_result=?, last_reason=? WHERE host=?`,
		record.CardJSON, record.Revision, record.Hash, record.UnknownFields,
		record.At.UnixMilli(), record.At.UnixMilli(), record.At.UnixMilli(), record.Outcome, record.Reason, record.Host); err != nil {
		return fmt.Errorf("gateway: store card: %w", err)
	}
	return nil
}
