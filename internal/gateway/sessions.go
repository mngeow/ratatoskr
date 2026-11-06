package gateway

// Sessions and the directory: which agents each host has registered, and which of
// them are still current.
//
// A registration is a new session id every time; the previous ones are ended, so
// "session gone" has a precise meaning rather than a guess.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No session has been started,
// ended or listed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	// agentNamePatternText is the same display-name rule the runtime uses, so a
	// name that can be launched locally can also be registered here.
	agentNamePatternText = `^[a-z0-9_-]{1,32}$`

	// maxAgentsPerHost bounds one registration. Two harnesses exist and the
	// runtime sends two names, so anything beyond a handful is a caller bug.
	maxAgentsPerHost = 8 // UNTESTED guess
)

// agentNamePattern is the compiled form of agentNamePatternText.
var agentNamePattern = regexp.MustCompile(agentNamePatternText)

// The two sentinels a rejected registration returns. Names are the caller's own
// and are not secret, so the message may name them.
var (
	ErrBadAgentName = errors.New("gateway: invalid agent name")
	ErrBadAgentList = errors.New("gateway: invalid agent list")
)

// SessionRow is one registration of one agent name on one host.
type SessionRow struct {
	ID        string
	Host      string
	Name      string
	StartedAt time.Time
	EndedAt   time.Time // zero while the session is open
}

// DirectoryRow is one agent the gateway knows about, with its host's heartbeat.
type DirectoryRow struct {
	Name          string
	Host          string
	SessionID     string
	LastHeartbeat time.Time // zero when the gateway has never heard from the host
}

// StartSessions registers a run's agents, ending that host's earlier sessions.
//
// Everything is validated before the database is touched: the host name, the
// number of names, duplicates, and each name against the display-name rule. A
// bad registration must not leave the previous one half-ended.
//
// UNTESTED.
func (s *Store) StartSessions(ctx context.Context, host string, names []string, now time.Time, newID func() (string, error)) ([]SessionRow, error) {
	if !hostNamePattern.MatchString(host) {
		return nil, fmt.Errorf("%w: %q", ErrBadHostName, host)
	}
	if newID == nil {
		return nil, errors.New("gateway: start sessions: nil id generator")
	}
	if err := validateAgentNames(names); err != nil {
		return nil, err
	}

	rows := make([]SessionRow, 0, len(names))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("gateway: begin sessions: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// The host's earlier sessions end first: one host has one live run.
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET ended_at=? WHERE host=? AND ended_at IS NULL`,
		now.UnixMilli(), host); err != nil {
		return nil, fmt.Errorf("gateway: end earlier sessions: %w", err)
	}

	for _, name := range names {
		id, err := newID()
		if err != nil {
			return nil, fmt.Errorf("gateway: new session id: %w", err)
		}
		row := SessionRow{ID: id, Host: host, Name: name, StartedAt: now}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES(?,?,?,?,NULL)`,
			row.ID, row.Host, row.Name, now.UnixMilli()); err != nil {
			return nil, fmt.Errorf("gateway: insert session: %w", err)
		}
		rows = append(rows, row)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("gateway: commit sessions: %w", err)
	}
	return rows, nil
}

// validateAgentNames applies every list rule before the database is touched.
func validateAgentNames(names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("%w: no agent names", ErrBadAgentList)
	}
	if len(names) > maxAgentsPerHost {
		return fmt.Errorf("%w: %d names, at most %d", ErrBadAgentList, len(names), maxAgentsPerHost)
	}

	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !agentNamePattern.MatchString(name) {
			return fmt.Errorf("%w: %q must match %s", ErrBadAgentName, name, agentNamePatternText)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate agent name %q", ErrBadAgentList, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// EndSessions ends every open session of a host. Ending none is not an error.
//
// UNTESTED.
func (s *Store) EndSessions(ctx context.Context, host string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET ended_at=? WHERE host=? AND ended_at IS NULL`,
		now.UnixMilli(), host); err != nil {
		return fmt.Errorf("gateway: end sessions: %w", err)
	}
	return nil
}

// CurrentSession returns the open session for one agent name on one host.
//
// UNTESTED.
func (s *Store) CurrentSession(ctx context.Context, host, name string) (SessionRow, bool, error) {
	var row SessionRow
	var started int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, host, name, started_at FROM sessions
		 WHERE host=? AND name=? AND ended_at IS NULL`, host, name).
		Scan(&row.ID, &row.Host, &row.Name, &started)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SessionRow{}, false, nil
	case err != nil:
		return SessionRow{}, false, fmt.Errorf("gateway: current session: %w", err)
	}
	row.StartedAt = time.UnixMilli(started)
	return row, true, nil
}

// EverSeen reports whether a name has ever been registered on a host, open or
// ended. It is what separates "never heard of it" from "it was here and is gone".
//
// UNTESTED.
func (s *Store) EverSeen(ctx context.Context, host, name string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM sessions WHERE host=? AND name=? LIMIT 1`, host, name).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("gateway: ever seen: %w", err)
	}
	return true, nil
}

// Directory lists every open session with its host's last heartbeat.
//
// The INNER JOIN on hosts is deliberate: a host whose pairing was removed has no
// row, and its sessions must not be advertised to anyone.
//
// UNTESTED.
func (s *Store) Directory(ctx context.Context) ([]DirectoryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.name, s.host, s.id, COALESCE(h.last_heartbeat_at, 0)
		FROM sessions s
		JOIN hosts h ON h.name = s.host
		WHERE s.ended_at IS NULL
		ORDER BY s.host, s.name`)
	if err != nil {
		return nil, fmt.Errorf("gateway: directory: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	directory := make([]DirectoryRow, 0)
	for rows.Next() {
		var row DirectoryRow
		var heartbeat int64
		if err := rows.Scan(&row.Name, &row.Host, &row.SessionID, &heartbeat); err != nil {
			return nil, fmt.Errorf("gateway: directory: %w", err)
		}
		if heartbeat != 0 {
			row.LastHeartbeat = time.UnixMilli(heartbeat)
		}
		directory = append(directory, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: directory: %w", err)
	}
	return directory, nil
}
