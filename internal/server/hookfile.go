package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// hookFileName is the file one agent's hook command reads. It holds the server
// URL and that agent's token, which is why it is written 0600.
const hookFileName = "hook-%s.json"

// agentNamePattern bounds an agent name before it is used as part of a path.
// Names come from the rig definition, but a name is not allowed to escape the
// session directory even if it somehow does.
var agentNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// hookFile is the on-disk form. Field names are short because this file is read
// by a process that runs on every harness hook.
type hookFile struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// WriteHookFile writes <sessionDir>/hook-<agentName>.json for one agent.
//
// It is the only writer of that file. The write goes to a temporary file in the
// same directory and is renamed into place, so the hook command never reads a
// half-written file, and the mode is 0600 from creation and again after the
// rename, so there is no window where another user could read the token.
//
// UNTESTED.
func WriteHookFile(sessionDir, agentName, serverAddr, token string) error {
	if !agentNamePattern.MatchString(agentName) {
		return fmt.Errorf("server: hook file: invalid agent name %q", agentName)
	}
	if sessionDir == "" {
		return errors.New("server: hook file: empty session directory")
	}
	if serverAddr == "" {
		return errors.New("server: hook file: empty server address")
	}
	if token == "" {
		return errors.New("server: hook file: empty token")
	}

	content, err := json.Marshal(hookFile{URL: loopbackURL(serverAddr), Token: token})
	if err != nil {
		return fmt.Errorf("server: hook file: marshal: %w", err)
	}

	temp, err := os.CreateTemp(sessionDir, ".hook-*.tmp")
	if err != nil {
		return fmt.Errorf("server: hook file: create temp: %w", err)
	}
	tempName := temp.Name()

	// A failure path must not leave the temporary file behind.
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("server: hook file: chmod temp: %w", err)
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return fmt.Errorf("server: hook file: write temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("server: hook file: close temp: %w", err)
	}

	target := filepath.Join(sessionDir, fmt.Sprintf(hookFileName, agentName))
	if err := os.Rename(tempName, target); err != nil {
		return fmt.Errorf("server: hook file: rename: %w", err)
	}
	tempName = ""

	// Setting the mode after the rename covers a filesystem that did not carry
	// the mode across, and costs one syscall per agent per run.
	if err := os.Chmod(target, 0o600); err != nil {
		return fmt.Errorf("server: hook file: chmod: %w", err)
	}
	return nil
}

// readHookFile reads one agent's hook file.
//
// UNTESTED.
func readHookFile(sessionDir, agentName string) (url, token string, err error) {
	if !agentNamePattern.MatchString(agentName) {
		return "", "", fmt.Errorf("server: hook file: invalid agent name %q", agentName)
	}

	path := filepath.Join(sessionDir, fmt.Sprintf(hookFileName, agentName))
	content, err := os.ReadFile(path) //nolint:gosec // the path is bound to the pattern above.
	if err != nil {
		return "", "", fmt.Errorf("server: hook file: read: %w", err)
	}

	var file hookFile
	if err := json.Unmarshal(content, &file); err != nil {
		return "", "", fmt.Errorf("server: hook file: parse: %w", err)
	}
	return file.URL, file.Token, nil
}

// loopbackURL builds the URL a hook file records. The address is the one the
// server bound, so it is already loopback.
func loopbackURL(serverAddr string) string {
	return "http://" + serverAddr
}
