package gateway

// The gateway's schema migrator: numbered embedded files applied in order, a backup before any
// change to an existing database, and refusals that touch nothing.
//
// The rules, and why:
//
//   - A database is only ever in one of four states, decided before anything is written: newer than
//     this binary (refused), equal (nothing to do), empty (created from scratch), or older and
//     non-empty (migrated). A database with tables but no schema version is none of these and is
//     refused untouched: it is not ours to guess about.
//   - Only the `--listen` process migrates an existing database. It holds the gateway lock, so no
//     other gateway can be running, and the admin commands (which do not take the lock) refuse an
//     older non-empty database and say to start the gateway. Creating a fresh database stays open to
//     every caller.
//   - An existing database is backed up first with VACUUM INTO, to a name this process claims
//     exclusively with a 0600 file, so a racing process cannot share it and the backup is never
//     created with a wider mode. A failed backup removes the claimed file (an empty .bak must never
//     look like a rollback point) and stops the migration: it is never skipped quietly.
//   - The migration runs in one immediate transaction that re-reads the version inside it. If the
//     version moved, the transaction rolls back and the migration fails with a clear error; with the
//     lock held and admin commands refusing, nothing else can migrate an existing database, so
//     there is no retry.
//   - Rollback to an older binary is: stop, restore the .bak, run the older binary. After this
//     migration an older gateway refuses the database as newer than itself.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. The migration has never been run against any
// database; its table test builds a temporary one and is written, not executed.

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNeedsGateway is returned by an admin command's Open when the database is older than this
// binary and not empty: only the running gateway migrates it.
var ErrNeedsGateway = errors.New("gateway: the database needs upgrading by the gateway")

// ErrNewerSchema is returned when the database was written by a newer binary.
var ErrNewerSchema = errors.New("gateway: the database was written by a newer binary")

// maxBackupSuffix bounds how many names are tried when the first is taken.
const maxBackupSuffix = 10

// migration is one numbered embedded file.
type migration struct {
	version int
	sql     string
}

// Migrated says what Open did to the database. The zero value means nothing was applied.
type Migrated struct {
	// From is the version the database was at: 0 for a database created from scratch.
	From int
	// To is the version it is at now.
	To int
	// Backup is the path of the backup taken before an existing database was migrated, or "" when
	// the database was created fresh.
	Backup string
}

// loadMigrations reads migrations/NNNN_name.sql in version order and checks the numbering is
// 1, 2, 3 ... with no gap, so a missing file is a build error and not a silent skip.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("gateway: read migrations: %w", err)
	}

	var all []migration
	for _, entry := range entries {
		number, _, _ := strings.Cut(entry.Name(), "_")
		version, err := strconv.Atoi(number)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("gateway: bad migration file name %q", entry.Name())
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("gateway: read migration %q: %w", entry.Name(), err)
		}
		all = append(all, migration{version: version, sql: string(body)})
	}
	if len(all) == 0 {
		return nil, errors.New("gateway: no migrations are embedded")
	}

	sort.Slice(all, func(i, j int) bool { return all[i].version < all[j].version })
	for index, m := range all {
		if m.version != index+1 {
			return nil, fmt.Errorf("gateway: migration numbering has a gap at version %d", index+1)
		}
	}
	return all, nil
}

// migrate brings the database at dbPath to the latest embedded version.
//
// mayMigrateExisting is true only for the process that holds the gateway lock.
//
// UNTESTED.
func migrate(ctx context.Context, db *sql.DB, dbPath string, mayMigrateExisting bool, now time.Time) (Migrated, error) {
	all, err := loadMigrations()
	if err != nil {
		return Migrated{}, err
	}
	latest := all[len(all)-1].version

	current, tables, err := readSchemaState(ctx, db)
	if err != nil {
		return Migrated{}, err
	}

	switch {
	case current > latest:
		return Migrated{}, fmt.Errorf("%w: it is at schema version %d and this binary understands up to %d: run a newer binary, or restore the backup taken before the upgrade", ErrNewerSchema, current, latest)

	case current == latest:
		return Migrated{}, nil

	case current == 0 && tables > 0:
		// Neither fresh nor ours: tables exist with no version recorded.
		return Migrated{}, errors.New("gateway: the database has tables but no schema version; refusing to touch it")

	case current == 0:
		return applyMigrations(ctx, db, all, 0, "")

	case !mayMigrateExisting:
		return Migrated{}, fmt.Errorf("%w: it is at schema version %d and this build needs %d: start the gateway once (rtk gateway --listen ...), which upgrades the database and keeps a backup; admin commands never upgrade an existing database", ErrNeedsGateway, current, latest)
	}

	backup, err := backUpDatabase(ctx, db, dbPath, current, now)
	if err != nil {
		return Migrated{}, err
	}
	migrated, err := applyMigrations(ctx, db, all, current, backup)
	if err != nil {
		return Migrated{}, fmt.Errorf("%w (the backup taken first is at %s)", err, backup)
	}
	return migrated, nil
}

