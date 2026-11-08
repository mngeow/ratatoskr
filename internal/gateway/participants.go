package gateway

// Participants: external adapters that use the participant API. Their pairing, their credentials, their
// last poll and the directory that includes their sessions.
//
// A participant session lives in the existing sessions table with the participant id in the host column, so
// CurrentSession, EverSeen, EndSession and the ledger's session columns work unchanged. That holds only
// while an id and a host name can never be equal; every path that creates either one checks the other
// inside the transaction that writes it.
//
// UNTESTED. Under GR-3 nothing here has been run, and migration 0003 has never touched a database.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The sentinels of this file. None of their texts may contain a code, a credential or a hash.
var (
	// ErrNameIsHost refuses a participant id that is a paired host.
	ErrNameIsHost = errors.New("gateway: that name is a paired host, and a participant id must differ from every host name")
	// ErrNameHasHostHistory refuses a participant id that a host used before: it still appears in
	// sessions.host or in the host part of a ledger address, even if the host was unpaired since.
	ErrNameHasHostHistory = errors.New("gateway: that name was used by a host before, so it stays reserved: choose a different participant id")
	// ErrNameIsParticipant refuses a host name that is a participant (revoked ones keep their name).
	ErrNameIsParticipant = errors.New("gateway: that name belongs to a participant, and a host name must differ from every participant id")
	// ErrNoSuchParticipant: no participant has that id, or it is not active.
	ErrNoSuchParticipant = errors.New("gateway: no such active participant")
	// ErrParticipantRevoked refuses a session for a participant that was revoked.
	ErrParticipantRevoked = errors.New("gateway: the participant is revoked")
)

// ParticipantRow is one participant as the admin command shows it.
type ParticipantRow struct {
	ID           string
	PairedAt     time.Time
	LastPoll     time.Time // zero when it has never polled
	RevokedAt    time.Time // zero while active
	OpenSessions int
}

// Revoked reports whether the participant has been revoked.
func (p ParticipantRow) Revoked() bool { return !p.RevokedAt.IsZero() }

// Revocation says exactly what a revoke ended.
type Revocation struct {
	SessionsEnded int
	CodesRemoved  int
}

// WireDirectoryRow is one open session in the wire directory: a host session or a participant session.
// LastSeen is the host's last heartbeat, or the participant's last poll; zero when never heard from.
type WireDirectoryRow struct {
	Name        string
	Host        string // the host name, or the participant id
	SessionID   string
	LastSeen    time.Time
	Participant bool
}

