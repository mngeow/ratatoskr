package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests are written but UNRUN: GR-3 forbids running them.

func TestPrepareSessionDirRefusesUnsafePaths(t *testing.T) {
	root := t.TempDir()

	realDir := filepath.Join(t.TempDir(), "only-copy")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A sentinel the symlink refusal must protect.
	if err := os.WriteFile(filepath.Join(realDir, "live-run.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The symlink's own base name is "session", so only the symlink check can
	// refuse it.
	link := filepath.Join(root, "session")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	fileAsSession := filepath.Join(root, "notasession")
	if err := os.WriteFile(fileAsSession, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	fileNamedSession := filepath.Join(root, "session-file", "session")
	if err := os.MkdirAll(filepath.Dir(fileNamedSession), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fileNamedSession, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	tests := []struct {
		name string
		dir  string
	}{
		{name: "empty", dir: ""},
		{name: "relative", dir: "session"},
		{name: "relative with parents", dir: "a/b/session"},
		{name: "wrong base name", dir: fileAsSession},
		{name: "a file named session", dir: fileNamedSession},
		{name: "a symlink", dir: link},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := PrepareSessionDir(tc.dir); err == nil {
				t.Fatalf("PrepareSessionDir accepted %q", tc.dir)
			}
		})
	}

	// The symlink refusal must not have emptied the directory it pointed at.
	if _, err := os.Stat(filepath.Join(realDir, "live-run.txt")); err != nil {
		t.Fatalf("the symlink target was emptied: %v", err)
	}
}

func TestPrepareSessionDirCreatesAndEmpties(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nested", "session")

	if err := PrepareSessionDir(dir); err != nil {
		t.Fatalf("first PrepareSessionDir: %v", err)
	}
	assertMode(t, dir, 0o700)

	// Populate it the way a previous run would have, including a subdirectory
	// and a file the hook command reads.
	if err := os.WriteFile(filepath.Join(dir, "hook-alpha.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := PrepareSessionDir(dir); err != nil {
		t.Fatalf("second PrepareSessionDir: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("session directory still holds %v", names)
	}

	// The directory itself survives, with the restricted mode.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("the session directory is no longer a directory")
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("mode = %o, want 700", mode)
	}
}
