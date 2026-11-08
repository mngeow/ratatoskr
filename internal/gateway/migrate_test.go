package gateway

// Untested: under GR-3 nothing here has been run. Every test builds its own temporary database in
// t.TempDir(); none touches a real gateway database or runs the migration against one.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openRawDatabase opens a database file with the gateway's connection string and nothing applied.
func openRawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// versionOneDatabase builds a version-1 database with one host and one message, the way the first
// release left it: 0001 applied and user_version = 1.
func versionOneDatabase(t *testing.T) (db *sql.DB, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), dbName)
	db = openRawDatabase(t, path)

	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if _, err := db.Exec(all[0].sql); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("set version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO hosts(name, credential_hash, created_at) VALUES('vmb', 'h', 1)`); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO messages(id, from_address, to_address, body, result, created_at, updated_at)
		VALUES('m1', 'a@vmb', 'b@mac', 'secret body', 'stored', 1, 1)`); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	return db, path
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	return version
}

func latestVersion(t *testing.T) int {
	t.Helper()
	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	return all[len(all)-1].version
}

func backupFiles(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

// TestMigrationsAreNumberedWithoutGaps: the embedded files are 1..n.
func TestMigrationsAreNumberedWithoutGaps(t *testing.T) {
	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for index, m := range all {
		if m.version != index+1 {
			t.Fatalf("migration %d has version %d", index, m.version)
		}
	}
	if len(all) < 3 {
		t.Fatalf("expected 0001 to 0003, got %d files", len(all))
	}
}

// TestFreshDatabaseIsCreatedWithoutABackup: an empty database takes every file and no backup.
func TestFreshDatabaseIsCreatedWithoutABackup(t *testing.T) {
	for _, mayMigrate := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), dbName)
		db := openRawDatabase(t, path)

		migrated, err := migrate(context.Background(), db, path, mayMigrate, time.Unix(100, 0))
		if err != nil {
			t.Fatalf("mayMigrate=%v: %v", mayMigrate, err)
		}
		latest := latestVersion(t)
		if migrated != (Migrated{From: 0, To: latest}) {
			t.Fatalf("mayMigrate=%v: migrated = %+v", mayMigrate, migrated)
		}
		if got := userVersion(t, db); got != latest {
			t.Fatalf("user_version = %d, want %d", got, latest)
		}
		if files := backupFiles(t, path); len(files) != 0 {
			t.Fatalf("a fresh database took a backup: %v", files)
		}
	}
}

// TestAdminCommandRefusesAnOlderDatabaseAndTouchesNothing: only the gateway migrates.
func TestAdminCommandRefusesAnOlderDatabaseAndTouchesNothing(t *testing.T) {
	db, path := versionOneDatabase(t)

	_, err := migrate(context.Background(), db, path, false, time.Unix(100, 0))
	if !errors.Is(err, ErrNeedsGateway) {
		t.Fatalf("err = %v, want ErrNeedsGateway", err)
	}
	if got := userVersion(t, db); got != 1 {
		t.Fatalf("user_version = %d after a refusal, want 1", got)
	}
	if files := backupFiles(t, path); len(files) != 0 {
		t.Fatalf("a refusal left a backup: %v", files)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'host_cards'`).Scan(&tables); err != nil {
		t.Fatalf("query: %v", err)
	}
	if tables != 0 {
		t.Fatal("a refusal created the new tables")
	}
}

// TestGatewayMigratesAnOlderDatabaseWithABackup: the backup is a readable copy of the old state, mode
// 0600, and the ledger and pairings survive.
func TestGatewayMigratesAnOlderDatabaseWithABackup(t *testing.T) {
	db, path := versionOneDatabase(t)
	now := time.Unix(1700000000, 0)

	migrated, err := migrate(context.Background(), db, path, true, now)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	latest := latestVersion(t)
	wantBackup := path + ".bak-v1-1700000000"
	if migrated != (Migrated{From: 1, To: latest, Backup: wantBackup}) {
		t.Fatalf("migrated = %+v", migrated)
	}
	if got := userVersion(t, db); got != latest {
		t.Fatalf("user_version = %d", got)
	}

	info, err := os.Stat(wantBackup)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("the backup is empty")
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o", info.Mode().Perm())
	}

	// The backup is the OLD schema with the old rows.
	backup := openRawDatabase(t, wantBackup)
	if got := userVersion(t, backup); got != 1 {
		t.Fatalf("backup user_version = %d, want 1", got)
	}
	var host string
	if err := backup.QueryRow(`SELECT name FROM hosts`).Scan(&host); err != nil || host != "vmb" {
		t.Fatalf("backup host = %q, %v", host, err)
	}

	// The live database kept its rows byte for byte.
	var body string
	if err := db.QueryRow(`SELECT body FROM messages WHERE id = 'm1'`).Scan(&body); err != nil || body != "secret body" {
		t.Fatalf("message body = %q, %v", body, err)
	}
}

