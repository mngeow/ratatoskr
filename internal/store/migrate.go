package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	sql     string
}

// loadMigrations reads migrations/NNNN_name.sql; NNNN is the schema version.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		num, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("store: bad migration name %q", e.Name())
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// migrate brings the database to the latest embedded version. A newer database
// is refused untouched. An older, non-empty one is backed up with VACUUM INTO
// first, then upgraded in one transaction. There are no downgrades.
func migrate(ctx context.Context, db *sql.DB) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	latest := ms[len(ms)-1].version

	var cur int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&cur); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	switch {
	case cur > latest:
		return fmt.Errorf("%w: database is version %d, this binary supports %d", ErrNewerSchema, cur, latest)
	case cur == latest:
		return nil
	}

	if cur > 0 {
		var path string
		var seq int
		var name string
		if err := db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
			return fmt.Errorf("store: locate database for backup: %w", err)
		}
		backup := fmt.Sprintf("%s.bak-v%d-%d", path, cur, time.Now().Unix())
		if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
			return fmt.Errorf("store: back up before migrating: %w", err)
		}
		if err := os.Chmod(backup, 0o600); err != nil {
			return fmt.Errorf("store: chmod backup: %w", err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, m := range ms {
		if m.version <= cur {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: migration %04d: %w", m.version, err)
		}
	}
	// PRAGMA takes no bound parameters; latest is an int we own.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", latest)); err != nil {
		return fmt.Errorf("store: set schema version: %w", err)
	}
	return tx.Commit()
}
