package gateway

// Pairing: one-time codes in, random credentials out, and a hash looked up on
// every authenticated call.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No code has been created,
// redeemed or authenticated.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// codeAlphabet has exactly 32 characters, which is what makes byte&31
	// unbiased: 24 letters (A-Z without I and O) plus 8 digits (2-9). A test
	// asserts the length so an edit cannot quietly break the uniformity.
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLength   = 12
	codeTTL      = 10 * time.Minute

	// credentialBytes is the entropy behind one host credential.
	credentialBytes = 32
)

// hostNamePattern is the host half of a network name.
var hostNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

// The package's exported errors. None of their texts may ever contain a code, a
// credential or a hash.
var (
	ErrBadCode      = errors.New("gateway: bad pairing code")
	ErrUnauthorized = errors.New("gateway: unauthorized")
	ErrNoSuchHost   = errors.New("gateway: no such host")
	ErrBadHostName  = errors.New("gateway: invalid host name")
)

// HostRow is one paired host as the admin command shows it.
type HostRow struct {
	Name          string
	PairedAt      time.Time
	LastHeartbeat time.Time // zero when the gateway has never heard from it
}

// PendingCode is one unused, unexpired pairing code.
type PendingCode struct {
	Host      string
	ExpiresAt time.Time
}

// CreatePairingCode mints one single-use code for a host and invalidates that
// host's older unused codes.
//
// The code is returned in its display form exactly once. Only the SHA-256 of its
// normalised form is stored, so the database cannot be used to recover a code.
//
// UNTESTED.
func (s *Store) CreatePairingCode(ctx context.Context, host string, now time.Time) (string, error) {
	if !hostNamePattern.MatchString(host) {
		return "", fmt.Errorf("%w: %q", ErrBadHostName, host)
	}

	code, err := newCode()
	if err != nil {
		return "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("gateway: begin pairing code: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// A host name must never equal a participant id (revoked ones keep their name).
	if err := refuseParticipantName(ctx, tx, host); err != nil {
		return "", err
	}

	// One live code per host: an older unused code must stop working the moment a
	// new one is minted.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM pairing_codes WHERE host=? AND used_at IS NULL`, host); err != nil {
		return "", fmt.Errorf("gateway: replace pairing codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pairing_codes(code_hash, host, created_at, expires_at, used_at) VALUES(?,?,?,?,NULL)`,
		hashText(normaliseCode(code)), host, now.UnixMilli(), now.Add(codeTTL).UnixMilli()); err != nil {
		return "", fmt.Errorf("gateway: insert pairing code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("gateway: commit pairing code: %w", err)
	}

	return displayCode(code), nil
}

// RedeemCode exchanges one code for a credential, for a named host.
//
// Every way of failing - unknown code, wrong host, already used, expired - gives
// the same ErrBadCode, so a wrong guess cannot be told apart from a stale one.
// Redeeming for a host that is already paired replaces its credential and ends
// that host's open sessions, so the old runtime cannot keep receiving offers.
//
// The credential is returned once and stored only as a hash.
//
// UNTESTED.
func (s *Store) RedeemCode(ctx context.Context, code, host string, now time.Time) (string, error) {
	if !hostNamePattern.MatchString(host) {
		return "", fmt.Errorf("%w: %q", ErrBadHostName, host)
	}
	credential, _, err := s.redeem(ctx, code, host, now)
	return credential, err
}

// RedeemCodeForAnyHost exchanges one code for a credential without the caller
// naming the host: the code was minted for one host, and that host is returned.
// This is what `rtk runtime pair --gateway ADDR --code CODE` uses, since the
// command line carries no host name. Every failure is the same ErrBadCode.
//
// UNTESTED.
func (s *Store) RedeemCodeForAnyHost(ctx context.Context, code string, now time.Time) (credential, host string, err error) {
	return s.redeem(ctx, code, "", now)
}

// redeem is the shared body. An empty host means "whichever host the code was
// minted for"; a non-empty host must match it.
func (s *Store) redeem(ctx context.Context, code, host string, now time.Time) (string, string, error) {
	codeHash := hashText(normaliseCode(code))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("gateway: begin redeem: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var storedHost string
	var expiresAt int64
	var usedAt sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT host, expires_at, used_at FROM pairing_codes WHERE code_hash=?`, codeHash).
		Scan(&storedHost, &expiresAt, &usedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", ErrBadCode
	case err != nil:
		return "", "", fmt.Errorf("gateway: read pairing code: %w", err)
	case (host != "" && storedHost != host) || usedAt.Valid || expiresAt <= now.UnixMilli():
		return "", "", ErrBadCode
	}
	host = storedHost

	// The name check runs in the transaction that writes the row, so a race with `participant pair`
	// cannot create both. A participant name is the same ErrBadCode as any other failure, and the code
	// is not marked used.
	if err := refuseParticipantName(ctx, tx, host); err != nil {
		if errors.Is(err, ErrNameIsParticipant) {
			return "", "", ErrBadCode
		}
		return "", "", err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE pairing_codes SET used_at=? WHERE code_hash=?`, now.UnixMilli(), codeHash); err != nil {
		return "", "", fmt.Errorf("gateway: mark pairing code used: %w", err)
	}

	credential, err := newCredential()
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hosts(name, credential_hash, created_at, last_heartbeat_at) VALUES(?,?,?,NULL)
		 ON CONFLICT(name) DO UPDATE SET credential_hash=excluded.credential_hash,
		   created_at=excluded.created_at, last_heartbeat_at=NULL`,
		host, hashText(credential), now.UnixMilli()); err != nil {
		return "", "", fmt.Errorf("gateway: store host: %w", err)
	}

	// A re-pair ends the old runtime's sessions: they must not be offered
	// messages they can no longer acknowledge.
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET ended_at=? WHERE host=? AND ended_at IS NULL`, now.UnixMilli(), host); err != nil {
		return "", "", fmt.Errorf("gateway: end sessions: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("gateway: commit redeem: %w", err)
	}
	return credential, host, nil
}

// Authenticate maps a presented credential to its host.
//
// It reads the database on every call and caches nothing: that is what makes
// `unpair` refuse the next request rather than the next restart.
//
// UNTESTED.
func (s *Store) Authenticate(ctx context.Context, credential string) (string, error) {
	if credential == "" {
		return "", ErrUnauthorized
	}

	var host string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM hosts WHERE credential_hash=?`, hashText(credential)).Scan(&host)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrUnauthorized
	case err != nil:
		return "", fmt.Errorf("gateway: authenticate: %w", err)
	}
	return host, nil
}

// LastHeartbeat returns when the gateway last heard from a host, and whether that
// host is paired at all.
//
// It is not authentication and it reads one row: the stream route uses it (through
// HostLive) to answer "has this host been quiet", and the admin view uses the same
// value. A paired host that has never sent a heartbeat gives a zero time and true.
//
// UNTESTED.
func (s *Store) LastHeartbeat(ctx context.Context, host string) (time.Time, bool, error) {
	var heartbeat sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT last_heartbeat_at FROM hosts WHERE name=?`, host).Scan(&heartbeat)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("gateway: last heartbeat: %w", err)
	}
	if !heartbeat.Valid {
		return time.Time{}, true, nil
	}
	return time.UnixMilli(heartbeat.Int64), true, nil
}