// TestBackupNameCollisionTakesTheNextName: a racing process (or a leftover) at the same second never
// shares or overwrites the backup.
func TestBackupNameCollisionTakesTheNextName(t *testing.T) {
	db, path := versionOneDatabase(t)
	now := time.Unix(1700000000, 0)

	taken := path + ".bak-v1-1700000000"
	if err := os.WriteFile(taken, []byte("someone else's file"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	migrated, err := migrate(context.Background(), db, path, true, now)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated.Backup != taken+"-2" {
		t.Fatalf("backup = %q, want %q", migrated.Backup, taken+"-2")
	}
	content, err := os.ReadFile(taken)
	if err != nil || string(content) != "someone else's file" {
		t.Fatalf("the earlier file was touched: %q, %v", content, err)
	}
}

// TestFailedBackupStopsTheMigrationAndLeavesNoFile: an unwritable backup location is a visible failure,
// never a skipped backup, and nothing is changed.
func TestFailedBackupStopsTheMigrationAndLeavesNoFile(t *testing.T) {
	db, path := versionOneDatabase(t)

	// A database path whose directory does not exist cannot take a backup next to it.
	missing := filepath.Join(t.TempDir(), "no-such-dir", dbName)

	_, err := migrate(context.Background(), db, missing, true, time.Unix(100, 0))
	if err == nil {
		t.Fatal("the migration went ahead without a backup")
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Fatalf("err = %v, want it to name the backup", err)
	}
	if got := userVersion(t, db); got != 1 {
		t.Fatalf("user_version = %d after a failed backup, want 1", got)
	}
	if files := backupFiles(t, path); len(files) != 0 {
		t.Fatalf("a failed backup left a file: %v", files)
	}
}

// TestTablesWithoutAVersionAreRefused: neither fresh nor migratable, and untouched.
func TestTablesWithoutAVersionAreRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbName)
	db := openRawDatabase(t, path)
	if _, err := db.Exec(`CREATE TABLE something_else (x INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, mayMigrate := range []bool{true, false} {
		if _, err := migrate(context.Background(), db, path, mayMigrate, time.Unix(100, 0)); err == nil {
			t.Fatalf("mayMigrate=%v: accepted a database with tables and no version", mayMigrate)
		}
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("tables = %d, %v: the refusal changed the database", tables, err)
	}
	if got := userVersion(t, db); got != 0 {
		t.Fatalf("user_version = %d, want 0", got)
	}
}

// TestNewerDatabaseIsRefusedWithoutABackup: an older binary on a newer database.
func TestNewerDatabaseIsRefusedWithoutABackup(t *testing.T) {
	db, path := versionOneDatabase(t)
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set version: %v", err)
	}

	for _, mayMigrate := range []bool{true, false} {
		if _, err := migrate(context.Background(), db, path, mayMigrate, time.Unix(100, 0)); !errors.Is(err, ErrNewerSchema) {
			t.Fatalf("mayMigrate=%v: err = %v, want ErrNewerSchema", mayMigrate, err)
		}
	}
	if files := backupFiles(t, path); len(files) != 0 {
		t.Fatalf("a refusal left a backup: %v", files)
	}
}

// TestUnpairCascadesToCardTables: the one DELETE FROM hosts an unpair does removes every card row.
func TestUnpairCascadesToCardTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbName)
	db := openRawDatabase(t, path)
	if _, err := migrate(context.Background(), db, path, true, time.Unix(100, 0)); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	statements := []string{
		`INSERT INTO hosts(name, credential_hash, created_at) VALUES('vmb', 'h', 1)`,
		`INSERT INTO host_card_targets(host, scheme, addr, class, created_at, updated_at) VALUES('vmb', 'http', '100.64.0.5:7778', 'tailscale', 1, 1)`,
		`INSERT INTO host_cards(host) VALUES('vmb')`,
		`INSERT INTO card_revisions(host, revision, etag, hash, card_json, fetched_at) VALUES('vmb', '1', 'e', 'h', '{}', 1)`,
		`INSERT INTO poll_attempts(host, at, outcome, duration_ms) VALUES('vmb', 1, '200', 5)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := db.Exec(`DELETE FROM hosts WHERE name = 'vmb'`); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	for _, table := range []string{"host_card_targets", "host_cards", "card_revisions", "poll_attempts"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s has %d rows after the host was deleted (%v)", table, count, err)
		}
	}
}

// TestTargetNeedsAPairedHost: a card target cannot name a host that is not paired.
func TestTargetNeedsAPairedHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbName)
	db := openRawDatabase(t, path)
	if _, err := migrate(context.Background(), db, path, true, time.Unix(100, 0)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, err := db.Exec(`INSERT INTO host_card_targets(host, scheme, addr, class, created_at, updated_at) VALUES('ghost', 'http', '127.0.0.1:1', 'loopback', 1, 1)`)
	if err == nil {
		t.Fatal("a target for an unpaired host was accepted")
	}
}