// CreateParticipantCode mints one single-use code for a participant id and invalidates that id's older
// unused codes. The id must not be a paired host. On an existing, non-revoked id the code is a rotation:
// the credential is replaced only when the code is redeemed.
//
// UNTESTED.
func (s *Store) CreateParticipantCode(ctx context.Context, participant string, now time.Time) (string, error) {
	if !hostNamePattern.MatchString(participant) {
		return "", fmt.Errorf("%w: %q", ErrBadHostName, participant)
	}

	code, err := newCode()
	if err != nil {
		return "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("gateway: begin participant code: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if err := refuseHostName(ctx, tx, participant); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM participant_pairing_codes WHERE participant=? AND used_at IS NULL`, participant); err != nil {
		return "", fmt.Errorf("gateway: replace participant codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO participant_pairing_codes(code_hash, participant, created_at, expires_at, used_at) VALUES(?,?,?,?,NULL)`,
		hashText(normaliseCode(code)), participant, now.UnixMilli(), now.Add(codeTTL).UnixMilli()); err != nil {
		return "", fmt.Errorf("gateway: insert participant code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("gateway: commit participant code: %w", err)
	}
	return displayCode(code), nil
}

// RedeemParticipantCode exchanges one code for a credential. participant may be empty ("whichever id the
// code was minted for"); if it is given, a mismatch fails.
//
// Every way of failing - unknown code, wrong id, already used, expired, and an id that has become a host
// name since the code was minted - gives the same ErrBadCode, and a failed redemption does not mark the
// code used. The name check runs in the same transaction that writes the row, so a race between this and
// `rtk gateway pair HOST` cannot create both.
//
// Redeeming for an id that already exists replaces its credential, clears a revocation and ends its open
// sessions, so the old adapter cannot keep receiving offers.
//
// UNTESTED.
func (s *Store) RedeemParticipantCode(ctx context.Context, code, participant string, now time.Time) (credential, id string, err error) {
	if participant != "" && !hostNamePattern.MatchString(participant) {
		return "", "", fmt.Errorf("%w: %q", ErrBadHostName, participant)
	}
	codeHash := hashText(normaliseCode(code))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("gateway: begin participant redeem: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var stored string
	var expiresAt int64
	var usedAt sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT participant, expires_at, used_at FROM participant_pairing_codes WHERE code_hash=?`, codeHash).
		Scan(&stored, &expiresAt, &usedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", ErrBadCode
	case err != nil:
		return "", "", fmt.Errorf("gateway: read participant code: %w", err)
	case (participant != "" && stored != participant) || usedAt.Valid || expiresAt <= now.UnixMilli():
		return "", "", ErrBadCode
	}

	if err := refuseHostName(ctx, tx, stored); err != nil {
		if errors.Is(err, ErrNameIsHost) || errors.Is(err, ErrNameHasHostHistory) {
			return "", "", ErrBadCode
		}
		return "", "", err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE participant_pairing_codes SET used_at=? WHERE code_hash=?`, now.UnixMilli(), codeHash); err != nil {
		return "", "", fmt.Errorf("gateway: mark participant code used: %w", err)
	}

	credential, err = newCredential()
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO participants(id, credential_hash, created_at, last_poll_at, revoked_at) VALUES(?,?,?,NULL,NULL)
		 ON CONFLICT(id) DO UPDATE SET credential_hash=excluded.credential_hash,
		   created_at=excluded.created_at, last_poll_at=NULL, revoked_at=NULL`,
		stored, hashText(credential), now.UnixMilli()); err != nil {
		return "", "", fmt.Errorf("gateway: store participant: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET ended_at=? WHERE host=? AND ended_at IS NULL`, now.UnixMilli(), stored); err != nil {
		return "", "", fmt.Errorf("gateway: end participant sessions: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("gateway: commit participant redeem: %w", err)
	}
	return credential, stored, nil
}

// refuseHostName refuses a participant id that is a paired host (ErrNameIsHost) or that a host used
// before (ErrNameHasHostHistory): an unpaired former host stays reserved. The history is a session row
// with that name in sessions.host, or that name as the host part of any ledger from_address or
// to_address. An id that is already a participant is exempt, because its own sessions and messages carry
// the id in the same places (rotation, and pairing again after a revoke).
func refuseHostName(ctx context.Context, tx *sql.Tx, name string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM participants WHERE id=?`, name).Scan(&one)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("gateway: check participant name: %w", err)
	}

	err = tx.QueryRowContext(ctx, `SELECT 1 FROM hosts WHERE name=?`, name).Scan(&one)
	switch {
	case err == nil:
		return ErrNameIsHost
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("gateway: check host name: %w", err)
	}

	// Address format is name@host and a name never contains '@', so the host part starts after the
	// first '@'.
	err = tx.QueryRowContext(ctx, `
		SELECT 1 WHERE EXISTS (SELECT 1 FROM sessions WHERE host=?)
		   OR EXISTS (SELECT 1 FROM messages WHERE substr(from_address, instr(from_address, '@') + 1)=?)
		   OR EXISTS (SELECT 1 FROM messages WHERE substr(to_address, instr(to_address, '@') + 1)=?)`,
		name, name, name).Scan(&one)
	switch {
	case err == nil:
		return ErrNameHasHostHistory
	case errors.Is(err, sql.ErrNoRows):
		return nil
	}
	return fmt.Errorf("gateway: check host history: %w", err)
}

// refuseParticipantName returns ErrNameIsParticipant when name is a participant, revoked or not: a revoked
// participant's name stays reserved.
func refuseParticipantName(ctx context.Context, tx *sql.Tx, name string) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM participants WHERE id=?`, name).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("gateway: check participant name: %w", err)
	}
	return ErrNameIsParticipant
}

