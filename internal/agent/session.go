package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// sessionBaseName is the only directory name PrepareSessionDir will empty.
const sessionBaseName = "session"

// PrepareSessionDir creates the session directory and empties it.
//
// It refuses to touch anything whose base name is not exactly "session", and
// refuses a symlink, so a mistake upstream cannot turn this into a recursive
// delete of somewhere else. App calls it only after the store lock is held, so it
// can never wipe a live run's files.
//
// UNTESTED.
func PrepareSessionDir(dir string) error {
	if dir == "" {
		return errors.New("agent: empty session directory")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("agent: session directory %q must be absolute", dir)
	}
	if filepath.Base(filepath.Clean(dir)) != sessionBaseName {
		return fmt.Errorf("agent: session directory %q must end in %q", dir, sessionBaseName)
	}

	info, err := os.Lstat(dir)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("agent: session directory %q is a symlink", dir)
	case err == nil && !info.IsDir():
		return fmt.Errorf("agent: session directory %q is not a directory", dir)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("agent: session directory %q: %w", dir, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("agent: create session directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("agent: chmod session directory %q: %w", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("agent: read session directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("agent: empty session directory %q: %w", dir, err)
		}
	}
	return nil
}
