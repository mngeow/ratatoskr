package gateway

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go
)

//go:embed migrations/0001_init.sql
var initSQL string

// schemaVersion is the version this binary knows. A database with a higher
// user_version is refused untouched.
const schemaVersion = 1

// ErrGatewayRunning reports that another gateway process holds the lock.
var ErrGatewayRunning = errors.New("gateway: another gateway process is running")

// Store is the gateway database. One handle serves reads and writes: unlike the
// runtime's store there is no single-writer goroutine here, because the gateway's
// writer is the HTTP layer and the admin commands are separate processes that
// SQLite serializes for us.
//
// UNTESTED.
type Store struct {
	dir  string
	db   *sql.DB
	lock *os.File

	closeOnce sync.Once
	closeErr  error
}

// Open prepares the gateway directory and opens its database.
//
// hold selects the gateway's process lock: the `--listen` process passes true and
// keeps it until Close; the admin commands pass false, so they can read and write
// the database while the gateway runs. Both are allowed to create and migrate a
// database the other has not created yet, because the migration runs inside one
// immediate transaction and re-reads the version inside it.
//
// UNTESTED.
func Open(ctx context.Context, stateDir string, hold bool) (_ *Store, err error) {
	dir, err := PrepareDir(stateDir)
	if err != nil {
		return nil, err
	}

	s := &Store{dir: dir}
	if hold {
		s.lock, err = acquireLock(filepath.Join(dir, lockName))
		if err != nil {
			return nil, err
		}
	}
	defer func() {
		if err != nil {
			s.closeDatabase()
			if s.lock != nil {
				_ = s.lock.Close()
			}
		}
	}()

	path := filepath.Join(dir, dbName)
	// Pre-create the database at 0600 before SQLite sees it, so it is never
	// created with a wider mode and then tightened.
	if err := prepareOwnedFile(path); err != nil {
		return nil, err
	}

	s.db, err = sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("gateway: open database: %w", err)
	}
	s.db.SetMaxOpenConns(4)

	if err := migrate(ctx, s.db); err != nil {
		return nil, err
	}
	if err := tightenFiles(dir); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the database and then releases the lock. It is idempotent.
//
// UNTESTED.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		closeErr := s.closeDatabase()
		if s.lock != nil {
			if err := s.lock.Close(); err != nil && closeErr == nil {
				closeErr = fmt.Errorf("gateway: close lock: %w", err)
			}
		}
		s.closeErr = closeErr
	})
	return s.closeErr
}

func (s *Store) closeDatabase() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	if err != nil {
		return fmt.Errorf("gateway: close database: %w", err)
	}
	return nil
}

// dsn builds the driver connection string.
//
// The pragmas mirror the runtime store's: a busy timeout so an admin command and
// the gateway can wait for each other, foreign keys on, WAL so readers do not
// block the writer, and a full sync so a committed ledger row survives a crash.
// _txlock=immediate is honoured by modernc.org/sqlite v1.61.1 (driver.go
// documents deferred/immediate/exclusive and sqlite.go parses it), which makes
// every BeginTx take the write lock at BEGIN: the migration cannot race another
// process between reading user_version and applying it.
//
// UNTESTED at runtime: the DSN pragma syntax is the same assumption the runtime
// store already records.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")

	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}

// migrate brings the database to schemaVersion, or refuses a newer one.
//
// The version is read inside the transaction, so a second process that waited for
// the write lock sees the applied version and does nothing. The statements are
// not IF NOT EXISTS: a race that slipped through would surface as an error rather
// than a half-applied schema. There is no backup and no downgrade.
//
// UNTESTED.
func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway: begin migration: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("gateway: read schema version: %w", err)
	}
	switch {
	case current > schemaVersion:
		return fmt.Errorf("gateway: database schema version %d is newer than this binary (%d)", current, schemaVersion)
	case current == schemaVersion:
		return nil
	}

	if _, err := tx.ExecContext(ctx, initSQL); err != nil {
		return fmt.Errorf("gateway: apply schema: %w", err)
	}
	// PRAGMA does not take a bound parameter; the value is a constant above.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("gateway: record schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway: commit migration: %w", err)
	}
	return nil
}
