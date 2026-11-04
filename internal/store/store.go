// Package store is Ratatoskr's SQLite message record (design section 9).
//
// One goroutine owns every write, each write is one short transaction that
// updates the message and appends its event together, and no transaction is
// held open across a PTY write or a hook wait (design "Commit order").
//
// Untested: under GR-3 (docs/design/ground-rules.md) nothing here has been run.
// The modernc.org/sqlite DSN pragma syntax and multi-statement Exec are
// assumptions taken from that module's docs and source.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go
)

const (
	dbFile   = "ratatoskr.db"
	lockFile = "ratatoskr.lock"
)

type writeReq struct {
	fn   func(tx *sql.Tx) error
	done chan error
}

// Store is safe for concurrent use.
type Store struct {
	lock *os.File
	wdb  *sql.DB // single connection, used only by the writer goroutine
	rdb  *sql.DB // read-only, for history queries

	reqs    chan writeReq // unbuffered: a received request is always answered
	stop    chan struct{}
	stopped chan struct{}

	closeOnce   sync.Once
	unavailable atomic.Bool
	cause       atomic.Pointer[error] // the first error that latched unavailable
}

// NewMessageID returns a globally unique, time-ordered message ID. It is the
// dedup key once messages cross machines (design section 14).
func NewMessageID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// Open creates dir (mode 0700) if needed, takes the per-machine lock, opens
// the database (mode 0600) and migrates it. It fails with ErrAlreadyRunning,
// ErrNewerSchema or a plain error, and in every failure leaves nothing open.
// Call Recover next, before launching any harness.
func Open(ctx context.Context, dir string) (_ *Store, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // MkdirAll is subject to umask and skips existing dirs
		return nil, fmt.Errorf("store: chmod dir: %w", err)
	}

	lock, err := acquireLock(filepath.Join(dir, lockFile))
	if err != nil {
		return nil, err
	}
	s := &Store{
		lock:    lock,
		reqs:    make(chan writeReq),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	defer func() {
		if err != nil {
			s.closeDBs()
			_ = lock.Close()
		}
	}()

	path := filepath.Join(dir, dbFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // fixes the mode before SQLite creates it
	if err != nil {
		return nil, fmt.Errorf("store: create db file: %w", err)
	}
	_ = f.Close()
	if err := os.Chmod(path, 0o600); err != nil { // O_CREATE's mode applies only to a new file
		return nil, fmt.Errorf("store: chmod db file: %w", err)
	}

	if s.wdb, err = sql.Open("sqlite", dsn(path, false)); err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	s.wdb.SetMaxOpenConns(1)
	if err = migrate(ctx, s.wdb); err != nil {
		return nil, err
	}
	if s.rdb, err = sql.Open("sqlite", dsn(path, true)); err != nil {
		return nil, fmt.Errorf("store: open read handle: %w", err)
	}
	if err = s.rdb.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("store: open read handle: %w", err)
	}

	go s.writer()
	return s, nil
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(FULL)")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}

// Close stops the writer after its current transaction, closes the database
// and releases the lock. Writes that have not started return ErrClosed.
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.stopped
		err = s.closeDBs()
		if e := s.lock.Close(); err == nil {
			err = e
		}
	})
	return err
}

func (s *Store) closeDBs() error {
	var first error
	for _, db := range []*sql.DB{s.rdb, s.wdb} {
		if db == nil {
			continue
		}
		if err := db.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Store) writer() {
	defer close(s.stopped)
	for {
		select {
		case <-s.stop:
			return
		case r := <-s.reqs:
			r.done <- s.exec(r.fn)
		}
	}
}

// do runs fn in one write transaction on the writer goroutine. ctx is honoured
// only until the writer accepts the request: after that the outcome is
// reported, never abandoned, so a caller that sees ctx.Err() knows nothing was
// started.
func (s *Store) do(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.unavailable.Load() {
		return fmt.Errorf("%w: %v", ErrUnavailable, s.UnavailableCause())
	}
	r := writeReq{fn: fn, done: make(chan error, 1)}
	select {
	case s.reqs <- r:
		return <-r.done
	case <-s.stop:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// exec runs on the writer goroutine only. A domain error (the message was not
// in the expected state) rolls back and leaves the store usable. Any other
// failure marks the store unavailable for good: stopping delivery is the safe
// side when a record may be missing.
func (s *Store) exec(fn func(tx *sql.Tx) error) error {
	tx, err := s.wdb.BeginTx(context.Background(), nil)
	if err != nil {
		return s.fail(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		if err == ErrStateChanged {
			return err
		}
		return s.fail(err)
	}
	if err := testCommitFault(); err != nil {
		_ = tx.Rollback()
		return s.fail(err)
	}
	if err := tx.Commit(); err != nil {
		return s.fail(err)
	}
	return nil
}

func (s *Store) fail(err error) error {
	s.cause.CompareAndSwap(nil, &err) // keep the first cause, the useful one
	s.unavailable.Store(true)
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// UnavailableCause returns the first error that made the store unavailable, or
// nil. The router shows it in its banner.
func (s *Store) UnavailableCause() error {
	if p := s.cause.Load(); p != nil {
		return *p
	}
	return nil
}