// readSchemaState reads the version and the count of user tables in one place, so both decisions
// come from the same look.
func readSchemaState(ctx context.Context, db *sql.DB) (version, tables int, err error) {
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, 0, fmt.Errorf("gateway: read schema version: %w", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return 0, 0, fmt.Errorf("gateway: read schema: %w", err)
	}
	return version, tables, nil
}

// applyMigrations applies every file above from, in one immediate transaction.
//
// The version (and, for a fresh database, the emptiness) is read again inside the transaction. A
// fresh database that another process finished creating while this one waited for the write lock is
// simply done. Anything else that moved is an error and nothing is applied.
func applyMigrations(ctx context.Context, db *sql.DB, all []migration, from int, backup string) (Migrated, error) {
	latest := all[len(all)-1].version

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Migrated{}, fmt.Errorf("gateway: begin migration: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var current, tables int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return Migrated{}, fmt.Errorf("gateway: read schema version: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return Migrated{}, fmt.Errorf("gateway: read schema: %w", err)
	}

	switch {
	case from == 0 && current == latest:
		// Another process created it while this one waited. Nothing to do, and nothing was backed up.
		return Migrated{}, nil
	case current != from:
		return Migrated{}, fmt.Errorf("gateway: the database schema version changed from %d to %d while it was being migrated; nothing was applied", from, current)
	case from == 0 && tables > 0:
		return Migrated{}, errors.New("gateway: the database stopped being empty while it was being created; nothing was applied")
	}

	for _, m := range all {
		if m.version <= from {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return Migrated{}, fmt.Errorf("gateway: apply migration %04d: %w", m.version, err)
		}
	}
	// PRAGMA does not take a bound parameter; latest is a number this package computed.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", latest)); err != nil {
		return Migrated{}, fmt.Errorf("gateway: record schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Migrated{}, fmt.Errorf("gateway: commit migration: %w", err)
	}
	return Migrated{From: from, To: latest, Backup: backup}, nil
}

// backUpDatabase copies the database to <db>.bak-v<from>-<unix> (then -2, -3 ... if that name is
// taken) and returns the path.
//
// The name is claimed first with O_CREATE|O_EXCL at 0600, which is both the race guard and what keeps
// the backup from ever existing with a wider mode. VACUUM INTO then fills the claimed, empty file
// (SQLite accepts an existing empty file; if this driver does not, the claim is removed and the
// plain name is used, and the mode is set after). The backup is a consistent snapshot of the committed
// database, WAL content included, and is taken outside any transaction because VACUUM cannot run in
// one.
//
// Any failure removes the file this call created and returns an error naming the path: a missing or
// empty backup must never look like a rollback point.
//
// UNTESTED. Whether VACUUM INTO accepts a pre-created empty file is the one assumption here that only
// a run can settle; the fallback below keeps the failure safe (the migration stops, visibly).
func backUpDatabase(ctx context.Context, db *sql.DB, dbPath string, from int, now time.Time) (string, error) {
	base := fmt.Sprintf("%s.bak-v%d-%d", dbPath, from, now.Unix())

	for attempt := 1; attempt <= maxBackupSuffix; attempt++ {
		name := base
		if attempt > 1 {
			name = fmt.Sprintf("%s-%d", base, attempt)
		}

		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("gateway: cannot create the backup file %s before migrating: %w", name, err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("gateway: cannot close the backup file %s: %w", name, err)
		}

		if err := vacuumInto(ctx, db, name); err != nil {
			_ = os.Remove(name)
			return "", fmt.Errorf("gateway: cannot back up the database to %s before migrating (nothing was changed): %w", name, err)
		}
		return name, nil
	}

	return "", fmt.Errorf("gateway: cannot find a free backup name next to the database (tried %d); nothing was changed", maxBackupSuffix)
}

// vacuumInto writes a snapshot to the claimed file and checks it is not empty.
func vacuumInto(ctx context.Context, db *sql.DB, name string) error {
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", name); err != nil {
		// The driver may refuse an existing (empty) file. Release the claim and let SQLite create
		// the file itself, then tighten its mode. The directory is private, so the window is small.
		if removeErr := os.Remove(name); removeErr != nil {
			return err
		}
		if _, retryErr := db.ExecContext(ctx, "VACUUM INTO ?", name); retryErr != nil {
			return retryErr
		}
		if chmodErr := os.Chmod(name, 0o600); chmodErr != nil {
			return chmodErr
		}
	}

	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return errors.New("the backup came out empty")
	}
	return nil
}
