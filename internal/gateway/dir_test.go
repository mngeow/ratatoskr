package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openStore opens a gateway store in a fresh temporary state directory. The
// caller gets the state directory too, so a second store can be opened beside it
// the way an admin command runs beside the gateway.
func openStore(t *testing.T, hold bool) (*Store, string) {
	t.Helper()

	stateDir := t.TempDir()
	store, err := Open(context.Background(), stateDir, hold)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store, stateDir
}

func TestPrepareDirCreatesAndRepairs(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")

	dir, err := PrepareDir(stateDir)
	if err != nil {
		t.Fatalf("PrepareDir: %v", err)
	}
	if want := filepath.Join(stateDir, "gateway"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	assertMode(t, dir, 0o700)

	// A directory that already exists at the wrong mode is repaired, because
	// MkdirAll skips an existing directory and would leave it alone.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := PrepareDir(stateDir); err != nil {
		t.Fatalf("second PrepareDir: %v", err)
	}
	assertMode(t, dir, 0o700)
}

// TestPrepareDirAllowsASymlinkedStateDir records the deliberate difference: the
// state directory may legitimately be a link the user made, while everything this
// package owns inside it may not.
func TestPrepareDirAllowsASymlinkedStateDir(t *testing.T) {
	realState := filepath.Join(t.TempDir(), "real-state")
	if err := os.MkdirAll(realState, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(realState, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dir, err := PrepareDir(link)
	if err != nil {
		t.Fatalf("PrepareDir refused a symlinked state directory: %v", err)
	}
	if want := filepath.Join(link, "gateway"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(filepath.Join(realState, "gateway")); err != nil {
		t.Fatalf("the gateway directory was not created inside the real directory: %v", err)
	}
}

func TestPrepareDirRefusesASymlinkedGatewayDir(t *testing.T) {
	stateDir := t.TempDir()

	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(stateDir, "gateway")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := PrepareDir(stateDir); err == nil {
		t.Fatal("PrepareDir accepted a symlinked gateway directory")
	}
}

func TestPrepareDirRefusesAFileAsGatewayDir(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "gateway"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := PrepareDir(stateDir); err == nil {
		t.Fatal("PrepareDir accepted a regular file where its directory belongs")
	}
}

func TestOpenCreatesTheDatabaseAt0600(t *testing.T) {
	store, stateDir := openStore(t, true)

	assertMode(t, filepath.Join(stateDir, "gateway", dbName), 0o600)
	assertMode(t, filepath.Join(stateDir, "gateway"), 0o700)
	// The handle exists and the schema is at the version this binary knows.
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if latest := migrations[len(migrations)-1].version; version != latest {
		t.Fatalf("user_version = %d, want %d", version, latest)
	}
}

// TestOpenRefusesASymlinkedDatabase: a link at the database path could point the
// ledger at a file this package does not own.
func TestOpenRefusesASymlinkedDatabase(t *testing.T) {
	stateDir := t.TempDir()
	dir, err := PrepareDir(stateDir)
	if err != nil {
		t.Fatalf("PrepareDir: %v", err)
	}

	target := filepath.Join(t.TempDir(), "elsewhere.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, dbName)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := Open(context.Background(), stateDir, false); err == nil {
		t.Fatal("Open accepted a symlinked database")
	}
}

// TestOpenRefusesASymlinkedLockFile: the lock has to protect this database.
func TestOpenRefusesASymlinkedLockFile(t *testing.T) {
	stateDir := t.TempDir()
	dir, err := PrepareDir(stateDir)
	if err != nil {
		t.Fatalf("PrepareDir: %v", err)
	}

	target := filepath.Join(t.TempDir(), "elsewhere.lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, lockName)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := Open(context.Background(), stateDir, true); err == nil {
		t.Fatal("Open accepted a symlinked lock file")
	}
}

// TestOnlyOneHolder is the gateway lock rule, and the reason an admin command
// passes hold=false.
func TestOnlyOneHolder(t *testing.T) {
	holder, stateDir := openStore(t, true)

	if _, err := Open(context.Background(), stateDir, true); !errors.Is(err, ErrGatewayRunning) {
		t.Fatalf("second holder error = %v, want ErrGatewayRunning", err)
	}

	admin, err := Open(context.Background(), stateDir, false)
	if err != nil {
		t.Fatalf("an admin store must open beside the holder: %v", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatalf("close admin store: %v", err)
	}

	// Releasing the lock lets the next holder in.
	if err := holder.Close(); err != nil {
		t.Fatalf("close holder: %v", err)
	}
	next, err := Open(context.Background(), stateDir, true)
	if err != nil {
		t.Fatalf("Open after the holder closed: %v", err)
	}
	if err := next.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	store, _ := openStore(t, false)

	if err := store.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMigrateRefusesANewerSchema(t *testing.T) {
	store, _ := openStore(t, false)

	if _, err := store.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set version: %v", err)
	}
	if _, err := migrate(context.Background(), store.db, filepath.Join(store.dir, dbName), true, time.Now()); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("migrate accepted a newer schema: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	store, _ := openStore(t, false)

	migrated, err := migrate(context.Background(), store.db, filepath.Join(store.dir, dbName), true, time.Now())
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if migrated != (Migrated{}) {
		t.Fatalf("a current database reported a migration: %+v", migrated)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
