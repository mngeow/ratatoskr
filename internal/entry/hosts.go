// Package entry builds an entry plan from the owner's host map and the gateway snapshot.
//
// It is pure: no network, no daemon, no exec, no shell. The owner's hosts.json is the only source of
// an ssh destination or a remote path, and nothing from the gateway can become either - the snapshot
// may contribute a host name and a session id, and even the session id is re-validated before it can
// appear in a command. The caller runs the plan's argv itself with exec.Command(Argv[0], Argv[1:]...),
// never through a shell.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No hosts.json has been read, no plan built and
// no ssh command constructed at run time.

package entry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const (
	// MaxHostsBytes bounds the file. A host map is a few hundred bytes; a large file means something
	// else was put there.
	MaxHostsBytes = 64 << 10

	// maxSSH is the contract's ssh destination limit.
	maxSSH = 253
	// maxPath is the contract's limit for the remote rtk path and the remote state home.
	maxPath = 512
	// maxHostKey bounds a host name key in the file.
	maxHostKey = 128
)

var (
	// ErrNoHostMap reports a missing hosts.json. It is not a failure: every host is then "manual".
	ErrNoHostMap = errors.New("entry: there is no hosts.json")

	// sshPattern is the contract's destination rule: a plain host, user@host, a tailnet name or an
	// ssh alias, and never something that reads as an option.
	sshPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$`)

	// pathPattern is the contract's rule for the remote rtk path and state home: absolute, and only
	// characters that cannot change the meaning of a command word.
	pathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

	// uuidPattern is the canonical 36-character UUID form a session id must have before it may appear
	// in a command.
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// HostEntry is one usable host from the owner's map.
type HostEntry struct {
	// SSH is the ssh destination exactly as the owner wrote it.
	SSH string
	// RTK is the absolute path of rtk on that machine.
	RTK string
	// StateHome, when set, becomes XDG_STATE_HOME for the remote command only.
	StateHome string
}

// HostResult is one host's outcome: usable when Reason is empty.
type HostResult struct {
	Entry  HostEntry
	Reason string
}

// HostMap is the whole file, with a file-level problem that applies to every host.
type HostMap struct {
	Hosts map[string]HostResult
	// Problem is why no host in this file can be used: a link, a mode, an owner, an oversize file,
	// bad JSON, or a version this build does not support. It never contains file content.
	Problem string
}

// ReadHostMap reads and validates the owner's host map.
//
// A missing file is ErrNoHostMap, which a caller treats as "every host is manual". Every other
// problem is a sentence in HostMap.Problem, so a plan degrades to a manual line rather than failing a
// window.
//
// UNTESTED.
func ReadHostMap(path string) (HostMap, error) {
	data, err := readHostsFile(path)
	if errors.Is(err, ErrNoHostMap) {
		return HostMap{}, ErrNoHostMap
	}
	if err != nil {
		return HostMap{Problem: err.Error()}, nil
	}

	var raw rawHostMap
	if err := json.Unmarshal(data, &raw); err != nil {
		return HostMap{Problem: "hosts.json is not valid JSON"}, nil
	}
	if raw.Version != 1 {
		return HostMap{Problem: fmt.Sprintf("hosts.json version %d is not supported", raw.Version)}, nil
	}

	hosts := make(map[string]HostResult, len(raw.Hosts))
	for key, body := range raw.Hosts {
		hosts[key] = decodeHost(key, body)
	}
	return HostMap{Hosts: hosts}, nil
}

// rawHostMap is the top level. Unknown top-level fields are ignored, as the contract says.
type rawHostMap struct {
	Version int                        `json:"version"`
	Hosts   map[string]json.RawMessage `json:"hosts"`
}

// rawHostEntry is one host. Unknown fields inside a host are refused, so a typo cannot be silently
// ignored and then relied on.
type rawHostEntry struct {
	SSH       string `json:"ssh"`
	RTK       string `json:"rtk"`
	StateHome string `json:"state_home"`
}

// decodeHost decodes and validates one host on its own, so a bad host costs only that host.
func decodeHost(key string, body json.RawMessage) HostResult {
	if reason := hostKeyProblem(key); reason != "" {
		return HostResult{Reason: reason}
	}

	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var raw rawHostEntry
	if err := decoder.Decode(&raw); err != nil {
		// The decoder's message names a field or a position, never a value.
		return HostResult{Reason: "this host's entry in hosts.json could not be read: " + cleanText(err.Error(), maxPath)}
	}

	entry := HostEntry{SSH: raw.SSH, RTK: raw.RTK, StateHome: raw.StateHome}
	if reason := validateHost(entry); reason != "" {
		return HostResult{Reason: reason}
	}
	return HostResult{Entry: entry}
}

// hostKeyProblem reports why a host name key cannot be used.
func hostKeyProblem(key string) string {
	if key == "" {
		return "hosts.json has an empty host name"
	}
	if len([]rune(key)) > maxHostKey {
		return "a host name in hosts.json is longer than 128 characters"
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return "a host name in hosts.json contains a control character"
		}
	}
	return ""
}

// validateHost applies the contract's patterns and returns the reason it fails, or "".
func validateHost(entry HostEntry) string {
	if entry.SSH == "" {
		return "ssh must be set"
	}
	if len(entry.SSH) > maxSSH || !sshPattern.MatchString(entry.SSH) {
		return "ssh must be a host name, user@host or an ssh alias (letters, digits and _ . @ : - only, at most 253 characters)"
	}
	if !validPath(entry.RTK) {
		return "rtk must be an absolute path without spaces (letters, digits and _ . / - only, at most 512 characters)"
	}
	if entry.StateHome != "" && !validPath(entry.StateHome) {
		return "state_home must be an absolute path without spaces (letters, digits and _ . / - only, at most 512 characters)"
	}
	return ""
}

// validPath is the contract's remote-path rule.
func validPath(value string) bool {
	if value == "" || len(value) > maxPath {
		return false
	}
	if !pathPattern.MatchString(value) {
		return false
	}
	// A path must also not walk upwards: ".." in a remote path is a mistake, not a destination.
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// IsUUID reports whether a value is the canonical 36-character UUID form.
//
// It is exported so the daemon's attach check and the command line use the same rule as the plan
// builder: one rule, one place.
//
// UNTESTED.
func IsUUID(value string) bool {
	return len(value) == 36 && uuidPattern.MatchString(value)
}

// cleanText removes control characters and bounds the length, so a gateway or file string can never
// rewrite a terminal line or unbounded a table.
//
// UNTESTED.
func cleanText(value string, max int) string {
	cleaned := make([]rune, 0, len(value))
	for _, r := range value {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
			continue
		}
		cleaned = append(cleaned, r)
		if len(cleaned) == max {
			break
		}
	}
	return strings.TrimSpace(string(cleaned))
}

// fileBase names the file in a refusal without a full path, which keeps a message short and still
// says which file is wrong.
func fileBase(path string) string {
	if base := filepath.Base(path); base != "" {
		return base
	}
	return path
}

// readAllBounded reads at most MaxHostsBytes+1 bytes, so an oversize file is detected without being
// loaded.
func readAllBounded(reader io.Reader, path string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxHostsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s could not be read", fileBase(path))
	}
	if len(data) > MaxHostsBytes {
		return nil, fmt.Errorf("%s is larger than %d KiB", fileBase(path), MaxHostsBytes>>10)
	}
	return data, nil
}

// statResult is what the platform file reader reports about the opened file.
type statResult struct {
	mode os.FileMode
	uid  uint32
	size int64
}
