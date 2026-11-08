package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go
)

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

	// migrated is what Open did to the schema, for the gateway to say out loud.
	migrated Migrated

	closeOnce sync.Once
	closeErr  error
}

// Open prepares the gateway directory and opens its database.
//
// hold selects the gateway's process lock: the `--listen` process passes true and
// keeps it until Close; the admin commands pass false, so they can read and write
// the database while the gateway runs. Both may create a database the other has not
// created yet. Only the process that holds the lock migrates an EXISTING database:
// an admin command facing an older non-empty one gets ErrNeedsGateway, because an
// older gateway may be running against it (see migrate.go).
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

	s.migrated, err = migrate(ctx, s.db, path, hold, time.Now())
	if err != nil {
		return nil, err
	}
	if err := tightenFiles(dir); err != nil {
		return nil, err
	}
	return s, nil
}

// Migration says what Open did to the schema: the zero value when the database was already
// current. Run prints it, with the backup path, when an existing database was upgraded.
func (s *Store) Migration() Migrated {
	return s.migrated
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

// closeDatabase closes the handle. It deliberately leaves s.db set: a goroutine that still holds the
// store (a kicked recompute, a parked ticker) then gets database/sql's clean "database is closed"
// error instead of a nil-pointer panic, and the write of a nil field would race its reads. A double
// close is already prevented by Close's sync.Once, and Open's failure path calls this at most once.
func (s *Store) closeDatabase() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
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
