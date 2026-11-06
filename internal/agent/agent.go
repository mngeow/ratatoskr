// Package agent turns one harness into the launch spec App starts: the argv, the
// complete environment, the session directory and, for Claude, a session id the
// router can bind at registration.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No harness has been
// launched, and nothing here has ever been executed.
package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	harnessClaude = "claude"
	harnessCodex  = "codex"
)

// agentNamePattern is the same shape server.WriteHookFile enforces. The display
// name is also the hook-file stem, so the two must not be able to disagree.
var agentNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// peerAddressPattern is what a peer entry may be: a local display name, or a
// network name of the form name@host. A connected run passes network names so the
// harness can address a peer on another machine; a local run passes bare names.
var peerAddressPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}(@[a-z0-9-]{1,63})?$`)

// Params is everything App knows about the agent it is about to launch.
type Params struct {
	ID          protocol.AgentID
	DisplayName string
	Harness     string // exactly "claude" or "codex"
	Binary      string // optional absolute path; default exec.LookPath(Harness)
	Dir         string // working directory for the harness
	RtkPath     string // absolute path of the running rtk binary
	SessionDir  string // absolute, .../session
	ServerAddr  string
	Token       string
	BaseEnv     []string // typically os.Environ(); filtered in env.go
	// Peers are the display names of the other agents. Only Codex's startup
	// prompt names them; every harness still has them validated.
	Peers []string
}

// Build validates the parameters and returns the launch spec for one harness.
//
// It writes nothing except inside SessionDir, and only for Claude. The
// LaunchSpec.Env it returns is complete, not an addition to the parent's: the
// child inherits nothing it was not given.
//
// HarnessVersion is left empty on purpose: App fills it from Version at startup,
// so Build never runs a harness.
//
// UNTESTED.
func Build(p Params) (protocol.LaunchSpec, error) {
	if err := validate(p); err != nil {
		return protocol.LaunchSpec{}, err
	}

	binary, err := resolveBinary(p)
	if err != nil {
		return protocol.LaunchSpec{}, err
	}

	spec := protocol.LaunchSpec{
		ID:          p.ID,
		DisplayName: p.DisplayName,
		Harness:     p.Harness,
		Dir:         p.Dir,
		Env:         buildEnv(p),
	}

	switch p.Harness {
	case harnessClaude:
		return claudeSpec(p, binary, spec)
	case harnessCodex:
		return codexSpec(p, binary, spec)
	default:
		// validate already refused this; the branch exists so a new harness
		// cannot be added by accident.
		return protocol.LaunchSpec{}, fmt.Errorf("agent: unsupported harness %q", p.Harness)
	}
}

// validate checks the parameters App must always be able to supply. A failure
// here is a wiring bug, not a harness problem.
func validate(p Params) error {
	switch {
	case p.ID == "":
		return errors.New("agent: empty agent ID")
	case p.DisplayName == "":
		return errors.New("agent: empty display name")
	case p.Dir == "":
		return errors.New("agent: empty working directory")
	case p.RtkPath == "":
		return errors.New("agent: empty rtk path")
	case p.SessionDir == "":
		return errors.New("agent: empty session directory")
	case p.ServerAddr == "":
		return errors.New("agent: empty server address")
	case p.Token == "":
		return errors.New("agent: empty token")
	}

	if !agentNamePattern.MatchString(p.DisplayName) {
		return fmt.Errorf("agent: display name %q must match %s", p.DisplayName, agentNamePattern)
	}
	if !filepath.IsAbs(p.RtkPath) {
		return fmt.Errorf("agent: rtk path %q must be absolute", p.RtkPath)
	}
	if !filepath.IsAbs(p.SessionDir) {
		return fmt.Errorf("agent: session directory %q must be absolute", p.SessionDir)
	}
	// A peer entry becomes part of a model-visible prompt, so it gets the same
	// treatment as our own display name rather than being trusted blindly. It may
	// be a bare display name or a network name (name@host).
	for _, peer := range p.Peers {
		if !peerAddressPattern.MatchString(peer) {
			return fmt.Errorf("agent: peer name %q must match %s", peer, peerAddressPattern)
		}
	}

	switch p.Harness {
	case harnessClaude, harnessCodex:
		return nil
	default:
		return fmt.Errorf("agent: unsupported harness %q", p.Harness)
	}
}

// resolveBinary finds the harness program.
//
// A Params.Binary exists for tests and for a harness installed somewhere PATH
// does not cover; when it is set it must be an absolute, executable file and
// LookPath is skipped. Otherwise the harness name is resolved on PATH and made
// absolute, because a child's Argv[0] should not depend on the child's PATH.
//
// UNTESTED.
func resolveBinary(p Params) (string, error) {
	if p.Binary != "" {
		if !filepath.IsAbs(p.Binary) {
			return "", fmt.Errorf("agent: binary %q must be absolute", p.Binary)
		}
		info, err := os.Stat(p.Binary)
		if err != nil {
			return "", fmt.Errorf("agent: binary %q: %w", p.Binary, err)
		}
		if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("agent: binary %q is not executable", p.Binary)
		}
		return p.Binary, nil
	}

	found, err := exec.LookPath(p.Harness)
	if err != nil {
		return "", fmt.Errorf("agent: %s is not on PATH: %w", p.Harness, err)
	}
	absolute, err := filepath.Abs(found)
	if err != nil {
		return "", fmt.Errorf("agent: resolve %q: %w", found, err)
	}
	return absolute, nil
}

// writeFileAtomic writes data to path with an explicit mode, through a temporary
// file in the same directory and a rename, so a reader never sees half a file
// and the mode is set before the content is visible.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)

	temp, err := os.CreateTemp(dir, ".agent-*.tmp")
	if err != nil {
		return fmt.Errorf("agent: create temp for %s: %w", path, err)
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()

	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("agent: chmod temp for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("agent: write temp for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("agent: close temp for %s: %w", path, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("agent: rename into %s: %w", path, err)
	}
	tempName = ""

	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("agent: chmod %s: %w", path, err)
	}
	return nil
}