// Unpair removes a host, its pairing codes and its open sessions in one
// transaction.
//
// UNTESTED.
func (s *Store) Unpair(ctx context.Context, host string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway: begin unpair: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	result, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE name=?`, host)
	if err != nil {
		return fmt.Errorf("gateway: delete host: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("gateway: delete host: %w", err)
	} else if affected == 0 {
		return ErrNoSuchHost
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM pairing_codes WHERE host=?`, host); err != nil {
		return fmt.Errorf("gateway: delete pairing codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET ended_at=? WHERE host=? AND ended_at IS NULL`, now.UnixMilli(), host); err != nil {
		return fmt.Errorf("gateway: end sessions: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway: commit unpair: %w", err)
	}
	return nil
}

// TouchHeartbeat records the gateway's own receive time for a host.
//
// UNTESTED.
func (s *Store) TouchHeartbeat(ctx context.Context, host string, now time.Time) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE hosts SET last_heartbeat_at=? WHERE name=?`, now.UnixMilli(), host)
	if err != nil {
		return fmt.Errorf("gateway: heartbeat: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("gateway: heartbeat: %w", err)
	} else if affected == 0 {
		return ErrNoSuchHost
	}
	return nil
}

// Hosts lists paired hosts by name.
//
// UNTESTED.
func (s *Store) Hosts(ctx context.Context) ([]HostRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, created_at, last_heartbeat_at FROM hosts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("gateway: list hosts: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	hosts := make([]HostRow, 0)
	for rows.Next() {
		var name string
		var created int64
		var heartbeat sql.NullInt64
		if err := rows.Scan(&name, &created, &heartbeat); err != nil {
			return nil, fmt.Errorf("gateway: list hosts: %w", err)
		}
		row := HostRow{Name: name, PairedAt: time.UnixMilli(created)}
		if heartbeat.Valid {
			row.LastHeartbeat = time.UnixMilli(heartbeat.Int64)
		}
		hosts = append(hosts, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list hosts: %w", err)
	}
	return hosts, nil
}

// PendingCodes lists codes that are still usable, soonest expiry first.
//
// UNTESTED.
func (s *Store) PendingCodes(ctx context.Context, now time.Time) ([]PendingCode, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT host, expires_at FROM pairing_codes
		 WHERE used_at IS NULL AND expires_at > ? ORDER BY expires_at, host`, now.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("gateway: list pairing codes: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	codes := make([]PendingCode, 0)
	for rows.Next() {
		var host string
		var expires int64
		if err := rows.Scan(&host, &expires); err != nil {
			return nil, fmt.Errorf("gateway: list pairing codes: %w", err)
		}
		codes = append(codes, PendingCode{Host: host, ExpiresAt: time.UnixMilli(expires)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list pairing codes: %w", err)
	}
	return codes, nil
}

// newCode draws a code uniformly from the alphabet.
//
// byte&31 is unbiased only because the alphabet has exactly 32 characters, which
// a test asserts.
func newCode() (string, error) {
	buffer := make([]byte, codeLength)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("gateway: read random code: %w", err)
	}
	code := make([]byte, codeLength)
	for i, b := range buffer {
		code[i] = codeAlphabet[int(b)&31]
	}
	return string(code), nil
}

// newCredential draws one credential.
func newCredential() (string, error) {
	buffer := make([]byte, credentialBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("gateway: read random credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// normaliseCode makes a typed code comparable: uppercase, without separators.
func normaliseCode(code string) string {
	return strings.ReplaceAll(strings.ToUpper(code), "-", "")
}

// displayCode renders a code in groups of four, which is how it is printed.
func displayCode(code string) string {
	if len(code) != codeLength {
		return code
	}
	return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
}

// hashText is the only way a code or credential is stored.
func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
