package a2a

// The SQLite implementation of Store, over the gateway's database handle (migration 0003).
//
// UNTESTED. The migration has never been run and no statement here has been executed.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SQLiteStore implements Store over a *sql.DB the gateway owns and migrates.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore wraps an open, migrated database.
func NewSQLiteStore(db *sql.DB) *SQLiteStore { return &SQLiteStore{db: db} }

var _ Store = (*SQLiteStore)(nil)

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v) }

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateParticipant implements Store.
func (s *SQLiteStore) CreateParticipant(ctx context.Context, p Participant, credentialHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO a2a_participants(id, credential_hash, created_at) VALUES(?,?,?)`,
		p.ID, credentialHash, ms(p.CreatedAt))
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("a2a: enrol participant: %w", err)
	}
	return nil
}

// participantColumns is an EXPLICIT list: outbound_secret is never selected here.
const participantColumns = `id, url, fingerprint, insecure_http, auth, card_json, created_at, revoked_at`

func scanParticipant(row interface{ Scan(...any) error }) (Participant, error) {
	var p Participant
	var insecure int
	var created int64
	var revoked sql.NullInt64
	if err := row.Scan(&p.ID, &p.URL, &p.Fingerprint, &insecure, &p.Auth, &p.CardJSON, &created, &revoked); err != nil {
		return Participant{}, err
	}
	p.InsecureHTTP = insecure != 0
	p.CreatedAt = fromMS(created)
	if revoked.Valid {
		p.RevokedAt = fromMS(revoked.Int64)
	}
	return p, nil
}

// GetParticipant implements Store.
func (s *SQLiteStore) GetParticipant(ctx context.Context, id string) (Participant, bool, error) {
	p, err := scanParticipant(s.db.QueryRowContext(ctx, `SELECT `+participantColumns+` FROM a2a_participants WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Participant{}, false, nil
	}
	if err != nil {
		return Participant{}, false, fmt.Errorf("a2a: read participant: %w", err)
	}
	return p, true, nil
}