// AuthenticateParticipant maps a presented credential to its participant id. It reads the database on every
// call and caches nothing, so a revocation refuses the very next request. A revoked participant, an unknown
// credential and an empty one are the same ErrUnauthorized.
//
// UNTESTED.
func (s *Store) AuthenticateParticipant(ctx context.Context, credential string) (string, error) {
	if credential == "" {
		return "", ErrUnauthorized
	}
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM participants WHERE credential_hash=? AND revoked_at IS NULL`, hashText(credential)).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrUnauthorized
	case err != nil:
		return "", fmt.Errorf("gateway: authenticate participant: %w", err)
	}
	return id, nil
}

// GetParticipant returns one participant, revoked or not, with its open session count.
//
// UNTESTED.
func (s *Store) GetParticipant(ctx context.Context, id string) (ParticipantRow, bool, error) {
	if id == "" {
		// listParticipants reads "" as "all of them".
		return ParticipantRow{}, false, nil
	}
	rows, err := s.listParticipants(ctx, id)
	if err != nil {
		return ParticipantRow{}, false, err
	}
	if len(rows) == 0 {
		return ParticipantRow{}, false, nil
	}
	return rows[0], true, nil
}

// Participants lists every participant, revoked ones included, by id.
//
// UNTESTED.
func (s *Store) Participants(ctx context.Context) ([]ParticipantRow, error) {
	return s.listParticipants(ctx, "")
}

func (s *Store) listParticipants(ctx context.Context, only string) ([]ParticipantRow, error) {
	query := `
		SELECT p.id, p.created_at, p.last_poll_at, p.revoked_at,
		       (SELECT COUNT(*) FROM sessions s WHERE s.host = p.id AND s.ended_at IS NULL)
		FROM participants p`
	var args []any
	if only != "" {
		query += ` WHERE p.id=?`
		args = append(args, only)
	}
	query += ` ORDER BY p.id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("gateway: list participants: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	out := make([]ParticipantRow, 0)
	for rows.Next() {
		var row ParticipantRow
		var created int64
		var poll, revoked sql.NullInt64
		if err := rows.Scan(&row.ID, &created, &poll, &revoked, &row.OpenSessions); err != nil {
			return nil, fmt.Errorf("gateway: list participants: %w", err)
		}
		row.PairedAt = time.UnixMilli(created)
		if poll.Valid {
			row.LastPoll = time.UnixMilli(poll.Int64)
		}
		if revoked.Valid {
			row.RevokedAt = time.UnixMilli(revoked.Int64)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list participants: %w", err)
	}
	return out, nil
}

// TouchPoll records the gateway's own time as the participant's last poll (written when a poll begins and
// when it ends). A revoked or unknown participant is ErrNoSuchParticipant.
//
// UNTESTED.
func (s *Store) TouchPoll(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE participants SET last_poll_at=? WHERE id=? AND revoked_at IS NULL`, now.UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("gateway: touch poll: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("gateway: touch poll: %w", err)
	} else if affected == 0 {
		return ErrNoSuchParticipant
	}
	return nil
}

// RevokeParticipant sets revoked_at, ends every open session of the participant and removes its unused
// pairing codes, in one transaction. It says exactly what it ended. An unknown or already revoked id is
// ErrNoSuchParticipant.
//
// UNTESTED.
func (s *Store) RevokeParticipant(ctx context.Context, id string, now time.Time) (Revocation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Revocation{}, fmt.Errorf("gateway: begin revoke: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	result, err := tx.ExecContext(ctx,
		`UPDATE participants SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now.UnixMilli(), id)
	if err != nil {
		return Revocation{}, fmt.Errorf("gateway: revoke participant: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Revocation{}, fmt.Errorf("gateway: revoke participant: %w", err)
	} else if affected == 0 {
		return Revocation{}, ErrNoSuchParticipant
	}

	var revocation Revocation
	ended, err := tx.ExecContext(ctx,
		`UPDATE sessions SET ended_at=? WHERE host=? AND ended_at IS NULL`, now.UnixMilli(), id)
	if err != nil {
		return Revocation{}, fmt.Errorf("gateway: end participant sessions: %w", err)
	}
	if n, err := ended.RowsAffected(); err == nil {
		revocation.SessionsEnded = int(n)
	}
	removed, err := tx.ExecContext(ctx,
		`DELETE FROM participant_pairing_codes WHERE participant=? AND used_at IS NULL`, id)
	if err != nil {
		return Revocation{}, fmt.Errorf("gateway: remove participant codes: %w", err)
	}
	if n, err := removed.RowsAffected(); err == nil {
		revocation.CodesRemoved = int(n)
	}

	if err := tx.Commit(); err != nil {
		return Revocation{}, fmt.Errorf("gateway: commit revoke: %w", err)
	}
	return revocation, nil
}

// AddParticipantSession registers one session of a participant, like AddSession does for a host, and checks
// in the same transaction that the participant exists and is not revoked, so a revocation cannot be raced
// by a registration. The limit is the host's (maxAgentsPerHost), counted after replacement.
//
// UNTESTED.
func (s *Store) AddParticipantSession(ctx context.Context, participant, name string, now time.Time, newID func() (string, error)) (SessionRow, error) {
	return s.addSession(ctx, participant, name, now, newID, func(ctx context.Context, tx *sql.Tx) error {
		var revoked sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM participants WHERE id=?`, participant).Scan(&revoked)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNoSuchParticipant
		case err != nil:
			return fmt.Errorf("gateway: check participant: %w", err)
		case revoked.Valid:
			return ErrParticipantRevoked
		}
		return nil
	})
}

// WireDirectory lists every open session a participant or a host may be told about: host sessions of
// paired hosts and open sessions of participants that are not revoked. Store.Directory is unchanged and
// still feeds the Network and Graph readers, which show hosts only.
//
// UNTESTED.
func (s *Store) WireDirectory(ctx context.Context) ([]WireDirectoryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.name, s.host, s.id, COALESCE(h.last_heartbeat_at, 0), 0
		FROM sessions s
		JOIN hosts h ON h.name = s.host
		WHERE s.ended_at IS NULL
		UNION ALL
		SELECT s.name, s.host, s.id, COALESCE(p.last_poll_at, 0), 1
		FROM sessions s
		JOIN participants p ON p.id = s.host AND p.revoked_at IS NULL
		WHERE s.ended_at IS NULL
		ORDER BY 2, 1`)
	if err != nil {
		return nil, fmt.Errorf("gateway: wire directory: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	out := make([]WireDirectoryRow, 0)
	for rows.Next() {
		var row WireDirectoryRow
		var seen int64
		var participant int
		if err := rows.Scan(&row.Name, &row.Host, &row.SessionID, &seen, &participant); err != nil {
			return nil, fmt.Errorf("gateway: wire directory: %w", err)
		}
		if seen != 0 {
			row.LastSeen = time.UnixMilli(seen)
		}
		row.Participant = participant == 1
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: wire directory: %w", err)
	}
	return out, nil
}

// ParticipantState says whether a participant is active (exists and is not revoked) and when it last polled
// (zero when never). One row, no session count: it is read on every send to a participant and on the
// inbox's revocation check.
//
// UNTESTED.
func (s *Store) ParticipantState(ctx context.Context, id string) (lastPoll time.Time, active bool, err error) {
	var poll sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT last_poll_at FROM participants WHERE id=? AND revoked_at IS NULL`, id).Scan(&poll)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("gateway: participant state: %w", err)
	}
	if poll.Valid {
		lastPoll = time.UnixMilli(poll.Int64)
	}
	return lastPoll, true, nil
}
