package store

import (
	"os"
	"path/filepath"
)

// DefaultDir is $XDG_STATE_HOME/ratatoskr, or ~/.local/state/ratatoskr, on both
// macOS and Linux (design section 9).
func DefaultDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "ratatoskr"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "ratatoskr"), nil
}