// ParticipantByCredential implements Store.
func (s *SQLiteStore) ParticipantByCredential(ctx context.Context, credentialHash string) (Participant, bool, error) {
	p, err := scanParticipant(s.db.QueryRowContext(ctx, `SELECT `+participantColumns+` FROM a2a_participants WHERE credential_hash=?`, credentialHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Participant{}, false, nil
	}
	if err != nil {
		return Participant{}, false, fmt.Errorf("a2a: read participant: %w", err)
	}
	return p, true, nil
}

// ListParticipants implements Store.
func (s *SQLiteStore) ListParticipants(ctx context.Context) ([]Participant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+participantColumns+` FROM a2a_participants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("a2a: list participants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Participant, 0, 4)
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, fmt.Errorf("a2a: list participants: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetRegistration implements Store.
func (s *SQLiteStore) SetRegistration(ctx context.Context, id, url, fingerprint string, insecureHTTP bool, cardJSON, auth, secret string) error {
	if auth != AuthBearer && auth != AuthNone {
		return errors.New("a2a: unknown outbound authentication mode")
	}
	if (auth == AuthBearer) != (secret != "") {
		return errors.New("a2a: a Bearer registration needs a secret and a no-auth one must not have one")
	}
	insecure := 0
	if insecureHTTP {
		insecure = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE a2a_participants SET url=?, fingerprint=?, insecure_http=?, card_json=?, auth=?, outbound_secret=? WHERE id=? AND revoked_at IS NULL`,
		url, fingerprint, insecure, cardJSON, auth, secret, id)
	if err != nil {
		return errors.New("a2a: register participant failed")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OutboundSecret implements Store.
func (s *SQLiteStore) OutboundSecret(ctx context.Context, id string) (string, error) {
	var secret string
	err := s.db.QueryRowContext(ctx, `SELECT outbound_secret FROM a2a_participants WHERE id=? AND revoked_at IS NULL`, id).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", errors.New("a2a: the outbound secret could not be read")
	}
	return secret, nil
}

// Revoke implements Store.
func (s *SQLiteStore) Revoke(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE a2a_participants SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, ms(at), id)
	if err != nil {
		return false, fmt.Errorf("a2a: revoke participant: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const taskColumns = `id, seq, context_id, owner, target_kind, target_id, target_host, state, reason, remote_task_id, body, version, created_at, updated_at`

func scanTask(row interface{ Scan(...any) error }) (Task, int64, error) {
	var t Task
	var seq int64
	var owner, body string
	var created, updated int64
	if err := row.Scan(&t.ID, &seq, &t.ContextID, &owner, &t.Target.Kind, &t.Target.ID, &t.Target.Host,
		&t.State, &t.Reason, &t.RemoteTaskID, &body, &t.Version, &created, &updated); err != nil {
		return Task{}, 0, err
	}
	t.Owner = ParsePrincipalKey(owner)
	t.Body = []byte(body)
	t.CreatedAt, t.UpdatedAt = fromMS(created), fromMS(updated)
	return t, seq, nil
}

// ParsePrincipalKey is the inverse of Principal.Key; an unreadable key gives the zero Principal, which
// is not Valid and so never matches a caller.
func ParsePrincipalKey(key string) Principal {
	kind, rest, ok := strings.Cut(key, ":")
	if !ok {
		return Principal{}
	}
	switch kind {
	case KindSession:
		host, session, ok := strings.Cut(rest, "/")
		if !ok {
			return Principal{}
		}
		return Principal{Kind: KindSession, Host: host, Session: session}
	case KindParticipant:
		return Principal{Kind: KindParticipant, Name: rest}
	}
	return Principal{}
}

// CreateTask implements Store.
func (s *SQLiteStore) CreateTask(ctx context.Context, t Task) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO a2a_tasks(id, seq, context_id, owner, target_kind, target_id, target_host, state, reason, remote_task_id, body, version, created_at, updated_at)
		 VALUES(?, (SELECT COALESCE(MAX(seq),0)+1 FROM a2a_tasks), ?,?,?,?,?,?,?,?,?,1,?,?)`,
		t.ID, t.ContextID, t.Owner.Key(), t.Target.Kind, t.Target.ID, t.Target.Host, t.State, t.Reason, t.RemoteTaskID,
		string(t.Body), ms(t.CreatedAt), ms(t.UpdatedAt))
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("a2a: create task: %w", err)
	}
	return nil
}

// GetTask implements Store.
func (s *SQLiteStore) GetTask(ctx context.Context, id string) (Task, bool, error) {
	t, _, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM a2a_tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, fmt.Errorf("a2a: read task: %w", err)
	}
	return t, true, nil
}

// SaveTask implements Store.
func (s *SQLiteStore) SaveTask(ctx context.Context, t Task) (Task, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE a2a_tasks SET state=?, reason=?, remote_task_id=?, body=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		t.State, t.Reason, t.RemoteTaskID, string(t.Body), ms(t.UpdatedAt), t.ID, t.Version)
	if err != nil {
		return Task{}, fmt.Errorf("a2a: save task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Task{}, ErrConflict
	}
	t.Version++
	return t, nil
}

// listWhere is the caller-scoped filter shared by ListTasks and CountTasks: the caller must be the owner or
// the target, so a handler cannot forget the scoping.
func listWhere(f ListFilter) (string, []any) {
	query := ` WHERE (owner=? OR (target_kind=? AND target_id=?))`
	args := []any{f.Caller.Key(), "", ""}
	if f.Caller.Kind == KindSession {
		args[1], args[2] = TargetSession, f.Caller.Session
	} else {
		args[1], args[2] = TargetService, f.Caller.Name
	}
	if f.ContextID != "" {
		query += ` AND context_id=?`
		args = append(args, f.ContextID)
	}
	if f.State != "" {
		query += ` AND state=?`
		args = append(args, f.State)
	}
	return query, args
}

// CountTasks implements Store.
func (s *SQLiteStore) CountTasks(ctx context.Context, f ListFilter) (int, error) {
	if !f.Caller.Valid() {
		return 0, nil
	}
	where, args := listWhere(f)
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM a2a_tasks`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("a2a: count tasks: %w", err)
	}
	return n, nil
}

// ListTasks implements Store. Newest status first (A2A spec: status timestamp descending), ties broken by
// creation order. The cursor is "<updated_ms>.<seq>" of the last task on the previous page.
func (s *SQLiteStore) ListTasks(ctx context.Context, f ListFilter) ([]Task, string, error) {
	if !f.Caller.Valid() {
		return nil, "", nil
	}
	size := f.PageSize
	if size <= 0 || size > 100 {
		size = 50
	}
	where, args := listWhere(f)
	if f.Cursor != "" {
		stamp, seq, ok := strings.Cut(f.Cursor, ".")
		before, err1 := strconv.ParseInt(stamp, 10, 64)
		after, err2 := strconv.ParseInt(seq, 10, 64)
		if !ok || err1 != nil || err2 != nil || before <= 0 || after <= 0 {
			return nil, "", ErrNotFound
		}
		where += ` AND (updated_at < ? OR (updated_at = ? AND seq < ?))`
		args = append(args, before, before, after)
	}
	args = append(args, size+1)

	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM a2a_tasks`+where+` ORDER BY updated_at DESC, seq DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", fmt.Errorf("a2a: list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tasks []Task
	var seqs []int64
	for rows.Next() {
		t, seq, err := scanTask(rows)
		if err != nil {
			return nil, "", fmt.Errorf("a2a: list tasks: %w", err)
		}
		tasks, seqs = append(tasks, t), append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(tasks) > size {
		last := tasks[size-1]
		next = strconv.FormatInt(last.UpdatedAt.UnixMilli(), 10) + "." + strconv.FormatInt(seqs[size-1], 10)
		tasks = tasks[:size]
	}
	return tasks, next, nil
}

func (s *SQLiteStore) openTasks(ctx context.Context, where string, args ...any) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM a2a_tasks WHERE `+where+` AND state NOT IN ('COMPLETED','FAILED','REJECTED') ORDER BY seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("a2a: list open tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Task
	for rows.Next() {
		t, _, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("a2a: list open tasks: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// OpenTasksFor implements Store.
func (s *SQLiteStore) OpenTasksFor(ctx context.Context, target Target) ([]Task, error) {
	return s.openTasks(ctx, `target_kind=? AND target_id=?`, target.Kind, target.ID)
}

// OpenServiceTasks implements Store.
func (s *SQLiteStore) OpenServiceTasks(ctx context.Context) ([]Task, error) {
	return s.openTasks(ctx, `target_kind=?`, TargetService)
}

// OpenTasksOwnedBy implements Store.
func (s *SQLiteStore) OpenTasksOwnedBy(ctx context.Context, owner Principal) ([]Task, error) {
	return s.openTasks(ctx, `owner=?`, owner.Key())
}

// DeleteTasks implements Store. It removes the tasks, their observations and their message links in one
// transaction; the ledger messages themselves are the caller's (Admin.PurgeMessages).
func (s *SQLiteStore) DeleteTasks(ctx context.Context, f DeleteFilter) (int, error) {
	var where string
	var args []any
	switch {
	case f.TaskID != "":
		where, args = `id=?`, []any{f.TaskID}
	case f.AllFinal:
		where = `state IN ('COMPLETED','FAILED','REJECTED')`
	case f.Participant != "":
		where, args = `owner=? OR (target_kind='service' AND target_id=?)`,
			[]any{Principal{Kind: KindParticipant, Name: f.Participant}.Key(), f.Participant}
	default:
		return 0, errors.New("a2a: no delete filter")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("a2a: delete tasks: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"a2a_observations", "a2a_dispatches"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE task_id IN (SELECT id FROM a2a_tasks WHERE `+where+`)`, args...); err != nil {
			return 0, fmt.Errorf("a2a: delete task records: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM a2a_tasks WHERE `+where, args...)
	if err != nil {
		return 0, fmt.Errorf("a2a: delete tasks: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("a2a: delete tasks: %w", err)
	}
	return int(n), nil
}

// AddObservation implements Store.
func (s *SQLiteStore) AddObservation(ctx context.Context, o Observation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO a2a_observations(task_id, kind, value, at) VALUES(?,?,?,?)`,
		o.TaskID, o.Kind, o.Value, ms(o.At))
	if err != nil {
		return fmt.Errorf("a2a: record observation: %w", err)
	}
	return nil
}

// Observations implements Store.
func (s *SQLiteStore) Observations(ctx context.Context, taskID string) ([]Observation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, kind, value, at FROM a2a_observations WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("a2a: list observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Observation
	for rows.Next() {
		var o Observation
		var at int64
		if err := rows.Scan(&o.TaskID, &o.Kind, &o.Value, &at); err != nil {
			return nil, fmt.Errorf("a2a: list observations: %w", err)
		}
		o.At = fromMS(at)
		out = append(out, o)
	}
	return out, rows.Err()
}

// LinkMessage implements Store.
func (s *SQLiteStore) LinkMessage(ctx context.Context, messageID, taskID, kind string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO a2a_dispatches(message_id, task_id, kind, created_at) VALUES(?,?,?,?)`,
		messageID, taskID, kind, ms(at))
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("a2a: link message: %w", err)
	}
	return nil
}

// TaskForMessage implements Store.
func (s *SQLiteStore) TaskForMessage(ctx context.Context, messageID string) (string, string, bool, error) {
	var taskID, kind string
	err := s.db.QueryRowContext(ctx, `SELECT task_id, kind FROM a2a_dispatches WHERE message_id=?`, messageID).Scan(&taskID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("a2a: read message link: %w", err)
	}
	return taskID, kind, true, nil
}

// MessagesFor implements Store.
func (s *SQLiteStore) MessagesFor(ctx context.Context, f DeleteFilter) ([]string, error) {
	var rows *sql.Rows
	var err error
	switch {
	case f.TaskID != "":
		rows, err = s.db.QueryContext(ctx, `SELECT message_id FROM a2a_dispatches WHERE task_id=?`, f.TaskID)
	case f.AllFinal:
		rows, err = s.db.QueryContext(ctx,
			`SELECT d.message_id FROM a2a_dispatches d JOIN a2a_tasks t ON t.id=d.task_id WHERE t.state IN ('COMPLETED','FAILED','REJECTED')`)
	case f.Participant != "":
		rows, err = s.db.QueryContext(ctx,
			`SELECT d.message_id FROM a2a_dispatches d JOIN a2a_tasks t ON t.id=d.task_id
			 WHERE t.owner=? OR (t.target_kind='service' AND t.target_id=?)`,
			Principal{Kind: KindParticipant, Name: f.Participant}.Key(), f.Participant)
	default:
		return nil, errors.New("a2a: no delete filter")
	}
	if err != nil {
		return nil, fmt.Errorf("a2a: list task messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("a2a: list task messages: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
